package objectworkspace

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

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
