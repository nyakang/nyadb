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
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type schemaDocumentHandle struct {
	schemaHandle
	reserve func(contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error)
}

func (h *schemaDocumentHandle) ReserveSchemaDesignDocument(r contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
	return h.reserve(r)
}

type schemaDocumentFake struct {
	ops    contract.SchemaDesignOperations
	closes atomic.Int32
	close  func() error
}

func (l *schemaDocumentFake) Operations() contract.SchemaDesignOperations    { return l.ops }
func (l *schemaDocumentFake) OriginalOutcome() *contract.SchemaDesignOutcome { return nil }
func (l *schemaDocumentFake) Close() error {
	l.closes.Add(1)
	if l.close != nil {
		return l.close()
	}
	return nil
}

func schemaLeaseRuntime(t *testing.T, reserve func(contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error)) (*dbruntime.Manager, contract.SchemaDesignTarget) {
	t.Helper()
	m := dbruntime.New()
	t.Cleanup(func() { _ = m.Close() })
	h := &schemaDocumentHandle{schemaHandle: schemaHandle{handle: handle{id: 1, caps: contract.Capabilities{DesignTables: true, Objects: true}}}, reserve: reserve}
	// Preserve legacy capability gates; leased methods alone publish no tools.
	h.apply = func(context.Context, contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
		return contract.SchemaDesignOutcome{State: "not_executed"}
	}
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
	s, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil {
		t.Fatal(err)
	}
	return m, contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: 1, Catalog: "main", Schema: "", Kind: "table", Name: "t"}, Generation: s.Generation()}
}
func schemaLeaseSnapshot(target contract.SchemaDesignTarget) contract.SchemaDesignSnapshot {
	return contract.SchemaDesignSnapshot{Target: target, Schema: schemaddl.Snapshot{Engine: "sqlite", Revision: "original", Exists: true, DDL: "CREATE TABLE t(id INTEGER)", Table: schemaddl.Table{Name: target.Object.Name, Clauses: []schemaddl.Clause{{ID: "column:0", Kind: "column", Name: "id", SQL: "INTEGER"}}}, SQLite: &schemaddl.SQLiteCatalog{Version: 12, Entries: []schemaddl.SQLiteCatalogEntry{{Type: "table", Name: target.Object.Name, Table: target.Object.Name, Root: 2, SQL: "CREATE TABLE t(id INTEGER)"}}}}}
}
func schemaLeaseOutcome(id string, sequence uint64, r contract.SchemaDesignRequest, state string, verified bool) contract.SchemaDesignOutcome {
	out := contract.SchemaDesignOutcome{State: state, Verified: verified}
	if verified {
		actual := r.Original.Clone()
		actual.Target.Object.Name = r.Desired.Name
		actual.Schema.Table = r.Desired.Clone()
		out.Actual = &actual
	}
	desired := r.Desired.Clone()
	out.Receipt = &contract.SchemaDesignWriteReceipt{DocumentID: id, Sequence: sequence, Operation: "apply", Original: r.Original.Clone(), Desired: &desired, Destination: r.Desired.Name, AllowColumnDrop: r.AllowColumnDrop, Statements: []string{"original complete native statement"}, NativeJSON: `{"results":[],"raft_index":9007199254740993}`, State: state, Verified: verified, Actual: out.Actual}
	return out
}
func reserveRuntimeSchema(t *testing.T, m *dbruntime.Manager, id string, target contract.SchemaDesignTarget) contract.SchemaDesignDocumentLease {
	t.Helper()
	l, err := m.ReserveSchemaDesignDocument(contract.SchemaDesignDocumentRequest{DocumentID: id, Target: target})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func TestSchemaDocumentRejectedNativeReceiptRemainsCompleteThroughReadsAndCallerMutation(t *testing.T) {
	var writes atomic.Int32
	m, target := schemaLeaseRuntime(t, func(r contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
		l := &schemaDocumentFake{}
		l.ops.Snapshot = func(_ context.Context, target contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
			return schemaLeaseSnapshot(target), nil
		}
		l.ops.CanApply = func(contract.SchemaDesignTarget) bool { return true }
		l.ops.Apply = func(_ context.Context, request contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
			writes.Add(1)
			out := schemaLeaseOutcome(r.DocumentID, 1, request, "not_executed", false)
			out.Receipt.NativeJSON = `{"status":401,"body":"original authentication rejection"}`
			out.Error = errors.New("original authentication rejected")
			return out
		}
		return l, nil
	})
	p, err := m.SchemaDesignDocuments(target.Object.ProfileID)
	if err != nil || p == nil {
		t.Fatal("original optional provider discovery failed", err)
	}
	l, err := p.ReserveSchemaDesignDocument(contract.SchemaDesignDocumentRequest{DocumentID: "rejected", Target: target})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	request := contract.SchemaDesignRequest{Original: schemaLeaseSnapshot(target), Desired: schemaLeaseSnapshot(target).Schema.Table.Clone()}
	out := l.Operations().Apply(context.Background(), request)
	if out.State != "not_executed" || out.Receipt == nil || out.Receipt.Original.Target.Generation != target.Generation {
		t.Fatal("native rejection was relabelled or its public scope was lost", out)
	}
	original := l.OriginalOutcome()
	if original == nil || original.Receipt.NativeJSON != out.Receipt.NativeJSON {
		t.Fatal("complete not-executed native receipt was discarded")
	}
	out.Receipt.NativeJSON = "caller mutation"
	out.Receipt.Desired.Clauses[0].SQL = "caller mutation"
	if _, err := l.Operations().Snapshot(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, l.OriginalOutcome()) || writes.Load() != 1 || !l.Operations().CanApply(target) {
		t.Fatal("rejection receipt was overwritten, replayed, or incorrectly blocked")
	}
}

func TestSchemaDocumentPreviewTranslatesGenerationClonesAndRespectsUnknownBarrier(t *testing.T) {
	var plans atomic.Int32
	var nativePlan schemaddl.Plan
	m, target := schemaLeaseRuntime(t, func(r contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
		l := &schemaDocumentFake{}
		l.ops.CanApply = func(contract.SchemaDesignTarget) bool { return true }
		l.ops.Plan = func(request contract.SchemaDesignRequest) (schemaddl.Plan, error) {
			plans.Add(1)
			if request.Original.Target.Generation != 973 || request.Original.Target.Object.Schema != "" {
				t.Error("preview changed native scope", request.Original.Target)
			}
			request.Desired.Clauses[0].SQL = "provider mutation"
			nativePlan = schemaddl.Plan{Mode: "add_column", SQL: []string{"ALTER TABLE main.t ADD COLUMN added TEXT"}, Dropped: []string{"original"}}
			return nativePlan, nil
		}
		l.ops.Apply = func(_ context.Context, request contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
			return schemaLeaseOutcome(r.DocumentID, 1, request, "uncertain", false)
		}
		return l, nil
	})
	l := reserveRuntimeSchema(t, m, "preview", target)
	r := contract.SchemaDesignRequest{Original: schemaLeaseSnapshot(target), Desired: schemaLeaseSnapshot(target).Schema.Table.Clone()}
	plan, err := l.Operations().Plan(r)
	if err != nil || plans.Load() != 1 || r.Desired.Clauses[0].SQL == "provider mutation" || l.OriginalOutcome() != nil {
		t.Fatal("pure preview mutated request or reserved a receipt", err)
	}
	plan.SQL[0] = "caller mutation"
	plan.Dropped[0] = "caller mutation"
	if nativePlan.SQL[0] == "caller mutation" || nativePlan.Dropped[0] == "caller mutation" {
		t.Fatal("preview output shared provider storage")
	}
	l.Operations().Apply(context.Background(), r)
	if _, err := l.Operations().Plan(r); !errors.Is(err, contract.ErrSchemaCommitUncertain) || plans.Load() != 1 {
		t.Fatal("unknown write barrier admitted a preview", err)
	}
}

func TestSchemaDocumentBindingCapturesGenerationAndRetargetsOnlyVerifiedCommit(t *testing.T) {
	var captured contract.SchemaDesignDocumentRequest
	var nativeOutcome contract.SchemaDesignOutcome
	raw := &schemaDocumentFake{}
	m, target := schemaLeaseRuntime(t, func(r contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
		captured = r
		return raw, nil
	})
	raw.ops.Snapshot = func(_ context.Context, r contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
		if r.Generation != 973 {
			t.Fatal("untranslated leased read", r)
		}
		return schemaLeaseSnapshot(r), nil
	}
	raw.ops.CanApply = func(contract.SchemaDesignTarget) bool { return true }
	raw.ops.Apply = func(_ context.Context, r contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
		if r.Original.Target.Generation != 973 {
			t.Fatal("untranslated leased write")
		}
		nativeOutcome = schemaLeaseOutcome("first", 1, r, "committed", true)
		return nativeOutcome
	}
	l := reserveRuntimeSchema(t, m, "first", target)
	if captured.DocumentID != "first" || captured.Target.Generation != 973 || captured.Target.Object.Schema != "" {
		t.Fatal("reservation changed original namespace or native generation", captured)
	}
	if _, err := m.ReserveSchemaDesignDocument(contract.SchemaDesignDocumentRequest{DocumentID: "first", Target: target}); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("duplicate original document accepted", err)
	}
	ops := l.Operations()
	snapshot, err := ops.Snapshot(context.Background(), target)
	if err != nil || snapshot.Target != target {
		t.Fatal(err)
	}
	request := contract.SchemaDesignRequest{Original: snapshot, Desired: snapshot.Schema.Table.Clone(), AllowColumnDrop: true}
	request.Desired.Name = "renamed"
	out := ops.Apply(context.Background(), request)
	if out.Error != nil || !out.Verified || out.Receipt == nil || out.Receipt.DocumentID != "first" || out.Receipt.Original.Target != target || out.Receipt.Actual.Target != out.Actual.Target || out.Actual.Target.Generation != target.Generation {
		t.Fatal("original receipt or public generations lost", out.Error)
	}
	if nativeOutcome.Receipt.Original.Target.Generation != 973 || nativeOutcome.Receipt.Actual.Target.Generation != 973 {
		t.Fatal("runtime rewrote provider-owned receipt")
	}
	out.Receipt.Original.Schema.SQLite.Entries[0].SQL = "mutated"
	out.Receipt.Statements[0] = "changed"
	out.Actual.Schema.Table.Clauses[0].SQL = "TEXT"
	original := l.OriginalOutcome()
	if original.Receipt.Statements[0] == "changed" || original.Receipt.Original.Schema.SQLite.Entries[0].SQL == "mutated" || original.Actual.Schema.Table.Clauses[0].SQL != "INTEGER" {
		t.Fatal("caller overwrote retained original settlement")
	}
	if _, err := ops.Snapshot(context.Background(), target); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("verified rename retained old document target", err)
	}
	renamed := target
	renamed.Object.Name = "renamed"
	if _, err := ops.Snapshot(context.Background(), renamed); err != nil {
		t.Fatal("stable original document could not follow verified rename", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if l.OriginalOutcome() != nil || l.Operations().Snapshot != nil || raw.closes.Load() != 1 {
		t.Fatal("close retained evidence or exposed original provider")
	}
	if _, err := ops.Snapshot(context.Background(), renamed); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("captured original binding survived document close", err)
	}
}

