package objectworkspace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func tableModalTester(s *State) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.Consume(c.Services())
		s.Modals(c)
	}, 900, 700)
}

func waitTableChange(t *testing.T, s *State, tt *ui.Tester) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for s.TablePending() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	tt.Frame()
	if s.TablePending() {
		t.Fatal("table operation did not settle")
	}
}

func TestTableActionErrorRemainsVisibleInDialog(t *testing.T) {
	s, session := testViewer()
	s.Runner = func(context.Context, int64, mysqlsvc.MySQLTableChange) error {
		return errors.New("permission denied by server")
	}
	s.BeginTableChange(session.ProfileID, "app_db", "orders", mysqlsvc.MySQLTableRename)
	tt := tableModalTester(s)
	if err := tt.Click("新表名称"); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Type("orders_new")
	if err := tt.Click("确认重命名"); err != nil {
		t.Fatal(err)
	}
	waitTableChange(t, s, tt)
	if !s.TableDialogOpen() || s.TableError() != "permission denied by server" || !tt.HasText(s.TableError()) {
		t.Fatalf("error feedback missing: %q", s.TableError())
	}
}

func TestCreateTableUsesEnteredNameAndReportsDestination(t *testing.T) {
	s, session := testViewer()
	requests := make(chan mysqlsvc.MySQLTableChange, 1)
	s.Runner = func(_ context.Context, id int64, request mysqlsvc.MySQLTableChange) error {
		if id != session.ProfileID {
			t.Error("creation lost the original connection")
		}
		requests <- request
		return nil
	}
	var status string
	s.deps.Status = func(message string) { status = message }
	s.BeginTableChange(session.ProfileID, "app_db", "orders", mysqlsvc.MySQLTableCreate)
	tt := tableModalTester(s)
	if err := tt.Click("新表名称"); err != nil {
		t.Fatal(err)
	}
	tt.Type("new_orders")
	if err := tt.Click("确认新建表"); err != nil {
		t.Fatal(err)
	}
	waitTableChange(t, s, tt)
	select {
	case request := <-requests:
		if request.Action != mysqlsvc.MySQLTableCreate || request.Database != "app_db" || request.Table != "new_orders" || request.NewName != "" {
			t.Fatalf("creation targeted the source instead of the entered name: %+v", request)
		}
	default:
		t.Fatal("confirmed creation did not reach the injected service")
	}
	if s.TableDialogOpen() || status != "新建表成功：app_db.new_orders" {
		t.Fatalf("creation reported the wrong destination: %q", status)
	}
}

func TestTableChangeOldGenerationCannotTouchCurrentBrowser(t *testing.T) {
	s, session := testViewer()
	var status string
	var changes int
	s.deps.Status = func(message string) { status = message }
	s.deps.Changed = func(ui.Services, TableEvent) { changes++ }
	results := make(chan mysqlTableChangeResult, 1)
	s.tablePending, s.tableDialog, s.tableResults = true, true, results
	results <- mysqlTableChangeResult{profileID: session.ProfileID, generation: session.Generation + 1, database: "app_db", table: "orders", action: mysqlsvc.MySQLTableDrop}
	s.Consume(ui.Services{})
	if changes != 0 || s.tableDialog || s.tablePending || !strings.Contains(status, "原会话已失效") {
		t.Fatalf("stale result changed browser: changes=%d status=%q", changes, status)
	}
}

func TestTableChangeParkedCompletionKeepsOriginalIdentity(t *testing.T) {
	s, active := testViewer()
	original := *active
	original.Generation = 4
	active.ProfileID = 2
	s.deps.SessionFor = func(id int64) Session {
		if id == original.ProfileID {
			return original
		}
		return *active
	}
	var got TableEvent
	s.deps.Changed = func(_ ui.Services, event TableEvent) { got = event }
	out := make(chan mysqlTableChangeResult, 1)
	s.tableResults, s.tablePending = out, true
	out <- mysqlTableChangeResult{profileID: original.ProfileID, generation: 4, database: "analytics", table: "report", newName: "copy", action: mysqlsvc.MySQLTableCopyData, err: errors.New("INSERT failed")}
	s.Consume(ui.Services{})
	if got.ProfileID != original.ProfileID || got.Generation != 4 || got.Database != "analytics" || got.Error == nil || s.tableDialog || s.tablePending {
		t.Fatalf("parked completion changed identity: %+v", got)
	}
}

func TestObjectCloseAllCancelsTableWorkerAndRevokesLateCompletion(t *testing.T) {
	s, _ := testViewer()
	started, canceled, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	s.Runner = func(ctx context.Context, _ int64, request mysqlsvc.MySQLTableChange) error {
		if request.Database != "db" || request.Table != "orders" {
			t.Error("worker lost table identity")
		}
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return nil
	}
	changes := 0
	s.deps.Changed = func(ui.Services, TableEvent) { changes++ }
	s.BeginTableChange(1, "db", "orders", mysqlsvc.MySQLTableDeleteRows)
	s.tableConfirmation = "orders"
	s.startMySQLTableChange(ui.Services{})
	waitSignal(t, started)
	d := s.OpenStructure(1, 1, "db", "orders")
	d.data = fakeStructure("db", "orders")
	s.presentation(1).Row = 1
	// Retain the original channel only in the test to acknowledge late work.
	originalResults := s.tableResults
	s.CloseAll()
	s.CloseAll()
	waitSignal(t, canceled)
	close(release)
	go func() { <-originalResults; done <- struct{}{} }()
	waitSignal(t, done)
	s.Consume(ui.Services{})
	if changes != 0 || s.tablePending || s.tableDialog || s.tableResults != nil || len(s.documents) != 0 || len(s.browsers) != 0 || d.data != nil {
		t.Fatal("closed workspace retained state or dispatched a late event")
	}
}

func TestObjectBrowserPresentationBelongsToEachProfile(t *testing.T) {
	snapshot := BrowserSnapshot{ProfileID: 1, Database: "db", Objects: []mysqlsvc.MySQLDBObject{{Name: "orders", Kind: "表"}, {Name: "sales", Kind: "视图"}}}
	s := New(Dependencies{Browser: func() BrowserSnapshot { return snapshot }})
	s.SetFilter(1, "视图")
	s.SelectRow(1, 0)
	snapshot.ProfileID = 2
	s.SetFilter(2, "表")
	s.SelectRow(2, 0)
	snapshot.ProfileID = 1
	if s.FilterKind(1) != "视图" || s.SelectedRow(1) != 0 || s.SelectedObjects()[0].Name != "sales" {
		t.Fatal("sibling profile replaced object presentation")
	}
	snapshot.Database = "other"
	if s.FilterKind(1) != "全部" || s.SelectedRow(1) != -1 {
		t.Fatal("database switch did not reset filter and selection")
	}
	snapshot.ProfileID = 2
	snapshot.Database = "db"
	if s.FilterKind(2) != "表" || s.SelectedRow(2) != 0 {
		t.Fatal("database switch reset another profile")
	}
}
