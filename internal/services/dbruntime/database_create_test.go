package dbruntime_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type createDatabaseHandle struct {
	schemaHandle
	reserve func(contract.DatabaseCreateDocumentRequest) (contract.DatabaseCreateDocumentLease, error)
}

func (h *createDatabaseHandle) CanCreateDatabase(t contract.DatabaseCreateTarget) bool {
	return t.Generation == 973 && t.Namespace.ProfileID == 1
}
func (h *createDatabaseHandle) ReserveDatabaseCreateDocument(r contract.DatabaseCreateDocumentRequest) (contract.DatabaseCreateDocumentLease, error) {
	return h.reserve(r)
}

type createDatabaseLease struct {
	snapshot func(context.Context) (contract.DatabaseCreateSnapshot, error)
	create   func(context.Context, contract.DatabaseCreateSnapshot) contract.DatabaseCreateOutcome
	close    func() error
}

func (l *createDatabaseLease) Snapshot(c context.Context) (contract.DatabaseCreateSnapshot, error) {
	return l.snapshot(c)
}
func (l *createDatabaseLease) Create(c context.Context, r contract.DatabaseCreateSnapshot) contract.DatabaseCreateOutcome {
	return l.create(c, r)
}
func (*createDatabaseLease) OriginalOutcome() *contract.DatabaseCreateOutcome { return nil }
func (l *createDatabaseLease) Close() error {
	if l.close != nil {
		return l.close()
	}
	return nil
}
func createDatabaseRuntime(t *testing.T, reserve func(contract.DatabaseCreateDocumentRequest) (contract.DatabaseCreateDocumentLease, error)) (*dbruntime.Manager, contract.DatabaseCreateDocumentsProvider, contract.DatabaseCreateTarget) {
	t.Helper()
	m := dbruntime.New()
	t.Cleanup(func() { _ = m.Close() })
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) {
		return &createDatabaseHandle{schemaHandle: schemaHandle{handle: handle{id: 1}}, reserve: reserve}, nil
	}))
	s, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.DatabaseCreateDocuments(1)
	if err != nil {
		t.Fatal(err)
	}
	return m, p, contract.DatabaseCreateTarget{Namespace: contract.NamespaceRef{ProfileID: 1, Schema: "new_database"}, Generation: s.Generation()}
}
func createDatabaseSnapshot(r contract.DatabaseCreateDocumentRequest) contract.DatabaseCreateSnapshot {
	return contract.DatabaseCreateSnapshot{Target: r.Target, Engine: "databend", Revision: "original-proof", CreateSQL: `CREATE DATABASE "new_database"`}
}
func createDatabaseResult(r contract.DatabaseCreateDocumentRequest, before contract.DatabaseCreateSnapshot) contract.DatabaseCreateOutcome {
	actual := before
	actual.Exists = true
	actual.NativeID = "9007199254740993"
	actual.DDL = "original complete definition"
	actual.Revision = "observed"
	return contract.DatabaseCreateOutcome{State: "committed", Verified: true, Actual: &actual, Receipt: &contract.DatabaseCreateReceipt{DocumentID: r.DocumentID, Sequence: 1, Original: before, SQL: before.CreateSQL, NativeJSON: `{"native":9007199254740993}`, State: "committed", Verified: true, Actual: &actual}}
}
func TestDatabaseCreateRuntimeTranslatesOriginalGenerationAndNeverReplays(t *testing.T) {
	var calls atomic.Int32
	m, p, target := createDatabaseRuntime(t, func(r contract.DatabaseCreateDocumentRequest) (contract.DatabaseCreateDocumentLease, error) {
		if r.Target.Generation != 973 {
			t.Fatal("public generation leaked", r)
		}
		return &createDatabaseLease{snapshot: func(context.Context) (contract.DatabaseCreateSnapshot, error) { return createDatabaseSnapshot(r), nil }, create: func(_ context.Context, before contract.DatabaseCreateSnapshot) contract.DatabaseCreateOutcome {
			calls.Add(1)
			if before.Target != r.Target {
				t.Fatal("wrong original", before)
			}
			return createDatabaseResult(r, before)
		}}, nil
	})
	l, err := p.ReserveDatabaseCreateDocument(contract.DatabaseCreateDocumentRequest{DocumentID: "original", Target: target})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	before, err := l.Snapshot(context.Background())
	if err != nil || before.Target != target {
		t.Fatal(before, err)
	}
	out := l.Create(context.Background(), before)
	if !out.Verified || out.Error != nil || out.Receipt.Original.Target != target || out.Actual.Target != target || out.Receipt.Actual.Target != target || out.Receipt.NativeJSON != `{"native":9007199254740993}` {
		t.Fatal(out)
	}
	retained := l.OriginalOutcome()
	out.Receipt.Actual.NativeID = "caller change"
	_, _ = l.Snapshot(context.Background())
	if !reflect.DeepEqual(retained, l.OriginalOutcome()) {
		t.Fatal("read/caller mutated receipt")
	}
	if out := l.Create(context.Background(), before); !errors.Is(out.Error, contract.ErrDatabaseCreateConsumed) || calls.Load() != 1 {
		t.Fatal("successful create replayed", out)
	}
	if err := m.Disconnect(1); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Snapshot(context.Background()); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed scope accepted", err)
	}
	if l.OriginalOutcome() != nil {
		t.Fatal("close retained cache")
	}
}

