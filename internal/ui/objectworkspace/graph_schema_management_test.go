package objectworkspace

import (
	"context"
	"errors"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphschema"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type schemaUIProvider struct{ l *schemaUILease }

func (p schemaUIProvider) SupportsGraphSchemaManagement(o contract.ObjectRef) bool {
	return graphSchemaObject(o)
}
func (p schemaUIProvider) ReserveGraphSchemaManagement(o contract.ObjectRef, id uint64) (contract.GraphSchemaManagementLease, error) {
	p.l.object = o
	return p.l, nil
}

type schemaUILease struct {
	object                           contract.ObjectRef
	submits, verifies, closes, reads atomic.Int32
	submit                           func(context.Context, contract.GraphSchemaPreparation, contract.GraphSchemaConfirmation) (contract.GraphSchemaManagementResult, error)
	verify                           func(context.Context) (contract.GraphSchemaManagementResult, error)
	draft                            contract.GraphSchemaDraft
}
type schemaUIObservation struct{ c contract.GraphSchemaCatalog }

func (o schemaUIObservation) Snapshot() contract.GraphSchemaCatalog { return o.c.Clone() }

type schemaUIPlan struct{ p contract.GraphSchemaPreview }

func (p schemaUIPlan) Preview() contract.GraphSchemaPreview { return p.p.Clone() }
func (l *schemaUILease) Object() contract.ObjectRef         { return l.object }
func (l *schemaUILease) Generation() uint64                 { return 7 }
func (l *schemaUILease) Read(context.Context) (contract.GraphSchemaObservation, error) {
	l.reads.Add(1)
	return schemaUIObservation{contract.GraphSchemaCatalog{Object: l.object}}, nil
}
func (l *schemaUILease) Prepare(o contract.GraphSchemaObservation, d contract.GraphSchemaDraft) (contract.GraphSchemaPreparation, error) {
	l.draft = d.Clone()
	return schemaUIPlan{contract.GraphSchemaPreview{Draft: d.Clone(), NonAtomicDrop: strings.HasPrefix(d.Action, "drop_"), Request: contract.GraphSchemaRequest{Database: l.object.Catalog, Query: "native request"}}}, nil
}
func (l *schemaUILease) Submit(ctx context.Context, p contract.GraphSchemaPreparation, c contract.GraphSchemaConfirmation) (contract.GraphSchemaManagementResult, error) {
	l.submits.Add(1)
	if l.submit != nil {
		return l.submit(ctx, p, c)
	}
	return contract.GraphSchemaManagementResult{State: "completed", ReceiptValid: true, Preview: p.Preview(), Receipt: contract.GraphResult{Attempted: true, Completed: true, Columns: []string{"original"}}}, nil
}
func (l *schemaUILease) Verify(ctx context.Context) (contract.GraphSchemaManagementResult, error) {
	l.verifies.Add(1)
	if l.verify != nil {
		return l.verify(ctx)
	}
	return contract.GraphSchemaManagementResult{State: "verified", ReceiptValid: true, HasVerification: true, Matched: true, Preview: contract.GraphSchemaPreview{Request: contract.GraphSchemaRequest{Query: "native request"}}, Receipt: contract.GraphResult{Attempted: true, Completed: true, Columns: []string{"original"}}}, nil
}
func (l *schemaUILease) Close() error { l.closes.Add(1); return nil }
func schemaUITest(t *testing.T) (*State, *Document, *Session, *schemaUILease) {
	current := &Session{ProfileID: 1, Generation: 7, Connected: true}
	l := &schemaUILease{}
	s := New(Dependencies{Session: func() Session { return *current }, SessionFor: func(id int64) Session {
		return Session{ProfileID: id, Generation: current.Generation, Connected: current.Connected}
	}, GraphSchemaManagementFor: func(int64) (contract.GraphSchemaManagementProvider, error) { return schemaUIProvider{l}, nil }})
	d := s.OpenQualifiedStructure(1, contract.ObjectRef{ProfileID: 1, Catalog: "neo4j", Kind: "graph_schema", Name: "graph_schema"}, "图索引与约束管理")
	if d == nil {
		t.Fatal("missing management document")
	}
	t.Cleanup(s.CloseAll)
	f := s.GraphSchemaForm(d)
	f.Name = "original"
	f.LabelsJSON = `["a,b","猫"]`
	f.PropertiesJSON = `["first","second"]`
	s.SetGraphSchemaForm(d, f)
	return s, d, current, l
}
func settleSchemaUI(t *testing.T, s *State, d *Document) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for d.loading {
		s.Consume(ui.Services{})
		if time.Now().After(end) {
			t.Fatal("request did not settle")
		}
		time.Sleep(time.Millisecond)
	}
}
func prepareSchemaUI(t *testing.T, s *State, d *Document) {
	t.Helper()
	s.ReadGraphSchema(d, nil)
	settleSchemaUI(t, s, d)
	if e := s.PrepareGraphSchema(d); e != nil {
		t.Fatal(e)
	}
}
func TestGraphSchemaUIExplicitReadOneSubmitVerifyAndIndependentEvidence(t *testing.T) {
	s, d, _, l := schemaUITest(t)
	s.Load(d, nil, false)
	if l.reads.Load() != 0 {
		t.Fatal("restore performed I/O")
	}
	prepareSchemaUI(t, s, d)
	if l.draft.LabelsOrTypes[0] != "a,b" || l.draft.Properties[1] != "second" {
		t.Fatal("identifier array changed")
	}
	s.SubmitGraphSchema(d, nil)
	settleSchemaUI(t, s, d)
	r := s.GraphSchemaResult(d)
	r.Receipt.Columns[0] = "changed"
	if s.GraphSchemaResult(d).Receipt.Columns[0] != "original" || !d.schemaManagement.pending || l.verifies.Load() != 0 {
		t.Fatal("receipt aliased or automatic verification")
	}
	s.ReadGraphSchema(d, nil)
	s.SubmitGraphSchema(d, nil)
	if l.reads.Load() != 1 || l.submits.Load() != 1 {
		t.Fatal("pending write replayed")
	}
	s.VerifyGraphSchema(d, nil)
	settleSchemaUI(t, s, d)
	if d.schemaManagement.pending || s.GraphSchemaResult(d).State != "verified" || d.DesignDirty() {
		t.Fatal("verified receipt not settled")
	}
}
func TestGraphSchemaUIDropConfirmationAndBoundedTypedJSON(t *testing.T) {
	s, d, _, l := schemaUITest(t)
	f := s.GraphSchemaForm(d)
	f.Action = "drop_constraint"
	s.SetGraphSchemaForm(d, f)
	prepareSchemaUI(t, s, d)
	if s.CanSubmitGraphSchema(d) {
		t.Fatal("drop needs name and acknowledgement")
	}
	d.schemaManagement.confirmation = f.Name
	if s.CanSubmitGraphSchema(d) {
		t.Fatal("drop acknowledgement omitted")
	}
	d.schemaManagement.acceptDrop = true
	l.submit = func(_ context.Context, p contract.GraphSchemaPreparation, c contract.GraphSchemaConfirmation) (contract.GraphSchemaManagementResult, error) {
		if c.Name != f.Name || !c.AcceptNonAtomicDrop {
			t.Error("confirmation not captured")
		}
		return contract.GraphSchemaManagementResult{State: "not_submitted", Preview: p.Preview()}, contract.ErrGraphSchemaChanged
	}
	s.SubmitGraphSchema(d, nil)
	settleSchemaUI(t, s, d)
	if d.schemaManagement.pending {
		t.Fatal("rejected preflight treated as sent")
	}
	draft, e := graphSchemaDraft(GraphSchemaForm{Action: "create_index", LabelsJSON: `["a,b"]`, PropertiesJSON: `["z"]`, OptionsJSON: `{"indexConfig":{"vector.dimensions":9007199254740993}}`})
	if e != nil || draft.Options.Properties[0].Value.Properties[0].Value.Text != "9007199254740993" {
		t.Fatal("integer rounded", e)
	}
	for _, raw := range []string{`{"n":3,"n":4}`, `{"indexConfig":{"n":3,"n":4}}`, `{"n":1.5}`, `{"n":9223372036854775808}`, `{} {}`, strings.Repeat("[", 18) + "0" + strings.Repeat("]", 18)} {
		if _, e := graphSchemaDraft(GraphSchemaForm{Action: "create_index", LabelsJSON: `[]`, PropertiesJSON: `[]`, OptionsJSON: raw}); e == nil {
			t.Fatal("invalid typed options accepted", raw)
		}
	}
	f.OptionsJSON = strings.Repeat("x", 1<<20)
	if _, e := graphSchemaDraft(f); !errors.Is(e, graphschema.ErrBounds) {
		t.Fatal("unbounded draft", e)
	}
}
func TestGraphSchemaUIParkedCanceledLateOriginalReceiptAndGeneration(t *testing.T) {
	s, d, current, l := schemaUITest(t)
	prepareSchemaUI(t, s, d)
	started, release := make(chan struct{}), make(chan struct{})
	l.submit = func(ctx context.Context, p contract.GraphSchemaPreparation, _ contract.GraphSchemaConfirmation) (contract.GraphSchemaManagementResult, error) {
		close(started)
		<-ctx.Done()
		<-release
		return contract.GraphSchemaManagementResult{State: "uncertain", Preview: p.Preview(), Receipt: contract.GraphResult{Attempted: true, Uncertain: true}}, ctx.Err()
	}
	s.SubmitGraphSchema(d, nil)
	<-started
	s.Cancel(d)
	if !d.loading {
		t.Fatal("cancel settled before acknowledgement")
	}
	current.ProfileID = 2
	close(release)
	time.Sleep(5 * time.Millisecond)
	s.Consume(ui.Services{})
	if !d.loading {
		t.Fatal("parked completion consumed")
	}
	current.ProfileID = 1
	current.Generation = 8
	settleSchemaUI(t, s, d)
	if !d.schemaManagement.lost || !d.schemaManagement.pending || !s.GraphSchemaResult(d).Receipt.Uncertain {
		t.Fatal("original late receipt erased")
	}
	if !s.GuardDesignLeave(d, nil) {
		t.Fatal("unresolved evidence lacks guard")
	}
	s.Close(d)
	s.Close(d)
	if l.closes.Load() != 1 || d.schemaManagement != nil {
		t.Fatal("close did not release once")
	}
}
func TestGraphSchemaUIFailedVerificationRetainsOriginalReceiptAndTruncatedPreviewNeedsReview(t *testing.T) {
	s, d, _, l := schemaUITest(t)
	prepareSchemaUI(t, s, d)
	d.schemaManagement.preview.Request.Query = strings.Repeat("x", 100000)
	if s.CanSubmitGraphSchema(d) {
		t.Fatal("truncated preview accepted without review")
	}
	d.schemaManagement.reviewed = true
	s.SubmitGraphSchema(d, nil)
	settleSchemaUI(t, s, d)
	l.verify = func(context.Context) (contract.GraphSchemaManagementResult, error) {
		return contract.GraphSchemaManagementResult{}, errors.New("read failed")
	}
	s.VerifyGraphSchema(d, nil)
	settleSchemaUI(t, s, d)
	if !s.GraphSchemaResult(d).Receipt.Completed || !d.schemaManagement.pending || l.submits.Load() != 1 {
		t.Fatal("failed verify erased/replayed receipt")
	}
}

func TestGraphSchemaUIRevokedSubmitRetainsOlderReceiptWithoutSettlingNewDraft(t *testing.T) {
	s, d, _, l := schemaUITest(t)
	prepareSchemaUI(t, s, d)
	s.SubmitGraphSchema(d, nil)
	settleSchemaUI(t, s, d)
	s.VerifyGraphSchema(d, nil)
	settleSchemaUI(t, s, d)
	older := s.GraphSchemaResult(d)
	f := s.GraphSchemaForm(d)
	f.Name = "new unsent draft"
	s.SetGraphSchemaForm(d, f)
	prepareSchemaUI(t, s, d)
	l.submit = func(context.Context, contract.GraphSchemaPreparation, contract.GraphSchemaConfirmation) (contract.GraphSchemaManagementResult, error) {
		return older.Clone(), contract.ErrRevoked
	}
	s.SubmitGraphSchema(d, nil)
	settleSchemaUI(t, s, d)
	if !d.schemaManagement.lost || !d.DesignDirty() || s.GraphSchemaForm(d).Name != f.Name || !s.GraphSchemaResult(d).Receipt.Completed {
		t.Fatal("retained older receipt settled a revoked new draft")
	}
}