func TestSchemaDocumentUnknownOutcomeSurvivesReadsAndBlocksEveryWrite(t *testing.T) {
	var calls atomic.Int32
	m, target := schemaLeaseRuntime(t, func(r contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
		raw := &schemaDocumentFake{}
		raw.ops.Snapshot = func(_ context.Context, t contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
			return schemaLeaseSnapshot(t), nil
		}
		raw.ops.CanApply = func(contract.SchemaDesignTarget) bool { return true }
		raw.ops.Apply = func(_ context.Context, request contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
			calls.Add(1)
			return schemaLeaseOutcome(r.DocumentID, 1, request, "uncertain", false)
		}
		raw.ops.CanDrop = raw.ops.CanApply
		raw.ops.CanCopy = raw.ops.CanApply
		raw.ops.CanRename = raw.ops.CanApply
		raw.ops.Drop = func(context.Context, contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
			t.Error("unknown write replayed as drop")
			return contract.SchemaDesignOutcome{}
		}
		raw.ops.Copy = func(context.Context, contract.SchemaCopyRequest) contract.SchemaDesignOutcome {
			t.Error("unknown write replayed as copy")
			return contract.SchemaDesignOutcome{}
		}
		raw.ops.Rename = func(context.Context, contract.SchemaRenameRequest) contract.SchemaDesignOutcome {
			t.Error("unknown write replayed as rename")
			return contract.SchemaDesignOutcome{}
		}
		return raw, nil
	})
	l := reserveRuntimeSchema(t, m, "unknown", target)
	ops := l.Operations()
	snapshot, err := ops.Snapshot(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	request := contract.SchemaDesignRequest{Original: snapshot, Desired: snapshot.Schema.Table.Clone()}
	request.Desired.Name = "original-destination"
	out := ops.Apply(context.Background(), request)
	if out.State != "uncertain" || out.Receipt == nil {
		t.Fatal(out)
	}
	retained := l.OriginalOutcome()
	if _, err := ops.Snapshot(context.Background(), target); err != nil {
		t.Fatal("independent original read was prohibited", err)
	}
	if !reflect.DeepEqual(retained, l.OriginalOutcome()) {
		t.Fatal("fresh read relabelled original unknown acknowledgement")
	}
	request.Desired.Name = "different-request"
	results := []contract.SchemaDesignOutcome{ops.Apply(context.Background(), request), ops.Drop(context.Background(), snapshot), ops.Copy(context.Background(), contract.SchemaCopyRequest{Original: snapshot, Destination: "copy"}), ops.Rename(context.Background(), contract.SchemaRenameRequest{Original: snapshot, Destination: "rename"})}
	for _, out := range results {
		if out.State != "not_executed" || !errors.Is(out.Error, contract.ErrSchemaCommitUncertain) || out.Receipt != nil {
			t.Fatal("blocked request relabelled original receipt", out)
		}
	}
	// The original evidence is fetched separately and still belongs to its request.
	if l.OriginalOutcome().Receipt.Destination != "original-destination" || calls.Load() != 1 || ops.CanApply(target) || ops.CanDrop(target) || ops.CanCopy(target) || ops.CanRename(target) {
		t.Fatal("unknown document allowed another write")
	}
	peer := reserveRuntimeSchema(t, m, "peer", target)
	out = peer.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: snapshot, Desired: snapshot.Schema.Table.Clone()})
	if out.Receipt == nil || out.Receipt.DocumentID != "peer" || calls.Load() != 2 {
		t.Fatal("original unknown result leaked to another document")
	}
}

