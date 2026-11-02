package sqlworkspace

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestSQLKeyboardRunsCaretAndAllWithoutChangingSelection(t *testing.T) {
	s, _ := historyTestState(t)
	d := s.Open(2, "SELECT 1;\nSELECT 2;", 7, "app", false)
	statements := make(chan string, 3)
	s.Executor = func(_ context.Context, id int64, db, sql string) (contract.SQLResult, error) {
		statements <- sql
		return contract.SQLResult{}, nil
	}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.ConsumeDocuments(c.Services())
		s.Consume()
		s.View(c, d)
		s.Modals(c)
	}, 1280, 800)
	tt.Frame()
	if err := tt.Click("SQL 编辑器"); err != nil {
		t.Fatal(err)
	}
	d.Editor.Select(11, 11)
	selection := d.Editor.Selection()
	tt.Key(commandModifier(), ui.KeyEnter)
	waitSQLUI(t, tt, d)
	if sql := <-statements; sql != "SELECT 2;" {
		t.Fatalf("caret shortcut ran %q", sql)
	}
	if d.Editor.Selection() != selection {
		t.Fatal("execution shortcut inserted a newline or moved selection")
	}
	tt.Key(0, ui.KeyF5)
	waitSQLUI(t, tt, d)
	if sql := <-statements; sql != "SELECT 1;\nSELECT 2;" {
		t.Fatalf("run-all shortcut ran %q", sql)
	}
	if d.Editor.Selection() != selection {
		t.Fatal("run all selected or replaced SQL text")
	}
	tt.Key(commandModifier(), ui.KeyL)
	if !s.history.open {
		t.Fatal("history shortcut was consumed by the editor")
	}
	tt.Key(commandModifier(), ui.KeyEnter)
	select {
	case sql := <-statements:
		t.Fatalf("modal leaked execution shortcut: %q", sql)
	default:
	}
}
func TestSQLKeyboardSaveAndSaveAsPreserveCancelledDraft(t *testing.T) {
	s := New(Dependencies{})
	t.Cleanup(s.CloseAll)
	d := s.Open(2, "old", 0, "", false)
	d.path = "original.sql"
	writes := make(chan string, 1)
	s.SetPersistence(nil, Files{Write: func(path, text string) error { writes <- path + ":" + text; return nil }, SavePath: func(string) (string, error) { return "", nil }}, nil)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.ConsumeDocuments(c.Services()); s.View(c, d); s.Modals(c) }, 1100, 700)
	tt.Frame()
	if err := tt.Click("SQL 编辑器"); err != nil {
		t.Fatal(err)
	}
	d.Editor.Select(0, d.Editor.Buffer.Len())
	d.Editor.Insert("SELECT saved")
	tt.Key(commandModifier(), ui.KeyS)
	waitDocumentIO(t, s)
	select {
	case written := <-writes:
		if written != "original.sql:SELECT saved" {
			t.Fatal("save shortcut targeted another file")
		}
	case <-time.After(time.Second):
		t.Fatal("save shortcut did not write")
	}
	if d.Dirty() {
		t.Fatal("saved text remained dirty")
	}
	d.Editor.Insert(" newer")
	text := d.Editor.Buffer.String()
	tt.Key(commandModifier()|ui.Shift, ui.KeyS)
	waitDocumentIO(t, s)
	if d.Editor.Buffer.String() != text || !d.Dirty() || d.path != "original.sql" {
		t.Fatal("cancelled save-as discarded text or changed file identity")
	}
}
