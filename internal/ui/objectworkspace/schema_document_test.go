package objectworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

type schemaOwnerTestProvider func(contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error)

func (f schemaOwnerTestProvider) ReserveSchemaDesignDocument(r contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
	return f(r)
}

type schemaOwnerTestLease struct {
	ops    contract.SchemaDesignOperations
	closes atomic.Int32
	close  func() error
}

func (l *schemaOwnerTestLease) Operations() contract.SchemaDesignOperations    { return l.ops }
func (l *schemaOwnerTestLease) OriginalOutcome() *contract.SchemaDesignOutcome { return nil }
func (l *schemaOwnerTestLease) Close() error {
	l.closes.Add(1)
	if l.close != nil {
		return l.close()
	}
	return nil
}

func schemaOwnerTestOutcome(id string, sequence uint64, request contract.SchemaDesignRequest, state string, verified bool) contract.SchemaDesignOutcome {
	out := contract.SchemaDesignOutcome{State: state, Verified: verified, Rows: 2}
	if verified {
		actual := request.Original.Clone()
		actual.Target.Object.Name = request.Desired.Name
		actual.Schema.Table = request.Desired.Clone()
		out.Actual = &actual
	}
	desired := request.Desired.Clone()
	out.Receipt = &contract.SchemaDesignWriteReceipt{DocumentID: id, Sequence: sequence, Operation: "apply", Original: request.Original.Clone(), Desired: &desired, Destination: desired.Name, AllowColumnDrop: request.AllowColumnDrop, Statements: []string{"ALTER TABLE main.t ADD COLUMN added TEXT"}, NativeJSON: `{"results":[{"rows_affected":2}],"raft_index":9007199254740993}`, State: state, Verified: verified, Rows: out.Rows, Actual: out.Actual}
	return out
}

func ownedSchemaTestState(t *testing.T) (*State, *Document, *Session, *ui.Tester, ui.Services, *schemaOwnerTestLease, *atomic.Int32) {
	t.Helper()
	s, d, w, tt, services := schemaTestState(t)
	original := d.schemaDesign.original.Clone()
	s.closeSchemaDesign(d)
	d.object.Schema = ""
	original.Target.Object = d.object
	original.Schema.SQLite = &schemaddl.SQLiteCatalog{Version: 12, Entries: []schemaddl.SQLiteCatalogEntry{{Type: "table", Name: "t", Table: "t", Root: 2, SQL: original.Schema.DDL}}}
	s.deps.SchemaTarget = func(id int64, _, name string) contract.SchemaDesignTarget {
		target := original.Target
		target.Object.ProfileID, target.Object.Name = id, name
		return target
	}
	reads := &atomic.Int32{}
	l := &schemaOwnerTestLease{}
	l.ops.CanApply = func(target contract.SchemaDesignTarget) bool { return target == original.Target }
	l.ops.Snapshot = func(_ context.Context, target contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
		reads.Add(1)
		if target != original.Target {
			return contract.SchemaDesignSnapshot{}, errors.New("wrong original target")
		}
		return original.Clone(), nil
	}
	l.ops.Plan = func(r contract.SchemaDesignRequest) (schemaddl.Plan, error) {
		return schemaddl.PlanSQLiteNative(r.Original.Schema, r.Desired, "main", r.AllowColumnDrop)
	}
	reserves := 0
	s.deps.SchemaDocumentsFor = func(int64) (contract.SchemaDesignDocumentsProvider, error) {
		return schemaOwnerTestProvider(func(r contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
			reserves++
			if r.DocumentID != "schema-design:1" || r.Target != original.Target {
				t.Fatalf("reservation changed original identity: %+v", r)
			}
			return l, nil
		}), nil
	}
	s.BeginDesign(d, services)
	waitSchema(t, tt, func() bool { return d.schemaDesign != nil && d.schemaDesign.loaded })
	if reserves != 1 || reads.Load() != 1 || d.schemaDesign.owner == nil {
		t.Fatal("designer did not reserve and read through its original lease")
	}
	return s, d, w, tt, services, l, reads
}

