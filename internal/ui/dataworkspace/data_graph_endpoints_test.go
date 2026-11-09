package dataworkspace

import (
	"context"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func graphEndpointWorkspace(t *testing.T) (*State, *Document, *Session, *contract.DataGraphEndpoints) {
	t.Helper()
	s, d, active := mutationWorkspace(t)
	d.object.Kind = "relationship"
	d.page = &contract.DataPage{Columns: []string{"element_id", "type", "start_element_id", "end_element_id", "properties", "relationship", "property.guard"}, Rows: [][]contract.DataCell{}, RowRefs: []*contract.RowRef{}, RowSnapshots: []*contract.RowSnapshot{}}
	d.columns = nil
	for i, name := range d.page.Columns {
		d.page.ColumnMetadata = append(d.page.ColumnMetadata, contract.StructureColumn{Name: name, Generated: i < 6, ValueKind: contract.Text})
		d.columns = append(d.columns, ui.TableColumn{ID: name, Title: name, Width: 150, MinWidth: 65})
	}
	e := &contract.DataGraphEndpoints{Object: d.object, Source: contract.GraphNode{ElementID: "source", Labels: []string{"original"}, Properties: []contract.GraphProperty{{Name: "list", Value: contract.GraphValue{Kind: "list", Values: []contract.GraphValue{{Kind: "integer", Text: "9223372036854775807"}}}}}}, Target: contract.GraphNode{ElementID: "target", Labels: []string{"target"}}, NativeJSON: `{"complete":"native endpoints"}`, Token: "original"}
	d.boundRuntime.InsertOmittedValues = true
	d.boundRuntime.ReadGraphEndpoints = func(context.Context, int64, string, string, string, string) (*contract.DataGraphEndpoints, error) {
		return e, nil
	}
	return s, d, active, e
}
func waitEndpointRead(t *testing.T, s *State, d *Document) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for d.graphEndpoints != nil && d.graphEndpoints.results != nil && time.Now().Before(deadline) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if d.graphEndpoints != nil && d.graphEndpoints.results != nil {
		t.Fatal("original endpoint read did not settle")
	}
}
func readEndpointChoice(t *testing.T, s *State, d *Document) {
	t.Helper()
	s.addMySQLDataRecord(ui.Services{}, d)
	if d.graphEndpoints == nil || d.HasDraft() {
		t.Fatal("relationship addition skipped explicit endpoint input")
	}
	d.graphEndpoints.source, d.graphEndpoints.target = "source", "target"
	s.readDataGraphEndpoints(ui.Services{}, d)
	waitEndpointRead(t, s, d)
	if d.graphEndpoints == nil || d.graphEndpoints.observation == nil {
		t.Fatal("complete original endpoints missing", d.edit.Err)
	}
}

