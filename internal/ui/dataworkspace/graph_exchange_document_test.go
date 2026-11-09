package dataworkspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphimport"
)

func graphNamespaceTestState(t *testing.T) (*State, *Document, *Session, *testDocumentLease) {
	t.Helper()
	active := &Session{ProfileID: 12, Generation: 3, Connected: true}
	object := contract.NamespaceRef{ProfileID: 12, Catalog: "empty"}.Object(contract.GraphNamespaceKind, "")
	_, cancel := context.WithCancel(context.Background())
	lease := &testDocumentLease{cancel: cancel}
	lease.runtime = Runtime{NativeReceipts: true, Capabilities: contract.Capabilities{GraphExchange: true, Documents: true, Import: true, Export: true},
		PreviewGraphImport: func(_ context.Context, id int64, db, name string, r contract.GraphImportRequest) (contract.GraphImportPlan, error) {
			if id != 12 || db != "empty" || name != "" {
				t.Fatal("namespace preview fabricated an object", id, db, name)
			}
			return graphTransferTestPlan(object, r), nil
		}, SubmitGraphImport: func(_ context.Context, id int64, db, name string, p contract.GraphImportPlan) (*contract.GraphImportResult, error) {
			return graphTransferTestResult(p.Snapshot()), nil
		},
		ExportGraph: func(_ context.Context, o contract.ObjectRef) (contract.GraphImportBundle, error) {
			if o != object {
				t.Fatal("export rebound namespace")
			}
			return contract.GraphImportBundle{Format: graphimport.Format, Source: o.Namespace()}, nil
		},
	}
	parent := Runtime{ReserveDocument: func(o contract.ObjectRef) (contract.DataDocumentLease, error) {
		if o != object {
			t.Fatal("namespace lease fabricated label", o)
		}
		return lease, nil
	}}
	s := New(Dependencies{Session: func() Session { return *active }, SessionFor: func(int64) Session { return *active }, Runtime: func() Runtime { return parent }})
	d := s.OpenObject(1, object, lease.runtime.Capabilities)
	if d == nil {
		t.Fatal("namespace document unavailable")
	}
	t.Cleanup(func() { s.Close(d) })
	return s, d, active, lease
}

func TestGraphNamespaceDocumentNeedsCompleteOriginalLeaseAndNoGrid(t *testing.T) {
	s, d, active, lease := graphNamespaceTestState(t)
	if !d.GraphExchange() || d.Object().Name != "" || d.Table() != "" || d.Kind() != "图交换" || d.Page() != nil || !d.capabilities.GraphExchange || d.Generation() != active.Generation {
		t.Fatal("namespace identity fabricated grid or lost generation")
	}
	s.Load(ui.Services{}, d, true)
	if d.Loading() || d.Page() != nil || s.inspectorPanel(d) != nil || s.WorkspaceSnapshot(d) != nil {
		t.Fatal("namespace opened grid, inspector or persisted transfer state")
	}
	if err := s.RestoreWorkspace(d, []byte(`{"source":"must not restore"}`)); err != nil {
		t.Fatal(err)
	}
	s.RequestAction(ui.Services{}, d, "import")
	if !d.transfer.Open || !d.transfer.GraphExchange || d.transfer.NewName != "" || len(d.transfer.Sources) != 0 {
		t.Fatal("namespace import inferred a table")
	}
	s.Close(d)
	s.Close(d)
	if lease.closed.Load() != 1 || s.documents[d.id] != nil || d.boundRuntime != nil {
		t.Fatal("namespace close leaked lease or document")
	}
}

func TestGraphNamespaceEmptyExchangeUsesOriginalAtomicFileAndParkedPublication(t *testing.T) {
	for _, format := range []string{"JSON", "JSONL"} {
		t.Run(format, func(t *testing.T) {
			s, d, active, _ := graphNamespaceTestState(t)
			s.RequestAction(ui.Services{}, d, "export")
			if !d.transfer.GraphExchange || d.transfer.Scope != "database" {
				t.Fatal("namespace exported a fabricated page")
			}
			path := filepath.Join(t.TempDir(), "empty.graph")
			d.transfer.Format, d.transfer.ExportPath = format, path
			s.startGraphExport(ui.Services{}, d)
			active.ProfileID = 99
			settleDocumentTransfer(t, s, d)
			if !d.transfer.Busy || d.transfer.Completed {
				t.Fatal("parked namespace completion changed active state")
			}
			active.ProfileID = 12
			settleDocumentTransfer(t, s, d)
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			b, err := graphimport.Parse(f, format)
			if err != nil || b.Source != d.Object().Namespace() || len(b.Nodes) != 0 || len(b.Relationships) != 0 || !d.transfer.Completed {
				t.Fatal("empty namespace exchange lost original source", err)
			}
			if d.Page() != nil || d.Loading() {
				t.Fatal("namespace export started grid read")
			}
		})
	}
}

