package sqlworkspace

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestRestoredSQLShowsOriginalScopeAndUsesItsDialectWithoutExecution(t *testing.T) {
	formats, completions, executions := 0, 0, 0
	s := New(Dependencies{
		Connected: func(int64) bool { return true },
		FormatSQL: func(profile int64, source string) (string, error) {
			formats++
			if profile != 7 {
				t.Fatalf("restored format used profile %d", profile)
			}
			return sqltext.FormatClickHouse(source)
		},
		CompletionCatalog: func(profile int64, database string) sqltext.CompletionCatalog {
			completions++
			if profile != 7 || database != "original" {
				t.Fatalf("restored completion used %d / %s", profile, database)
			}
			return sqltext.CompletionCatalog{Dialect: "clickhouse", Namespace: database}
		},
	})
	t.Cleanup(s.CloseAll)
	s.Executor = func(context.Context, int64, string, string) (contract.SQLResult, error) {
		executions++
		return contract.SQLResult{}, nil
	}
	d := s.RestoreDocument(queries.Document{ID: 2, ProfileID: 7, Database: "original", Text: "--heading\nselect 1;"})
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d) }, 1100, 700)
	tt.Frame()
	if !tt.HasText("连接 #7 · original") || tt.HasText("请连接数据库后新建查询") {
		t.Fatal("restored document hid its original scope or offered misleading guidance")
	}
	s.Format(d)
	s.completions(d, editor.CompletionRequest{Version: d.Editor.Buffer.Version(), Offset: d.Editor.Buffer.Len()})
	s.Run(nil, d)
	if formats != 1 || completions != 1 || executions != 0 || d.profileID != 0 || !d.NeedsRecovery() || d.lease != nil {
		t.Fatal("read-only editing changed the recovery state or executed SQL")
	}
}
