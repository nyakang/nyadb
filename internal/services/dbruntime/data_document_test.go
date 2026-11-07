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

type describedDocumentHandle struct {
	documentHandle
	features     contract.Capabilities
	reservations atomic.Int32
}

func (h *describedDocumentHandle) DataDocumentCapabilities() contract.Capabilities { return h.features }
func (h *describedDocumentHandle) ReserveDataDocument(o contract.ObjectRef) (contract.DataDocumentLease, error) {
	h.reservations.Add(1)
	return h.documentHandle.ReserveDataDocument(o)
}
func TestDocumentFeaturesAreLazyAndReservedFunctionsConstrainAdvertisement(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	caps := contract.Capabilities{Documents: true, EditableTables: true, Count: true, FieldChunks: true, Export: true, JSONL: true, Keyset: true}
	h := &describedDocumentHandle{documentHandle: documentHandle{handle: handle{id: 1, caps: caps}, lease: &documentLease{}}, features: caps}
	m.Register(connections.Elasticsearch, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
	p, _ := connections.Defaults(connections.Elasticsearch)
	p.ID = 1
	p.Name = "document capability test"
	s, err := m.ConnectSession(context.Background(), p, "")
	if err != nil {
		t.Fatal(err)
	}
	if c := s.Capabilities(); !c.EditableTables || !c.Count || !c.Export || !c.Keyset || c.Transactions {
		t.Fatal("document advertisement lost", c)
	}
	r := m.Data(1)
	if h.reservations.Load() != 0 || r.Saver != nil || r.AdvancedLoader != nil {
		t.Fatal("capability discovery opened an unowned document")
	}
	l, err := r.ReserveDocument(m.Namespace(1, "cluster").Object("index", "docs"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	bound := l.Runtime()
	if c := bound.Capabilities; c.EditableTables || c.Count || c.Export || c.JSONL || c.FieldChunks || c.Keyset {
		t.Fatal("missing bound executors were advertised", c)
	}
	if h.reservations.Load() != 1 {
		t.Fatal("binding reserved another resource")
	}
	h.features = contract.Capabilities{}
	if c := s.Capabilities(); c.EditableTables || c.Count || c.Export {
		t.Fatal("missing feature declaration was accepted", c)
	}
}