func TestDatabaseCreateRuntimeRetainsRejectedNativeReceipt(t *testing.T) {
	for _, fault := range []string{"foreign-original", "foreign-actual", "other-document", "different-sql", "missing-receipt", "zero-sequence"} {
		t.Run(fault, func(t *testing.T) {
			var calls atomic.Int32
			_, p, target := createDatabaseRuntime(t, func(r contract.DatabaseCreateDocumentRequest) (contract.DatabaseCreateDocumentLease, error) {
				return &createDatabaseLease{snapshot: func(context.Context) (contract.DatabaseCreateSnapshot, error) { return createDatabaseSnapshot(r), nil }, create: func(_ context.Context, before contract.DatabaseCreateSnapshot) contract.DatabaseCreateOutcome {
					calls.Add(1)
					out := createDatabaseResult(r, before).Clone()
					switch fault {
					case "foreign-original":
						out.Receipt.Original.Target.Namespace.ProfileID++
					case "foreign-actual":
						out.Actual.NativeID = "different"
					case "other-document":
						out.Receipt.DocumentID = "other"
					case "different-sql":
						out.Receipt.SQL += "; DROP DATABASE other"
					case "missing-receipt":
						out.Receipt = nil
					case "zero-sequence":
						out.Receipt.Sequence = 0
					}
					return out
				}}, nil
			})
			l, err := p.ReserveDatabaseCreateDocument(contract.DatabaseCreateDocumentRequest{DocumentID: "original", Target: target})
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			before, err := l.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			out := l.Create(context.Background(), before)
			if out.Verified || out.State != "committed" || !errors.Is(out.Error, contract.ErrDatabaseCreateUnresolved) {
				t.Fatal("untrusted outcome accepted", out)
			}
			if out.Receipt != nil && (!out.Receipt.Verified || out.Receipt.NativeJSON != `{"native":9007199254740993}`) {
				t.Fatal("raw receipt overwritten", out)
			}
			retained := l.OriginalOutcome()
			_, _ = l.Snapshot(context.Background())
			_ = l.Create(context.Background(), before)
			if !reflect.DeepEqual(retained, l.OriginalOutcome()) || calls.Load() != 1 {
				t.Fatal("rejected outcome repaired or replayed")
			}
		})
	}
}

func TestDatabaseCreateRuntimeCloseWaitsForOriginalAcknowledgement(t *testing.T) {
	entered, release, settled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	_, p, target := createDatabaseRuntime(t, func(r contract.DatabaseCreateDocumentRequest) (contract.DatabaseCreateDocumentLease, error) {
		return &createDatabaseLease{snapshot: func(context.Context) (contract.DatabaseCreateSnapshot, error) { return createDatabaseSnapshot(r), nil }, create: func(ctx context.Context, before contract.DatabaseCreateSnapshot) contract.DatabaseCreateOutcome {
			close(entered)
			<-ctx.Done()
			<-release
			defer close(settled)
			return createDatabaseResult(r, before)
		}, close: func() error { <-settled; return nil }}, nil
	})
	l, err := p.ReserveDatabaseCreateDocument(contract.DatabaseCreateDocumentRequest{DocumentID: "original", Target: target})
	if err != nil {
		t.Fatal(err)
	}
	before, err := l.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan contract.DatabaseCreateOutcome, 1)
	go func() { result <- l.Create(context.Background(), before) }()
	<-entered
	closed := make(chan struct{})
	go func() { _ = l.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("close did not await original")
	case <-result:
		t.Fatal("lost original settlement")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case out := <-result:
		if out.State != "committed" || out.Verified || out.Receipt == nil || !out.Receipt.Verified || out.Error == nil {
			t.Fatal("late ACK overwritten", out)
		}
	case <-time.After(time.Second):
		t.Fatal("write did not settle")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not settle")
	}
	if l.OriginalOutcome() != nil {
		t.Fatal("closed receipt retained")
	}
}

func TestDatabaseCreateRuntimeLateReservationClosesOnlyCapturedLease(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var reservations, originalCloses, replacementCloses atomic.Int32
	m, p, target := createDatabaseRuntime(t, func(contract.DatabaseCreateDocumentRequest) (contract.DatabaseCreateDocumentLease, error) {
		if reservations.Add(1) == 1 {
			close(started)
			<-release
			return &createDatabaseLease{close: func() error { originalCloses.Add(1); return nil }}, nil
		}
		return &createDatabaseLease{close: func() error { replacementCloses.Add(1); return nil }}, nil
	})
	r := contract.DatabaseCreateDocumentRequest{DocumentID: "late", Target: target}
	reserved := make(chan error, 1)
	go func() {
		l, err := p.ReserveDatabaseCreateDocument(r)
		if l != nil {
			err = errors.New("late original lease published")
		}
		reserved <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("reservation did not start")
	}
	if _, err := p.ReserveDatabaseCreateDocument(r); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("pending document ID accepted twice", err)
	}
	disconnected := make(chan error, 1)
	go func() { disconnected <- m.Disconnect(1) }()
	deadline := time.Now().Add(5 * time.Second)
	for p.CanCreateDatabase(target) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if p.CanCreateDatabase(target) {
		t.Fatal("original session was not revoked")
	}
	select {
	case <-disconnected:
		t.Fatal("disconnect did not await pending reservation")
	default:
	}
	replacement, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := m.DatabaseCreateDocuments(1)
	if err != nil {
		t.Fatal(err)
	}
	r.Target.Generation = replacement.Generation()
	lease, err := provider.ReserveDatabaseCreateDocument(r)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	once.Do(func() { close(release) })
	select {
	case err := <-reserved:
		if !errors.Is(err, contract.ErrRevoked) {
			t.Fatal("late reservation was published", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reservation did not settle")
	}
	select {
	case err := <-disconnected:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect did not settle")
	}
	if originalCloses.Load() != 1 || replacementCloses.Load() != 0 || !provider.CanCreateDatabase(r.Target) {
		t.Fatal("late cleanup leaked original or closed replacement")
	}
}
