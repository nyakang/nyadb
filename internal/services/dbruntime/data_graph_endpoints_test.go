package dbruntime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type graphEndpointHandle struct {
	mutationHandle
	endpoints        *contract.DataGraphEndpoints
	reads            atomic.Int32
	entered, unblock chan struct{}
}

func (h *graphEndpointHandle) DataRuntime() contract.DataRuntime {
	r := h.mutationHandle.DataRuntime()
	r.ReadGraphEndpoints = func(context.Context, int64, string, string, string, string) (*contract.DataGraphEndpoints, error) {
		h.reads.Add(1)
		if h.entered != nil {
			close(h.entered)
			<-h.unblock
		}
		return h.endpoints, nil
	}
	r.Saver = func(context.Context, int64, string, string, []contract.DataEdit) (*contract.DataEditResult, error) {
		h.saved.Add(1)
		return &contract.DataEditResult{CommitConfirmed: true}, nil
	}
	r.PreviewEdits = func(_ context.Context, _ int64, _, _ string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
		h.previewed.Add(1)
		h.plan = &mutationPreview{snapshot: contract.DataMutationSnapshot{Object: h.endpoints.Object, Edits: edits}}
		return h.plan, nil
	}
	return r
}

func TestDataGraphEndpointsBindOriginalGenerationQualifiedObjectAndPreviewPayload(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	var handles []*graphEndpointHandle
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := &graphEndpointHandle{mutationHandle: mutationHandle{handle: handle{id: p.ID, caps: contract.Capabilities{EditableTables: true}}}}
		h.endpoints = &contract.DataGraphEndpoints{Object: h.Namespace("main").Object("relationship", "owned"), Source: contract.GraphNode{ElementID: "source", Labels: []string{"original"}}, Target: contract.GraphNode{ElementID: "target"}, Token: "native-original"}
		handles = append(handles, h)
		return h, nil
	}))
	ctx := context.Background()
	if _, err := m.ConnectSession(ctx, profile(1), ""); err != nil {
		t.Fatal(err)
	}
	r, original := m.Data(1), handles[0]
	if _, err := r.ReadGraphEndpoints(ctx, 2, "main", "owned", "source", "target"); !errors.Is(err, connections.ErrInvalid) || original.reads.Load() != 0 {
		t.Fatal("endpoint read crossed profiles", err)
	}
	for _, scope := range [][2]string{{"foreign", "owned"}, {"main", "other"}} {
		if _, err := r.ReadGraphEndpoints(ctx, 1, scope[0], scope[1], "source", "target"); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("foreign endpoint observation returned", err)
		}
	}
	e, err := r.ReadGraphEndpoints(ctx, 1, "main", "owned", "source", "target")
	if err != nil {
		t.Fatal(err)
	}
	original.endpoints.Source.Labels[0] = "provider-mutated"
	if e.Source.Labels[0] != "original" {
		t.Fatal("public endpoint read exposed provider-owned labels")
	}
	edit := contract.DataEdit{Kind: "insert", GraphEndpoints: e}
	if _, err := r.Saver(ctx, 1, "main", "owned", []contract.DataEdit{edit}); !errors.Is(err, contract.ErrUnsupported) || original.saved.Load() != 0 {
		t.Fatal("endpoint creation bypassed one-attempt preview", err)
	}
	plan, err := r.PreviewEdits(ctx, 1, "main", "owned", []contract.DataEdit{edit})
	if err != nil {
		t.Fatal(err)
	}
	e.Source.Labels[0] = "caller-mutated"
	if plan.Snapshot().Edits[0].GraphEndpoints.Source.Labels[0] != "original" {
		t.Fatal("preview adopted later mutable endpoint input")
	}
	_ = plan.Close()
	foreign := contract.CloneDataGraphEndpoints(e)
	foreign.Object.Catalog = "foreign"
	before := original.previewed.Load()
	if _, err := r.PreviewEdits(ctx, 1, "main", "owned", []contract.DataEdit{{Kind: "insert", GraphEndpoints: foreign}}); !errors.Is(err, connections.ErrInvalid) || original.previewed.Load() != before {
		t.Fatal("foreign endpoint creation dispatched", err)
	}
	original.entered, original.unblock = make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() { _, err := r.ReadGraphEndpoints(ctx, 1, "main", "owned", "source", "target"); finished <- err }()
	select {
	case <-original.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("original endpoint reader did not enter")
	}
	if _, err := m.ConnectSession(ctx, profile(1), ""); err != nil {
		t.Fatal(err)
	}
	close(original.unblock)
	select {
	case err := <-finished:
		if !errors.Is(err, contract.ErrRevoked) {
			t.Fatal("late original endpoints adopted replacement generation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original endpoint reader did not settle")
	}
}

func TestDataMutationWithoutEndpointFeatureRejectsGraphInsertBeforeDispatch(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	h := &mutationHandle{handle: handle{id: 1, caps: contract.Capabilities{EditableTables: true}}}
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
	if _, err := m.ConnectSession(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	e := &contract.DataGraphEndpoints{Object: h.Namespace("main").Object("relationship", "owned")}
	if _, err := m.Data(1).PreviewEdits(context.Background(), 1, "main", "owned", []contract.DataEdit{{Kind: "insert", GraphEndpoints: e}}); !errors.Is(err, contract.ErrUnsupported) || h.previewed.Load() != 0 {
		t.Fatal("unsupported provider silently ignored relationship endpoints", err)
	}
}