func TestSchemaDocumentRejectsForeignOriginalReceiptWithoutRewritingEvidence(t *testing.T) {
	for _, change := range []string{"document", "original", "desired", "actual", "state", "missing", "no_actual", "empty_native", "missing_statements", "invalid_verified_state"} {
		t.Run(change, func(t *testing.T) {
			raw := &schemaDocumentFake{}
			m, target := schemaLeaseRuntime(t, func(contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
				return raw, nil
			})
			raw.ops.Snapshot = func(_ context.Context, t contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
				return schemaLeaseSnapshot(t), nil
			}
			raw.ops.CanApply = func(contract.SchemaDesignTarget) bool { return true }
			raw.ops.Apply = func(_ context.Context, r contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
				out := schemaLeaseOutcome("first", 1, r, "committed", true)
				switch change {
				case "document":
					out.Receipt.DocumentID = "another-document"
				case "original":
					out.Receipt.Original.Target.Object.ProfileID = 2
				case "desired":
					out.Receipt.Desired.Name = "other-desired"
				case "actual":
					a := out.Actual.Clone()
					a.Target.Object.Name = "another-result"
					out.Receipt.Actual = &a
				case "state":
					out.Receipt.State = "rolled_back"
				case "missing":
					out.Receipt = nil
				case "no_actual":
					out.Actual, out.Receipt.Actual = nil, nil
				case "empty_native":
					out.Receipt.NativeJSON = ""
				case "missing_statements":
					out.Receipt.Statements = nil
				case "invalid_verified_state":
					out.State, out.Receipt.State = "rolled_back", "rolled_back"
				}
				return out
			}
			l := reserveRuntimeSchema(t, m, "first", target)
			ops := l.Operations()
			snapshot, _ := ops.Snapshot(context.Background(), target)
			r := contract.SchemaDesignRequest{Original: snapshot, Desired: snapshot.Schema.Table.Clone()}
			out := ops.Apply(context.Background(), r)
			if out.Error == nil || out.Verified || out.Actual != nil {
				t.Fatal("foreign or missing original acknowledgement accepted", out)
			}
			if change == "original" && (out.Receipt.Original.Target.Object.ProfileID != 2 || out.Receipt.Original.Target.Generation != 973) {
				t.Fatal("foreign original proof relabelled as captured public scope")
			}
			if change == "document" && out.Receipt.DocumentID != "another-document" {
				t.Fatal("foreign document ID rewritten")
			}
			if change == "state" && out.Receipt.State != "rolled_back" {
				t.Fatal("original receipt state rewritten")
			}
			if change == "missing" && out.State != "uncertain" {
				t.Fatal("missing commit acknowledgement misclassified")
			}
			if second := ops.Apply(context.Background(), r); !errors.Is(second.Error, contract.ErrSchemaCommitUncertain) {
				t.Fatal("invalid commit acknowledgement permitted replay")
			}
		})
	}
}