func addSchemaOwnerTestColumn(d *Document) {
	x := d.schemaDesign
	x.draft.Clauses = append(x.draft.Clauses, schemaddl.Clause{ID: "new:added", Kind: "column", Name: "added", SQL: "TEXT"})
}

func TestSchemaOwnedDesignerParksOriginalWriteAndReadNeverUnblocksOrReplacesEvidence(t *testing.T) {
	s, d, w, tt, services, l, reads := ownedSchemaTestState(t)
	active := true
	s.deps.DocumentActive = func(uint64) bool { return active }
	started := make(chan contract.SchemaDesignRequest, 1)
	release := make(chan struct{})
	l.ops.Apply = func(_ context.Context, r contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
		started <- r.Clone()
		<-release
		return schemaOwnerTestOutcome("schema-design:1", 1, r, "uncertain", false)
	}
	s.deps.SchemaDesigned = func(_ ui.Services, e SchemaDesignEvent) {
		e.Outcome.Receipt.NativeJSON = "callback mutation"
		e.Outcome.Receipt.Desired.Clauses[0].Name = "callback mutation"
	}
	// Once reserved, preview, apply and reads must not consult a replacement.
	s.deps.SchemaDesignFor = func(int64) contract.SchemaDesignOperations {
		t.Error("owned designer consulted the current session operations")
		return contract.SchemaDesignOperations{}
	}
	addSchemaOwnerTestColumn(d)
	if err := s.PrepareSchemaDesign(d); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(d.schemaDesign.plan.SQL, "\n"), `"main".`) || reads.Load() != 1 {
		t.Fatal("preview changed physical qualifier or performed I/O")
	}
	s.executeSchemaDesign(d, services)
	request := <-started
	d.schemaDesign.draft.Clauses[0].Name = "UI mutation after dispatch"
	active = false
	close(release)
	waitSchema(t, tt, func() bool { return len(d.schemaDesign.results) == 1 })
	if !d.DesignBusy() || d.SchemaDesignOriginalOutcome() != nil {
		t.Fatal("parked receipt consumed without original document activation")
	}
	w.ProfileID = 84
	active = true
	tt.Frame()
	if !d.DesignBusy() {
		t.Fatal("parked receipt consumed on another profile")
	}
	w.ProfileID = 83
	waitSchema(t, tt, func() bool { return !d.DesignBusy() })
	original := d.SchemaDesignOriginalOutcome()
	if original == nil || original.Receipt.Desired.Clauses[0].Name != "id" || original.Receipt.NativeJSON == "callback mutation" || !d.schemaDesign.owner.blocked {
		t.Fatal("original immutable request/receipt was lost")
	}
	if request.Desired.Clauses[0].Name != "id" {
		t.Fatal("worker shared the mutable draft")
	}
	draft := d.schemaDesign.draft.Clone()
	if err := s.PrepareSchemaDesign(d); err == nil {
		t.Fatal("unknown write could be previewed for replay")
	}
	s.loadSchemaDesign(d, services)
	waitSchema(t, tt, func() bool { return !d.DesignBusy() })
	if reads.Load() != 2 || !reflect.DeepEqual(original, d.SchemaDesignOriginalOutcome()) || !reflect.DeepEqual(draft, d.schemaDesign.draft) || !d.schemaDesign.blocked {
		t.Fatal("later observation erased original proof, draft or write barrier")
	}
	original.Receipt.Desired.Clauses[0].Name = "caller mutation"
	if d.SchemaDesignOriginalOutcome().Receipt.Desired.Clauses[0].Name != "id" {
		t.Fatal("receipt getter shared its retained cache")
	}
}

