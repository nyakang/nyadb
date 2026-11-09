package dbruntime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type mutationPreview struct {
	snapshot contract.DataMutationSnapshot
	closed   atomic.Int32
}

func (p *mutationPreview) Snapshot() contract.DataMutationSnapshot { return p.snapshot }
func (p *mutationPreview) Close() error                            { p.closed.Add(1); return nil }

type mutationHandle struct {
	handle
	kinds            []string
	plan             *mutationPreview
	previewed, saved atomic.Int32
	started, release chan struct{}
}

func (h *mutationHandle) Namespace(name string) contract.NamespaceRef {
	return contract.NamespaceRef{ProfileID: h.id, Catalog: name}
}
func (h *mutationHandle) DataRuntime() contract.DataRuntime {
	return contract.DataRuntime{Capabilities: h.caps, PreviewEditKinds: h.kinds,
		Saver: func(context.Context, int64, string, string, []contract.DataEdit) (*contract.DataEditResult, error) {
			return nil, contract.ErrUnsupported
		},
		PreviewEdits: func(_ context.Context, id int64, db, table string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
			h.previewed.Add(1)
			if h.started != nil {
				close(h.started)
				<-h.release
			}
			return h.plan, nil
		},
		SubmitEdits: func(_ context.Context, id int64, db, table string, p contract.DataMutationPlan) (*contract.DataEditResult, error) {
			if p != h.plan {
				return nil, connections.ErrInvalid
			}
			h.saved.Add(1)
			return &contract.DataEditResult{CommitConfirmed: true}, nil
		}}
}

func TestDataMutationRuntimeRejectsForeignPlansAndRetiresLateOriginalPreviews(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	var handles []*mutationHandle
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := &mutationHandle{handle: handle{id: p.ID, caps: contract.Capabilities{EditableTables: true}}, kinds: []string{"insert", "delete"}}
		h.plan = &mutationPreview{snapshot: contract.DataMutationSnapshot{Object: h.Namespace("main").Object("table", "owned"), Edits: []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"bytes": {Bytes: []byte{1}}}}}}}
		handles = append(handles, h)
		return h, nil
	}))
	ctx := context.Background()
	if _, err := m.ConnectSession(ctx, profile(1), ""); err != nil {
		t.Fatal(err)
	}
	r := m.Data(1)
	h := handles[0]
	h.kinds[0] = "changed"
	if r.PreviewEditKinds[0] != "insert" {
		t.Fatal("runtime retained caller-owned mutation modes")
	}
	foreign := h.plan.snapshot.Object
	foreign.ProfileID = 2
	if _, err := r.PreviewEdits(ctx, 1, "main", "owned", []contract.DataEdit{{Kind: "delete", Original: &contract.RowSnapshot{Ref: contract.RowRef{Object: foreign}}}}); !errors.Is(err, connections.ErrInvalid) || h.previewed.Load() != 0 {
		t.Fatal("foreign original row reached preview executor", err)
	}
	p, err := r.PreviewEdits(ctx, 1, "main", "owned", nil)
	if err != nil {
		t.Fatal(err)
	}
	x := p.Snapshot()
	x.Edits[0].Values["bytes"].Bytes[0] = 9
	if p.Snapshot().Edits[0].Values["bytes"].Bytes[0] != 1 {
		t.Fatal("runtime preview accessor exposed retained original bytes")
	}
	if _, err = r.SubmitEdits(ctx, 1, "main", "owned", h.plan); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("raw or caller-created plan bypassed owning runtime binding", err)
	}
	if _, err = r.SubmitEdits(ctx, 1, "main", "other", p); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("plan crossed original object scope", err)
	}
	if _, err = r.SubmitEdits(ctx, 1, "main", "owned", p); err != nil || h.saved.Load() != 1 {
		t.Fatal("original runtime did not unwrap its original adapter plan", err)
	}
	h.started, h.release = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		late, err := r.PreviewEdits(ctx, 1, "main", "owned", nil)
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
	if err = <-done; !errors.Is(err, contract.ErrRevoked) || h.plan.closed.Load() != 1 {
		t.Fatal("late original preview leaked a handle after replacement", err)
	}
	if _, err = r.SubmitEdits(ctx, 1, "main", "owned", p); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("original preview followed replacement session", err)
	}
	if _, err = m.Data(1).SubmitEdits(ctx, 1, "main", "owned", p); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("replacement runtime accepted an original-generation plan", err)
	}
	if h.saved.Load() != 1 || handles[1].saved.Load() != 0 || !m.IsConnected(1) {
		t.Fatal("old preview submission damaged replacement")
	}
}
