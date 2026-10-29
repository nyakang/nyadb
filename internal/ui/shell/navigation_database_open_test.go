package shell

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func TestNavigatorUnopenedDatabaseSelectionDoesNotLoad(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	id := s.mysqlSessionProfileID
	var reads atomic.Int32
	s.mysqlObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) {
		reads.Add(1)
		return nil, nil
	}
	// A cache populated elsewhere is not permission to open its navigator node.
	s.mysqlObjectCache["analytics"] = []mysqlsvc.MySQLDBObject{{Name: "cached_table", Kind: "表"}}
	for _, db := range s.mysqlSessionDatabases {
		if s.navigation.Children(navigation.DatabaseNode(id, db).Key()) != nil {
			t.Fatalf("unopened %s has an expansion arrow", db)
		}
	}
	if err := tt.Click("数据库：app_db"); err != nil {
		t.Fatal(err)
	}
	if s.mysqlSelectedDatabase != "app_db" || !tt.HasText("双击打开数据库") {
		t.Fatal("single click did not select the database and show the opening hint")
	}
	tt.Key(0, ui.KeyDown)
	if s.mysqlSelectedDatabase != "analytics" || s.objectRow != -1 || tt.HasText("cached_table") {
		t.Fatal("keyboard selection displayed an unopened database's cached objects")
	}
	if err := tt.RightClick("数据库：app_db"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tt.Menu(), "打开数据库") {
		t.Fatalf("database menu lacks open action: %v", tt.Menu())
	}
	tt.CloseMenu()
	tt.Frame()
	if reads.Load() != 0 || s.mysqlObjectPending != "" || s.mysqlDatabaseOpenedFor(id, "app_db") {
		t.Fatal("selection or right-click started a metadata read")
	}
}

func TestNavigatorDoubleClickAndEnterOpenOnceAndRetainDisclosure(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	id := s.mysqlSessionProfileID
	var reads atomic.Int32
	release := make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	defer finish()
	s.mysqlObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) {
		reads.Add(1)
		<-release
		return nil, nil // An empty database still has object categories.
	}
	if err := doubleClickDatabase(tt, "数据库：app_db"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	key := navigation.DatabaseNode(id, "app_db").Key()
	if !s.mysqlDatabaseOpenedFor(id, "app_db") || !s.navigation.Tree.Open.Has(key) ||
		!tt.HasText("正在读取对象…") {
		t.Fatal("double-click did not open and show metadata progress")
	}
	request := s.mysqlObjectRequest
	tt.Key(0, ui.KeyEnter)
	tt.Key(0, ui.KeyEnter)
	if s.mysqlObjectRequest != request {
		t.Fatal("repeated Enter restarted a pending metadata request")
	}
	finish()
	waitMySQLObjects(t, s, tt)
	if reads.Load() != 1 || len(s.navigation.Children(key)) != 7 {
		t.Fatal("empty database lost categories or was read more than once")
	}
	tt.Key(0, ui.KeyLeft)
	tt.Frame()
	if s.navigation.Tree.Open.Has(key) || s.navigation.Children(key) == nil {
		t.Fatal("folding forgot that the database had been opened")
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if !s.navigation.Tree.Open.Has(key) || reads.Load() != 1 {
		t.Fatal("Enter did not reopen using the cached empty database")
	}
}

func TestNavigatorExplicitActionsOpenUnopenedDatabase(t *testing.T) {
	for _, action := range []string{"menu", "enter", "refresh"} {
		t.Run(action, func(t *testing.T) {
			s, tt := mysqlObjectTestState(t)
			var reads atomic.Int32
			s.mysqlObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) {
				reads.Add(1)
				return []mysqlsvc.MySQLDBObject{{Name: "orders", Kind: "表"}}, nil
			}
			if err := tt.Click("数据库：app_db"); err != nil {
				t.Fatal(err)
			}
			switch action {
			case "menu":
				if err := tt.RightClick("数据库：app_db"); err != nil {
					t.Fatal(err)
				}
				if err := tt.ChooseMenuItem("打开数据库"); err != nil {
					t.Fatal(err)
				}
			case "enter":
				tt.Key(0, ui.KeyEnter)
			case "refresh":
				tt.Key(0, ui.KeyF5)
			}
			waitMySQLObjects(t, s, tt)
			if reads.Load() != 1 || !s.mysqlDatabaseOpenedFor(s.mysqlSessionProfileID, "app_db") ||
				!tt.HasText("orders") || !tt.HasText("表") {
				t.Fatal("explicit action failed to open and load the database")
			}
		})
	}
}

