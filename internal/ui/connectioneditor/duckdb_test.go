package connectioneditor

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestDuckDBEditorPersistsFileOptionsAndRevokesOldPicker(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	if err := s.OpenNew(connections.DuckDB, ""); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.State.View(c, 720, 520) }, 720, 520)
	setEngineOptionInput(t, tt, "连接名称", "DuckDB file")
	path := filepath.Join(t.TempDir(), "data.duckdb")
	s.SelectEngineFile(DuckDBMainFile, -1, s.EngineFileVersion(), path)
	tt.Frame()
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	setEngineOptionInput(t, tt, "DuckDB 内存限制", "256")
	setEngineOptionInput(t, tt, "DuckDB 工作线程数", "2")
	setEngineOptionInput(t, tt, "后台连接上限", "3")
	setEngineOptionInput(t, tt, "SQL 会话上限", "4")
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatalf("save: %v %s", err, s.Error())
	}
	p := profiles[0]
	if p.Kind != connections.DuckDB || p.Database != path || p.DuckDBOptions.MemoryLimitMB != 256 || p.DuckDBOptions.Threads != 2 || p.DuckDBOptions.MaxConnections != 3 || p.DuckDBOptions.SQLMaxConnections != 4 {
		t.Fatalf("persisted options: %+v", p)
	}
	s.OpenProfile(p)
	version := s.EngineFileVersion()
	s.Cancel()
	s.OpenProfile(p)
	s.SelectEngineFile(DuckDBMainFile, -1, version, "wrong.duckdb")
	if s.connectionDraft.Database != path {
		t.Fatal("old file picker replaced the reopened draft")
	}
}

func TestDuckDBAsyncCheckCapturesOptionsAndClosesCleanly(t *testing.T) {
	started := make(chan connections.Profile, 1)
	finish := make(chan struct{})
	t.Cleanup(func() { close(finish) })
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, password string) error {
		started <- p
		<-finish
		return nil
	}})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.DuckDB, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "DuckDB async"
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, 720, 520) }, 720, 520)
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-started:
		if p.Kind != connections.DuckDB || p.Database != ":memory:" || p.DuckDBOptions.DefaultSchema != "main" {
			t.Fatalf("captured profile: %+v", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("connection check did not start")
	}
	s.Cancel()
	s.OpenNew(connections.SQLite, "")
	tt.Frame()
	if s.connectionTesting || s.connectionTestResults != nil {
		t.Fatal("closed DuckDB check retained its callbacks")
	}
}
