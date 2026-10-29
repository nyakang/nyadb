package shell

import (
	"context"
	"errors"
	"strings"
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
	if s.currentTab().query.ProfileID() != profile.ID || s.currentTab().query.Database() != "db_test" {
		t.Fatalf("SQL tab not bound to fixture profile/schema: %+v", s.currentTab())
	}
	return s, tt, profile.ID
}

func waitSQLUI(t *testing.T, s *State, tt *ui.Tester, query *tab) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for query.query.Loading() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	tt.Frame()
	if query.query.Loading() {
		t.Fatal("SQL UI request did not complete")
	}
}

func TestSQLTabsProfileIsolationAndCloseCancel(t *testing.T) {
	s, tt, idA := sqlUITestState(t)
	a := s.currentTab()
	a.query.Editor.Buffer.Set("SELECT A")
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
	b.query.Editor.Buffer.Set("SELECT B")
	if b.query.ProfileID() != bProfile.ID || a.query.ProfileID() != idA || b.query.Database() != "db_other" {
		t.Fatalf("SQL tabs lost their connection identity: A=%+v B=%+v", a, b)
	}
	startedA := make(chan struct{}, 1)
	s.sqlWorkspace.Executor = func(ctx context.Context, profile int64, database, statement string) (mysqlsvc.MySQLSQLResult, error) {
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
	if b.query.Result() == nil || b.query.Result().Rows[0][0] != "B" {
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
	if !a.query.Loading() {
		t.Fatal("A must remain busy while executing")
	}
	s.selectProfileByName("SQL Fixture B")
	if s.activeTab != 0 {
		t.Fatal("switching profiles did not leave A's SQL workspace")
	}
	s.activeTab = 1
	s.closeTab(1)
	tt.Frame()
	if a.query.Loading() {
		t.Fatal("closing A tab left running UI state")
	}
	if b.query.Result() == nil || b.query.Result().Rows[0][0] != "B" {
		t.Fatal("closing A erased B results")
	}
}

func TestSQLQueryUIErrorsAffectedRowsAndDisconnect(t *testing.T) {
	s, tt, id := sqlUITestState(t)
	q := s.currentTab()
	q.query.Editor.Buffer.Set("BAD SQL")
	s.sqlWorkspace.Executor = func(context.Context, int64, string, string) (mysqlsvc.MySQLSQLResult, error) {
		return mysqlsvc.MySQLSQLResult{}, errors.New("SQL syntax error near BAD")
	}
	if err := tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, q)
	if q.query.Result() != nil || !strings.Contains(q.query.Error(), "SQL syntax error") {
		t.Fatalf("SQL error disappeared: %+v / %q", q.query.Result(), q.query.Error())
	}
	q.query.Editor.Buffer.Set("UPDATE fixture SET x=1")
	q.query.Editor.Select(0, 0)
	s.sqlWorkspace.Executor = func(_ context.Context, profile int64, database, sql string) (mysqlsvc.MySQLSQLResult, error) {
		if profile != id || database != "db_test" || sql != "UPDATE fixture SET x=1" {
			return mysqlsvc.MySQLSQLResult{}, errors.New("wrong update target")
		}
		return mysqlsvc.MySQLSQLResult{AffectedRows: 4, Duration: 6 * time.Millisecond}, nil
	}
	if err := tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, q)
	if q.query.Result() == nil || q.query.Result().AffectedRows != 4 || !strings.Contains(s.status, "影响 4 行") {
		t.Fatalf("affected rows missing: %+v / %s", q.query.Result(), s.status)
	}
	s.disconnectMySQLProfile(id)
	tt.Frame()
	if s.sessions.StatusFor(id) != "未连接" || q.query.Result() != nil ||
		!strings.Contains(q.query.Error(), "连接已断开") ||
		q.query.Editor.Buffer.String() != "UPDATE fixture SET x=1" {
		t.Fatalf("disconnect lost editor state or kept result: error=%q result=%+v", q.query.Error(), q.query.Result())
	}
}