func TestGraphNamespacePreviewConfirmationUnknownAndReplacementKeepOriginal(t *testing.T) {
	for _, mode := range []string{"confirmed", "unknown", "generation"} {
		t.Run(mode, func(t *testing.T) {
			s, d, active, lease := graphNamespaceTestState(t)
			s.RequestAction(ui.Services{}, d, "import")
			b := contract.GraphImportBundle{Format: graphimport.Format, Nodes: []contract.GraphNode{{ElementID: "new"}}, Relationships: []contract.GraphRelationship{{ElementID: "self", StartElementID: "new", EndElementID: "new", Type: "owned"}}}
			d.transfer.GraphBundle = &b
			if mode == "unknown" {
				lease.runtime.SubmitGraphImport = func(_ context.Context, _ int64, _, _ string, p contract.GraphImportPlan) (*contract.GraphImportResult, error) {
					r := graphTransferTestResult(p.Snapshot())
					r.CommitConfirmed = false
					r.OutcomeUnknown = true
					r.Identities = nil
					r.NodesCreated = 0
					r.RelationshipsCreated = 0
					return r, context.DeadlineExceeded
				}
				*d.boundRuntime = lease.runtime
			}
			s.previewGraphTransfer(ui.Services{}, d)
			settleDocumentTransfer(t, s, d)
			if d.transfer.GraphPlan == nil {
				t.Fatal("empty namespace lost original preview", d.transfer.Err)
			}
			s.confirmGraphTransfer(ui.Services{}, d)
			if d.transfer.Submitted {
				t.Fatal("namespace preview authorized import without confirmation")
			}
			if mode == "generation" {
				active.Generation++
				s.confirmGraphTransfer(ui.Services{}, d)
				if d.transfer.Submitted || d.transfer.GraphBundle == nil {
					t.Fatal("replacement admitted original namespace write")
				}
				return
			}
			d.transfer.Confirmation = "empty"
			// The fixture receipt must describe precisely this source and direction.
			if mode == "confirmed" {
				d.boundRuntime.SubmitGraphImport = func(_ context.Context, _ int64, _, _ string, p contract.GraphImportPlan) (*contract.GraphImportResult, error) {
					r := graphTransferTestResult(p.Snapshot())
					r.Identities = []contract.GraphImportIdentity{{Kind: "node", SourceID: "new", DestinationID: "created"}, {Kind: "relationship", SourceID: "self", DestinationID: "edge"}}
					return r, nil
				}
			}
			s.confirmGraphTransfer(ui.Services{}, d)
			settleDocumentTransfer(t, s, d)
			if mode == "confirmed" {
				if !d.transfer.Completed || d.Loading() || d.Page() != nil {
					t.Fatal("namespace success requested grid refresh", d.transfer.Err)
				}
			} else {
				if !d.transfer.Uncertain || d.transfer.GraphBundle == nil || d.transfer.GraphSnapshot.NativeJSON == "" || d.edit.Uncertain == nil {
					t.Fatal("namespace unknown import lost source, preview or leave guard")
				}
				s.RequestAction(ui.Services{}, d, "export")
				if d.transfer.Kind != "import" {
					t.Fatal("unknown namespace write discarded original evidence")
				}
			}
		})
	}
}

func TestGraphNamespacePathReadRevokesOldSourceAndCapturesOriginalPath(t *testing.T) {
	s, d, active, _ := graphNamespaceTestState(t)
	s.RequestAction(ui.Services{}, d, "import")
	path := filepath.Join(t.TempDir(), "source.json")
	if e := os.WriteFile(path, []byte(`{"Format":"nyadb.graph.v1","Nodes":[{"ElementID":"new"}]}`), 0600); e != nil {
		t.Fatal(e)
	}
	s.readGraphImportFile(ui.Services{}, d, path)
	d.transfer.GraphSourcePath = "later-unread-file.json"
	active.ProfileID = 99
	settleDocumentTransfer(t, s, d)
	if !d.transfer.Busy || d.transfer.GraphBundle != nil {
		t.Fatal("parked path read replaced original source")
	}
	active.ProfileID = 12
	settleDocumentTransfer(t, s, d)
	if d.transfer.GraphBundle == nil || d.transfer.Path != path || d.transfer.GraphSourcePath != path || len(d.transfer.GraphBundle.Nodes) != 1 {
		t.Fatal("path read followed mutable input", d.transfer.Err)
	}
	s.readGraphImportFile(ui.Services{}, d, path+".missing")
	settleDocumentTransfer(t, s, d)
	if d.transfer.GraphBundle != nil || d.transfer.Path != "" || d.transfer.Err == "" {
		t.Fatal("failed new file retained an actionable old source")
	}
}
