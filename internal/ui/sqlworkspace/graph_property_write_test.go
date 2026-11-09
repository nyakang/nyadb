package sqlworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type propertyTestLease struct {
	*neighborTestLease
	write  func(context.Context, contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error)
	writes atomic.Int32
}

func (l *propertyTestLease) WriteGraphProperty(ctx context.Context, r contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error) {
	l.writes.Add(1)
	return l.write(ctx, r.Clone())
}
func propertyDocumentTest(t *testing.T, write func(context.Context, contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error)) (*State, *Document, *propertyTestLease) {
	s, d, base := neighborhoodDocument(t, func(context.Context, contract.GraphQueryRequest) (contract.GraphResult, error) {
		return contract.GraphResult{}, nil
	})
	l := &propertyTestLease{neighborTestLease: base, write: write}
	d.lease = l
	d.catalog, d.namespaceSchema = "neo4j", ""
	return s, d, l
}
func propertyReceiptTest(r contract.GraphPropertyWriteRequest) contract.GraphPropertyWriteReceipt {
	entity := contract.GraphValue{Kind: r.Target.Kind}
	p := []contract.GraphProperty{{Name: r.Property, Value: r.After.Clone()}}
	if r.Target.Kind == "node" {
		entity.Node = &contract.GraphNode{ElementID: r.Target.ElementID, Labels: append([]string(nil), r.Target.Labels...), Properties: p}
	} else {
		entity.Relationship = &contract.GraphRelationship{ElementID: r.Target.ElementID, StartElementID: r.Target.SourceID, EndElementID: r.Target.TargetID, Type: r.Target.RelationshipType, Properties: p}
	}
	return contract.GraphPropertyWriteReceipt{Query: "fixed native write", Applied: true, Result: contract.GraphResult{Attempted: true, Completed: true, Columns: []string{"identity", "kind", "value", "entity"}, Rows: [][]contract.GraphValue{{{Kind: "string", Text: r.Target.ElementID}, {Kind: "string", Text: r.After.Kind}, r.After.Clone(), entity}}, Summary: contract.GraphSummary{Database: r.Namespace.Catalog, Bookmarks: []string{"original native bookmark"}, Counters: map[string]int64{"properties_set": 2}}}}
}
func startPropertyTest(t *testing.T, s *State, d *Document, text string) {
	t.Helper()
	g := d.graphVisualState()
	g.focus("n:a")
	p := g.properties()[0]
	s.openGraphPropertyDraft(d, g, p)
	if d.graphPropertyDraft == nil {
		t.Fatal("qualified graph property draft unavailable")
	}
	d.graphPropertyDraft.text = text
	s.previewGraphProperty(d)
	if d.graphPropertyDraft.preview == nil {
		t.Fatal("valid graph property preview unavailable", d.graphPropertyDraft.error)
	}
}
func TestGraphPropertyOriginalPreviewParkedWriteAndIndependentVisualReceipt(t *testing.T) {
	requests := make(chan contract.GraphPropertyWriteRequest, 1)
	release := make(chan struct{})
	s, d, l := propertyDocumentTest(t, func(_ context.Context, r contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error) {
		requests <- r
		<-release
		return propertyReceiptTest(r), nil
	})
	defer s.CloseAll()
	active := true
	s.deps.Active = func(*Document) bool { return active }
	before, _ := json.Marshal(d.result)
	startPropertyTest(t, s, d, "new 猫 '\n")
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, 1280, 800)
	tt.Frame()
	g := d.graphVisualState()
	position, zoom, pan := g.positions["n:a"], g.zoom, g.pan
	if e := tt.Click(locales.Text("确认保存图属性")); e != nil {
		t.Fatal(e)
	}
	r := <-requests
	if r.Namespace != d.Namespace() || r.Target.Kind != "node" || r.Target.ElementID != "a" || r.After.Text != "new 猫 '\n" || r.Before.Text != "Alice <&> 猫" {
		t.Fatal("wrong original immutable request", r)
	}
	d.graphPropertyDraft.text = "later mutation"
	s.Run(nil, d)
	s.RunExport(d)
	s.RunFile(d)
	if !d.Loading() || l.executions.Load() != 0 || s.pinResult(d) {
		t.Fatal("another native operation raced the original write")
	}
	active = false
	close(release)
	s.Consume()
	if !d.Loading() || len(d.graphPropertyRecords) != 0 {
		t.Fatal("parked property result published")
	}
	active = true
	consumeUntil(t, s, func() bool { return !d.Loading() })
	after, _ := json.Marshal(d.result)
	if !bytes.Equal(before, after) || d.graphPropertyDraft != nil || len(d.graphPropertyRecords) != 1 || !d.graphPropertyRecords[0].Receipt.Applied || g.node("n:a").Value.Properties[0].Value.Text != r.After.Text {
		t.Fatal("write changed original query/grid or lost actual native observation")
	}
	tt.Frame()
	if g.positions["n:a"] != position || g.zoom != zoom || g.pan != pan || len(g.node("n:a").Cells) == 0 {
		t.Fatal("property admission moved original layout/camera or lost original record links")
	}
	if l.writes.Load() != 1 {
		t.Fatal("native write replayed")
	}
}
func TestGraphPropertyConflictCancellationAndLostReceiptNeverReplay(t *testing.T) {
	for _, mode := range []string{"conflict", "cancel", "revoked", "denied"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan struct{})
			complete := make(chan struct{})
			s, d, l := propertyDocumentTest(t, func(ctx context.Context, r contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error) {
				close(started)
				if mode == "cancel" {
					<-ctx.Done()
					<-complete
					return contract.GraphPropertyWriteReceipt{Query: "native conditional write", Result: contract.GraphResult{Attempted: true, Uncertain: true}}, contract.ErrSQLSessionLost
				}
				<-complete
				if mode == "denied" {
					return contract.GraphPropertyWriteReceipt{Query: "native conditional write", Result: contract.GraphResult{Attempted: true, Uncertain: true, FailureCode: "Neo.ClientError.Security.Forbidden"}}, contract.ErrSQLSessionLost
				}
				if mode == "conflict" {
					return contract.GraphPropertyWriteReceipt{Query: "native conditional write", Conflict: true, Result: contract.GraphResult{Attempted: true, Completed: true, Columns: []string{"identity", "kind", "value", "entity"}, Summary: contract.GraphSummary{Counters: map[string]int64{"properties_set": 1}}}}, nil
				}
				return propertyReceiptTest(r), nil
			})
			defer s.CloseAll()
			before, _ := json.Marshal(d.result)
			startPropertyTest(t, s, d, "draft")
			s.saveGraphProperty(d)
			<-started
			if mode == "cancel" {
				s.RequestCancel(d)
				s.Consume()
				if !d.Loading() || !d.CancelRequested() {
					t.Fatal("cancellation settled before original acknowledgement")
				}
			}
			if mode == "revoked" {
				l.revoked.Store(true)
			}
			close(complete)
			consumeUntil(t, s, func() bool { return !d.Loading() })
			after, _ := json.Marshal(d.result)
			if !bytes.Equal(before, after) || d.graphPropertyDraft == nil || len(d.graphPropertyRecords) != 1 || d.graphPropertyDraft.text != "draft" {
				t.Fatal("failed property operation lost draft/original native evidence")
			}
			if mode != "conflict" && (!d.NeedsRecovery() || !d.graphPropertyDraft.unresolved) {
				t.Fatal("uncertain/stale property write did not require recovery")
			}
			s.saveGraphProperty(d)
			s.Run(nil, d)
			if l.writes.Load() != 1 || l.executions.Load() != 0 || !s.GuardLeave(d, nil) || !s.GuardWorkspaceClose(nil) {
				t.Fatal("write replayed or unprocessed draft could leave silently")
			}
		})
	}
}
func TestGraphPropertySnapshotsReadbackAndJSONOwnIndependentNativeValues(t *testing.T) {
	s, d, l := propertyDocumentTest(t, func(_ context.Context, r contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error) {
		return propertyReceiptTest(r), nil
	})
	defer s.CloseAll()
	startPropertyTest(t, s, d, "confirmed")
	s.saveGraphProperty(d)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	if !s.pinResult(d) {
		t.Fatal(d.Error())
	}
	snap := d.snapshots[0].Document
	if len(snap.graphPropertyRecords) != 1 || snap.graphPropertyBytes != d.graphPropertyBytes || snap.graphVisualState().node("n:a").Value.Properties[0].Value.Text != "confirmed" {
		t.Fatal("snapshot lost independent write receipt or display observation")
	}
	g := d.graphVisualState()
	g.propertyEntities["n:a"].Node.Properties[0].Value.Text = "mutated after pin"
	d.graphPropertyRecords[0].Receipt.Result.Rows[0][3].Node.Properties[0].Value.Text = "mutated receipt"
	if snap.graphVisualState().node("n:a").Value.Properties[0].Value.Text != "confirmed" || snap.graphPropertyRecords[0].Receipt.Result.Rows[0][3].Node.Properties[0].Value.Text != "confirmed" {
		t.Fatal("snapshot aliases live receipt")
	}
	var exported []byte
	exportDone := make(chan struct{})
	s.SetPersistence(nil, Files{SavePath: func(string) (string, error) { return "property.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var b bytes.Buffer
		e := write(&b)
		exported = b.Bytes()
		close(exportDone)
		return e
	}}, nil)
	s.exportGraphPropertyRecords(snap)
	select {
	case <-exportDone:
	case <-time.After(3 * time.Second):
		t.Fatal("native receipt export did not finish")
	}
	if !strings.Contains(string(exported), "confirmed") || !strings.Contains(string(exported), "original native bookmark") {
		t.Fatal("independent property export lost native receipt")
	}
	read := contract.GraphResult{Attempted: true, Completed: true, Columns: []string{"entity"}, Rows: [][]contract.GraphValue{{{Kind: "node", Node: &contract.GraphNode{ElementID: "a", Properties: []contract.GraphProperty{{Name: "name", Value: contract.GraphValue{Kind: "string", Text: "fresh explicit observation"}}}}}}}}
	l.graph = func(context.Context, contract.GraphQueryRequest) (contract.GraphResult, error) { return read, nil }
	s.verifyGraphProperty(d, 0)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	if len(d.graphPropertyRecords) != 2 || !d.graphPropertyRecords[1].Verification || d.graphPropertyRecords[1].Receipt.Applied || d.graphVisualState().node("n:a").Value.Properties[0].Value.Text != "fresh explicit observation" {
		t.Fatal("explicit readback was treated as write confirmation")
	}
	s.verifyGraphProperty(snap, 0)
	if snap.Loading() {
		t.Fatal("snapshot issued live readback")
	}
	d.removeSnapshot(0)
	if snap.graphPropertyRecords != nil || !snap.closed {
		t.Fatal("snapshot close retained native property records")
	}
}

