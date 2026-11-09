package dataworkspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphimport"
)

type graphTransferPlanTest struct {
	mu       sync.Mutex
	snapshot contract.GraphImportSnapshot
	closed   atomic.Int32
}

func (p *graphTransferPlanTest) Snapshot() contract.GraphImportSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return contract.CloneGraphImportSnapshot(p.snapshot)
}
func (p *graphTransferPlanTest) Close() error {
	p.mu.Lock()
	p.snapshot = contract.GraphImportSnapshot{}
	p.mu.Unlock()
	p.closed.Add(1)
	return nil
}

func graphTransferTestState(t *testing.T) (*State, *Document, *Session) {
	t.Helper()
	s, d, active, _ := documentImportTestState(t)
	d.boundRuntime.DocumentImport = false
	d.boundRuntime.AsyncWrites = false
	d.boundRuntime.PreviewGraphImport = func(_ context.Context, _ int64, _, _ string, r contract.GraphImportRequest) (contract.GraphImportPlan, error) {
		return graphTransferTestPlan(d.object, r), nil
	}
	d.boundRuntime.SubmitGraphImport = func(_ context.Context, _ int64, _, _ string, p contract.GraphImportPlan) (*contract.GraphImportResult, error) {
		return graphTransferTestResult(p.Snapshot()), nil
	}
	s.openDataImport(d)
	b := contract.GraphImportBundle{Format: graphimport.Format, Nodes: []contract.GraphNode{{ElementID: "a", Labels: []string{"owned"}, Properties: []contract.GraphProperty{{Name: "p", Value: contract.GraphValue{Kind: "list", Values: []contract.GraphValue{{Kind: "integer", Text: "1"}}}}}}}, Relationships: []contract.GraphRelationship{{ElementID: "edge", StartElementID: "a", EndElementID: "external", Type: "owned"}}}
	d.transfer.GraphBundle = &b
	d.transfer.GraphBindings = []contract.GraphImportBinding{{SourceID: "external", DestinationID: "existing"}}
	d.transfer.Step = 1
	return s, d, active
}
func graphTransferTestPlan(object contract.ObjectRef, r contract.GraphImportRequest) *graphTransferPlanTest {
	s := contract.GraphImportSnapshot{Object: object, Request: contract.CloneGraphImportRequest(r), Nodes: 1, NativeJSON: `{"original":"complete source and anchors"}`}
	for _, b := range r.Bindings {
		s.Anchors = append(s.Anchors, contract.GraphImportAnchor{SourceID: b.SourceID, Node: contract.GraphNode{ElementID: b.DestinationID}})
	}
	return &graphTransferPlanTest{snapshot: s}
}
func graphTransferTestResult(s contract.GraphImportSnapshot) *contract.GraphImportResult {
	return &contract.GraphImportResult{Object: s.Object, BeginConfirmed: true, CommitAttempted: true, CommitConfirmed: true, NodesCreated: 1, RelationshipsCreated: 1, NativeJSON: `{"CommitConfirmed":true}`, Identities: []contract.GraphImportIdentity{{Kind: "binding", SourceID: "external", DestinationID: "existing"}, {Kind: "node", SourceID: "a", DestinationID: "created"}, {Kind: "relationship", SourceID: "edge", DestinationID: "created-edge"}}}
}

