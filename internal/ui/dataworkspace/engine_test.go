package dataworkspace

import (
	"context"
	"errors"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/engines"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"path/filepath"
	"testing"
	"time"
)

func engineFixture(t *testing.T) (*State, *Document, *engines.Session, *ui.Tester) {
	t.Helper()
	// Register directory cleanup before resource cleanup so documents and
	// sessions release all SQLite handles before Windows removes the file.
	path := filepath.Join(t.TempDir(), "grid.sqlite")
	m := engines.NewManager()
	t.Cleanup(func() { m.Close() })
	if e := m.Connect(context.Background(), connections.Profile{ID: 81, Kind: connections.SQLite, Database: path}, ""); e != nil {
		t.Fatal(e)
	}
	session, _ := m.Session(81)
	lease, _ := session.Reserve(context.Background())
	_, e := lease.Execute(context.Background(), `CREATE TABLE records(id INTEGER PRIMARY KEY,note TEXT,payload BLOB,gen TEXT GENERATED ALWAYS AS (note||'x')); INSERT INTO records VALUES(1,'before',x'00ff');`)
	lease.Close()
	if e != nil {
		t.Fatal(e)
	}
	state := New(Dependencies{Session: func() Session {
		return Session{ProfileID: 81, Generation: session.Generation(), Connected: session.Alive()}
	}, Runtime: session.DataRuntime})
	t.Cleanup(state.CloseAll)
	doc := state.OpenObject(2, contract.ObjectRef{ProfileID: 81, Catalog: "main", Schema: "main", Kind: "table", Name: "records"}, session.Capabilities())
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); state.Consume(c.Services()); state.View(c, doc); state.Modals(c) }, 1280, 800)
	state.Load(ui.Services{}, doc, true)
	waitEngine(t, state, doc, tt)
	return state, doc, session, tt
}
func waitEngine(t *testing.T, state *State, doc *Document, tt *ui.Tester) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for doc.Busy() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(2 * time.Millisecond)
	}
	tt.Frame()
	if doc.Busy() {
		t.Fatalf("executor did not acknowledge: loading=%v edit=%v transaction=%v export=%v err=%s editErr=%s request=%d", doc.loading, doc.edit.Busy, doc.transaction.busy, doc.export.Busy, doc.err, doc.edit.Err, doc.edit.Request)
	}
}
func TestEngineNativeGridExplicitTransactionDoesNotReplayEdits(t *testing.T) {
	state, doc, session, tt := engineFixture(t)
	if !tt.HasText("before") {
		t.Fatalf("grid missing %v", tt.Texts())
	}
	state.Begin(ui.Services{}, doc)
	waitEngine(t, state, doc, tt)
	if !doc.HasTransaction() {
		t.Fatal("no transaction")
	}
	state.EditCell(ui.Services{}, doc, 0, "note", contract.Cell{Kind: contract.Text, Text: "after"})
	state.Save(ui.Services{}, doc)
	waitEngine(t, state, doc, tt)
	if doc.HasDraft() || !doc.HasTransaction() || doc.Page().Rows[0][1].Text != "after" {
		t.Fatalf("saved edit replayable or transaction lost %+v", doc)
	}
	outside, e := session.Page(context.Background(), contract.PageRequest{Object: doc.Object(), Limit: 100})
	if e != nil || outside.Rows[0][1].Text != "before" {
		t.Fatalf("transaction not isolated %v %#v", e, outside)
	}
	state.Commit(ui.Services{}, doc)
	waitEngine(t, state, doc, tt)
	if doc.HasTransaction() {
		t.Fatal("commit retained lease")
	}
	after, e := session.Page(context.Background(), contract.PageRequest{Object: doc.Object(), Limit: 100})
	if e != nil || after.Rows[0][1].Text != "after" {
		t.Fatalf("commit %v %#v", e, after)
	}
	doc.selection.Add(0)
	doc.grid.Cells.Anchor = "note"
	doc.grid.Cells.Cursor = "note"
	names, rows, _ := mysqlDataSelectedPreview(doc)
	if v := mysqlDataDelimitedPreview(names, rows, '\t', false, true); v != "after" {
		t.Fatalf("copy leaked unselected columns %q", v)
	}
}
func TestEngineGuardInactiveCloseKeepsIdentityAndWaitsSave(t *testing.T) {
	state, doc, session, tt := engineFixture(t)
	release := make(chan struct{})
	runtime := session.DataRuntime()
	save := runtime.Saver
	runtime.Saver = func(ctx context.Context, id int64, db, table string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
		select {
		case <-release:
			return save(ctx, id, db, table, edits)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	state.deps.RuntimeFor = func(int64) Runtime { return runtime }
	state.deps.SessionFor = stateSessionFor(state)
	state.deps.Session = func() Session { return Session{ProfileID: 999} }
	state.EditCell(ui.Services{}, doc, 0, "note", contract.Cell{Kind: contract.Text, Text: "guard-save"})
	closed := false
	if !state.GuardLeave(doc, func(ui.Services) { closed = true }) {
		t.Fatal("dirty guard absent")
	}
	tt.Frame()
	if e := tt.Click("应用更改"); e != nil {
		t.Fatal(e)
	}
	if closed {
		t.Fatal("guard completed before executor")
	}
	close(release)
	waitEngine(t, state, doc, tt)
	if !closed || doc.HasDraft() {
		t.Fatal("inactive saved document did not complete close guard")
	}
}

func TestEngineUncertainCommitPreservesDraftAndExplicitVerification(t *testing.T) {
	state, doc, session, tt := engineFixture(t)
	runtime := session.DataRuntime()
	save := runtime.Saver
	runtime.Saver = func(ctx context.Context, id int64, db, table string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
		result, err := save(ctx, id, db, table, edits)
		if err == nil {
			result.CommitUncertain = true
			err = errors.New("lost commit acknowledgement")
		}
		return result, err
	}
	state.deps.Runtime = func() Runtime { return runtime }
	state.EditCell(ui.Services{}, doc, 0, "note", contract.Cell{Kind: contract.Text, Text: "maybe-saved"})
	state.Save(ui.Services{}, doc)
	waitEngine(t, state, doc, tt)
	if doc.edit.Uncertain == nil || !doc.HasDraft() {
		t.Fatal("uncertain commit discarded draft")
	}
	state.Save(ui.Services{}, doc)
	if doc.Busy() {
		t.Fatal("uncertain write replay started")
	}
	state.VerifySave(ui.Services{}, doc)
	waitEngine(t, state, doc, tt)
	if doc.edit.Uncertain != nil || doc.HasDraft() || doc.Page().Rows[0][1].Text != "maybe-saved" {
		t.Fatalf("explicit fresh verification failed: %s", doc.Error())
	}
}
func TestEngineParkedPageAndGenerationMismatch(t *testing.T) {
	state, doc, _, tt := engineFixture(t)
	active := false
	state.deps.SessionFor = stateSessionFor(state)
	original := state.deps.Session
	state.deps.Session = func() Session {
		if !active {
			return Session{ProfileID: 999}
		}
		return original()
	}
	state.deps.Session = original
	state.Load(ui.Services{}, doc, true)
	state.deps.Session = func() Session {
		if !active {
			return Session{ProfileID: 999}
		}
		return original()
	}
	time.Sleep(20 * time.Millisecond)
	tt.Frame()
	if !doc.Busy() {
		t.Fatal("parked result consumed")
	}
	active = true
	waitEngine(t, state, doc, tt)
	if doc.Error() != "" {
		t.Fatal(doc.Error())
	}
	doc.generation++
	state.Load(ui.Services{}, doc, true)
	if doc.Busy() || doc.Error() == "" {
		t.Fatal("changed generation allowed read")
	}
}

func stateSessionFor(s *State) func(int64) Session {
	snapshot := s.session()
	return func(int64) Session { return snapshot }
}

func TestUnifiedExplicitCommitLostReceiptAfterKeyChangeAndDelete(t *testing.T) {
	for _, deleteRow := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed key", true: "delete"}[deleteRow], func(t *testing.T) {
			state, doc, session, tt := engineFixture(t)
			runtime := session.DataRuntime()
			commit := runtime.Transactions.Commit
			runtime.Transactions.Commit = func(ctx context.Context, tx contract.DataTransaction) error {
				if e := commit(ctx, tx); e != nil {
					return e
				}
				return &contract.CommitError{Result: contract.CommitResult{Uncertain: true}, Err: errors.New("lost explicit commit receipt")}
			}
			state.deps.Runtime = func() Runtime { return runtime }
			state.Begin(ui.Services{}, doc)
			waitEngine(t, state, doc, tt)
			state.EditCell(ui.Services{}, doc, 0, "id", contract.Cell{Kind: contract.Integer, Text: "9"})
			state.Save(ui.Services{}, doc)
			waitEngine(t, state, doc, tt)
			if deleteRow {
				doc.edit.Batch = true
				state.deleteMySQLDataRecords(ui.Services{}, doc, 0)
			} else {
				state.EditCell(ui.Services{}, doc, 0, "note", contract.Cell{Kind: contract.Text, Text: "latest"})
			}
			state.Save(ui.Services{}, doc)
			waitEngine(t, state, doc, tt)
			if len(doc.transaction.keys) != 1 {
				t.Fatal("same logical row staged twice after key change")
			}
			state.Commit(ui.Services{}, doc)
			waitEngine(t, state, doc, tt)
			if doc.edit.Uncertain == nil || !doc.HasDraft() {
				t.Fatal("uncertain transaction discarded evidence")
			}
			// Supply the executor's final staged verification identity as a lost-receipt fixture.
			if deleteRow {
				doc.edit.Uncertain.Saved = []contract.Edit{{Kind: "delete", Identity: contract.RowIdentity{Columns: []string{"id"}, Values: []contract.Cell{{Kind: contract.Integer, Text: "9"}}}}}
			} else {
				doc.edit.Uncertain.Saved = []contract.Edit{{Kind: "update", Identity: contract.RowIdentity{Columns: []string{"id"}, Values: []contract.Cell{{Kind: contract.Integer, Text: "9"}}}, Values: map[string]contract.Cell{"id": {Kind: contract.Integer, Text: "9"}, "note": {Kind: contract.Text, Text: "latest"}}}}
			}
			state.Save(ui.Services{}, doc)
			if doc.Busy() {
				t.Fatal("uncertain transaction replayed")
			}
			state.VerifySave(ui.Services{}, doc)
			waitEngine(t, state, doc, tt)
			if doc.edit.Uncertain != nil || doc.HasDraft() || doc.HasTransaction() {
				t.Fatalf("explicit verification failed: %s", doc.edit.Err)
			}
			if deleteRow && len(doc.Page().Rows) != 0 {
				t.Fatal("deleted row restored after acknowledgement")
			}
		})
	}
}
func TestUnifiedSaveCancellationWaitsForAcknowledgement(t *testing.T) {
	state, doc, _, tt := engineFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	state.Saver = func(ctx context.Context, id int64, db, table string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
		close(started)
		<-release
		return &contract.DataEditResult{CommitUncertain: true}, ctx.Err()
	}
	state.EditCell(ui.Services{}, doc, 0, "note", contract.Cell{Kind: contract.Text, Text: "pending"})
	state.Save(ui.Services{}, doc)
	<-started
	state.stopMySQLDataEdit(doc)
	tt.Frame()
	if !doc.Busy() {
		t.Fatal("cancel enabled replay before acknowledgement")
	}
	close(release)
	waitEngine(t, state, doc, tt)
	if !doc.HasDraft() || doc.edit.Uncertain == nil {
		t.Fatal("uncertain cancellation lost draft")
	}
}