func TestGraphPropertyDisconnectRetainsCapturedRequestAndOriginalRecovery(t *testing.T) {
	started := make(chan struct{})
	s, d, l := propertyDocumentTest(t, func(ctx context.Context, r contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error) {
		close(started)
		<-ctx.Done()
		return contract.GraphPropertyWriteReceipt{Query: "original property write", Result: contract.GraphResult{Attempted: true, Uncertain: true}}, contract.ErrSQLSessionLost
	})
	defer s.CloseAll()
	startPropertyTest(t, s, d, "retained after disconnect")
	s.saveGraphProperty(d)
	<-started
	l.revoked.Store(true)
	s.DisconnectProfile(17)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	if d.profileID != 0 || d.recoveryProfile != 17 || d.recoveryCatalog != "neo4j" || d.graphPropertyDraft == nil || d.graphPropertyDraft.text != "retained after disconnect" || len(d.graphPropertyRecords) != 1 || d.graphPropertyRecords[0].Request.Namespace.ProfileID != 17 || !d.graphPropertyRecords[0].Receipt.Result.Uncertain {
		t.Fatal("disconnect lost original namespace/draft/receipt or pointed recovery at empty profile")
	}
}
func TestGraphPropertyWorkspaceLeaveRevisitsEveryDraftWithoutSavingSQL(t *testing.T) {
	s, d, _ := propertyDocumentTest(t, func(context.Context, contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error) {
		return contract.GraphPropertyWriteReceipt{}, nil
	})
	defer s.CloseAll()
	startPropertyTest(t, s, d, "first draft")
	d.graphPropertyOpen = false
	other := s.Open(2, "RETURN 2", 17, "neo4j", false)
	other.graphPropertyDraft = &graphPropertyDraft{text: "second draft"}
	completed := 0
	if !s.GuardWorkspaceClose(func(ui.Services) { completed++ }) {
		t.Fatal("workspace close ignored graph drafts")
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Modals(c) }, 720, 520)
	for i := 0; i < 2; i++ {
		tt.Frame()
		if s.persistence.graphLeaveDoc == nil || completed != 0 {
			t.Fatal("workspace guard lost another original document")
		}
		if e := tt.Click(locales.Text("放弃属性草稿")); e != nil {
			t.Fatal(e)
		}
	}
	if completed != 1 || d.graphPropertyDraft != nil || other.graphPropertyDraft != nil || s.persistence.graphLeaveDoc != nil {
		t.Fatal("workspace discard did not visit every graph draft exactly once")
	}
}