func TestSchemaDocumentCancelCloseWaitsCapturedWriteAndPreservesOriginalReceipt(t *testing.T) {
	started, ack, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(ack) })
	raw := &schemaDocumentFake{}
	var calls atomic.Int32
	m, target := schemaLeaseRuntime(t, func(contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
		return raw, nil
	})
	raw.ops.CanApply = func(contract.SchemaDesignTarget) bool { return true }
	raw.ops.Apply = func(ctx context.Context, r contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		close(canceled)
		<-ack
		return schemaLeaseOutcome("held", 1, r, "committed", true)
	}
	l := reserveRuntimeSchema(t, m, "held", target)
	ops := l.Operations()
	r := contract.SchemaDesignRequest{Original: schemaLeaseSnapshot(target), Desired: schemaLeaseSnapshot(target).Schema.Table.Clone()}
	done := make(chan contract.SchemaDesignOutcome, 1)
	go func() { done <- ops.Apply(context.Background(), r) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("original writer did not start")
	}
	queued := make(chan contract.SchemaDesignOutcome, 1)
	go func() { queued <- ops.Apply(context.Background(), r) }()
	closed := make(chan struct{})
	go func() { _ = l.Close(); close(closed) }()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("close did not cancel original writer")
	}
	select {
	case <-done:
		t.Fatal("original writer abandoned unacknowledged commit")
	case <-closed:
		t.Fatal("close abandoned captured original executor")
	case <-time.After(30 * time.Millisecond):
	}
	release.Do(func() { close(ack) })
	select {
	case out := <-done:
		if !out.Verified || out.State != "committed" || out.Receipt == nil || out.Receipt.DocumentID != "held" || out.Receipt.Original.Target != target {
			t.Fatal("late original commit receipt lost", out.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original writer did not settle")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("close did not finish after original acknowledgement")
	}
	select {
	case out := <-queued:
		if out.State != "not_executed" || out.Error == nil {
			t.Fatal("queued write executed during close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued admission did not cancel")
	}
	if calls.Load() != 1 || raw.closes.Load() != 1 || l.OriginalOutcome() != nil {
		t.Fatal("close replayed writes or retained document caches")
	}
}

// Done is first observed by admission after the request has been cloned. This
// lets the test edit its own draft while a captured write waits for its turn.
type capturedSchemaContext struct {
	context.Context
	captured chan struct{}
	once     sync.Once
}

func (c *capturedSchemaContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.captured) })
	return c.Context.Done()
}

