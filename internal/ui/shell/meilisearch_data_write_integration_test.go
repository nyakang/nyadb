package shell

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/meilisearch"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
)

func captureMeilisearchDataWrite(t *testing.T, s *State, phase string) {
	t.Helper()
	output := os.Getenv("NYADB_MEILISEARCH_WRITE_IMAGES")
	if output == "" {
		return
	}
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(s.View, size[0], size[1])
			label := "保存数据更改"
			if phase == "admitted" || phase == "failed" {
				label = "观察任务结果"
			}
			if phase == "evidence" {
				label = "复制写入任务受理"
			}
			if phase == "guard" {
				label = "关闭并放弃本地草稿与证据"
			}
			if r, ok := tt.Find(locales.Text(label)); !ok || r.W <= 0 || r.X < 0 || r.X+r.W > float32(size[0]) || r.H <= 0 || r.Y < 0 || r.Y+r.H > float32(size[1]) {
				t.Fatal("native localized write action outside viewport", phase, language, r)
			}
			if phase == "admitted" || phase == "failed" {
				for _, action := range []string{"写入任务证据", "关闭待核验文档"} {
					if r, ok := tt.Find(locales.Text(action)); !ok || r.X < 0 || r.X+r.W > float32(size[0]) || r.Y < 0 || r.Y+r.H > float32(size[1]) {
						t.Fatal("async toolbar action clipped", language, action, r)
					}
				}
			}
			status := ""
			switch phase {
			case "admitted":
				status = "任务已受理，尚未确认完成；草稿已保留，禁止重复提交"
			case "failed":
				status = "任务失败或已取消；草稿已保留，禁止重放，请检查任务证据"
			case "saved":
				status = "已核验：数据更改已提交"
			}
			if status != "" && !strings.Contains(strings.Join(tt.Texts(), "\n"), locales.Text(status)) {
				t.Fatal("native async status did not follow locale", phase, language)
			}
			folder := filepath.Join(output, language)
			if err := os.MkdirAll(folder, 0700); err != nil {
				t.Fatal(err)
			}
			writeDomainCapture(t, folder, fmt.Sprintf("data-write-%s-%dx%d", phase, size[0], size[1]), tt)
		}
	}
}