func TestGraphTransferParkedPreviewExplicitConfirmationAndOriginalCompletion(t *testing.T) {
	s, d, active := graphTransferTestState(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var writes atomic.Int32
	var event TableImportEvent
	d.boundRuntime.PreviewGraphImport = func(_ context.Context, id int64, db, name string, r contract.GraphImportRequest) (contract.GraphImportPlan, error) {
		close(entered)
		<-release
		if id != 12 || db != "owned" || name != "documents" {
			return nil, errors.New("foreign original")
		}
		return graphTransferTestPlan(d.object, r), nil
	}
	d.boundRuntime.SubmitGraphImport = func(_ context.Context, id int64, db, name string, p contract.GraphImportPlan) (*contract.GraphImportResult, error) {
		writes.Add(1)
		return graphTransferTestResult(p.Snapshot()), nil
	}
	s.deps.TableImported = func(_ ui.Services, e TableImportEvent) { event = e }
	s.previewGraphTransfer(ui.Services{}, d)
	<-entered
	active.ProfileID = 99
	close(release)
	settleDocumentTransfer(t, s, d)
	if !d.transfer.Busy || d.transfer.GraphPlan != nil || writes.Load() != 0 {
		t.Fatal("parked preview changed active document")
	}
	active.ProfileID = 12
	settleDocumentTransfer(t, s, d)
	if d.transfer.Busy || d.transfer.GraphPlan == nil || d.transfer.Step != 2 || writes.Load() != 0 {
		t.Fatal("original preview not retained for confirmation")
	}
	s.confirmGraphTransfer(ui.Services{}, d)
	if writes.Load() != 0 || d.transfer.Submitted {
		t.Fatal("missing confirmation submitted graph")
	}
	d.transfer.Confirmation = "owned"
	s.confirmGraphTransfer(ui.Services{}, d)
	settleDocumentTransfer(t, s, d)
	if !d.transfer.Completed || d.transfer.Uncertain || writes.Load() != 1 || d.transfer.GraphResult == nil || event.ProfileID != 12 || event.Generation != 3 || event.Database != "owned" || !event.Completed {
		t.Fatal("original confirmed graph completion missing")
	}
	s.confirmGraphTransfer(ui.Services{}, d)
	if writes.Load() != 1 {
		t.Fatal("graph import replayed")
	}
}

func TestGraphTransferRejectsChangedSourceMalformedPreviewAndIncompleteReceipt(t *testing.T) {
	for _, mode := range []string{"source", "preview", "count", "identity", "commit", "nil", "stale", "lost"} {
		t.Run(mode, func(t *testing.T) {
			s, d, active := graphTransferTestState(t)
			var calls atomic.Int32
			if mode == "preview" {
				d.boundRuntime.PreviewGraphImport = func(_ context.Context, _ int64, _, _ string, r contract.GraphImportRequest) (contract.GraphImportPlan, error) {
					p := graphTransferTestPlan(d.object, r)
					p.snapshot.Request.Bundle.Relationships[0].EndElementID = "a"
					return p, nil
				}
			}
			d.boundRuntime.SubmitGraphImport = func(_ context.Context, _ int64, _, _ string, p contract.GraphImportPlan) (*contract.GraphImportResult, error) {
				calls.Add(1)
				r := graphTransferTestResult(p.Snapshot())
				switch mode {
				case "count":
					r.NodesCreated = 2
				case "identity":
					r.Identities[1].SourceID = "other"
				case "commit":
					r.CommitConfirmed = false
				case "nil":
					return nil, nil
				case "lost":
					r.OutcomeUnknown = true
					r.CommitConfirmed = false
					return r, errors.New("lost")
				}
				return r, nil
			}
			s.previewGraphTransfer(ui.Services{}, d)
			settleDocumentTransfer(t, s, d)
			if mode == "preview" {
				if d.transfer.GraphPlan != nil || d.transfer.Err == "" {
					t.Fatal("altered source preview accepted")
				}
				return
			}
			if mode == "source" {
				d.transfer.GraphBundle.Nodes[0].Properties[0].Value.Values[0].Text = "2"
			}
			d.transfer.Confirmation = "owned"
			s.confirmGraphTransfer(ui.Services{}, d)
			if mode == "source" {
				if calls.Load() != 0 || d.transfer.GraphPlan != nil || d.transfer.Err == "" {
					t.Fatal("changed source reached submit")
				}
				return
			}
			original := d.page
			if mode == "stale" {
				active.Generation++
			}
			settleDocumentTransfer(t, s, d)
			if d.transfer.Completed || !d.transfer.Uncertain || d.edit.Uncertain == nil || d.transfer.GraphBundle == nil || d.transfer.GraphSnapshot.NativeJSON == "" || d.page != original || calls.Load() != 1 {
				t.Fatal("incomplete/stale receipt lost source or admitted success", mode)
			}
			s.confirmGraphTransfer(ui.Services{}, d)
			if calls.Load() != 1 {
				t.Fatal("uncertain graph replayed")
			}
			s.stopDataTransfer(d)
			if d.transfer.Open || !d.transfer.Uncertain || d.transfer.GraphBundle == nil {
				t.Fatal("hidden graph wizard lost unknown evidence")
			}
		})
	}
}

func TestGraphTransferCanceledAndClosedPreviewReleasesLateAndUnconsumedHandles(t *testing.T) {
	for _, closeDoc := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "close"}[closeDoc], func(t *testing.T) {
			s, d, _ := graphTransferTestState(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var p *graphTransferPlanTest
			d.boundRuntime.PreviewGraphImport = func(_ context.Context, _ int64, _, _ string, r contract.GraphImportRequest) (contract.GraphImportPlan, error) {
				p = graphTransferTestPlan(d.object, r)
				close(entered)
				<-release
				return p, nil
			}
			s.previewGraphTransfer(ui.Services{}, d)
			<-entered
			if closeDoc {
				s.Close(d)
			} else {
				s.stopDataTransfer(d)
				if !d.transfer.Busy || !d.transfer.Canceling {
					t.Fatal("cancel did not wait for reader acknowledgement")
				}
			}
			close(release)
			if !closeDoc {
				settleDocumentTransfer(t, s, d)
				if d.transfer.Busy || d.transfer.GraphPlan != nil || d.transfer.GraphBundle == nil {
					t.Fatal("canceled preview not settled")
				}
			}
			deadline := time.Now().Add(3 * time.Second)
			for p.closed.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if p.closed.Load() == 0 {
				t.Fatal("late original graph plan leaked")
			}
		})
	}
}

