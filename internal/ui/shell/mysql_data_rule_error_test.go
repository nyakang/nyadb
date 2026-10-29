package shell

import (
	"context"
	"errors"
	"testing"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestMySQLRuleEditorRemainsUsableAfterQueryError(t *testing.T) {
	s, tt := structureTestBrowser(t)
	s.mysqlDataAdvancedLoader = func(_ context.Context, _ int64, db, table string, o mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error) {
		if o.FilterMode == "contains" {
			return nil, errors.New("unsupported filter type")
		}
		return &mysqlsvc.MySQLDataPage{
			Database: db, Table: table, Limit: o.Limit, Offset: o.Offset, StableOrder: true,
			Columns: []string{"id", "note"}, ColumnKinds: []string{"int", "varchar"},
			Rows: [][]mysqlsvc.MySQLDataCell{{{Text: "1"}, {Text: "good"}}},
		}, nil
	}
	if err := tt.Click("打开选中表数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if err := tt.Click("筛选 & 排序"); err != nil {
		t.Fatal(err)
	}
	tab := s.currentTab()
	tab.dataFilterColumnDraft = "id"
	tab.dataFilterModeDraft = "包含"
	tab.dataFilterValueDraft = "1"
	tt.Frame()
	if err := tt.Click("应用筛选"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if tab.dataPage != nil || tab.dataErr == "" {
		t.Fatal("expected query error")
	}
	if !tt.HasText("筛选 & 排序") {
		t.Fatal("lost editor on server error")
	}
	if err := tt.Click("清除筛选"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if tab.dataErr != "" || tab.dataPage == nil || !tt.HasText("good") {
		t.Fatalf("could not recover by clearing filters: %+v", tab)
	}
}