func TestMeilisearchNativePublicDataWriteGridAdmissionObservationAndEvidence(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	cfg, _ := connections.MeilisearchProfileConfig(parsed.Profile)
	admin, err := meilisearch.Open(context.Background(), cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	uid := "owned_grid_write_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	submit := func(method, path, body string) {
		t.Helper()
		r, _, err := admin.Submit(context.Background(), meilisearch.Request{Method: method, Path: path, Body: []byte(body)})
		if err == nil {
			_, err = admin.WaitTask(context.Background(), r, 30*time.Second)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	submit("POST", "/indexes", `{"uid":"`+uid+`","primaryKey":"id"}`)
	defer submit("DELETE", "/indexes/"+uid, "")
	submit("POST", "/indexes/"+uid+"/documents", `[{"id":9007199254740993,"old":true,"n":1}]`)
	s, tt, _, p, services := keyPanelState(t, parsed.Profile, parsed.Password)
	defer s.CloseMySQLSessions()
	w := s.sessions.Current()
	s.submitNavigation(*services, navigation.DatabaseNode(p.ID, uid))
	taskPanelWait(t, tt, func() bool { return w.Pending == "" && w.Cache[uid] != nil })
	s.submitNavigation(*services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: uid, Category: "文档", Object: "documents"})
	d := s.currentTab().data
	if d == nil {
		t.Fatal("public original data document unavailable")
	}
	taskPanelWait(t, tt, func() bool { return !d.Busy() })
	if d.Page() == nil || d.Page().Editable == nil || !*d.Page().Editable || !d.Page().ColumnMetadata[0].Generated {
		t.Fatal("original JSON or derived ID edit binding")
	}
	s.dataWorkspace.EditCell(*services, d, 0, "id", contract.Cell{Kind: contract.Integer, Text: "1"})
	if d.HasDraft() {
		t.Fatal("derived native ID editable")
	}
	source := `{"id":9007199254740993,"n":9007199254740993,"猫":"🐱"}`
	s.dataWorkspace.EditCell(*services, d, 0, "document", contract.Cell{Kind: contract.JSON, Text: source})
	tt.Frame()
	if !d.HasDraft() || !s.dataWorkspace.CanSave(d) {
		t.Fatal("public JSON draft not staged")
	}
	captureMeilisearchDataWrite(t, s, "draft")
	if err = tt.Click(locales.Text("保存数据更改")); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !d.Busy() })
	if !d.HasDraft() || s.dataWorkspace.CanSave(d) || !strings.Contains(strings.Join(tt.Texts(), "\n"), locales.Text("任务已受理，尚未确认完成；草稿已保留，禁止重复提交")) {
		t.Fatal("native admission cleared draft or reported completion", tt.Texts())
	}
	captureMeilisearchDataWrite(t, s, "admitted")
	if err = tt.Click(locales.Text("写入任务证据")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err = tt.Click(locales.Text("复制写入任务受理")); err != nil {
		t.Fatal(err)
	}
	admission := tt.Clipboard()
	if !strings.Contains(admission, `"taskUid"`) || !strings.Contains(admission, uid) {
		t.Fatal("original admission evidence unavailable")
	}
	captureMeilisearchDataWrite(t, s, "evidence")
	if err = tt.Click(locales.Text("关闭")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for d.HasDraft() && time.Now().Before(deadline) {
		if err = tt.Click(locales.Text("观察任务结果")); err != nil {
			t.Fatal(err)
		}
		taskPanelWait(t, tt, func() bool { return !d.Busy() })
		time.Sleep(20 * time.Millisecond)
	}
	if d.HasDraft() || s.dataWorkspace.CanSave(d) || d.Page() == nil || strings.Contains(d.Page().Rows[0][1].Text, `"old"`) || !strings.Contains(d.Page().Rows[0][1].Text, "9007199254740993") {
		t.Fatal("native success/readback did not settle grid")
	}
	raw, _, err := admin.ReadDocument(context.Background(), uid, "9007199254740993")
	if err != nil || strings.Contains(string(raw), `"old"`) {
		t.Fatal("public save failed to replace actual JSON", err)
	}
	if err = tt.Click(locales.Text("写入任务证据")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err = tt.Click(locales.Text("复制写入任务观察")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tt.Clipboard(), `"succeeded"`) {
		t.Fatal("terminal original task evidence missing")
	}
	if err = tt.Click(locales.Text("复制写入任务受理")); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != admission {
		t.Fatal("success replaced original admission")
	}
	if err = tt.Click(locales.Text("关闭")); err != nil {
		t.Fatal(err)
	}
	captureMeilisearchDataWrite(t, s, "saved")
	submit("PATCH", "/indexes/"+uid+"/settings", `{"filterableAttributes":["_geo"]}`)
	s.dataWorkspace.EditCell(*services, d, 0, "document", contract.Cell{Kind: contract.JSON, Text: `{"id":9007199254740993,"_geo":{"lat":"invalid","lng":0}}`})
	tt.Frame()
	if err = tt.Click(locales.Text("保存数据更改")); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !d.Busy() })
	deadline = time.Now().Add(30 * time.Second)
	for !strings.Contains(strings.Join(tt.Texts(), "\n"), locales.Text("任务失败或已取消；草稿已保留，禁止重放，请检查任务证据")) && time.Now().Before(deadline) {
		if err = tt.Click(locales.Text("观察任务结果")); err != nil {
			t.Fatal(err)
		}
		taskPanelWait(t, tt, func() bool { return !d.Busy() })
	}
	if !d.HasDraft() || s.dataWorkspace.CanSave(d) {
		t.Fatal("failed task cleared draft or permitted replay")
	}
	captureMeilisearchDataWrite(t, s, "failed")
	if err = tt.Click(locales.Text("关闭待核验文档")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	captureMeilisearchDataWrite(t, s, "guard")
	if err = tt.Click(locales.Text("继续观察")); err != nil {
		t.Fatal(err)
	}
	if s.currentTab().data != d || !d.HasDraft() {
		t.Fatal("close cancel discarded original draft")
	}
	if err = tt.Click(locales.Text("关闭待核验文档")); err != nil {
		t.Fatal(err)
	}
	if err = tt.Click(locales.Text("关闭并放弃本地草稿与证据")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if s.currentTab().data == d || d.Page() != nil || !s.runtime.IsConnected(p.ID) {
		t.Fatal("explicit failed-document close retained original lease or closed session")
	}
	t.Log("public qualified original shared grid: immutable derived ID, native complete JSON replacement, admission retained drafts, explicit task observation/readback, full original receipt/task copying and bilingual wide/narrow presentation verified")
}