func TestGraphTransferReceiptMappingRequiresCompleteOriginalReferences(t *testing.T) {
	_, d, _ := graphTransferTestState(t)
	r, _ := graphTransferRequest(&d.transfer)
	snapshot := graphTransferTestPlan(d.object, r).Snapshot()
	if !graphImportReceiptComplete(snapshot, graphTransferTestResult(snapshot)) {
		t.Fatal("complete original graph receipt rejected")
	}
	for _, change := range []func(*contract.GraphImportResult){func(r *contract.GraphImportResult) { r.Identities[0].DestinationID = "foreign" }, func(r *contract.GraphImportResult) { r.Identities = append(r.Identities, r.Identities[0]) }, func(r *contract.GraphImportResult) { r.RollbackConfirmed = true }, func(r *contract.GraphImportResult) { r.Object.ProfileID++ }} {
		result := graphTransferTestResult(snapshot)
		change(result)
		if graphImportReceiptComplete(snapshot, result) {
			t.Fatal("malformed graph mapping admitted")
		}
	}
	copy := contract.CloneGraphImportSnapshot(snapshot)
	copy.Request.Bundle.Relationships[0].EndElementID = "a"
	if graphimport.EquivalentRequest(snapshot.Request, copy.Request) || !reflect.DeepEqual(snapshot.Request, r) {
		t.Fatal("source identity comparison lost original direction")
	}
}

func TestGraphTransferAtomicExportPreservesOriginalFileOnInvalidGraphAndCancel(t *testing.T) {
	for _, mode := range []string{"complete", "invalid", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, d, _ := graphTransferTestState(t)
			s.closeDataTransfer(d)
			s.openDataExport(d)
			d.transfer.GraphExchange, d.transfer.Format = true, "JSONL"
			path := filepath.Join(t.TempDir(), "graph.jsonl")
			if err := os.WriteFile(path, []byte("existing file"), 0600); err != nil {
				t.Fatal(err)
			}
			d.transfer.ExportPath = path
			entered := make(chan struct{})
			d.boundRuntime.ExportGraph = func(ctx context.Context, object contract.ObjectRef) (contract.GraphImportBundle, error) {
				if object != d.object {
					return contract.GraphImportBundle{}, errors.New("wrong original object")
				}
				close(entered)
				if mode == "cancel" {
					<-ctx.Done()
					return contract.GraphImportBundle{}, ctx.Err()
				}
				bundle := contract.GraphImportBundle{Format: graphimport.Format, Source: object.Namespace(), Nodes: []contract.GraphNode{{ElementID: "complete"}}}
				if mode == "invalid" {
					bundle.Nodes = append(bundle.Nodes, bundle.Nodes[0])
				}
				return bundle, nil
			}
			s.startGraphExport(ui.Services{}, d)
			<-entered
			if mode == "cancel" {
				s.stopDataTransfer(d)
				if !d.transfer.Busy {
					t.Fatal("export canceled before acknowledgement")
				}
			}
			settleDocumentTransfer(t, s, d)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "complete" {
				if !d.transfer.Completed || string(raw) == "existing file" {
					t.Fatal("complete graph export not published")
				}
			} else if d.transfer.Completed || string(raw) != "existing file" {
				t.Fatal("failed graph export replaced original file")
			}
		})
	}
}
