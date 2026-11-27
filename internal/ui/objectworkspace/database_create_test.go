package objectworkspace

import (
	"context"
	"io"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type createUIProvider func(contract.DatabaseCreateDocumentRequest) (contract.DatabaseCreateDocumentLease, error)

func (createUIProvider) CanCreateDatabase(t contract.DatabaseCreateTarget) bool {
	return t.Namespace.ProfileID == 83 && t.Namespace.Schema != ""
}
func (f createUIProvider) ReserveDatabaseCreateDocument(r contract.DatabaseCreateDocumentRequest) (contract.DatabaseCreateDocumentLease, error) {
	return f(r)
}

type createUILease struct {
	r   contract.DatabaseCreateDocumentRequest
	run func(context.Context, contract.DatabaseCreateDocumentRequest, contract.DatabaseCreateSnapshot) contract.DatabaseCreateOutcome
}

func (l *createUILease) Snapshot(context.Context) (contract.DatabaseCreateSnapshot, error) {
	return contract.DatabaseCreateSnapshot{Target: l.r.Target, Engine: "databend", Revision: "proof", CreateSQL: "CREATE DATABASE original"}, nil
}
func (l *createUILease) Create(ctx context.Context, v contract.DatabaseCreateSnapshot) contract.DatabaseCreateOutcome {
	return l.run(ctx, l.r, v)
}
func (*createUILease) OriginalOutcome() *contract.DatabaseCreateOutcome { return nil }
func (*createUILease) Close() error                                     { return nil }
func createUIResult(r contract.DatabaseCreateDocumentRequest, v contract.DatabaseCreateSnapshot) contract.DatabaseCreateOutcome {
	a := v
	a.Exists = true
	a.NativeID = "61"
	a.DDL = "complete original definition"
	return contract.DatabaseCreateOutcome{State: "committed", Verified: true, Actual: &a, Receipt: &contract.DatabaseCreateReceipt{DocumentID: r.DocumentID, Sequence: 1, Original: v, SQL: v.CreateSQL, NativeJSON: `{"ack":true}`, State: "committed", Verified: true, Actual: &a}}
}
func databaseCreateUIState(t *testing.T, run func(context.Context, contract.DatabaseCreateDocumentRequest, contract.DatabaseCreateSnapshot) contract.DatabaseCreateOutcome) (*State, *Session, *uint64, *ui.Tester, ui.Services) {
	t.Helper()
	w := &Session{ProfileID: 83, Generation: 9, Connected: true}
	generation := uint64(73)
	s := New(Dependencies{Session: func() Session { return *w }, SessionFor: func(int64) Session { return *w }, Namespace: func(id int64, name string) contract.NamespaceRef {
		return contract.NamespaceRef{ProfileID: id, Schema: name}
	}, DatabaseCreateTarget: func(id int64, name string) contract.DatabaseCreateTarget {
		return contract.DatabaseCreateTarget{Namespace: contract.NamespaceRef{ProfileID: id, Schema: name}, Generation: generation}
	}, DatabaseCreateFor: func(int64) (contract.DatabaseCreateDocumentsProvider, error) {
		return createUIProvider(func(r contract.DatabaseCreateDocumentRequest) (contract.DatabaseCreateDocumentLease, error) {
			return &createUILease{r: r, run: run}, nil
		}), nil
	}})
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.Consume(services); s.Modals(c) }, 800, 650)
	s.BeginDatabaseCreate(83)
	tt.Frame()
	s.readDatabaseCreate(services)
	waitSchema(t, tt, func() bool { return !s.DatabaseCreatePending() })
	t.Cleanup(s.CloseAll)
	return s, w, &generation, tt, services
}

func TestDatabaseCreateUIRejectsChangedNameAndRetainsParkedLateReceipt(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s, w, generation, tt, services := databaseCreateUIState(t, func(_ context.Context, r contract.DatabaseCreateDocumentRequest, v contract.DatabaseCreateSnapshot) contract.DatabaseCreateOutcome {
		calls.Add(1)
		close(entered)
		<-release
		return createUIResult(r, v)
	})
	x := s.databaseCreate
	name := x.name
	x.name = "other"
	s.applyDatabaseCreate(services)
	if calls.Load() != 0 || s.DatabaseCreatePending() {
		t.Fatal("changed preview dispatched")
	}
	x.name = name
	s.applyDatabaseCreate(services)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("write not started")
	}
	w.ProfileID = 84
	close(release)
	deadline := time.Now().Add(time.Second)
	for len(x.results) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(x.results) == 0 {
		t.Fatal("worker did not return")
	}
	tt.Frame()
	if !s.DatabaseCreatePending() || s.DatabaseCreateOriginalOutcome() != nil {
		t.Fatal("foreign profile consumed result")
	}
	w.ProfileID = 83
	w.Generation++
	*generation++
	tt.Frame()
	out := s.DatabaseCreateOriginalOutcome()
	if out == nil || out.State != "committed" || out.Verified || out.Receipt == nil || !out.Receipt.Verified || out.Receipt.Original.Target.Generation != 73 {
		t.Fatal("late receipt overwritten", out)
	}
	s.readDatabaseCreate(services)
	waitSchema(t, tt, func() bool { return !s.DatabaseCreatePending() })
	s.applyDatabaseCreate(services)
	if calls.Load() != 1 || !reflect.DeepEqual(out, s.DatabaseCreateOriginalOutcome()) {
		t.Fatal("explicit new observation replayed or replaced old outcome")
	}
}

func TestDatabaseCreateUIExportCloseRevokesCapturedFileWrite(t *testing.T) {
	s, _, _, tt, services := databaseCreateUIState(t, func(_ context.Context, r contract.DatabaseCreateDocumentRequest, v contract.DatabaseCreateSnapshot) contract.DatabaseCreateOutcome {
		return createUIResult(r, v)
	})
	s.applyDatabaseCreate(services)
	waitSchema(t, tt, func() bool { return !s.DatabaseCreatePending() })
	entered, release := make(chan struct{}), make(chan struct{})
	var writes atomic.Int32
	s.SetSchemaReceiptFiles(func(string) (string, error) { close(entered); <-release; return "original.json", nil }, func(string, func(io.Writer) error) error { writes.Add(1); return nil })
	x := s.databaseCreate
	s.exportDatabaseCreateReceipt(services, x)
	transfer := x.transfer
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("picker not started")
	}
	s.closeDatabaseCreate()
	close(release)
	select {
	case err := <-transfer.results:
		if err == nil {
			t.Fatal("closed export succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("closed export not settled")
	}
	if writes.Load() != 0 || s.DatabaseCreateDialogOpen() || s.DatabaseCreateOriginalOutcome() != nil {
		t.Fatal("closed export wrote or retained receipt")
	}
}
