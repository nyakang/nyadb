package shell

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func tableInfoSampleTab() *tab {
	doc := mysqlInspectorSampleTab()
	doc.kind, doc.dataProfileID = tabMySQLData, 8
	return doc
}

func tableInfoSampleState(doc *tab) *State {
	s := New()
	s.tabs = append(s.tabs, doc)
	s.activeTab = 1
	s.mysqlSessionProfileID, s.mysqlSessionStatus = 8, "已连接"
	return s
}

func tableInfoTester(s *State, doc *tab) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		s.consumeMySQLTableInfo()
		theme.Apply(c)
		s.mysqlDataObjectInspector(c, doc)
	}, 320, 720)
}

func waitTableInfo(t *testing.T, tt *ui.Tester, waiting func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for waiting() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		tt.Frame()
	}
	tt.Frame()
	if waiting() {
		t.Fatal("table inspector operation did not finish")
	}
}

func TestMySQLTableInspectorDefaultsToTableAndExplicitlySelectsField(t *testing.T) {
	doc := tableInfoSampleTab()
	s := tableInfoSampleState(doc)
	var counts atomic.Int32
	s.mysqlTableInfoLoader = func(context.Context, int64, string, string) (*connections.MySQLTableInfo, error) {
		rows := int64(21)
		return &connections.MySQLTableInfo{Database: doc.dataDatabase, Name: doc.dataTable, Kind: "BASE TABLE", Engine: "InnoDB", EstimatedRows: &rows}, nil
	}
	s.mysqlTableCountLoader = func(context.Context, int64, string, string) (int64, error) { counts.Add(1); return 37, nil }
	tt := tableInfoTester(s, doc)
	waitTableInfo(t, tt, func() bool { return doc.dataInfo.loading })
	if !tt.HasText("估算行数") || !tt.HasText("InnoDB") || counts.Load() != 0 {
		t.Fatalf("initial panel: %v", tt.Texts())
	}
	doc.dataSelectedRow = 0
	doc.dataState.Cells = ui.ColumnRange{Cursor: "label", Anchor: "label"}
	tt.Frame()
	if doc.dataInfo.fieldMode || !tt.HasText("估算行数") {
		t.Fatal("cell selection replaced table mode")
	}
	if err := tt.Click("字段信息"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !doc.dataInfo.fieldMode || !tt.HasText("varchar(80)") || !tt.HasText("sample") {
		t.Fatalf("field mode lost preview: %v", tt.Texts())
	}
	if err := tt.Click("表信息"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("获取整表行数"); err != nil {
		t.Fatal(err)
	}
	waitTableInfo(t, tt, func() bool { return doc.dataInfo.countLoading })
	if counts.Load() != 1 || doc.dataInfo.count == nil || *doc.dataInfo.count != 37 || !tt.HasText("行数统计不应用当前筛选条件") {
		t.Fatalf("count semantics: %v", tt.Texts())
	}
}

func TestMySQLTableInfoFailureDoesNotChangeDataOrCancelCount(t *testing.T) {
	doc := tableInfoSampleTab()
	s := tableInfoSampleState(doc)
	page := doc.dataPage
	started, release := make(chan struct{}), make(chan struct{})
	s.mysqlTableInfoLoader = func(context.Context, int64, string, string) (*connections.MySQLTableInfo, error) {
		return nil, errors.New("metadata denied")
	}
	s.mysqlTableCountLoader = func(ctx context.Context, _ int64, _, _ string) (int64, error) {
		close(started)
		select {
		case <-release:
			return 0, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	tt := tableInfoTester(s, doc)
	waitTableInfo(t, tt, func() bool { return doc.dataInfo.loading })
	if doc.dataPage != page || doc.dataErr != "" || !tt.HasText("metadata denied") {
		t.Fatal("metadata failure affected data page")
	}
	if err := tt.Click("获取整表行数"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("count did not start")
	}
	s.cancelMySQLData(doc)
	if !doc.dataInfo.countLoading {
		t.Fatal("data cancellation canceled independent count")
	}
	close(release)
	waitTableInfo(t, tt, func() bool { return doc.dataInfo.countLoading })
	if doc.dataInfo.count == nil || *doc.dataInfo.count != 0 {
		t.Fatal("exact zero count was lost")
	}
}

func TestMySQLTableCountCancelPreservesLastResultAndRejectsLateResult(t *testing.T) {
	doc := tableInfoSampleTab()
	doc.dataInfo.attempted = true
	oldCount := int64(14)
	doc.dataInfo.count = &oldCount
	s := tableInfoSampleState(doc)
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.mysqlTableCountLoader = func(context.Context, int64, string, string) (int64, error) {
		close(started)
		<-release
		close(finished)
		return 99, nil
	}
	tt := tableInfoTester(s, doc)
	if err := tt.Click("获取整表行数"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("count did not start")
	}
	if err := tt.Click("取消计数"); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-finished
	tt.Frame()
	if doc.dataInfo.countLoading || *doc.dataInfo.count != 14 || !tt.HasText("行数统计已取消") {
		t.Fatal("late count replaced canceled result")
	}
}

func TestMySQLTableInfoDiscardStaleScopeAndDisposeBothRequests(t *testing.T) {
	doc := tableInfoSampleTab()
	s := tableInfoSampleState(doc)
	infoCanceled, countCanceled := false, false
	doc.dataInfo.loading, doc.dataInfo.countLoading = true, true
	doc.dataInfo.cancel = func() { infoCanceled = true }
	doc.dataInfo.countCancel = func() { countCanceled = true }
	out := make(chan mysqlTableInfoResult, 1)
	out <- mysqlTableInfoResult{generation: s.mysqlSessionGeneration + 1, profileID: 8, database: doc.dataDatabase, table: doc.dataTable,
		info: &connections.MySQLTableInfo{Database: doc.dataDatabase, Name: doc.dataTable, Engine: "obsolete"}}
	doc.dataInfo.results = out
	s.consumeMySQLTableInfo()
	if doc.dataInfo.info != nil {
		t.Fatal("stale session result was accepted")
	}
	s.cancelMySQLTableInfo(doc)
	if !infoCanceled || !countCanceled || doc.dataInfo.loading || doc.dataInfo.countLoading || doc.dataInfo.results != nil || doc.dataInfo.countResults != nil {
		t.Fatal("disposal did not cancel both requests")
	}
}
