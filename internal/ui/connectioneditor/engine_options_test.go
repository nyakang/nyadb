package connectioneditor

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestSQLiteAttachmentEditorPreservesDraftAndRevokesFileCompletions(t *testing.T) {
	dir := t.TempDir()
	p, _ := connections.Defaults(connections.SQLite)
	p.Name, p.Database = "attached", filepath.Join(dir, "main.sqlite")
	p.SQLiteOptions.Attachments = []connections.SQLiteAttachment{{Name: "aux", File: filepath.Join(dir, "aux.sqlite")}}
	s := New(Dependencies{})
	s.OpenProfile(p)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, 720, 520) }, 720, 520)
	if err := tt.Click("附加的数据库"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("添加附加库"); err != nil {
		t.Fatal(err)
	}
	setEngineOptionInput(t, tt, "附加库名称", "second")
	setEngineOptionInput(t, tt, "附加库文件", filepath.Join(dir, "second.sqlite"))
	if err := tt.Click("附加库只读"); err != nil {
		t.Fatal(err)
	}
	draft, err := s.draftProfile()
	if err != nil || len(draft.SQLiteOptions.Attachments) != 2 || draft.SQLiteOptions.Attachments[1].Name != "second" || !draft.SQLiteOptions.Attachments[1].ReadOnly {
		t.Fatalf("attachment draft: %+v %v", draft, err)
	}
	if len(p.SQLiteOptions.Attachments) != 1 || p.SQLiteOptions.Attachments[0].Name != "aux" {
		t.Fatal("editor changed original saved profile")
	}
	draft.SQLiteOptions.Attachments[0].Name = "changed outside"
	if s.connectionDraft.SQLiteOptions.Attachments[0].Name != "aux" {
		t.Fatal("worker draft shares mutable input")
	}
	version := s.EngineFileVersion()
	if err := tt.Click("移除附加库"); err != nil {
		t.Fatal(err)
	}
	s.SelectEngineFile(SQLiteAttachedFile, 0, version, filepath.Join(dir, "wrong.sqlite"))
	if s.connectionDraft.SQLiteOptions.Attachments[0].File != p.SQLiteOptions.Attachments[0].File {
		t.Fatal("late selection changed different attachment")
	}
	version = s.EngineFileVersion()
	s.Cancel()
	s.OpenProfile(p)
	s.SelectEngineFile(SQLiteMainFile, -1, version, filepath.Join(dir, "wrong-main.sqlite"))
	s.EngineFilePickerError(version)
	if s.connectionDraft.Database != p.Database || s.Error() != "" {
		t.Fatal("old native chooser completion changed reopened editor")
	}
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	setEngineOptionInput(t, tt, "SQLite 锁等待超时", "broken")
	if _, err := s.draftProfile(); err == nil || s.sqliteBusyTimeout != "broken" {
		t.Fatal("invalid number was silently replaced")
	}
}

func TestPostgreSQLAdvancedAndTLSInputsReachableAndCapturedForTest(t *testing.T) {
	started := make(chan connections.Profile, 1)
	finish := make(chan struct{})
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, password string) error {
		started <- p
		<-finish
		return nil
	}})
	t.Cleanup(func() { close(finish) })
	if err := s.OpenNew(connections.PostgreSQL, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "PostgreSQL"
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, 720, 520) }, 720, 520)
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []struct{ label, text string }{{"连接超时（秒）", "8"}, {"最大连接数", "3"}, {"语句超时（毫秒）", "2000"}, {"锁等待超时（毫秒）", "500"}, {"默认模式", "tenant"}} {
		setEngineOptionInput(t, tt, value.label, value.text)
	}
	if err := tt.Click("SSL"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"PostgreSQL TLS 模式", "PostgreSQL CA 证书", "PostgreSQL 客户端证书", "PostgreSQL 客户端密钥", "保存连接", "取消连接向导"} {
		r, ok := tt.Find(name)
		if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > 720 || r.Y+r.H > 520 {
			t.Fatalf("control clipped: %s %+v", name, r)
		}
	}
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-started:
		if p.PostgreSQLOptions.ConnectTimeoutSeconds != 8 || p.PostgreSQLOptions.MaxConnections != 3 || p.PostgreSQLOptions.DefaultSchema != "tenant" || p.PostgreSQLOptions.StatementTimeoutMS != 2000 {
			t.Fatalf("test captured wrong options: %+v", p.PostgreSQLOptions)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("provider test did not start")
	}
	s.Cancel()
	s.OpenNew(connections.SQLite, "")
	tt.Frame()
	if s.connectionTestResults != nil || s.connectionTesting || strings.Contains(s.connectionTestMessage, "PostgreSQL") {
		t.Fatal("closed test retained callbacks")
	}
}

func setEngineOptionInput(t *testing.T, tt *ui.Tester, label, value string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Type(value)
}
