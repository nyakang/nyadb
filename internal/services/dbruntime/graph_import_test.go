package dbruntime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphimport"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type runtimeGraphPlan struct {
	snapshot contract.GraphImportSnapshot
	closed   atomic.Int32
}

func (p *runtimeGraphPlan) Snapshot() contract.GraphImportSnapshot { return p.snapshot }
func (p *runtimeGraphPlan) Close() error                           { p.closed.Add(1); return nil }

type runtimeGraphHandle struct {
	handle
	plan             *runtimeGraphPlan
	started, release chan struct{}
	submitted        atomic.Int32
}

func (h *runtimeGraphHandle) Namespace(db string) contract.NamespaceRef {
	return contract.NamespaceRef{ProfileID: h.id, Catalog: db}
}
func (h *runtimeGraphHandle) DataRuntime() contract.DataRuntime {
	return contract.DataRuntime{Capabilities: h.caps,
		PreviewGraphImport: func(_ context.Context, _ int64, _, _ string, r contract.GraphImportRequest) (contract.GraphImportPlan, error) {
			if h.started != nil {
				close(h.started)
				<-h.release
			}
			h.plan.snapshot.Request = r
			return h.plan, nil
		},
		SubmitGraphImport: func(_ context.Context, _ int64, _, _ string, p contract.GraphImportPlan) (*contract.GraphImportResult, error) {
			if p != h.plan {
				return nil, connections.ErrInvalid
			}
			h.submitted.Add(1)
			return &contract.GraphImportResult{Object: h.plan.snapshot.Object, CommitConfirmed: true}, nil
		},
		ExportGraph: func(_ context.Context, o contract.ObjectRef) (contract.GraphImportBundle, error) {
			return contract.GraphImportBundle{Format: graphimport.Format, Source: o.Namespace()}, nil
		},
	}
}

func TestGraphImportRuntimeOriginalPlanCopiesScopesAndLateGeneration(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	var handles []*runtimeGraphHandle
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := &runtimeGraphHandle{handle: handle{id: p.ID, caps: contract.Capabilities{Import: true, Export: true}}, plan: &runtimeGraphPlan{snapshot: contract.GraphImportSnapshot{Object: contract.NamespaceRef{ProfileID: p.ID, Catalog: "main"}.Object("label", "owned"), NativeJSON: "{}"}}}
		handles = append(handles, h)
		return h, nil
	}))
	ctx := context.Background()
	if _, err := m.ConnectSession(ctx, profile(1), ""); err != nil {
		t.Fatal(err)
	}
	r, h := m.Data(1), handles[0]
	request := contract.GraphImportRequest{Bundle: contract.GraphImportBundle{Format: graphimport.Format, Nodes: []contract.GraphNode{{ElementID: "a", Labels: []string{"owned"}}}}}
	p, err := r.PreviewGraphImport(ctx, 1, "main", "owned", request)
	if err != nil {
		t.Fatal(err)
	}
	request.Bundle.Nodes[0].Labels[0] = "caller changed"
	copy := p.Snapshot()
	copy.Request.Bundle.Nodes[0].Labels[0] = "UI changed"
	if p.Snapshot().Request.Bundle.Nodes[0].Labels[0] != "owned" {
		t.Fatal("runtime retained mutable caller/adapter snapshot")
	}
	for _, foreign := range []contract.GraphImportPlan{h.plan, &runtimeGraphPlan{}} {
		if _, err = r.SubmitGraphImport(ctx, 1, "main", "owned", foreign); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("raw foreign graph plan authorized", err)
		}
	}
	if _, err = r.SubmitGraphImport(ctx, 1, "main", "other", p); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("graph plan crossed original object", err)
	}
	if _, err = r.SubmitGraphImport(ctx, 1, "main", "owned", p); err != nil || h.submitted.Load() != 1 {
		t.Fatal("original graph binding not unwrapped", err)
	}
	if _, err = r.ExportGraph(ctx, contract.NamespaceRef{ProfileID: 2, Catalog: "main"}.Object("label", "owned")); err == nil {
		t.Fatal("exchange crossed original profile")
	}
	if _, err = r.ExportGraph(ctx, h.plan.snapshot.Object); err != nil {
		t.Fatal("original graph export rejected", err)
	}
	h.started, h.release = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		late, err := r.PreviewGraphImport(ctx, 1, "main", "owned", request)
		if late != nil {
			_ = late.Close()
		}
		done <- err
	}()
	<-h.started
	if _, err = m.ConnectSession(ctx, profile(1), ""); err != nil {
		t.Fatal(err)
	}
	close(h.release)
	if err = <-done; !errors.Is(err, contract.ErrRevoked) || h.plan.closed.Load() != 1 || handles[1].plan.closed.Load() != 0 {
		t.Fatal("late graph preview affected replacement generation", err)
	}
	if _, err = m.Data(1).SubmitGraphImport(ctx, 1, "main", "owned", p); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("replacement admitted original plan", err)
	}
}

func TestGraphImportCapabilityRequiresCompleteOriginalPreviewAndSubmit(t *testing.T) {
	r := contract.DataRuntime{Capabilities: contract.Capabilities{Import: true}}
	if contract.BoundDataCapabilities(r).Import {
		t.Fatal("unbound graph import enabled")
	}
	r.PreviewGraphImport = func(context.Context, int64, string, string, contract.GraphImportRequest) (contract.GraphImportPlan, error) {
		return nil, nil
	}
	if contract.BoundDataCapabilities(r).Import {
		t.Fatal("preview-only graph import enabled")
	}
	r.SubmitGraphImport = func(context.Context, int64, string, string, contract.GraphImportPlan) (*contract.GraphImportResult, error) {
		return nil, nil
	}
	if !contract.BoundDataCapabilities(r).Import {
		t.Fatal("complete graph import binding disabled")
	}
}

func TestGraphNamespaceCapabilityRequiresCompleteGraphBinding(t *testing.T) {
	r := contract.DataRuntime{Capabilities: contract.Capabilities{GraphExchange: true, Import: true, Export: true}}
	if contract.BoundDataCapabilities(r).GraphExchange {
		t.Fatal("unbound graph namespace enabled")
	}
	r.PreviewGraphImport = func(context.Context, int64, string, string, contract.GraphImportRequest) (contract.GraphImportPlan, error) {
		return nil, nil
	}
	r.SubmitGraphImport = func(context.Context, int64, string, string, contract.GraphImportPlan) (*contract.GraphImportResult, error) {
		return nil, nil
	}
	if contract.BoundDataCapabilities(r).GraphExchange {
		t.Fatal("import-only namespace graph exchange enabled")
	}
	r.ExportGraph = func(context.Context, contract.ObjectRef) (contract.GraphImportBundle, error) {
		return contract.GraphImportBundle{}, nil
	}
	if !contract.BoundDataCapabilities(r).GraphExchange {
		t.Fatal("complete original namespace exchange disabled")
	}
	if contract.IntersectCapabilities(r.Capabilities, contract.Capabilities{Import: true, Export: true}).GraphExchange {
		t.Fatal("namespace capability bypassed product intersection")
	}
}
