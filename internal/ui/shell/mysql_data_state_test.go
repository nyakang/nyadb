package shell

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func waitMySQLData(t *testing.T, s *State, tt *ui.Tester) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for s.currentTab().kind == tabMySQLData && s.currentTab().dataLoading && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	tt.Frame()
	if s.currentTab().kind != tabMySQLData || s.currentTab().dataLoading {
		t.Fatal("data page load did not settle")
	}
}
func TestMySQLNativeReadOnlyDataPagingAndTabLifecycle(t *testing.T) {
	s, tt := structureTestBrowser(t)
	var mu sync.Mutex
	calls := []int64{}
	s.mysqlDataLoader = func(ctx context.Context, id int64, db, table string, offset int64, limit int) (*mysqlsvc.MySQLDataPage, error) {
		mu.Lock()
		calls = append(calls, offset)
		mu.Unlock()
		page := &mysqlsvc.MySQLDataPage{Database: db, Table: table, Offset: offset, Limit: limit,
			Columns: []string{"id", "note"}, StableOrder: true}
		if offset == 0 {
			page.HasMore = true
			page.Rows = [][]mysqlsvc.MySQLDataCell{{{Text: "1"}, {Text: "real-value"}}, {{Text: "2"}, {Null: true}}}
		} else {
			page.Rows = [][]mysqlsvc.MySQLDataCell{{{Text: "3"}, {Text: "last-page"}}}
		}
		return page, nil
	}
	if err := tt.Click("打开选中表数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if !tt.HasText("real-value") || s.currentTab().kind != tabMySQLData || len(s.tabs) != 2 {
		t.Fatalf("not showing real page: %v", tt.Texts())
	}
	if err := tt.Click("下一页数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if !tt.HasText("last-page") || s.currentTab().dataOffset != 100 {
		t.Fatal("next page didn't load")
	}
	if err := tt.Click("上一页数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if !tt.HasText("real-value") {
		t.Fatal("previous page didn't load")
	}
	if err := tt.Click("刷新表数据：orders"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	mu.Lock()
	n := len(calls)
	mu.Unlock()
	if n != 4 {
		t.Fatalf("refresh did not fetch: %d calls", n)
	}
	if err := tt.Click("对象"); err != nil {
		t.Fatal(err)
	}
	s.mysqlStructureLoader = func(_ context.Context, _ int64, db, table string) (*mysqlsvc.MySQLTableStructure, error) {
		return fakeStructure(db, table), nil
	}
	if err := tt.Click("打开选中表结构"); err != nil {
		t.Fatal(err)
	}
	waitMySQLStructure(t, s, tt)
	// An already-open data Tab must never become the legacy simulated preview.
	if len(s.tabs) != 3 || s.tabs[1].kind != tabMySQLData || s.currentTab().kind != tabStructure {
		t.Fatalf("structure/data tabs shared state: %+v", s.tabs)
	}
	s.disconnectMySQLSession()
	tt.Frame()
	if len(s.tabs) != 1 {
		t.Fatalf("disconnect retained MySQL data: %+v", s.tabs)
	}
}

func TestMySQLDataLateResultAndErrorRetry(t *testing.T) {
	s, tt := structureTestBrowser(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	s.mysqlDataLoader = func(ctx context.Context, id int64, db, table string, offset int64, limit int) (*mysqlsvc.MySQLDataPage, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			close(started)
			<-release // deliberately non-cooperative loader.
			return &mysqlsvc.MySQLDataPage{Database: db, Table: "obsolete", Columns: []string{"stale"}}, nil
		}
		if n == 2 {
			return nil, errors.New("simulated failure")
		}
		return &mysqlsvc.MySQLDataPage{Database: db, Table: table, Columns: []string{"id"}, Rows: [][]mysqlsvc.MySQLDataCell{{{Text: "new"}}}, Limit: limit}, nil
	}
	if err := tt.Click("打开选中表数据"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	p := s.currentTab()
	s.cancelMySQLData(p)
	p.dataErr = "已取消"
	tt.Frame()
	if err := tt.Click("重试读取数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if !strings.Contains(p.dataErr, "simulated failure") {
		t.Fatalf("error missing: %v", p.dataErr)
	}
	if err := tt.Click("重试读取数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	close(release)
	for i := 0; i < 5; i++ {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if p.dataPage == nil || p.dataPage.Table == "obsolete" || !tt.HasText("new") {
		t.Fatal("late stale result replaced newer page")
	}
	if err := tt.Click("关闭 app_db.orders · 数据"); err != nil {
		t.Fatal(err)
	}
	if len(s.tabs) != 1 {
		t.Fatal("closing data Tab failed")
	}
}

// A schema refresh must only evict data belonging to removed schemas.
func TestMySQLDataTabsPruneOnlyMissingSchema(t *testing.T) {
	s, tt := structureTestBrowser(t)
	s.mysqlDataLoader = func(_ context.Context, id int64, db, table string, offset int64, limit int) (*mysqlsvc.MySQLDataPage, error) {
		return &mysqlsvc.MySQLDataPage{
			Database: db, Table: table, Columns: []string{"id"},
			Rows:  [][]mysqlsvc.MySQLDataCell{{{Text: db}}},
			Limit: limit, StableOrder: true,
		}, nil
	}
	if err := tt.Click("打开选中表数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if err := tt.Click("对象"); err != nil {
		t.Fatal(err)
	}
	if err := doubleClickDatabase(tt, "数据库：analytics"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	s.sessions.Current().Row = 0
	tt.Frame()
	if err := tt.Click("打开选中表数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if len(s.tabs) != 3 || s.currentTab().dataPage.Database != "analytics" {
		t.Fatalf("cross schema data isolation failed: %+v", s.tabs)
	}
	s.sessions.ReconcileDatabases([]string{"app_db"})
	tt.Frame()
	if len(s.tabs) != 2 || s.activeTab != 0 || s.tabs[1].kind != tabMySQLData ||
		s.tabs[1].dataPage.Database != "app_db" {
		t.Fatalf("schema refresh discarded valid data or retained stale page: %+v", s.tabs)
	}
}
