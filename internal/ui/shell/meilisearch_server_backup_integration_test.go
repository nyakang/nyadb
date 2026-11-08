package shell

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/locales"
)

func serverBackupCapture(t *testing.T, s *State, action, phase string) {
	t.Helper()
	output := os.Getenv("NYADB_MEILISEARCH_BACKUP_IMAGES")
	if output == "" {
		return
	}
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(s.View, size[0], size[1])
			label := "确认创建服务器 " + action
			if phase == "receipt" || phase == "result" {
				if err := tt.Click(locales.Text("管理回执与结果")); err != nil {
					t.Fatal(err)
				}
				label = "复制原管理请求"
			}
			r, ok := tt.Find(locales.Text(label))
			if !ok || r.H <= 0 || r.Y < 0 || r.Y+r.H > float32(size[1])-18 {
				t.Fatal("localized backup control outside viewport", phase, language, r)
			}
			folder := filepath.Join(output, language)
			if err := os.MkdirAll(folder, 0700); err != nil {
				t.Fatal(err)
			}
			writeDomainCapture(t, folder, fmt.Sprintf("server-backup-%s-%s-%dx%d", action, phase, size[0], size[1]), tt)
		}
	}
}
func TestMeilisearchNativePublicServerBackupPreviewAdmissionAndArtifactEvidence(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	s, _, profiles, _ := profileTestState(t)
	defer s.CloseMySQLSessions()
	p := parsed.Profile
	p.ID, p.Name = 0, "Native server backups"
	var err error
	p, err = profiles.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	s.sessions.Current().Password = parsed.Password
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	if err = tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	s.openTasks(services, p.ID, "")
	d := s.currentTab().object
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	for _, action := range []string{"dump", "snapshot"} {
		tt.Frame()
		if err = taskPanelAction(tt, "创建服务器 "+action, "查看任务详情"); err != nil {
			t.Fatal(err)
		}
		serverBackupCapture(t, s, action, "preview")
		if err = tt.Click("确认创建服务器 " + action); err != nil {
			t.Fatal(err)
		}
		taskPanelWait(t, tt, func() bool { return !d.Loading() })
		result := s.objectWorkspace.TaskManagementResult(d)
		if result.State != "enqueued" || result.Artifact.Kind != "" || result.Receipt.Raw == "" || len(result.Targets) != 0 {
			t.Fatal("backup admission lost", d.Error(), result)
		}
		serverBackupCapture(t, s, action, "receipt")
		tt.Frame()
		if err = taskPanelAction(tt, "观察原管理任务（30 秒）", "查看任务详情"); err != nil {
			t.Fatal(err)
		}
		taskPanelWait(t, tt, func() bool { return !d.Loading() })
		result = s.objectWorkspace.TaskManagementResult(d)
		if result.State != "succeeded" || result.Artifact.Kind != action || result.CountsKnown || result.Request != "POST /"+map[string]string{"dump": "dumps", "snapshot": "snapshots"}[action] {
			t.Fatal("backup artifact lost", d.Error(), result)
		}
		serverBackupCapture(t, s, action, "result")
		tt.Frame()
		if err = tt.Click("复制原管理任务结果"); err != nil {
			t.Fatal(err)
		}
		if tt.Clipboard() != result.TaskRaw {
			t.Fatal("full backup task copy lost")
		}
		raw := string(s.objectWorkspace.DocumentSnapshot(d))
		if strings.Contains(raw, result.Receipt.Raw) || strings.Contains(raw, "dumpUid") {
			t.Fatal("backup evidence persisted")
		}
	}
	t.Log("public original global task toolbar: localized previews, native backup admission, explicit observation, original artifact JSON, full copy and no workspace persistence")
}
