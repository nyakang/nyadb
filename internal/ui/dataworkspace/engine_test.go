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

func engineFixture(t *testing.T) (*EngineState, *EngineDocument, *engines.Session, *ui.Tester) {
	t.Helper()
	m := engines.NewManager()
	t.Cleanup(func() { m.Close() })
	if e := m.Connect(context.Background(), connections.Profile{ID: 81, Kind: connections.SQLite, Database: filepath.Join(t.TempDir(), "grid.sqlite")}, ""); e != nil {
		t.Fatal(e)
	}
	session, _ := m.Session(81)
	lease, _ := session.Reserve(context.Background())
	_, e := lease.Execute(context.Background(), `CREATE TABLE records(id INTEGER PRIMARY KEY,note TEXT,payload BLOB,gen TEXT GENERATED ALWAYS AS (note||'x')); INSERT INTO records VALUES(1,'before',x'00ff');`)
	lease.Close()
	if e != nil {
		t.Fatal(e)
	}
	state := NewEngineState(EngineDependencies{Session: func(id int64) (contract.Session, error) { return m.Session(id) }})
	t.Cleanup(state.CloseAll)
	doc := state.Open(2, contract.ObjectRef{ProfileID: 81, Catalog: "main", Schema: "main", Kind: "table", Name: "records"})
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); state.Consume(c.Services()); state.View(c, doc); state.Modals(c) }, 1280, 800)
	state.Load(doc)
	waitEngine(t, state, doc, tt)
	return state, doc, session, tt
}
func waitEngine(t *testing.T, state *EngineState, doc *EngineDocument, tt *ui.Tester) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for doc.Busy && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(2 * time.Millisecond)
	}
	tt.Frame()
	if doc.Busy {
		t.Fatal("executor did not acknowledge")
	}
}
func TestEngineNativeGridExplicitTransactionDoesNotReplayEdits(t *testing.T) {
	state, doc, session, tt := engineFixture(t)
	if !tt.HasText("before") {
		t.Fatalf("grid missing %v", tt.Texts())
	}
	state.Begin(doc)
	waitEngine(t, state, doc, tt)
	if !doc.HasTransaction() {
		t.Fatal("no transaction")
	}
	state.EditCell(doc, 0, "note", contract.Cell{Kind: contract.Text, Text: "after"})
	state.Save(doc)
	waitEngine(t, state, doc, tt)
	if doc.HasDraft() || !doc.HasTransaction() || doc.Page.Rows[0][1].Text != "after" {
		t.Fatalf("saved edit replayable or transaction lost %+v", doc)
	}
	outside, e := session.Page(context.Background(), contract.PageRequest{Object: doc.Object, Limit: 100})
	if e != nil || outside.Rows[0][1].Text != "before" {
		t.Fatalf("transaction not isolated %v %#v", e, outside)
	}
	state.Commit(doc)
	waitEngine(t, state, doc, tt)
	if doc.HasTransaction() {
		t.Fatal("commit retained lease")
	}
	after, e := session.Page(context.Background(), contract.PageRequest{Object: doc.Object, Limit: 100})
	if e != nil || after.Rows[0][1].Text != "after" {
		t.Fatalf("commit %v %#v", e, after)
	}
	doc.selection.Add(0)
	doc.grid.Cells.Anchor = "note"
	doc.grid.Cells.Cursor = "note"
	if v := state.Copy(doc); v != "after" {
		t.Fatalf("copy leaked unselected columns %q", v)
	}
}
func TestEngineGuardInactiveCloseKeepsIdentityAndWaitsSave(t *testing.T) {
	state, doc, session, tt := engineFixture(t)
	release := make(chan struct{})
	state.deps.Session = func(int64) (contract.Session, error) { return gatedSession{Session: session, release: release}, nil }
	state.deps.Active = func(*EngineDocument) bool { return false }
	state.EditCell(doc, 0, "note", contract.Cell{Kind: contract.Text, Text: "guard-save"})
	closed := false
	if !state.GuardLeave(doc, func(ui.Services) { closed = true }) {
		t.Fatal("dirty guard absent")
	}
	tt.Frame()
	if e := tt.Click("保存后关闭"); e != nil {
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

type uncertainSession struct{ contract.Session }

func (s uncertainSession) Begin(ctx context.Context) (contract.Transaction, error) {
	tx, e := s.Session.Begin(ctx)
	return uncertainTx{Transaction: tx}, e
}

type uncertainTx struct{ contract.Transaction }

func (t uncertainTx) Commit(ctx context.Context) (contract.CommitResult, error) {
	r, e := t.Transaction.Commit(ctx)
	if e != nil {
		return r, e
	}
	r.Uncertain = true
	return r, errors.New("lost commit acknowledgement")
}
func TestEngineUncertainCommitPreservesDraftAndExplicitVerification(t *testing.T) {
	state, doc, session, tt := engineFixture(t)
	state.deps.Session = func(int64) (contract.Session, error) { return uncertainSession{session}, nil }
	state.EditCell(doc, 0, "note", contract.Cell{Kind: contract.Text, Text: "maybe-saved"})
	state.Save(doc)
	waitEngine(t, state, doc, tt)
	if !doc.Uncertain || !doc.HasDraft() {
		t.Fatal("uncertain commit discarded draft")
	}
	state.Save(doc)
	if doc.Busy {
		t.Fatal("uncertain write replay started")
	}
	state.Verify(doc)
	waitEngine(t, state, doc, tt)
	if doc.Uncertain || doc.HasDraft() || doc.Page.Rows[0][1].Text != "maybe-saved" {
		t.Fatalf("explicit fresh verification failed: %s", doc.Error)
	}
}
func TestEngineParkedPageAndGenerationMismatch(t *testing.T) {
	state, doc, _, tt := engineFixture(t)
	active := false
	state.deps.Active = func(*EngineDocument) bool { return active }
	state.Load(doc)
	time.Sleep(20 * time.Millisecond)
	tt.Frame()
	if !doc.Busy {
		t.Fatal("parked result consumed")
	}
	active = true
	waitEngine(t, state, doc, tt)
	if doc.Error != "" {
		t.Fatal(doc.Error)
	}
	doc.generation++
	state.Load(doc)
	if doc.Busy || doc.Error == "" {
		t.Fatal("changed generation allowed read")
	}
}

type gatedSession struct {
	contract.Session
	release <-chan struct{}
}

func (s gatedSession) Begin(ctx context.Context) (contract.Transaction, error) {
	tx, e := s.Session.Begin(ctx)
	return gatedTx{Transaction: tx, release: s.release}, e
}

type gatedTx struct {
	contract.Transaction
	release <-chan struct{}
}

func (t gatedTx) Apply(ctx context.Context, o contract.ObjectRef, e []contract.Edit) (contract.CommitResult, error) {
	select {
	case <-t.release:
		return t.Transaction.Apply(ctx, o, e)
	case <-ctx.Done():
		return contract.CommitResult{}, ctx.Err()
	}
}