func TestSchemaOwnedDesignerUnknownLeaveAcknowledgesWithoutDiscardingDraftOrReceipt(t *testing.T) {
	s, d, _, tt, _, _, _ := ownedSchemaTestState(t)
	addSchemaOwnerTestColumn(d)
	r := contract.SchemaDesignRequest{Original: d.schemaDesign.original.Clone(), Desired: d.schemaDesign.draft.Clone()}
	d.schemaDesign.owner.settle(contract.SchemaDesignWriteReceipt{Operation: "apply", Original: r.Original, Desired: &r.Desired, Destination: r.Desired.Name}, schemaOwnerTestOutcome("schema-design:1", 1, r, "uncertain", false))
	d.schemaDesign.blocked = true
	draft, receipt := d.schemaDesign.draft.Clone(), d.SchemaDesignOriginalOutcome()
	left := 0
	if !s.GuardDesignLeave(d, func(ui.Services) { left++ }) {
		t.Fatal("unknown original write left without a decision")
	}
	tt.Frame()
	if _, ok := tt.Find("放弃修改"); ok {
		t.Fatal("unknown leave still offered draft destruction")
	}
	if err := tt.Click("保留草稿并离开"); err != nil {
		t.Fatal(err)
	}
	if left != 1 || !reflect.DeepEqual(draft, d.schemaDesign.draft) || !reflect.DeepEqual(receipt, d.SchemaDesignOriginalOutcome()) || !d.schemaDesign.owner.blocked || s.GuardDesignLeave(d, nil) {
		t.Fatal("leave decision erased evidence, replayed, or failed to acknowledge")
	}
}

func TestSchemaOwnedDesignerCloseWaitsOriginalAcknowledgementAndRevokesUICache(t *testing.T) {
	s, d, _, _, services, l, _ := ownedSchemaTestState(t)
	started, canceled, ack := make(chan struct{}), make(chan struct{}), make(chan struct{})
	l.ops.Apply = func(ctx context.Context, r contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-ack
		return schemaOwnerTestOutcome("schema-design:1", 1, r, "rolled_back", false)
	}
	l.close = func() error { <-ack; return nil }
	addSchemaOwnerTestColumn(d)
	if err := s.PrepareSchemaDesign(d); err != nil {
		t.Fatal(err)
	}
	s.executeSchemaDesign(d, services)
	<-started
	s.cancelSchemaDesign(d)
	<-canceled
	if !d.DesignBusy() {
		t.Fatal("cancellation released the original executor")
	}
	closed := make(chan struct{})
	go func() { s.Close(d); close(closed) }()
	select {
	case <-closed:
		t.Fatal("close returned before original executor acknowledgement")
	case <-time.After(20 * time.Millisecond):
	}
	close(ack)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not settle original lease")
	}
	if l.closes.Load() != 1 || d.SchemaDesignOriginalOutcome() != nil || d.schemaDesign != nil || !d.closed {
		t.Fatal("terminal close retained callbacks, lease or original evidence")
	}
}

