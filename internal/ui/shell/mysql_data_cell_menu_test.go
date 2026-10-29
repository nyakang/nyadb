package shell

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestMySQLCellMenuQueriesServerAndSynchronizesRuleDrafts(t *testing.T) {
	s, tt := structureTestBrowser(t)
	requests := make(chan mysqlsvc.MySQLDataOptions, 10)
	s.mysqlDataAdvancedLoader = func(_ context.Context, _ int64, db, table string, o mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error) {
		requests <- o
		return &mysqlsvc.MySQLDataPage{Database: db, Table: table, Limit: o.Limit, Offset: o.Offset, StableOrder: true,
			Columns: []string{"id", "note", "score"}, ColumnKinds: []string{"bigint", "varchar", "int"},
			Rows: [][]mysqlsvc.MySQLDataCell{{{Text: "1"}, {Text: "alpha"}, {Text: "7"}}, {{Text: "2"}, {Null: true}, {Text: "8"}}}}, nil
	}
	if err := tt.Click("打开选中表数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	<-requests
	doc := s.currentTab()
	doc.dataFilterColumn, doc.dataFilterMode, doc.dataFilterValue = "score", "equal", "7"
	doc.dataSortColumn, doc.dataSort = "id", ui.SortOrder{Column: "id"}
	if err := tt.RightClick("alpha"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("筛选", "字段 = 值"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	o := <-requests
	if len(o.Filters) != 2 || o.Filters[0].Column != "score" || o.Filters[1].Column != "note" || o.Filters[1].Value != "alpha" ||
		o.SortColumn != "id" || o.FilterMode != "" || o.Offset != 0 || o.After != nil || !o.UseKeyset {
		t.Fatalf("quick filter dropped rules or did not query from the first page: %+v", o)
	}
	if doc.dataFilterColumnDraft != "score" || len(doc.dataFilterExtraDraft) != 1 || doc.dataFilterExtraDraft[0].Mode != "精确匹配" {
		t.Fatal("quick filter was not reflected in the rule editor")
	}
	if err := tt.RightClick("NULL"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("筛选", "字段 = 值"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	o = <-requests
	if len(o.Filters) != 2 || o.Filters[1].Mode != "is_null" || o.Filters[1].Value != "" || doc.dataFilterExtraDraft[0].Mode != "为空" {
		t.Fatalf("NULL was compared as a string or appended as a contradictory rule: %+v", o.Filters)
	}
	if err := tt.RightClick("alpha"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("排序", "降序排序"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	o = <-requests
	if o.SortColumn != "note" || !o.SortDescending || len(o.Filters) != 2 || len(doc.dataSortDraft) != 1 || doc.dataSortDraft[0].Direction != "降序" {
		t.Fatalf("context sort did not reach the server or preserve filters: %+v", o)
	}
	if err := tt.RightClick("alpha"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("排序", "移除所有排序"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	o = <-requests
	if o.SortColumn != "" || len(o.Sorts) != 0 || len(o.Filters) != 2 || len(doc.dataSortDraft) != 0 {
		t.Fatal("remove sorts cleared filters or retained a stale sort draft")
	}
	// Preview data and binary hex values cannot become value comparisons.
	doc.dataPage.Rows[0][1].Truncated = true
	tt.Frame()
	if err := tt.RightClick("alpha"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("筛选", "字段 = 值"); err == nil {
		t.Fatal("truncated value comparison was enabled")
	}
	if len(requests) != 0 || doc.dataLoading {
		t.Fatal("disabled quick filter launched a query")
	}
}

func TestMySQLCellFilterLimitAndBinaryPreviewBoundaries(t *testing.T) {
	for _, kind := range []string{"blob", "binary", "bit", "point", "linestring", "multipolygon"} {
		if mysqlDataCellFilterValueAvailable(kind, mysqlsvc.MySQLDataCell{Text: "0x1234"}) {
			t.Fatalf("%s hex preview accepted as a field value", kind)
		}
	}
	s := New()
	doc := alignedDataTestTab()
	for _, name := range []string{"a", "b", "c", "d", "e", "f"} {
		doc.dataFilterRules = append(doc.dataFilterRules, mysqlsvc.MySQLDataFilterRule{Column: name, Mode: "is_null"})
	}
	s.applyMySQLDataCellFilter(ui.Services{}, doc, "label", "equal", mysqlsvc.MySQLDataCell{Text: "alpha"})
	if len(doc.dataFilterRules) != 6 || doc.dataRuleError == "" || doc.dataLoading || doc.dataPage == nil {
		t.Fatal("over-limit filter mutated the applied query or launched a load")
	}
}
