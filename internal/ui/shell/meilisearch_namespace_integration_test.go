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
	"github.com/nyakang/nyadb/locales"
)

func namespaceCapture(t *testing.T, s *State, action, phase string) {
	t.Helper()
	output := os.Getenv("NYADB_MEILISEARCH_NAMESPACE_IMAGES")
	if output == "" {
		return
	}
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(s.View, size[0], size[1])
			label := "读取原索引信息"
			if phase == "preview" {
				label = "确认提交索引操作"
			}
			if phase == "guard" {
				label = "继续编辑"
			}
			if phase == "receipt" || phase == "result" {
				if e := tt.Click(locales.Text("管理回执与结果")); e != nil {
					t.Fatal(e)
				}
				label = "复制原管理请求"
			}
			r, ok := tt.Find(locales.Text(label))
			if !ok || r.H <= 0 || r.Y < 0 || r.Y+r.H > float32(size[1])-18 {
				t.Fatal("localized namespace control outside viewport", phase, language, r)
			}
			folder := filepath.Join(output, language)
			if e := os.MkdirAll(folder, 0700); e != nil {
				t.Fatal(e)
			}
			writeDomainCapture(t, folder, fmt.Sprintf("namespace-%s-%s-%dx%d", action, phase, size[0], size[1]), tt)
		}
	}
}
func TestMeilisearchNativePublicNamespaceFormsSixActionsAndEvidence(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	cfg, _ := connections.MeilisearchProfileConfig(parsed.Profile)
	admin, e := meilisearch.Open(context.Background(), cfg, parsed.Password)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	uid := "owned_namespace_ui_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	other, renamed := uid+"_other", uid+"_renamed"
	submit := func(path, body string) {
		r, _, e := admin.Submit(context.Background(), meilisearch.Request{Method: "POST", Path: path, Body: []byte(body)})
		if e == nil {
			_, e = admin.WaitTask(context.Background(), r, 30*time.Second)
		}
		if e != nil {
			t.Fatal(e)
		}
	}
	submit("/indexes", `{"uid":"`+other+`","primaryKey":"sku"}`)
	submit("/indexes/"+other+"/documents", `[{"sku":"other","text":"另一个🐱","n":9007199254740995}]`)
	s, tt, _, p, services := keyPanelState(t, parsed.Profile, parsed.Password)
	defer s.CloseMySQLSessions()
	if e = tt.Click("工具"); e != nil {
		t.Fatal(e)
	}
	if e = tt.ChooseMenuItem("管理索引…"); e != nil {
		t.Fatal("public namespace menu missing", e)
	}
	d := s.currentTab().object
	managementID := s.currentTab().id
	if d == nil {
		t.Fatal("namespace menu did not open task page")
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	ops := []struct{ action, label, uid, dest, pk string }{
		{"create", "创建索引", uid, "", ""},
		{"primary_key", "修改索引主键", uid, "", "id"},
		{"rename", "重命名索引", uid, renamed, ""},
		{"swap", "原子交换两个索引", renamed, other, ""},
		{"clear", "清空索引文档", renamed, "", ""},
		{"delete", "删除索引", renamed, "", ""},
	}
	var settingsID, dataID, siblingID, sqlID uint64
	for i, op := range ops {
		if i > 0 {
			if !s.objectWorkspace.OpenNamespaceManagement(d) {
				t.Fatal("namespace editor did not reopen", d.Error())
			}
			tt.Frame()
		}
		if e = tt.Click("索引操作"); e != nil {
			t.Fatal(e)
		}
		if e = tt.Click(op.label); e != nil {
			t.Fatal("namespace operation chooser", op.label, e)
		}
		taskPanelField(t, tt, "索引 UID", op.uid)
		if op.dest != "" {
			taskPanelField(t, tt, "目标索引 UID", op.dest)
		}
		if op.pk != "" {
			taskPanelField(t, tt, "主键字段（创建时可留空）", op.pk)
		}
		if e = tt.Click("读取原索引信息"); e != nil {
			t.Fatal(e)
		}
		taskPanelWait(t, tt, func() bool { return !d.Loading() })
		if d.Error() != "" {
			t.Fatal("native original namespace form read", d.Error())
		}
		namespaceCapture(t, s, op.action, "form")
		tt.Frame()
		if e = tt.Click("预览索引操作"); e != nil {
			t.Fatal(e)
		}
		if !tt.HasText("确认提交索引操作") {
			t.Fatal("native namespace preview missing", d.Error())
		}
		confirm := op.uid
		if op.dest != "" {
			confirm += " / " + op.dest
		}
		taskPanelField(t, tt, "索引操作确认", confirm)
		namespaceCapture(t, s, op.action, "preview")
		tt.Frame()
		if e = tt.Click("确认提交索引操作"); e != nil {
			t.Fatal(e)
		}
		if op.action == "rename" {
			if d.Loading() || !tt.HasText("保留原索引设置草稿？") {
				t.Fatal("namespace submission bypassed original settings draft guard")
			}
			namespaceCapture(t, s, op.action, "guard")
			tt.Frame()
			if e = tt.Click("继续编辑"); e != nil {
				t.Fatal(e)
			}
			if d.Loading() {
				t.Fatal("canceling original scope leave dispatched rename")
			}
			s.selectTab(managementID)
			tt.Frame()
			if e = tt.Click("确认提交索引操作"); e != nil {
				t.Fatal(e)
			}
			if e = tt.Click("放弃本地草稿并离开"); e != nil {
				t.Fatal(e)
			}
			for _, tab := range s.tabs {
				if tab.id == settingsID || tab.id == dataID {
					t.Fatal("settled affected namespace documents retained")
				}
			}
			if !s.selectTab(siblingID) {
				t.Fatal("namespace guard disposed sibling document")
			}
			if !s.selectTab(sqlID) || s.currentTab().query.Editor.Buffer.String() != "GET /indexes/original_explicit_request" {
				t.Fatal("namespace guard changed SQL/REST text")
			}
			s.selectTab(managementID)
		}
		taskPanelWait(t, tt, func() bool { return !d.Loading() })
		r := s.objectWorkspace.TaskManagementResult(d)
		if r.State != "enqueued" || r.Namespace == nil || r.NamespaceVerified || r.Receipt.Raw == "" {
			t.Fatal("public namespace admission", op.action, d.Error(), r)
		}
		namespaceCapture(t, s, op.action, "receipt")
		tt.Frame()
		if e = taskPanelAction(tt, "观察原管理任务（30 秒）", "查看任务详情"); e != nil {
			t.Fatal(e)
		}
		taskPanelWait(t, tt, func() bool { return !d.Loading() })
		r = s.objectWorkspace.TaskManagementResult(d)
		if r.State != "succeeded" || !r.NamespaceVerified {
			t.Fatal("public namespace readback", op.action, d.Error(), r)
		}
		taskPanelWait(t, tt, func() bool { return !s.sessions.Current().Refreshing() })
		namespaceCapture(t, s, op.action, "result")
		tt.Frame()
		if e = taskPanelAction(tt, "复制索引操作完整证据", "复制原管理请求"); e != nil {
			t.Fatal(e)
		}
		copy := tt.Clipboard()
		if !strings.Contains(copy, r.Request) || !strings.Contains(copy, r.TaskRaw) || !strings.Contains(copy, r.Namespace.Draft.Ref.Catalog) || !strings.Contains(copy, "StatsRaw") {
			t.Fatal("complete namespace evidence copy missing")
		}
		snapshot := string(s.objectWorkspace.DocumentSnapshot(d))
		if strings.Contains(snapshot, op.uid) || strings.Contains(snapshot, r.Receipt.Raw) {
			t.Fatal("namespace observations persisted")
		}
		if op.action == "primary_key" {
			submit("/indexes/"+uid+"/documents", `[{"id":"original","text":"原始🐱","n":9007199254740993}]`)
			s.nextTabID++
			dataID = s.nextTabID
			data := s.dataWorkspace.OpenObject(dataID, contract.ObjectRef{ProfileID: p.ID, Catalog: uid, Kind: "documents", Name: "documents"}, contract.Capabilities{Documents: true, EditableTables: true, Metadata: true, Count: true, Export: true})
			if data == nil || data.Error() != "" {
				t.Fatal("original affected native data lease", data)
			}
			s.tabs = append(s.tabs, &tab{id: dataID, kind: tabMySQLData, title: "owned data", data: data})
			s.nextTabID++
			siblingID = s.nextTabID
			sibling := s.objectWorkspace.OpenQualifiedStructure(siblingID, contract.ObjectRef{ProfileID: p.ID, Catalog: other, Kind: "index", Name: other}, "索引信息")
			s.tabs = append(s.tabs, &tab{id: siblingID, kind: tabStructure, title: "sibling", object: sibling})
			s.openQuery()
			sqlID = s.currentTab().id
			s.currentTab().query.Editor.Buffer.Set("GET /indexes/original_explicit_request")
			s.nextTabID++
			settingsID = s.nextTabID
			settings := s.objectWorkspace.OpenSettings(settingsID, contract.ObjectRef{ProfileID: p.ID, Catalog: uid, Kind: "settings", Name: "settings"})
			if settings == nil {
				t.Fatal("original affected settings document")
			}
			s.tabs = append(s.tabs, &tab{id: settingsID, kind: tabStructure, title: "owned settings", object: settings})
			s.selectTab(settingsID)
			s.objectWorkspace.LoadSettings(settings, services.Invalidate, true)
			taskPanelWait(t, tt, func() bool { return !settings.Loading() })
			s.objectWorkspace.EditSettings(settings)
			tt.Frame()
			taskPanelField(t, tt, "完整设置草稿", `{"stopWords":["unsaved_owned"]}`)
			if !settings.DesignDirty() {
				t.Fatal("original settings draft missing")
			}
			s.selectTab(managementID)
		}
	}
	response, e := admin.Do(context.Background(), meilisearch.Request{Method: "GET", Path: "/indexes/" + other + "/documents/original"})
	if e != nil || !strings.Contains(string(response.Body), "9007199254740993") {
		t.Fatal("public swap/clear/drop changed sibling exact contents", e)
	}
	if s.sessions.Current().ProfileID != p.ID || s.currentTab().object != d {
		t.Fatal("namespace result changed original profile or task document")
	}
	_ = services
	t.Log("public menu and six editable native namespace forms, original metadata previews, typed UID/pair confirmation, asynchronous admission, explicit task plus metadata/count verification, exact sibling contents and full evidence copying; no workspace persistence")
}
