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

func sqlUITestState(t *testing.T) (*State, *ui.Tester, int64) {
	t.Helper()
	s, tt, service, _ := profileTestState(t)
	profile, err := service.Create(context.Background(), connections.Profile{
		Name: "SQL Fixture A", Kind: connections.MySQL, Host: "127.0.0.1", Port: 3306,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectedProfileID = profile.ID
	s.sessions.Current().ProfileID = profile.ID
	s.sessions.Current().Status = "已连接"
	s.sessions.Current().Databases = []string{"db_test"}
	s.sessions.Current().Database = "db_test"
	s.openQuery()
	tt.Frame()
	if s.currentTab().sqlProfileID != profile.ID || s.currentTab().sqlDatabase != "db_test" {
		t.Fatalf("SQL tab not bound to fixture profile/schema: %+v", s.currentTab())
	}
	return s, tt, profile.ID
}

func waitSQLUI(t *testing.T, s *State, tt *ui.Tester, query *tab) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for query.sqlLoading && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	tt.Frame()
	if query.sqlLoading {
		t.Fatal("SQL UI request did not complete")
	}
}

func TestSQLQueryUIRunsSelectedTextAndRendersResults(t *testing.T) {
	s, tt, id := sqlUITestState(t)
	q := s.currentTab()
	q.editor.Buffer.Set("SELECT 1;\nSELECT 2;")
	q.editor.Select(0, len("SELECT 1;"))
	var received string
	s.mysqlSQLExecutor = func(ctx context.Context, profile int64, database, statement string) (mysqlsvc.MySQLSQLResult, error) {
		if profile != id || database != "db_test" {
			return mysqlsvc.MySQLSQLResult{}, errors.New("wrong schema/profile")
		}
		received = statement
		return mysqlsvc.MySQLSQLResult{
			Columns: []string{"result"}, Rows: [][]string{{"1"}}, Duration: 12 * time.Millisecond,
		}, nil
	}
	if err := tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, q)
	if received != "SELECT 1;" || q.sqlResult == nil || len(q.sqlResult.Rows) != 1 || q.sqlResult.Rows[0][0] != "1" {
		t.Fatalf("selected SQL was not executed/rendered: received=%q result=%+v err=%s", received, q.sqlResult, q.sqlError)
	}
	if !strings.Contains(s.status, "1 行") || q.sqlError != "" {
		t.Fatalf("missing row count/status: %s (%s)", s.status, q.sqlError)
	}
	// Reproducible headless UI evidence; no real server or original is started.
	if output := os.Getenv("NYADB_SQL_WORKSPACE_CAPTURE"); output != "" {
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
	// Without selection the SQL statement at the caret is dispatched.
	q.editor.Select(len("SELECT 1;\nSELECT "), len("SELECT 1;\nSELECT "))
	if err := tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, q)
	if received != "SELECT 2;" {
		t.Fatalf("caret SQL not dispatched: %q", received)
	}
	// Explicit Select All sends the full authored selection.
	q.editor.Select(0, q.editor.Buffer.Len())
	if err := tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, q)
	if received != "SELECT 1;\nSELECT 2;" {
		t.Fatalf("selected full SQL not dispatched: %q", received)
	}
}

func TestSQLQueryUICancelWaitsForWorkerAndRetainsEditor(t *testing.T) {
	s, tt, id := sqlUITestState(t)
	q := s.currentTab()
	q.editor.Buffer.Set("SELECT SLEEP(30)")
	started := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	release := make(chan struct{})
	calls := 0
	var mu sync.Mutex
	s.mysqlSQLExecutor = func(ctx context.Context, profile int64, database, statement string) (mysqlsvc.MySQLSQLResult, error) {
		if profile != id || database != "db_test" || statement != "SELECT SLEEP(30)" {
			return mysqlsvc.MySQLSQLResult{}, errors.New("wrong SQL identity")
		}
		mu.Lock()
		calls++
		mu.Unlock()
		started <- struct{}{}
		<-ctx.Done()
		canceled <- struct{}{}
		<-release
		return mysqlsvc.MySQLSQLResult{}, ctx.Err()
	}
	if err := tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("SQL did not start")
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if !q.sqlLoading || !q.sqlCancelRequested {
		t.Fatal("cancel must wait for driver acknowledgement")
	}
	s.runMySQLSQL(ui.Services{}, q) // blocked while previous cancellation is pending
	mu.Lock()
	n := calls
	mu.Unlock()
	if n != 1 {
		t.Fatalf("duplicate SQL while canceling: %d", n)
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("SQL cancellation did not reach worker")
	}
	close(release)
	waitSQLUI(t, s, tt, q)
	if q.sqlResult != nil || !strings.Contains(q.sqlError, "服务器端更改可能已生效") ||
		q.editor.Buffer.String() != "SELECT SLEEP(30)" {
		t.Fatalf("cancel misreported results or discarded SQL: error=%s result=%+v", q.sqlError, q.sqlResult)
	}
}