func TestDataGraphEndpointDraftHistoryAndConfirmationOwnCompleteObservations(t *testing.T) {
	s, d, _, provider := graphEndpointWorkspace(t)
	readEndpointChoice(t, s, d)
	provider.Source.Labels[0] = "provider changed"
	if d.graphEndpoints.observation.Source.Labels[0] != "original" {
		t.Fatal("endpoint UI borrowed provider values")
	}
	evidence := d.graphEndpoints.observation
	s.stageDataGraphRelationship(ui.Services{}, d)
	s.finishMySQLCellEdit(d, false)
	draft := mysqlDataDraftForRow(d, 0)
	if draft == nil || draft.GraphEndpoints == nil || len(draft.Values) != 0 || d.page.Rows[0][2].Text != "source" || d.page.Rows[0][3].Text != "target" || mysqlDataCanEdit(d, 0, 2) {
		t.Fatal("relationship draft lost known immutable endpoints or invented properties")
	}
	if s.dataCellPreview(d, d.page, 0, 2).Text != "source" || s.dataCellPreview(d, d.page, 0, 3).Text != "target" || s.dataCellPreview(d, d.page, 0, 1).Text != d.table {
		t.Fatal("known endpoint observations were displayed as generated fields")
	}
	evidence.Source.Labels[0] = "choice changed"
	if draft.GraphEndpoints.Source.Labels[0] != "original" {
		t.Fatal("draft borrowed released choice evidence")
	}
	if !s.undoDraft(d, false) || d.HasDraft() || !s.undoDraft(d, true) {
		t.Fatal("endpoint insertion undo/redo failed")
	}
	draft = mysqlDataDraftForRow(d, 0)
	draft.GraphEndpoints.Source.Properties[0].Value.Values[0].Text = "0"
	if !s.undoDraft(d, false) || !s.undoDraft(d, true) || mysqlDataDraftForRow(d, 0).GraphEndpoints.Source.Properties[0].Value.Values[0].Text != "9223372036854775807" {
		t.Fatal("undo history shared nested endpoint evidence")
	}
	d.boundRuntime.PreviewEdits = func(_ context.Context, _ int64, _, _ string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
		return mutationPlanFor(d, edits), nil
	}
	var submits atomic.Int32
	d.boundRuntime.SubmitEdits = func(context.Context, int64, string, string, contract.DataMutationPlan) (*contract.DataEditResult, error) {
		submits.Add(1)
		return nil, nil
	}
	s.Save(ui.Services{}, d)
	waitMutation(t, s, d)
	if !d.mutation.open || d.mutation.snapshot.Edits[0].GraphEndpoints == nil {
		t.Fatal("confirmation omitted endpoint evidence")
	}
	mysqlDataDraftForRow(d, 0).GraphEndpoints.Target.Labels[0] = "later draft"
	s.confirmDataMutation(ui.Services{}, d)
	if submits.Load() != 0 || !d.HasDraft() {
		t.Fatal("changed endpoint draft authorized submission")
	}
}

func TestDataGraphEndpointCanceledAndParkedReadsWaitForOriginalAcknowledgement(t *testing.T) {
	for _, action := range []string{"cancel", "park", "generation", "close"} {
		t.Run(action, func(t *testing.T) {
			s, d, active, e := graphEndpointWorkspace(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var canceled atomic.Bool
			d.boundRuntime.ReadGraphEndpoints = func(ctx context.Context, _ int64, _, _, source, target string) (*contract.DataGraphEndpoints, error) {
				if source != "source" || target != "target" {
					t.Error("reader used later input")
				}
				close(entered)
				<-release
				canceled.Store(ctx.Err() != nil)
				return e, nil
			}
			s.addMySQLDataRecord(ui.Services{}, d)
			d.graphEndpoints.source, d.graphEndpoints.target = "source", "target"
			s.readDataGraphEndpoints(ui.Services{}, d)
			results := d.graphEndpoints.results
			<-entered
			switch action {
			case "cancel":
				s.stopMySQLDataEdit(d)
				if !d.edit.Busy || d.graphEndpoints == nil || d.graphEndpoints.results == nil {
					t.Fatal("endpoint cancellation preceded executor acknowledgement")
				}
			case "park", "generation":
				active.ProfileID = 99
			case "close":
				s.Close(d)
			}
			close(release)
			if action == "close" {
				select {
				case r := <-results:
					if r.endpoints != nil || r.err == nil || !canceled.Load() {
						t.Fatal("closed endpoint reader returned reusable authorization")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("closed reader did not acknowledge")
				}
				if d.graphEndpoints != nil || s.documents[d.id] != nil {
					t.Fatal("closed document retained endpoint caches")
				}
			} else if action == "cancel" {
				waitEndpointRead(t, s, d)
				if d.edit.Busy || d.graphEndpoints != nil || d.HasDraft() || !canceled.Load() {
					t.Fatal("canceled endpoint read changed drafts or retained authorization")
				}
			} else {
				select {
				case r := <-results:
					out := make(chan dataGraphEndpointResult, 1)
					out <- r
					d.graphEndpoints.results = out
				case <-time.After(5 * time.Second):
					t.Fatal("parked endpoint reader stalled")
				}
				s.Consume(ui.Services{})
				if d.graphEndpoints.observation != nil || !d.edit.Busy {
					t.Fatal("another profile consumed original endpoints")
				}
				active.ProfileID = d.profileID
				if action == "generation" {
					active.Generation++
				}
				s.Consume(ui.Services{})
				if action == "generation" {
					if d.graphEndpoints != nil || d.edit.Busy || d.HasDraft() {
						t.Fatal("old generation admitted endpoint authorization")
					}
				} else if d.graphEndpoints.observation == nil || !reflect.DeepEqual(d.graphEndpoints.observation, e) {
					t.Fatal("return did not consume original endpoints")
				}
			}
		})
	}
}

func TestDataGraphEndpointRejectsMissingForeignAndOversizedReaderEvidence(t *testing.T) {
	for _, kind := range []string{"missing", "foreign", "wrong-source", "no-token", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			s, d, _, e := graphEndpointWorkspace(t)
			var reads atomic.Int32
			d.boundRuntime.ReadGraphEndpoints = func(context.Context, int64, string, string, string, string) (*contract.DataGraphEndpoints, error) {
				reads.Add(1)
				switch kind {
				case "missing":
					return nil, nil
				case "foreign":
					e.Object.Catalog = "foreign"
				case "wrong-source":
					e.Source.ElementID = "foreign"
				case "no-token":
					e.Token = ""
				case "oversized":
					e.NativeJSON = strings.Repeat("x", contract.DataEditMaxBytes)
				}
				return e, nil
			}
			s.addMySQLDataRecord(ui.Services{}, d)
			s.readDataGraphEndpoints(ui.Services{}, d)
			if reads.Load() != 0 || d.graphEndpoints.err == "" {
				t.Fatal("missing explicit IDs dispatched")
			}
			d.graphEndpoints.source, d.graphEndpoints.target = "source", "target"
			s.readDataGraphEndpoints(ui.Services{}, d)
			waitEndpointRead(t, s, d)
			if d.graphEndpoints == nil || d.graphEndpoints.observation != nil || d.graphEndpoints.err == "" || d.HasDraft() {
				t.Fatal("invalid endpoint evidence became a draft", kind)
			}
			s.stageDataGraphRelationship(ui.Services{}, d)
			if d.HasDraft() {
				t.Fatal("invalid native endpoint observation authorized staging")
			}
			s.cancelDataGraphEndpoints(d)
			if d.edit.Busy {
				t.Fatal("failed endpoint read retained busy state after cancellation")
			}
		})
	}
}

