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
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
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
	s.sessions.Opener = func(context.Context, connections.ActiveSettings, string) error { return nil }
	s.sessions.DatabaseLister = func(context.Context, int64) ([]string, error) {
		return []string{"app_db", "analytics"}, nil
	}
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("连接所选 MySQL"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for s.sessions.Current().Status == "连接中" && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if s.sessions.Current().Status != "已连接" {
		t.Fatalf("could not start test session: %v", s.sessions.Current().Error)
	}
	return s, tt
}

func waitMySQLObjects(t *testing.T, s *State, tt *ui.Tester) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for s.sessions.Current().Pending != "" && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	tt.Frame()
	if s.sessions.Current().Pending != "" {
		t.Fatal("metadata request did not finish")
	}
}

func doubleClickDatabase(tt *ui.Tester, label string) error {
	if err := tt.Click(label); err != nil {
		return err
	}
	return tt.Click(label)
}

func TestMySQLObjectTreeLazyLoadingFiltersAndRefresh(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	var mu sync.Mutex
	requests := map[string]int{}
	s.sessions.ObjectLister = func(ctx context.Context, id int64, database string) ([]mysqlsvc.MySQLDBObject, error) {
		mu.Lock()
		requests[database]++
		mu.Unlock()
		if database == "app_db" {
			return []mysqlsvc.MySQLDBObject{
				{Name: "orders", Kind: "表", Engine: "InnoDB", Comment: "customer data"},
				{Name: "sales", Kind: "视图", Comment: "sales aggregation"},
			}, nil
		}
		return []mysqlsvc.MySQLDBObject{{Name: "facts", Kind: "表", Engine: "InnoDB"}}, nil
	}
	if _, ok := s.sessions.Current().Cache["app_db"]; ok {
		t.Fatal("metadata loaded before expansion")
	}
	if err := doubleClickDatabase(tt, "数据库：app_db"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	if s.sessions.Current().Database != "app_db" || len(s.sessions.Current().Cache["app_db"]) != 2 ||
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
	if len(s.objectWorkspace.SelectedObjects()) != 1 || s.objectWorkspace.SelectedObjects()[0].Name != "sales" {
		rect, _ := tt.Find("筛选对象：视图")
		t.Fatalf("view filtering did not apply: kind=%q selected=%q objects=%v filterBounds=%+v",
			s.objectWorkspace.FilterKind(s.sessions.Current().ProfileID), s.sessions.Current().Database, s.objectWorkspace.SelectedObjects(), rect)
	}
	if err := doubleClickDatabase(tt, "数据库：analytics"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	if s.sessions.Current().Database != "analytics" || len(s.objectWorkspace.SelectedObjects()) != 1 ||
		s.objectWorkspace.SelectedObjects()[0].Name != "facts" {
		t.Fatal("database switch displayed wrong objects")
	}
	if err := doubleClickDatabase(tt, "数据库：app_db"); err != nil {
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
	if s.sessions.Current().Database != "" || len(s.sessions.Current().Cache) != 0 ||
		strings.Contains(strings.Join(tt.Texts(), "|"), "orders") {
		t.Fatal("disconnect left stale database objects on screen")
	}
}

func TestMySQLObjectBrowserIgnoresCancelledLateResults(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s.sessions.ObjectLister = func(ctx context.Context, id int64, database string) ([]mysqlsvc.MySQLDBObject, error) {
		if database == "app_db" {
			once.Do(func() { close(started) })
			<-release // deliberately ignores context to simulate a late driver
			return []mysqlsvc.MySQLDBObject{{Name: "stale_orders", Kind: "表"}}, nil
		}
		return []mysqlsvc.MySQLDBObject{{Name: "new_facts", Kind: "视图"}}, nil
	}
	if err := doubleClickDatabase(tt, "数据库：app_db"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first request did not start")
	}
	if err := doubleClickDatabase(tt, "数据库：analytics"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	close(release)
	for i := 0; i < 4; i++ {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if s.sessions.Current().Database != "analytics" || len(s.objectWorkspace.SelectedObjects()) != 1 ||
		s.objectWorkspace.SelectedObjects()[0].Name != "new_facts" {
		t.Fatalf("stale response replaced current database: %v", s.objectWorkspace.SelectedObjects())
	}
	s.disconnectMySQLSession()
	tt.Frame()
	if len(s.sessions.Current().Cache) != 0 {
		t.Fatal("canceled metadata request escaped disconnect")
	}
}

func TestMySQLObjectBrowserDisplaysErrorsAndRetry(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	fail := true
	s.sessions.ObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) {
		if fail {
			return nil, errors.New("read metadata denied")
		}
		return []mysqlsvc.MySQLDBObject{{Name: "ok_table", Kind: "表"}}, nil
	}
	if err := doubleClickDatabase(tt, "数据库：app_db"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	if !strings.Contains(s.sessions.Current().Errors["app_db"], "denied") {
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
	if s.sessions.Current().Errors["app_db"] != "" || len(s.objectWorkspace.SelectedObjects()) != 1 {
		rect, _ := tt.Find("刷新元数据：app_db")
		t.Fatalf("successful retry did not clear metadata error: pending=%q error=%q objects=%v refreshBounds=%+v",
			s.sessions.Current().Pending, s.sessions.Current().Errors["app_db"], s.objectWorkspace.SelectedObjects(), rect)
	}
}