func TestSQLTabsProfileIsolationAndCloseCancel(t *testing.T) {
	s, tt, idA := sqlUITestState(t)
	a := s.currentTab()
	a.editor.Buffer.Set("SELECT A")
	bProfile, err := s.profileService.Create(context.Background(), connections.Profile{
		Name: "SQL Fixture B", Kind: connections.MySQL, Host: "127.0.0.1", Port: 3306,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.sessions.Activate(bProfile.ID)
	s.sessions.Current().ProfileID = bProfile.ID
	s.sessions.Current().Status = "已连接"
	s.sessions.Current().Database = "db_other"
	s.selectedProfileID = bProfile.ID
	s.openQuery()
	b := s.currentTab()
	b.editor.Buffer.Set("SELECT B")
	if b.sqlProfileID != bProfile.ID || a.sqlProfileID != idA || b.sqlDatabase != "db_other" {
		t.Fatalf("SQL tabs lost their connection identity: A=%+v B=%+v", a, b)
	}
	startedA := make(chan struct{}, 1)
	s.mysqlSQLExecutor = func(ctx context.Context, profile int64, database, statement string) (mysqlsvc.MySQLSQLResult, error) {
		if profile == idA && database == "db_test" && statement == "SELECT A" {
			startedA <- struct{}{}
			<-ctx.Done()
			return mysqlsvc.MySQLSQLResult{}, ctx.Err()
		}
		if profile == bProfile.ID && database == "db_other" && statement == "SELECT B" {
			return mysqlsvc.MySQLSQLResult{Columns: []string{"b"}, Rows: [][]string{{"B"}}}, nil
		}
		return mysqlsvc.MySQLSQLResult{}, errors.New("profile/schema contamination")
	}
	tt.Frame()
	if err := tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, b)
	if b.sqlResult == nil || b.sqlResult.Rows[0][0] != "B" {
		t.Fatal("B did not get its own results")
	}
	// Returning to A restores its parked profile before executing.
	s.activeTab = 1
	tt.Frame()
	if s.sessions.Current().ProfileID != idA {
		t.Fatalf("returning to A SQL tab did not restore session: %d", s.sessions.Current().ProfileID)
	}
	if err := tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-startedA:
	case <-time.After(3 * time.Second):
		t.Fatal("A SQL did not start")
	}
	if !a.sqlLoading {
		t.Fatal("A must remain busy while executing")
	}
	s.selectProfileByName("SQL Fixture B")
	if s.activeTab != 0 {
		t.Fatal("switching profiles did not leave A's SQL workspace")
	}
	s.activeTab = 1
	s.closeTab(1)
	tt.Frame()
	if a.sqlLoading || a.sqlResults != nil {
		t.Fatal("closing A tab left running UI state")
	}
	if b.sqlResult == nil || b.sqlResult.Rows[0][0] != "B" {
		t.Fatal("closing A erased B results")
	}
}

func TestSQLQueryUIErrorsAffectedRowsAndDisconnect(t *testing.T) {
	s, tt, id := sqlUITestState(t)
	q := s.currentTab()
	q.editor.Buffer.Set("BAD SQL")
	s.mysqlSQLExecutor = func(context.Context, int64, string, string) (mysqlsvc.MySQLSQLResult, error) {
		return mysqlsvc.MySQLSQLResult{}, errors.New("SQL syntax error near BAD")
	}
	if err := tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, q)
	if q.sqlResult != nil || !strings.Contains(q.sqlError, "SQL syntax error") {
		t.Fatalf("SQL error disappeared: %+v / %q", q.sqlResult, q.sqlError)
	}
	q.editor.Buffer.Set("UPDATE fixture SET x=1")
	q.editor.Select(0, 0)
	s.mysqlSQLExecutor = func(_ context.Context, profile int64, database, sql string) (mysqlsvc.MySQLSQLResult, error) {
		if profile != id || database != "db_test" || sql != "UPDATE fixture SET x=1" {
			return mysqlsvc.MySQLSQLResult{}, errors.New("wrong update target")
		}
		return mysqlsvc.MySQLSQLResult{AffectedRows: 4, Duration: 6 * time.Millisecond}, nil
	}
	if err := tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, q)
	if q.sqlResult == nil || q.sqlResult.AffectedRows != 4 || !strings.Contains(s.status, "影响 4 行") {
		t.Fatalf("affected rows missing: %+v / %s", q.sqlResult, s.status)
	}
	s.disconnectMySQLProfile(id)
	tt.Frame()
	if s.sessions.StatusFor(id) != "未连接" || q.sqlResult != nil ||
		!strings.Contains(q.sqlError, "连接已断开") ||
		q.editor.Buffer.String() != "UPDATE fixture SET x=1" {
		t.Fatalf("disconnect lost editor state or kept result: error=%q result=%+v", q.sqlError, q.sqlResult)
	}
}
