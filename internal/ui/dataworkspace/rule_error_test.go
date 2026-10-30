package dataworkspace

import (
	"context"
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestMySQLRuleEditorRemainsUsableAfterQueryError(t *testing.T) {
	s, doc, tt := dataTestWorkspace(t)
	s.AdvancedLoader = func(_ context.Context, _ int64, db, table string, o mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error) {
		if o.FilterMode == "contains" {
			return nil, errors.New("unsupported filter type")
		}
		return &mysqlsvc.MySQLDataPage{
			Database: db, Table: table, Limit: o.Limit, Offset: o.Offset, StableOrder: true,
			Columns: []string{"id", "note"}, ColumnKinds: []string{"int", "varchar"},
			Rows: [][]mysqlsvc.MySQLDataCell{{{Text: "1"}, {Text: "good"}}},
		}, nil
	}
	s.Load(ui.Services{}, doc, false)
	tt.Frame()
	waitMySQLData(t, s, doc, tt)
	if err := tt.Click("筛选 & 排序"); err != nil {
		t.Fatal(err)
	}
	Document := doc
	Document.filterColumnDraft = "id"
	Document.filterModeDraft = "包含"
	Document.filterValueDraft = "1"
	tt.Frame()
	if err := tt.Click("应用筛选"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, doc, tt)
	if Document.page != nil || Document.err == "" {
		t.Fatal("expected query error")
	}
	if !tt.HasText("筛选 & 排序") {
		t.Fatal("lost editor on server error")
	}
	if err := clickDataToolbar(tt, "清除筛选"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, doc, tt)
	if Document.err != "" || Document.page == nil || !tt.HasText("good") {
		t.Fatalf("could not recover by clearing filters: %+v", Document)
	}
}
