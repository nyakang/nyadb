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

type documentHandle struct {
	handle
	lease *documentLease
}
type documentLease struct{ reads, closed atomic.Int32 }

func (h *documentHandle) ReserveDataDocument(contract.ObjectRef) (contract.DataDocumentLease, error) {
	return h.lease, nil
}
func (h *documentHandle) Namespace(name string) contract.NamespaceRef {
	return contract.NamespaceRef{ProfileID: h.id, Catalog: name}
}
func (l *documentLease) Runtime() contract.DataRuntime {
	return contract.DataRuntime{Capabilities: contract.Capabilities{Documents: true, EditableTables: true}, AdvancedLoader: func(context.Context, int64, string, string, contract.DataOptions) (*contract.DataPage, error) {
		l.reads.Add(1)
		return &contract.DataPage{}, nil
	}}
}
func (l *documentLease) Close() error { l.closed.Add(1); return nil }

func TestDataDocumentRuntimeCannotRebindToReplacementAndCloseKeepsOriginalLease(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	var handles []*documentHandle
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := &documentHandle{handle: handle{id: p.ID}, lease: &documentLease{}}
		handles = append(handles, h)
		return h, nil
	}))
	first, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil {
		t.Fatal(err)
	}
	o := m.Namespace(1, "main").Object("table", "docs")
	r := m.Data(1)
	foreign := o
	foreign.ProfileID = 2
	if _, err = r.ReserveDocument(foreign); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign profile admitted", err)
	}
	l, err := r.ReserveDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	bound := l.Runtime()
	if bound.ReserveDocument != nil || bound.Capabilities.Documents || bound.Capabilities.EditableTables {
		t.Fatal("lease bypassed public feature intersection")
	}
	if _, err = bound.AdvancedLoader(context.Background(), 1, "main", "docs", contract.DataOptions{Object: o}); err != nil {
		t.Fatal(err)
	}
	second, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation() <= first.Generation() {
		t.Fatal("replacement lost generation")
	}
	if _, err = bound.AdvancedLoader(context.Background(), 1, "main", "docs", contract.DataOptions{Object: o}); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old document followed replacement", err)
	}
	if _, err = r.ReserveDocument(o); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old reservation followed replacement", err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if handles[0].lease.closed.Load() != 1 || handles[0].lease.reads.Load() != 1 || handles[1].lease.closed.Load() != 0 || !m.IsConnected(1) {
		t.Fatal("old Close damaged replacement or leaked resource")
	}
}