func TestSchemaOriginalReceiptPreviewCopyExportRetainsCompleteUnicodeAndOriginalNativeJSON(t *testing.T) {
	s, d, w, tt, services, _, _ := ownedSchemaTestState(t)
	r := contract.SchemaDesignRequest{Original: d.schemaDesign.original.Clone(), Desired: d.schemaDesign.draft.Clone()}
	out := schemaOwnerTestOutcome("schema-design:1", 1, r, "uncertain", false)
	out.Receipt.NativeJSON = `{"results":[],"proof":"` + strings.Repeat("原始", 20000) + `","raft_index":9007199254740993}`
	d.schemaDesign.owner.settle(contract.SchemaDesignWriteReceipt{Operation: "apply", Original: r.Original, Desired: &r.Desired, Destination: r.Desired.Name}, out)
	d.schemaDesign.blocked, d.schemaDesign.page = true, 6
	tt.Frame()
	if len(d.schemaDesign.owner.receiptPreview) > schemaReceiptPreviewLimit || !utf8.ValidString(d.schemaDesign.owner.receiptPreview) || !d.schemaDesign.owner.receiptTruncated {
		t.Fatal("receipt preview was unbounded or split a UTF-8 character")
	}
	if err := tt.Click("复制完整回执"); err != nil {
		t.Fatal(err)
	}
	clipboard := services.ReadClipboard()
	var decoded struct {
		Version int
		Kind    string
		Receipt contract.SchemaDesignWriteReceipt
	}
	if json.Unmarshal([]byte(clipboard), &decoded) != nil || decoded.Version != 1 || decoded.Kind != "schema-original-write-receipt" || !reflect.DeepEqual(decoded.Receipt, *out.Receipt) {
		t.Fatal("clipboard omitted or rewrote complete original evidence")
	}
	picked, release := make(chan struct{}), make(chan struct{})
	written := make(chan string, 1)
	s.SetSchemaReceiptFiles(func(string) (string, error) {
		close(picked)
		<-release
		return "original.json", nil
	}, func(_ string, work func(io.Writer) error) error {
		var b bytes.Buffer
		err := work(&b)
		written <- b.String()
		return err
	})
	s.exportSchemaReceipt(services, d.schemaDesign.owner)
	<-picked
	// Mutation and parking after the picker starts cannot change the export.
	d.schemaDesign.owner.last.Receipt.NativeJSON = "later mutation"
	w.ProfileID = 84
	close(release)
	select {
	case exported := <-written:
		if exported != clipboard {
			t.Fatal("file worker read a later receipt instead of its immutable snapshot")
		}
	case <-time.After(time.Second):
		t.Fatal("complete original export did not finish")
	}
	tt.Frame()
	if d.schemaDesign.owner.transfer == nil {
		t.Fatal("parked export completion consumed on another profile")
	}
	w.ProfileID = 83
	waitSchema(t, tt, func() bool { return d.schemaDesign.owner.transfer == nil })
}

func TestSchemaOriginalReceiptCloseOrCancelDuringPickerRevokesFileWriteAdmission(t *testing.T) {
	for _, closeDocument := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "close"}[closeDocument], func(t *testing.T) {
			s, d, _, tt, services, _, _ := ownedSchemaTestState(t)
			r := contract.SchemaDesignRequest{Original: d.schemaDesign.original.Clone(), Desired: d.schemaDesign.draft.Clone()}
			x := d.schemaDesign.owner
			x.settle(contract.SchemaDesignWriteReceipt{Operation: "apply", Original: r.Original, Desired: &r.Desired, Destination: r.Desired.Name}, schemaOwnerTestOutcome(x.id, 1, r, "uncertain", false))
			picked, release := make(chan struct{}), make(chan struct{})
			var writes atomic.Int32
			s.SetSchemaReceiptFiles(func(string) (string, error) { close(picked); <-release; return "original.json", nil }, func(string, func(io.Writer) error) error { writes.Add(1); return nil })
			s.exportSchemaReceipt(services, x)
			<-picked
			job := x.transfer
			if closeDocument {
				s.Close(d)
			} else {
				job.cancel()
			}
			close(release)
			select {
			case err := <-job.results:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("picker did not retain cancellation", err)
				}
			case <-time.After(time.Second):
				t.Fatal("picker did not settle")
			}
			tt.Frame()
			if writes.Load() != 0 {
				t.Fatal("canceled picker admitted a file write")
			}
			if closeDocument && (x.last != nil || x.lease != nil || x.receiptText != "" || x.transfer != nil) {
				t.Fatal("closed owner retained evidence or file work")
			}
		})
	}
}

