package dbruntime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type keysHandle struct {
	tasksHandle
	keys *keysLease
}
type keysLease struct{ calls, closed int }

func (h *keysHandle) SupportsKeysDocument(o contract.ObjectRef) bool {
	return o.ProfileID == h.id && o.Kind == "keys" && o.Catalog == "" && o.Schema == ""
}
func (h *keysHandle) ReserveKeysDocument(contract.ObjectRef) (contract.KeysDocumentLease, error) {
	return h.keys, nil
}
func (h *keysHandle) RecoverKeysDocument(_ contract.ObjectRef, old contract.KeysDocumentLease) (contract.KeyRecovery, error) {
	_ = old.Close()
	return contract.KeyRecovery{Lease: h.keys, Last: contract.KeyResult{State: "uncertain"}, Uncertain: true}, nil
}
func (l *keysLease) List(context.Context, uint64, uint64) (contract.KeyPage, error) {
	l.calls++
	return contract.KeyPage{}, nil
}
func (l *keysLease) Read(_ context.Context, r contract.KeyRecord) (contract.KeyRecord, error) {
	l.calls++
	r.Actions[0] = "changed"
	return r, nil
}
func (l *keysLease) Reveal(r contract.KeyRecord) (string, error) {
	l.calls++
	r.Indexes[0] = "changed"
	return "temporary original secret", nil
}
func (l *keysLease) Prepare(_ string, r contract.KeyRecord, d contract.KeyDraft) (contract.KeyPlan, error) {
	l.calls++
	r.Actions[0], d.Actions[0] = "changed", "changed"
	return contract.KeyPlan{}, nil
}
func (l *keysLease) Apply(_ context.Context, p contract.KeyPlan) (contract.KeyResult, error) {
	l.calls++
	p.Original.Actions[0], p.Draft.Indexes[0] = "changed", "changed"
	return contract.KeyResult{State: "completed", Status: 200}, nil
}
func (l *keysLease) Close() error { l.closed++; return nil }

func TestKeysPublishedGenerationAndOriginalRecoveryPreserveInputs(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	var handles []*keysHandle
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := &keysHandle{tasksHandle: tasksHandle{handle: handle{id: p.ID, caps: contract.Capabilities{Metadata: true}}}, keys: &keysLease{}}
		handles = append(handles, h)
		return h, nil
	}))
	first, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.KeysDocuments(1)
	if err != nil {
		t.Fatal(err)
	}
	o := contract.ObjectRef{ProfileID: 1, Kind: "keys", Name: "keys"}
	foreign := o
	foreign.ProfileID = 2
	if p.SupportsKeysDocument(foreign) {
		t.Fatal("foreign profile key scope published")
	}
	if _, err = p.ReserveKeysDocument(foreign); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign profile key reservation published", err)
	}
	l, err := p.ReserveKeysDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	r := contract.KeyRecord{Actions: []string{"search"}, Indexes: []string{"owned*"}}
	d := contract.KeyDraft{Actions: []string{"search"}, Indexes: []string{"owned*"}}
	if _, err = l.List(context.Background(), 0, 20); err != nil {
		t.Fatal(err)
	}
	_, _ = l.Read(context.Background(), r)
	_, _ = l.Reveal(r)
	_, _ = l.Prepare("update", r, d)
	_, _ = l.Apply(context.Background(), contract.KeyPlan{Original: r, Draft: d})
	if r.Actions[0] != "search" || r.Indexes[0] != "owned*" || d.Actions[0] != "search" || d.Indexes[0] != "owned*" {
		t.Fatal("adapter mutated caller's captured key payload")
	}
	second, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil || second.Generation() <= first.Generation() {
		t.Fatal(err)
	}
	if p.SupportsKeysDocument(o) {
		t.Fatal("original key provider followed replacement")
	}
	for _, operation := range []func() error{
		func() error { _, e := p.ReserveKeysDocument(o); return e },
		func() error { _, e := l.List(context.Background(), 0, 20); return e },
		func() error { _, e := l.Read(context.Background(), r); return e },
		func() error { _, e := l.Reveal(r); return e },
		func() error { _, e := l.Prepare("update", r, d); return e },
		func() error { _, e := l.Apply(context.Background(), contract.KeyPlan{}); return e },
	} {
		if err = operation(); !errors.Is(err, contract.ErrRevoked) {
			t.Fatal("original key operation followed replacement", err)
		}
	}
	next, err := m.KeysDocuments(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = next.RecoverKeysDocument(foreign, l); err == nil {
		t.Fatal("key recovery expanded original scope")
	}
	recovery, err := next.RecoverKeysDocument(o, l)
	if err != nil || !recovery.Uncertain || recovery.Last.State != "uncertain" || handles[0].keys.calls != 5 || handles[0].keys.closed != 1 || handles[1].keys.calls != 0 || handles[1].keys.closed != 0 {
		t.Fatal("recovery lost uncertainty, performed I/O or closed replacement", err)
	}
	_ = recovery.Lease.Close()
}
