package sqlworkspace

import (
	"context"
	"errors"
	"image/png"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestSQLQueryUIRunsSelectedTextAndRendersResults(t *testing.T) {
	s, q, tt, id, status := sqlUITestState(t)
	q.Editor.Buffer.Set("SELECT 1;\nSELECT 2;")
	q.Editor.Select(0, len("SELECT 1;"))
	var received string
	s.Executor = func(ctx context.Context, profile int64, database, statement string) (mysqlsvc.MySQLSQLResult, error) {
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
	waitSQLUI(t, tt, q)
	if received != "SELECT 1;" || q.Result() == nil || len(q.Result().Rows) != 1 || q.Result().Rows[0][0] != "1" {
		t.Fatalf("selected SQL was not executed/rendered: received=%q result=%+v err=%s", received, q.Result(), q.Error())
	}
	if !strings.Contains(*status, "1 行") || q.Error() != "" {
		t.Fatalf("missing row count/status: %s (%s)", *status, q.Error())
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
	q.Editor.Select(len("SELECT 1;\nSELECT "), len("SELECT 1;\nSELECT "))
	if err := tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, tt, q)
	if received != "SELECT 2;" {
		t.Fatalf("caret SQL not dispatched: %q", received)
	}
	// Explicit Select All sends the full authored selection.
	q.Editor.Select(0, q.Editor.Buffer.Len())
	if err := tt.Click("运行当前或选中 SQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, tt, q)
	if received != "SELECT 1;\nSELECT 2;" {
		t.Fatalf("selected full SQL not dispatched: %q", received)
	}
}

func TestSQLQueryUICancelWaitsForWorkerAndRetainsEditor(t *testing.T) {
	s, q, tt, id, _ := sqlUITestState(t)
	q.Editor.Buffer.Set("SELECT SLEEP(30)")
	started := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	release := make(chan struct{})
	calls := 0
	var mu sync.Mutex
	s.Executor = func(ctx context.Context, profile int64, database, statement string) (mysqlsvc.MySQLSQLResult, error) {
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
	if !q.Loading() || !q.CancelRequested() {
		t.Fatal("cancel must wait for driver acknowledgement")
	}
	s.Run(nil, q) // blocked while previous cancellation is pending
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
	waitSQLUI(t, tt, q)
	if q.Result() != nil || !strings.Contains(q.Error(), "服务器端更改可能已生效") ||
		q.Editor.Buffer.String() != "SELECT SLEEP(30)" {
		t.Fatalf("cancel misreported results or discarded SQL: error=%s result=%+v", q.Error(), q.Result())
	}
}