func TestSchemaDocumentDiscoveryErrorsNeverSilentlyFallbackAndReturnedLeaseIsClosed(t *testing.T) {
	s := New(Dependencies{SchemaDocumentsFor: func(int64) (contract.SchemaDesignDocumentsProvider, error) {
		return nil, errors.New("original provider disappeared")
	}})
	if owner, err := s.reserveSchemaDocument("first", contract.SchemaDesignTarget{}); err == nil || owner != nil {
		t.Fatal("provider failure silently used current session")
	}
	l := &schemaOwnerTestLease{}
	s.deps.SchemaDocumentsFor = func(int64) (contract.SchemaDesignDocumentsProvider, error) {
		return schemaOwnerTestProvider(func(contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
			return l, errors.New("partial reservation")
		}), nil
	}
	if owner, err := s.reserveSchemaDocument("first", contract.SchemaDesignTarget{}); err == nil || owner != nil || l.closes.Load() != 1 {
		t.Fatal("partial original reservation leaked")
	}
	s.deps.SchemaDocumentsFor = func(int64) (contract.SchemaDesignDocumentsProvider, error) { return nil, contract.ErrUnsupported }
	if owner, err := s.reserveSchemaDocument("first", contract.SchemaDesignTarget{}); err != nil || owner != nil {
		t.Fatal("explicit legacy provider lost compatibility")
	}
}

func TestSchemaOwnerRejectsForeignOrIncompleteReceiptWithoutRewritingOriginalEvidence(t *testing.T) {
	for _, kind := range []string{"document", "sequence", "original", "desired", "destination", "options", "native", "actual"} {
		t.Run(kind, func(t *testing.T) {
			s, d, _, tt, services, l, _ := ownedSchemaTestState(t)
			addSchemaOwnerTestColumn(d)
			r := contract.SchemaDesignRequest{Original: d.schemaDesign.original.Clone(), Desired: d.schemaDesign.draft.Clone(), AllowColumnDrop: true}
			out := schemaOwnerTestOutcome("schema-design:1", 1, r, "committed", true)
			switch kind {
			case "document":
				out.Receipt.DocumentID = "another-document"
			case "sequence":
				out.Receipt.Sequence = 0
			case "original":
				out.Receipt.Original.Target.Object.ProfileID++
			case "desired":
				out.Receipt.Desired.Clauses[0].SQL = "changed"
			case "destination":
				out.Receipt.Destination = "another-table"
			case "options":
				out.Receipt.AllowColumnDrop = false
			case "native":
				out.Receipt.NativeJSON = "truncated"
			case "actual":
				out.Actual.Target.Object.ProfileID++
				out.Receipt.Actual = out.Actual
			}
			original := out.Receipt.Clone()
			l.ops.Apply = func(context.Context, contract.SchemaDesignRequest) contract.SchemaDesignOutcome { return out.Clone() }
			if err := s.PrepareSchemaDesign(d); err != nil {
				t.Fatal(err)
			}
			s.executeSchemaDesign(d, services)
			waitSchema(t, tt, func() bool { return !d.DesignBusy() })
			retained := d.SchemaDesignOriginalOutcome()
			if retained == nil || retained.Verified || retained.State != "committed" || !d.schemaDesign.owner.blocked || !d.DesignDirty() || !reflect.DeepEqual(*retained.Receipt, original) {
				t.Fatal("rejected receipt was trusted, discarded or rewritten", retained)
			}
		})
	}
}

func TestSchemaOwnedDesignerTerminalHostCallbackCannotRepopulateClosedCache(t *testing.T) {
	s, d, _, tt, services, l, _ := ownedSchemaTestState(t)
	addSchemaOwnerTestColumn(d)
	l.ops.Apply = func(_ context.Context, r contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
		return schemaOwnerTestOutcome("schema-design:1", 1, r, "committed", true)
	}
	x := d.schemaDesign
	s.deps.SchemaDesigned = func(ui.Services, SchemaDesignEvent) { s.Close(d) }
	if err := s.PrepareSchemaDesign(d); err != nil {
		t.Fatal(err)
	}
	s.executeSchemaDesign(d, services)
	waitSchema(t, tt, func() bool { return d.closed })
	if x.owner.last != nil || x.owner.lease != nil || d.schemaDesign != nil || d.data != nil || s.documents[d.id] != nil || l.closes.Load() != 1 {
		t.Fatal("terminal callback restored original document caches")
	}
}