func TestSchemaDocumentQueuedWriteCapturesDraftBeforeAdmissionWait(t *testing.T) {
	started, ack := make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(ack) })
	var calls atomic.Int32
	raw := &schemaDocumentFake{}
	m, target := schemaLeaseRuntime(t, func(contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
		return raw, nil
	})
	raw.ops.CanApply = func(contract.SchemaDesignTarget) bool { return true }
	raw.ops.Apply = func(_ context.Context, r contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
		sequence := uint64(calls.Add(1))
		if sequence == 1 {
			close(started)
			<-ack
			return schemaLeaseOutcome("queue", sequence, r, "rolled_back", false)
		}
		if r.Desired.Clauses[0].SQL != "INTEGER" || r.Original.Schema.SQLite.Entries[0].SQL != "CREATE TABLE t(id INTEGER)" {
			t.Error("queued request borrowed caller-owned draft or catalog")
		}
		return schemaLeaseOutcome("queue", sequence, r, "committed", true)
	}
	l := reserveRuntimeSchema(t, m, "queue", target)
	ops := l.Operations()
	r := contract.SchemaDesignRequest{Original: schemaLeaseSnapshot(target), Desired: schemaLeaseSnapshot(target).Schema.Table.Clone()}
	first := make(chan contract.SchemaDesignOutcome, 1)
	go func() { first <- ops.Apply(context.Background(), r) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("original write did not start")
	}
	queued := r.Clone()
	ctx := &capturedSchemaContext{Context: context.Background(), captured: make(chan struct{})}
	second := make(chan contract.SchemaDesignOutcome, 1)
	go func() { second <- ops.Apply(ctx, queued) }()
	select {
	case <-ctx.captured:
	case <-time.After(5 * time.Second):
		t.Fatal("queued request did not capture admission")
	}
	queued.Desired.Clauses[0].SQL = "TEXT"
	queued.Original.Schema.SQLite.Entries[0].SQL = "caller-edited-catalog"
	release.Do(func() { close(ack) })
	if out := <-first; out.State != "rolled_back" || out.Error != nil {
		t.Fatal("original rollback receipt was lost", out.Error)
	}
	if out := <-second; !out.Verified || out.Error != nil || out.Receipt.Sequence != 2 || out.Receipt.Desired.Clauses[0].SQL != "INTEGER" {
		t.Fatal("captured original queued request changed", out.Error)
	}
}

