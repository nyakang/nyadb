package shell

import (
	"context"
	"errors"
	"image/png"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func mysqlObjectTestState(t *testing.T) (*State, *ui.Tester) {
	t.Helper()
	s, _, svc, _ := profileTestState(t)
	profile, err := svc.Create(context.Background(), connections.Profile{
		Name: "Metadata test", Kind: connections.MySQL, Host: "localhost", Port: 3306,
		MySQLAdvanced: connections.DefaultMySQLAdvanced(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.selectedProfileID = profile.ID
	s.loadProfiles()
	s.mysqlSessionOpener = func(context.Context, connections.ActiveSettings, string) error { return nil }
	s.mysqlDatabaseLister = func(context.Context, int64) ([]string, error) {
		return []string{"app_db", "analytics"}, nil
	}
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("连接所选 MySQL"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for s.mysqlSessionStatus == "连接中" && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if s.mysqlSessionStatus != "已连接" {
		t.Fatalf("could not start test session: %v", s.mysqlSessionError)
	}
	return s, tt
}

func waitMySQLObjects(t *testing.T, s *State, tt *ui.Tester) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for s.mysqlObjectPending != "" && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	tt.Frame()
	if s.mysqlObjectPending != "" {
		t.Fatal("metadata request did not finish")
	}
}

func TestMySQLObjectTreeLazyLoadingFiltersAndRefresh(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	var mu sync.Mutex
	requests := map[string]int{}
	s.mysqlObjectLister = func(ctx context.Context, id int64, database string) ([]connections.MySQLDBObject, error) {
		mu.Lock()
		requests[database]++
		mu.Unlock()
		if database == "app_db" {
			return []connections.MySQLDBObject{
				{Name: "orders", Kind: "表", Engine: "InnoDB", Comment: "customer data"},
				{Name: "sales", Kind: "视图", Comment: "sales aggregation"},
			}, nil
		}
		return []connections.MySQLDBObject{{Name: "facts", Kind: "表", Engine: "InnoDB"}}, nil
	}
	if _, ok := s.mysqlObjectCache["app_db"]; ok {
		t.Fatal("metadata loaded before expansion")
	}
	if err := tt.Click("数据库：app_db"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	if s.mysqlSelectedDatabase != "app_db" || len(s.mysqlObjectCache["app_db"]) != 2 ||
		!strings.Contains(strings.Join(tt.Texts(), "|"), "orders") {
		t.Fatalf("object browser did not show real metadata: %v", tt.Texts())
	}
	// Opt-in reproducible headless screenshot, saved only under temp/research.
	if output := os.Getenv("NYADB_MYSQL_METADATA_CAPTURE"); output != "" {
		file, err := os.Create(output)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(file, tt.Image()); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// Keep the 1280px screenshot above for visual comparison, but exercise
	// toolbar actions in a wider viewport. At 1280px the workspace shares
	// its width with the navigator and inspector; font metrics on Linux CI
	// can push the toolbar's hit targets into its clipped trailing edge.
	tt.SetSize(1600, 800)
	if err := tt.Click("筛选对象：视图"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(s.mysqlSelectedObjects()) != 1 || s.mysqlSelectedObjects()[0].Name != "sales" {
		rect, _ := tt.Find("筛选对象：视图")
		t.Fatalf("view filtering did not apply: kind=%q selected=%q objects=%v filterBounds=%+v",
			s.mysqlObjectKind, s.mysqlSelectedDatabase, s.mysqlSelectedObjects(), rect)
	}
	if err := tt.Click("数据库：analytics"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	if s.mysqlSelectedDatabase != "analytics" || len(s.mysqlSelectedObjects()) != 1 ||
		s.mysqlSelectedObjects()[0].Name != "facts" {
		t.Fatal("database switch displayed wrong objects")
	}
	if err := tt.Click("数据库：app_db"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	mu.Lock()
	count := requests["app_db"]
	mu.Unlock()
	if count != 1 {
		t.Fatal("switching to loaded database should reuse metadata cache")
	}
	if err := tt.Click("刷新元数据：app_db"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	mu.Lock()
	count = requests["app_db"]
	mu.Unlock()
	if count != 2 {
		t.Fatalf("explicit refresh did not reload metadata: %d", count)
	}
	s.disconnectMySQLSession()
	tt.Frame()
	if s.mysqlSelectedDatabase != "" || len(s.mysqlObjectCache) != 0 ||
		strings.Contains(strings.Join(tt.Texts(), "|"), "orders") {
		t.Fatal("disconnect left stale database objects on screen")
	}
}

func TestMySQLObjectBrowserIgnoresCancelledLateResults(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s.mysqlObjectLister = func(ctx context.Context, id int64, database string) ([]connections.MySQLDBObject, error) {
		if database == "app_db" {
			once.Do(func() { close(started) })
			<-release // deliberately ignores context to simulate a late driver
			return []connections.MySQLDBObject{{Name: "stale_orders", Kind: "表"}}, nil
		}
		return []connections.MySQLDBObject{{Name: "new_facts", Kind: "视图"}}, nil
	}
	if err := tt.Click("数据库：app_db"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first request did not start")
	}
	if err := tt.Click("数据库：analytics"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	close(release)
	for i := 0; i < 4; i++ {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if s.mysqlSelectedDatabase != "analytics" || len(s.mysqlSelectedObjects()) != 1 ||
		s.mysqlSelectedObjects()[0].Name != "new_facts" {
		t.Fatalf("stale response replaced current database: %v", s.mysqlSelectedObjects())
	}
	s.disconnectMySQLSession()
	tt.Frame()
	if len(s.mysqlObjectCache) != 0 {
		t.Fatal("canceled metadata request escaped disconnect")
	}
}

func TestMySQLObjectBrowserDisplaysErrorsAndRetry(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	fail := true
	s.mysqlObjectLister = func(context.Context, int64, string) ([]connections.MySQLDBObject, error) {
		if fail {
			return nil, errors.New("read metadata denied")
		}
		return []connections.MySQLDBObject{{Name: "ok_table", Kind: "表"}}, nil
	}
	if err := tt.Click("数据库：app_db"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	if !strings.Contains(s.mysqlObjectErrors["app_db"], "denied") {
		t.Fatal("metadata error missing")
	}
	fail = false
	// The refresh button sits at the far right of the shared toolbar and
	// must not depend on a particular runner's font metrics for hit testing.
	tt.SetSize(1600, 800)
	if err := tt.Click("刷新元数据：app_db"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	if s.mysqlObjectErrors["app_db"] != "" || len(s.mysqlSelectedObjects()) != 1 {
		rect, _ := tt.Find("刷新元数据：app_db")
		t.Fatalf("successful retry did not clear metadata error: pending=%q error=%q objects=%v refreshBounds=%+v",
			s.mysqlObjectPending, s.mysqlObjectErrors["app_db"], s.mysqlSelectedObjects(), rect)
	}
}
