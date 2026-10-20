package shell

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestConnectionDialogChooseBackCancel(t *testing.T) {
	s := New()
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("连接"); err != nil {
		t.Fatal(err)
	}
	if !s.connectionDialog || s.connectionStep != 0 || !tt.HasText("选择一个连接类型：") {
		t.Fatal("connection action did not open chooser")
	}
	if err := tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionStep != 0 || !strings.Contains(s.connectionError, "选择数据库") {
		t.Fatalf("missing selection was not rejected: step=%d error=%q open=%t", s.connectionStep, s.connectionError, s.connectionDialog)
	}
	if err := tt.Click("选择 PostgreSQL"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionStep != 1 || s.connectionDraft.Kind != connections.PostgreSQL || s.connectionPort != "5432" ||
		s.connectionDraft.Database != "postgres" {
		t.Fatalf("unexpected PostgreSQL form: %+v", s.connectionDraft)
	}
	if err := tt.Click("上一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionStep != 0 {
		t.Fatal("back did not restore type chooser")
	}
	if err := tt.Click("取消连接向导"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDialog || s.connectionSecret != "" {
		t.Fatal("cancel should close and clear session secret")
	}
}

func TestConnectionDraftValidation(t *testing.T) {
	s := New()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionDraft.Name = "test"
	s.connectionPort = "99999"
	if _, err := s.draftProfile(); err == nil {
		t.Fatal("invalid port accepted")
	}
	s.connectionPort = "3306"
	s.connectionDraft.Host = ""
	if _, err := s.draftProfile(); err == nil {
		t.Fatal("empty host accepted")
	}
	s.connectionDraft.Host = "localhost"
	if _, err := s.draftProfile(); err != nil {
		t.Fatal(err)
	}
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.SQLite)
	s.nextConnectionStep()
	s.connectionDraft.Name = "local"
	if _, err := s.draftProfile(); err == nil {
		t.Fatal("empty SQLite file accepted")
	}
	s.connectionDraft.Database = "/tmp/example.sqlite"
	if _, err := s.draftProfile(); err != nil {
		t.Fatal(err)
	}
	s.connectionSecret = "secret"
	s.closeConnectionDialog()
	if s.connectionSecret != "" {
		t.Fatal("secret left after dialog")
	}
}