func TestSchemaDocumentRejectsReusedOriginalSequence(t *testing.T) {
	raw := &schemaDocumentFake{}
	m, target := schemaLeaseRuntime(t, func(contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
		return raw, nil
	})
	raw.ops.CanApply = func(contract.SchemaDesignTarget) bool { return true }
	raw.ops.Apply = func(_ context.Context, r contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
		return schemaLeaseOutcome("sequence", 1, r, "rolled_back", false)
	}
	l := reserveRuntimeSchema(t, m, "sequence", target)
	ops := l.Operations()
	r := contract.SchemaDesignRequest{Original: schemaLeaseSnapshot(target), Desired: schemaLeaseSnapshot(target).Schema.Table.Clone()}
	first := ops.Apply(context.Background(), r)
	if first.State != "rolled_back" || first.Error != nil {
		t.Fatal(first.Error)
	}
	second := ops.Apply(context.Background(), r)
	if second.State != "uncertain" || !errors.Is(second.Error, contract.ErrSchemaCommitUncertain) || second.Receipt.State != "rolled_back" || second.Receipt.Sequence != 1 {
		t.Fatal("reused acknowledgement authorized a later request", second.Error)
	}
	if third := ops.Apply(context.Background(), r); !errors.Is(third.Error, contract.ErrSchemaCommitUncertain) || third.Receipt != nil {
		t.Fatal("reused original acknowledgement allowed replay")
	}
}

func TestSchemaDocumentLateReservationClosesOnlyOriginalLease(t *testing.T) {
	started, ack := make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(ack) })
	raw := &schemaDocumentFake{}
	m, target := schemaLeaseRuntime(t, func(contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
		close(started)
		<-ack
		return raw, nil
	})
	reserved := make(chan error, 1)
	go func() {
		l, err := m.ReserveSchemaDesignDocument(contract.SchemaDesignDocumentRequest{DocumentID: "late", Target: target})
		if l != nil {
			err = errors.New("late original lease published")
		}
		reserved <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("reservation did not reach original provider")
	}
	if _, err := m.ReserveSchemaDesignDocument(contract.SchemaDesignDocumentRequest{DocumentID: "late", Target: target}); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("pending original document ID was not reserved", err)
	}
	disconnected := make(chan error, 1)
	go func() { disconnected <- m.Disconnect(target.Object.ProfileID) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := m.Session(target.Object.ProfileID); err != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-disconnected:
		t.Fatal("disconnect released pending original reservation")
	case <-time.After(30 * time.Millisecond):
	}
	release.Do(func() { close(ack) })
	select {
	case err := <-reserved:
		if !errors.Is(err, contract.ErrRevoked) {
			t.Fatal("late reservation did not remain in original scope", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original reservation did not settle")
	}
	select {
	case err := <-disconnected:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect did not settle")
	}
	if raw.closes.Load() != 1 {
		t.Fatal("late original lease was leaked or closed twice")
	}
}
