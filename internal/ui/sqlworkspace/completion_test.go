package sqlworkspace

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestSQLCompletionUsesOriginalScopeAndInsertsOnlyQualifiedPrefix(t *testing.T) {
	for _, tc := range []struct{ dialect, marked, want string }{
		{"mysql", "SELECT u.em| FROM users u", "SELECT u.email FROM users u"},
		{"postgres", "SELECT u.odd| FROM users u", "SELECT u.\"odd column\" FROM users u"},
		{"postgres", "SELECT u.\"odd|\" FROM users u", "SELECT u.\"odd column\" FROM users u"},
	} {
		t.Run(tc.dialect+tc.marked, func(t *testing.T) {
			calls := 0
			s := New(Dependencies{CompletionCatalog: func(id int64, db string) sqltext.CompletionCatalog {
				calls++
				if id != 7 || db != "original" {
					t.Fatalf("completion retargeted profile %d / %s", id, db)
				}
				return sqltext.CompletionCatalog{Dialect: tc.dialect, Namespace: db, Objects: []sqltext.CompletionObject{{Namespace: db, Name: "users", Columns: []string{"email", "odd column"}}}}
			}})
			t.Cleanup(s.CloseAll)
			offset := strings.Index(tc.marked, "|")
			d := s.Open(2, strings.Replace(tc.marked, "|", "", 1), 7, "original", false)
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d) }, 1100, 700)
			tt.Frame()
			if err := tt.Click("SQL 编辑器"); err != nil {
				t.Fatal(err)
			}
			d.Editor.Select(offset, offset)
			// MyGo binds completion to Control+Space on every platform.
			tt.Key(ui.Ctrl, ui.KeySpace)
			tt.Key(0, ui.KeyTab)
			if got := d.Editor.Buffer.String(); got != tc.want || calls == 0 {
				t.Fatalf("completion insertion %q, want %q (calls %d)", got, tc.want, calls)
			}
			d.Editor.Run(editor.Undo)
			tt.Frame()
			if got := d.Editor.Buffer.String(); got != strings.Replace(tc.marked, "|", "", 1) {
				t.Fatalf("completion was not one undoable edit: %q", got)
			}
			version := d.Editor.Buffer.Version()
			d.Editor.Insert(" changed")
			calls = 0
			s.completions(d, editor.CompletionRequest{Offset: offset, Version: version})
			if calls != 0 {
				t.Fatal("old completion request inspected a newer document")
			}
			version = d.Editor.Buffer.Version()
			s.Close(d)
			s.completions(d, editor.CompletionRequest{Offset: offset, Version: version})
			if calls != 0 {
				t.Fatal("closed document retained completion callbacks")
			}
		})
	}
}