func TestGraphPropertyPreflightFailureAllowsNewPreviewWithoutAutomaticSubmission(t *testing.T) {
	var calls atomic.Int32
	s, d, l := propertyDocumentTest(t, func(_ context.Context, r contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error) {
		if calls.Add(1) == 1 {
			return contract.GraphPropertyWriteReceipt{}, errors.New("native parameter validation failed before dispatch")
		}
		return propertyReceiptTest(r), nil
	})
	defer s.CloseAll()
	startPropertyTest(t, s, d, "first draft")
	s.saveGraphProperty(d)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	p := d.graphPropertyDraft
	if p == nil || p.attempted || p.unresolved || p.preview != nil || p.text != "first draft" || len(d.graphPropertyRecords) != 1 || d.graphPropertyRecords[0].Receipt.Result.Attempted || l.writes.Load() != 1 {
		t.Fatal("pre-dispatch failure was treated as an uncertain commit or retried automatically")
	}
	s.saveGraphProperty(d)
	if l.writes.Load() != 1 {
		t.Fatal("missing new explicit preview permitted resubmission")
	}
	p.text = "corrected draft"
	s.previewGraphProperty(d)
	s.saveGraphProperty(d)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	if l.writes.Load() != 2 || d.graphPropertyDraft != nil || len(d.graphPropertyRecords) != 2 || !d.graphPropertyRecords[1].Receipt.Applied || d.graphPropertyRecords[1].Request.After.Text != "corrected draft" {
		t.Fatal("known unsubmitted draft could not be explicitly corrected")
	}
}
func TestGraphPropertyScalarValidationAndDraftBusyGuards(t *testing.T) {
	for _, v := range []struct{ k, s string }{{"integer", "01"}, {"integer", "9223372036854775808"}, {"float", "NaN"}, {"float", "Inf"}, {"boolean", "TRUE"}, {"bytes", "AA=="}} {
		if _, e := graphPropertyValue(v.k, v.s); e == nil {
			t.Fatal("invalid editable scalar", v)
		}
	}
	s, d, l := propertyDocumentTest(t, func(context.Context, contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error) {
		return contract.GraphPropertyWriteReceipt{}, errors.New("unused")
	})
	defer s.CloseAll()
	startPropertyTest(t, s, d, "pending draft")
	s.Run(nil, d)
	s.RunExport(d)
	s.RunFile(d)
	s.expandGraph(d)
	if d.Loading() || l.writes.Load() != 0 || l.executions.Load() != 0 {
		t.Fatal("unsubmitted draft was ignored by native busy guards")
	}
	d.graphPropertyBytes = graphPropertyRecordBudget
	s.saveGraphProperty(d)
	if l.writes.Load() != 0 {
		t.Fatal("native write exceeded record retention budget")
	}
}