func TestNavigatorSingleClickDoesNotCancelOpenedSiblingRead(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	var reads atomic.Int32
	release := make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	defer finish()
	s.mysqlObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) {
		reads.Add(1)
		<-release
		return []mysqlsvc.MySQLDBObject{{Name: "a_orders", Kind: "表"}}, nil
	}
	if err := doubleClickDatabase(tt, "数据库：app_db"); err != nil {
		t.Fatal(err)
	}
	request := s.mysqlObjectRequest
	if err := tt.Click("数据库：analytics"); err != nil {
		t.Fatal(err)
	}
	if s.mysqlObjectRequest != request || s.mysqlObjectPending != "app_db" ||
		s.mysqlDatabaseOpenedFor(s.mysqlSessionProfileID, "analytics") {
		t.Fatal("single click cancelled or replaced a sibling's explicit read")
	}
	finish()
	waitMySQLObjects(t, s, tt)
	if reads.Load() != 1 || s.mysqlSelectedDatabase != "analytics" ||
		len(s.mysqlObjectCache["app_db"]) != 1 || tt.HasText("a_orders") || !tt.HasText("双击打开数据库") {
		t.Fatal("late sibling result changed the selection or started another read")
	}
}

func TestNavigatorFailedDatabaseStaysOpenUntilExplicitRetry(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	var reads atomic.Int32
	s.mysqlObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) {
		if reads.Add(1) == 1 {
			return nil, errors.New("permission denied")
		}
		return []mysqlsvc.MySQLDBObject{{Name: "orders", Kind: "表"}}, nil
	}
	if err := doubleClickDatabase(tt, "数据库：app_db"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	tt.Key(0, ui.KeyEnter)
	tt.Key(0, ui.KeyLeft)
	tt.Key(0, ui.KeyRight)
	tt.Advance(time.Second)
	if reads.Load() != 1 || !tt.HasText("读取失败，点击重试") {
		t.Fatal("selection or expansion retried a failed read automatically")
	}
	if err := tt.Click("读取失败，点击重试"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	if reads.Load() != 2 || s.mysqlObjectErrors["app_db"] != "" || !tt.HasText("orders") {
		t.Fatal("explicit retry did not recover the opened database")
	}
}

func TestNavigatorOpenedDatabaseStatePrunedAndResetOnReconnect(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	id := s.mysqlSessionProfileID
	for _, db := range s.mysqlSessionDatabases {
		s.markMySQLDatabaseOpened(id, db)
		s.mysqlObjectCache[db] = nil
		s.navigation.Tree.Open.Add(navigation.DatabaseNode(id, db).Key())
	}
	s.reconcileMySQLDatabases([]string{"app_db"})
	if !s.mysqlDatabaseOpenedFor(id, "app_db") || s.mysqlDatabaseOpenedFor(id, "analytics") ||
		s.navigation.Tree.Open.Has(navigation.DatabaseNode(id, "analytics").Key()) {
		t.Fatal("schema refresh did not preserve only the surviving opened database")
	}
	s.disconnectMySQLSession()
	tt.Frame()
	if err := tt.Click("连接所选 MySQL"); err != nil {
		t.Fatal(err)
	}
	waitMultiProfileSession(t, s, tt)
	if s.mysqlDatabaseOpenedFor(id, "app_db") || s.navigation.Children(navigation.DatabaseNode(id, "app_db").Key()) != nil ||
		s.navigation.Tree.Open.Has(navigation.DatabaseNode(id, "app_db").Key()) {
		t.Fatal("reconnect restored stale database opening state")
	}
}

func TestNavigatorRemovedDatabaseCannotPublishLateSiblingResult(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	id := s.mysqlSessionProfileID
	release := make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	defer finish()
	s.mysqlObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) {
		<-release // Deliberately ignore cancellation as a late driver might.
		return []mysqlsvc.MySQLDBObject{{Name: "stale_orders", Kind: "表"}}, nil
	}
	if err := doubleClickDatabase(tt, "数据库：app_db"); err != nil {
		t.Fatal(err)
	}
	results := s.mysqlObjectResults
	if err := tt.Click("数据库：analytics"); err != nil {
		t.Fatal(err)
	}
	s.mysqlSessionDatabases = []string{"analytics"}
	s.reconcileMySQLDatabases(s.mysqlSessionDatabases)
	finish()
	select {
	case <-results:
	case <-time.After(time.Second):
		t.Fatal("cancelled metadata job did not finish")
	}
	tt.Frame()
	if _, cached := s.mysqlObjectCache["app_db"]; cached || s.mysqlObjectPending != "" ||
		s.mysqlDatabaseOpenedFor(id, "app_db") || s.mysqlSelectedDatabase != "analytics" {
		t.Fatal("removed database republished metadata or changed its sibling's context")
	}
}