func TestDataGraphEndpointInputWidgetsPreserveExplicitDirectionWithVisibleCaptions(t *testing.T) {
	s, d, _, _ := graphEndpointWorkspace(t)
	s.addMySQLDataRecord(ui.Services{}, d)
	tt := ui.NewTester(func(c *ui.Context) { s.View(c, d); s.Modals(c) }, 720, 520)
	tt.Frame()
	for _, input := range []struct{ label, value string }{{"输入源节点 element ID", "source"}, {"输入目标节点 element ID", "target"}} {
		if err := tt.Click(locales.Text(input.label)); err != nil {
			t.Fatal(err)
		}
		tt.Command("selectAll")
		tt.Type(input.value)
	}
	if d.graphEndpoints.source != "source" || d.graphEndpoints.target != "target" {
		t.Fatal("visible captions intercepted input or changed direction")
	}
	if err := tt.Click(locales.Text("读取端点")); err != nil {
		t.Fatal(err)
	}
	waitEndpointRead(t, s, d)
	tt.Frame()
	for _, label := range []string{"源节点 element ID", "目标节点 element ID", "读取端点", "复制完整端点观察", "添加关系草稿", "取消"} {
		box, ok := tt.Find(locales.Text(label))
		if !ok || box.X < 0 || box.Y < 0 || box.X+box.W > 720 || box.Y+box.H > 520 {
			t.Fatal("endpoint caption/control clipped", label, box)
		}
	}
	if err := tt.Click(locales.Text("添加关系草稿")); err != nil {
		t.Fatal(err)
	}
	if mysqlDataDraftForRow(d, 0).GraphEndpoints.Source.ElementID != "source" {
		t.Fatal("widget endpoint direction was not captured")
	}
}
