package dataworkspace

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func transactionWorkspace(t *testing.T) (*State, *Document, *Session) {
	t.Helper()
	session := &Session{ProfileID: 12, Generation: 2, Connected: true}
	s := New(Dependencies{Session: func() Session { return *session }})
	d := s.Open(1, 12, "db", "orders")
	ref := &mysqlsvc.MySQLRowRef{Database: "db", Table: "orders", Columns: []string{"id"}, Values: [][]byte{[]byte("1")}}
	hash := "before"
	snapshot := &mysqlsvc.MySQLRowSnapshot{Ref: *ref, Columns: []string{"id", "note"}, Fingerprints: []*string{&hash, &hash}}
	d.page = &mysqlsvc.MySQLDataPage{Database: "db", Table: "orders", Limit: 20, Columns: []string{"id", "note"}, ColumnKinds: []string{"bigint", "varchar"}, ColumnMetadata: []mysqlsvc.MySQLStructureColumn{{Name: "id", DataType: "bigint"}, {Name: "note", DataType: "varchar"}}, Rows: [][]mysqlsvc.MySQLDataCell{{{Text: "1"}, {Text: "before"}}}, RowRefs: []*mysqlsvc.MySQLRowRef{ref}, RowSnapshots: []*mysqlsvc.MySQLRowSnapshot{snapshot}}
	page := d.page
	s.AdvancedLoader = func(context.Context, int64, string, string, mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error) {
		return cloneEditingPage(page), nil
	}
	s.Transactions = DataTransactions{
		Begin: func(context.Context, int64, string, string) (*mysqlsvc.MySQLDataTransaction, error) {
			return &mysqlsvc.MySQLDataTransaction{}, nil
		},
		Commit: func(context.Context, *mysqlsvc.MySQLDataTransaction) error { return nil }, Rollback: func(context.Context, *mysqlsvc.MySQLDataTransaction) error { return nil }, Close: func(*mysqlsvc.MySQLDataTransaction) {},
		Page: func(context.Context, *mysqlsvc.MySQLDataTransaction, int64, string, string, mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error) {
			return cloneEditingPage(page), nil
		},
		Row: func(context.Context, *mysqlsvc.MySQLDataTransaction, int64, string, string, mysqlsvc.MySQLRowRef) (*mysqlsvc.MySQLDataRow, error) {
			return &mysqlsvc.MySQLDataRow{Snapshot: cloneSnapshot(snapshot), Values: map[string]mysqlsvc.MySQLDataValue{"note": {Text: "pending"}}}, nil
		},
		Save: func(_ context.Context, _ *mysqlsvc.MySQLDataTransaction, _ int64, _, _ string, edits []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataEditResult, error) {
			r := &mysqlsvc.MySQLDataEditResult{}
			for _, edit := range edits {
				r.Outcomes = append(r.Outcomes, mysqlsvc.MySQLDataEditOutcome{Kind: edit.Kind, Original: edit.Original, Saved: cloneSnapshot(snapshot), PreviewColumns: []string{"id", "note"}, Previews: []mysqlsvc.MySQLDataCell{{Text: "1"}, {Text: edit.Values["note"].Text}}})
			}
			return r, nil
		},
	}
	t.Cleanup(func() { s.Close(d) })
	return s, d, session
}
func consumeTransactionResult(t *testing.T, s *State, d *Document) {
	t.Helper()
	select {
	case r := <-d.transaction.results:
		out := make(chan dataTransactionResult, 1)
		out <- r
		d.transaction.results = out
		s.consumeDataTransactions(ui.Services{})
	case <-time.After(time.Second):
		t.Fatal("transaction executor did not acknowledge")
	}
}
func consumeTransactionPage(t *testing.T, s *State, d *Document) {
	t.Helper()
	if d.results == nil {
		return
	}
	select {
	case r := <-d.results:
		out := make(chan mysqlDataResult, 1)
		out <- r
		d.results = out
		s.consumePages()
	case <-time.After(time.Second):
		t.Fatal("transaction page did not complete")
	}
}
func TestDataTransactionParkedBeginRetainsOriginalProfileAndCloseRevokes(t *testing.T) {
	s, d, session := transactionWorkspace(t)
	closed := make(chan struct{}, 1)
	s.Transactions.Close = func(*mysqlsvc.MySQLDataTransaction) { closed <- struct{}{} }
	s.beginDataTransaction(ui.Services{}, d)
	result := <-d.transaction.results
	out := make(chan dataTransactionResult, 1)
	out <- result
	d.transaction.results = out
	session.ProfileID = 13
	s.Consume(ui.Services{})
	if !dataTransactionBusy(d) || dataTransactionActive(d) {
		t.Fatal("parked begin changed the active profile")
	}
	s.Close(d)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close leaked unconsumed transaction token")
	}
}
func TestDataTransactionRuntimeOwnsPageRowAndSaveRouting(t *testing.T) {
	s, d, _ := transactionWorkspace(t)
	var pages, rows, saves atomic.Int32
	pageFn, rowFn, saveFn := s.Transactions.Page, s.Transactions.Row, s.Transactions.Save
	s.Transactions.Page = func(ctx context.Context, token *mysqlsvc.MySQLDataTransaction, p int64, db, tb string, o mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error) {
		pages.Add(1)
		return pageFn(ctx, token, p, db, tb, o)
	}
	s.Transactions.Row = func(ctx context.Context, token *mysqlsvc.MySQLDataTransaction, p int64, db, tb string, ref mysqlsvc.MySQLRowRef) (*mysqlsvc.MySQLDataRow, error) {
		rows.Add(1)
		return rowFn(ctx, token, p, db, tb, ref)
	}
	s.Transactions.Save = func(ctx context.Context, token *mysqlsvc.MySQLDataTransaction, p int64, db, tb string, edits []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataEditResult, error) {
		saves.Add(1)
		return saveFn(ctx, token, p, db, tb, edits)
	}
	s.beginDataTransaction(ui.Services{}, d)
	consumeTransactionResult(t, s, d)
	consumeTransactionPage(t, s, d)
	if _, err := s.rowReaderFor(d)(context.Background(), 12, "db", "orders", *d.page.RowRefs[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.saverFor(d)(context.Background(), 12, "db", "orders", []mysqlsvc.MySQLDataEdit{{Kind: "update", Values: map[string]mysqlsvc.MySQLDataValue{"note": {Text: "pending"}}}}); err != nil {
		t.Fatal(err)
	}
	if pages.Load() != 1 || rows.Load() != 1 || saves.Load() != 1 {
		t.Fatal("transaction requests escaped dedicated runtime")
	}
	if s.GuardLeave(d, nil) {
		t.Fatal("transaction alone blocked page/filter navigation")
	}
	if !s.GuardTransactionLeave(d, nil) || !d.transaction.leaveOpen {
		t.Fatal("close omitted transaction leave decision")
	}
}
func TestDataTransactionUnknownCommitPreservesDraftAndProhibitsReplay(t *testing.T) {
	s, d, _ := transactionWorkspace(t)
	var saves, commits atomic.Int32
	saveFn := s.Transactions.Save
	s.Transactions.Save = func(ctx context.Context, token *mysqlsvc.MySQLDataTransaction, p int64, db, tb string, edits []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataEditResult, error) {
		saves.Add(1)
		return saveFn(ctx, token, p, db, tb, edits)
	}
	s.Transactions.Commit = func(context.Context, *mysqlsvc.MySQLDataTransaction) error {
		commits.Add(1)
		return mysqlsvc.ErrMySQLDataCommitUncertain
	}
	s.beginDataTransaction(ui.Services{}, d)
	consumeTransactionResult(t, s, d)
	consumeTransactionPage(t, s, d)
	draft := s.mysqlRowDraft(d, 0)
	draft.Values["note"] = mysqlsvc.MySQLDataValue{Text: "pending"}
	d.page.Rows[0][1].Text = "pending"
	s.commitDataTransaction(ui.Services{}, d)
	select {
	case r := <-d.edit.Results:
		out := make(chan mysqlEditResult, 1)
		out <- r
		d.edit.Results = out
		s.consumeEdits(ui.Services{})
	case <-time.After(time.Second):
		t.Fatal("pending save did not complete")
	}
	consumeTransactionResult(t, s, d)
	if !d.transaction.uncertain || !d.HasDraft() || d.edit.Uncertain == nil || saves.Load() != 1 || commits.Load() != 1 {
		t.Fatal("uncertain explicit commit lost drafts")
	}
	s.saveMySQLData(ui.Services{}, d)
	s.commitDataTransaction(ui.Services{}, d)
	s.beginDataTransaction(ui.Services{}, d)
	if d.edit.Busy || dataTransactionBusy(d) || saves.Load() != 1 || commits.Load() != 1 {
		t.Fatal("uncertain transaction replayed")
	}
}
func TestDataTransactionCancelStaysBusyUntilExecutorAcknowledges(t *testing.T) {
	s, d, _ := transactionWorkspace(t)
	started, release := make(chan struct{}), make(chan struct{})
	s.Transactions.Commit = func(context.Context, *mysqlsvc.MySQLDataTransaction) error { close(started); <-release; return nil }
	s.beginDataTransaction(ui.Services{}, d)
	consumeTransactionResult(t, s, d)
	consumeTransactionPage(t, s, d)
	s.commitDataTransaction(ui.Services{}, d)
	<-started
	s.stopDataTransaction(d)
	if !dataTransactionBusy(d) {
		t.Fatal("cancel enabled replay before driver acknowledgement")
	}
	close(release)
	consumeTransactionResult(t, s, d)
	if dataTransactionActive(d) || dataTransactionBusy(d) {
		t.Fatal("acknowledged commit remained active")
	}
}
func TestDataTransactionGenerationMismatchNeverRunsLeaveContinuation(t *testing.T) {
	s, d, session := transactionWorkspace(t)
	s.beginDataTransaction(ui.Services{}, d)
	consumeTransactionResult(t, s, d)
	consumeTransactionPage(t, s, d)
	called := false
	d.transaction.after = func(ui.Services) { called = true }
	s.commitDataTransaction(ui.Services{}, d)
	session.Generation++
	consumeTransactionResult(t, s, d)
	if called || !d.transaction.uncertain || dataTransactionBusy(d) || d.edit.Uncertain == nil {
		t.Fatal("old-generation commit replaced current state or allowed replay")
	}
}

func TestDataTransactionTransferBusyAndUncertainBlockAllEntryPoints(t *testing.T) {
	s, d, _ := transactionWorkspace(t)
	for _, uncertain := range []bool{false, true} {
		d.transfer.Busy, d.transfer.Uncertain = !uncertain, uncertain
		s.beginDataTransaction(ui.Services{}, d)
		if dataTransactionBusy(d) || s.saverFor(d) != nil || s.rowReaderFor(d) != nil || s.advancedLoaderFor(d) != nil {
			t.Fatal("transfer and transaction accessed the same document concurrently")
		}
		if !s.GuardTransactionLeave(d, nil) || !s.GuardLeave(d, nil) {
			t.Fatal("in-flight transfer escaped leave guard")
		}
	}
	d.transfer.Busy, d.transfer.Uncertain = false, false
}

func TestDataTransactionRollbackDiscardsDraftOnlyAfterAcknowledgement(t *testing.T) {
	s, d, _ := transactionWorkspace(t)
	started, release := make(chan struct{}), make(chan struct{})
	s.Transactions.Rollback = func(context.Context, *mysqlsvc.MySQLDataTransaction) error { close(started); <-release; return nil }
	s.beginDataTransaction(ui.Services{}, d)
	consumeTransactionResult(t, s, d)
	consumeTransactionPage(t, s, d)
	draft := s.mysqlRowDraft(d, 0)
	draft.Values["note"] = mysqlsvc.MySQLDataValue{Text: "pending"}
	d.page.Rows[0][1].Text = "pending"
	s.rollbackDataTransaction(ui.Services{}, d)
	<-started
	if !d.HasDraft() || !dataTransactionBusy(d) {
		t.Fatal("rollback dropped draft before acknowledgement")
	}
	close(release)
	consumeTransactionResult(t, s, d)
	consumeTransactionPage(t, s, d)
	if d.HasDraft() || dataTransactionActive(d) || d.page.Rows[0][1].Text != "before" {
		t.Fatal("rollback retained pending data")
	}
}

func TestDataTransactionFieldInspectorUsesOriginalTokenAndBoundedRead(t *testing.T) {
	s, d, _ := transactionWorkspace(t)
	s.inspector.deps.Session = s.deps.Session
	var received atomic.Int32
	s.Transactions.Chunk = func(_ context.Context, token *mysqlsvc.MySQLDataTransaction, profile int64, database, table, column string, ref mysqlsvc.MySQLRowRef, offset int64, units int) (*mysqlsvc.MySQLFieldChunk, error) {
		if token == nil || profile != 12 || database != "db" || table != "orders" || column != "note" || string(ref.Values[0]) != "1" || offset != 0 || units != 1024 {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		received.Add(1)
		return &mysqlsvc.MySQLFieldChunk{Text: "pending", TotalUnits: 7, NextOffset: 7}, nil
	}
	s.beginDataTransaction(ui.Services{}, d)
	consumeTransactionResult(t, s, d)
	consumeTransactionPage(t, s, d)
	d.selectedRow = 0
	d.grid.Cells.Cursor = "note"
	p := s.inspectorPanel(d)
	s.inspector.LoadChunk(p, nil)
	select {
	case r := <-p.chunkResults:
		out := make(chan chunkResult, 1)
		out <- r
		p.chunkResults = out
		s.inspector.consumeChunks()
	case <-time.After(time.Second):
		t.Fatal("transaction field read did not complete")
	}
	if received.Load() != 1 || p.ChunkText() != "pending" || p.ChunkError() != "" {
		t.Fatal("field inspector escaped original transaction")
	}
	s.Close(d)
	if p.chunkLoader != nil || p.chunkError != nil {
		t.Fatal("closed inspector retained its owning transaction")
	}
}

func TestDataTransactionCommitWaitsForInsertedRowIdentityRefresh(t *testing.T) {
	s, d, _ := transactionWorkspace(t)
	var commits atomic.Int32
	s.Transactions.Commit = func(context.Context, *mysqlsvc.MySQLDataTransaction) error { commits.Add(1); return nil }
	s.Transactions.Save = func(context.Context, *mysqlsvc.MySQLDataTransaction, int64, string, string, []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataEditResult, error) {
		return &mysqlsvc.MySQLDataEditResult{Outcomes: []mysqlsvc.MySQLDataEditOutcome{{Kind: "insert"}}}, nil
	}
	s.beginDataTransaction(ui.Services{}, d)
	consumeTransactionResult(t, s, d)
	consumeTransactionPage(t, s, d)
	d.page.Rows = append(d.page.Rows, []mysqlsvc.MySQLDataCell{{Text: "2"}, {Text: "inserted"}})
	d.page.RowRefs = append(d.page.RowRefs, nil)
	d.page.RowSnapshots = append(d.page.RowSnapshots, nil)
	d.edit.Drafts = map[string]*mysqlRowDraft{"new": {Key: "new", Row: 1, Kind: "insert", Values: map[string]mysqlsvc.MySQLDataValue{"note": {Text: "inserted"}}}}
	s.commitDataTransaction(ui.Services{}, d)
	select {
	case r := <-d.edit.Results:
		out := make(chan mysqlEditResult, 1)
		out <- r
		d.edit.Results = out
		s.consumeEdits(ui.Services{})
	case <-time.After(time.Second):
		t.Fatal("insert did not complete")
	}
	if !d.loading || !d.transaction.commitAfterRead || commits.Load() != 0 {
		t.Fatal("commit ignored inserted-row identity refresh")
	}
	consumeTransactionPage(t, s, d)
	s.consumeDataTransactions(ui.Services{})
	consumeTransactionResult(t, s, d)
	if commits.Load() != 1 || dataTransactionActive(d) {
		t.Fatal("commit continuation was dropped after inserted-row refresh")
	}
}
