package dbruntime_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type fakeMemberFacet struct {
	base     *kvFakeLease
	document uint64
	sequence atomic.Uint64
	execute  func(contract.KVMemberScope) (contract.KVMemberResult, error)
	last     *fakeMemberLease
}
type fakeMemberObservation struct{ directory contract.KVMemberDirectory }

func (o *fakeMemberObservation) Snapshot() contract.KVMemberDirectory { return o.directory.Clone() }
func (*fakeMemberObservation) Close() error                           { return nil }
func (f *fakeMemberFacet) ObserveMembers(context.Context) (contract.KVMemberObservation, error) {
	return &fakeMemberObservation{contract.KVMemberDirectory{Scope: f.base.Scope(), DocumentID: f.document, Result: contract.KVResult{Completed: true, NativeCaptured: true, Native: []byte{0, 255}}, Members: []contract.KVMember{{ID: 18446744073709551615, Name: "original", ClientURLs: []string{"http://original:2379"}}}}}, nil
}
func (f *fakeMemberFacet) BindMember(_ context.Context, o contract.KVMemberObservation, t contract.KVMemberTarget) (contract.KVMemberBinding, error) {
	d := o.Snapshot()
	s := contract.KVMemberScope{Original: f.base.Scope(), DocumentID: f.document, LeaseID: f.sequence.Add(1), Directory: d, Member: d.Members[0].Clone(), Endpoint: t.Endpoint, Version: t.Version}
	f.last = &fakeMemberLease{scope: s, execute: f.execute}
	return contract.KVMemberBinding{Lease: f.last, Scope: s.Clone()}, nil
}

type fakeMemberLease struct {
	scope    contract.KVMemberScope
	executes atomic.Int32
	closed   atomic.Bool
	execute  func(contract.KVMemberScope) (contract.KVMemberResult, error)
	last     contract.KVMemberResult
}

func (l *fakeMemberLease) Scope() contract.KVMemberScope { return l.scope.Clone() }
func (l *fakeMemberLease) CheckMemberLease() error {
	if l.closed.Load() {
		return contract.ErrRevoked
	}
	return nil
}
func (l *fakeMemberLease) ObserveStatus(context.Context) (contract.KVMemberResult, error) {
	return contract.KVMemberResult{Scope: l.Scope(), Sequence: 1, Result: contract.KVResult{Completed: true, NativeCaptured: true, Native: []byte{7}}}, nil
}

type fakeMemberPreparation struct{}

func (*fakeMemberPreparation) Snapshot() contract.KVRequest {
	return contract.KVRequest{Method: "/etcdserverpb.Maintenance/Defragment", JSON: "{}"}
}
func (*fakeMemberPreparation) Writes() bool { return true }
func (*fakeMemberLease) PrepareDefragment() (contract.KVMemberPreparation, error) {
	return &fakeMemberPreparation{}, nil
}
func (l *fakeMemberLease) ExecuteMember(context.Context, contract.KVMemberPreparation) (contract.KVMemberResult, error) {
	l.executes.Add(1)
	var err error
	if l.execute != nil {
		l.last, err = l.execute(l.Scope())
	} else {
		l.last = contract.KVMemberResult{Scope: l.Scope(), Sequence: 2, Result: contract.KVResult{Request: (&fakeMemberPreparation{}).Snapshot(), Completed: true, Attempted: true, NativeCaptured: true}}
	}
	return l.last.Clone(), err
}
func (l *fakeMemberLease) LastMemberResult() contract.KVMemberResult { return l.last.Clone() }
func (l *fakeMemberLease) Close() error                              { l.closed.Store(true); return nil }

type fakeMembersLease struct {
	*kvFakeLease
	*fakeMemberFacet
}
type fakeMembersAuthLease struct {
	*kvAuthFakeLease
	*fakeMemberFacet
}
type fakeMembersBrowseLease struct {
	*kvBrowserFakeLease
	*fakeMemberFacet
}
type fakeMembersAuthBrowseLease struct {
	*kvAuthBrowserFakeLease
	*fakeMemberFacet
}
type fakeMembersMaintenanceLease struct {
	*kvMaintenanceFakeLease
	*fakeMemberFacet
}
type fakeMembersMaintenanceAuthLease struct {
	*kvMaintenanceAuthFakeLease
	*fakeMemberFacet
}
type fakeMembersMaintenanceBrowseLease struct {
	*kvMaintenanceBrowserFakeLease
	*fakeMemberFacet
}
type fakeMembersMaintenanceAuthBrowseLease struct {
	*kvMaintenanceAuthBrowserFakeLease
	*fakeMemberFacet
}

func withFakeMembers(l contract.KVRequestLease, f *fakeMemberFacet) contract.KVRequestLease {
	switch b := l.(type) {
	case *kvFakeLease:
		return &fakeMembersLease{b, f}
	case *kvAuthFakeLease:
		return &fakeMembersAuthLease{b, f}
	case *kvBrowserFakeLease:
		return &fakeMembersBrowseLease{b, f}
	case *kvAuthBrowserFakeLease:
		return &fakeMembersAuthBrowseLease{b, f}
	case *kvMaintenanceFakeLease:
		return &fakeMembersMaintenanceLease{b, f}
	case *kvMaintenanceAuthFakeLease:
		return &fakeMembersMaintenanceAuthLease{b, f}
	case *kvMaintenanceBrowserFakeLease:
		return &fakeMembersMaintenanceBrowseLease{b, f}
	case *kvMaintenanceAuthBrowserFakeLease:
		return &fakeMembersMaintenanceAuthBrowseLease{b, f}
	}
	panic("missing fake facet combination")
}

func TestKVMembersRuntimeOpaqueAuthorityOriginalDocumentAndLateRecoveryGeneration(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	var facets []*fakeMemberFacet
	var recoveryEvidence contract.KVRecovery
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := kvHandleFor(p.ID)
		h.reserve = func(document uint64) (contract.KVRequestLease, error) {
			b := &kvFakeLease{scope: h.KVScope()}
			f := &fakeMemberFacet{base: b, document: document}
			facets = append(facets, f)
			return withFakeMembers(b, f), nil
		}
		h.recover = func(old contract.KVRequestLease) (contract.KVRecovery, error) {
			old.Close()
			fresh, _ := h.reserve(37)
			result := recoveryEvidence
			result.Lease = fresh
			return result, nil
		}
		return h, nil
	}))
	// Make the original public generation differ from both raw generation 1
	// and the publication used by recovery.
	for i := 0; i < 2; i++ {
		if err := m.Connect(context.Background(), profile(1), ""); err != nil {
			t.Fatal(err)
		}
	}
	provider, _ := m.KVRequests(1)
	old, _ := provider.ReserveKV(37)
	defer old.Close()
	f := old.(contract.KVMembers)
	o, err := f.ObserveMembers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	d := o.Snapshot()
	if d.DocumentID != 37 || d.Scope.Generation != old.Scope().Generation || d.Scope.Generation == 1 {
		t.Fatal("directory lost original publication", d.Scope)
	}
	copied := reflect.New(reflect.TypeOf(o).Elem())
	copied.Elem().Set(reflect.ValueOf(o).Elem())
	target := contract.KVMemberTarget{MemberID: 18446744073709551615, Endpoint: "http://alias:2379", Version: "3.5.35"}
	if _, err := f.BindMember(context.Background(), copied.Interface().(contract.KVMemberObservation), target); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("copied runtime observation acquired authority", err)
	}
	if err := copied.Interface().(contract.KVMemberObservation).Close(); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("copied observation closed original", err)
	}
	foreign, _ := provider.ReserveKV(38)
	defer foreign.Close()
	if _, err := foreign.(contract.KVMembers).BindMember(context.Background(), o, target); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("foreign document observation accepted", err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	facets[0].execute = func(s contract.KVMemberScope) (contract.KVMemberResult, error) {
		close(started)
		<-release
		r := contract.KVMemberResult{Scope: s, Sequence: 2, DirectoryCheck: s.Directory.Clone(), Before: contract.KVResult{Native: []byte{0, 255}, NativeCaptured: true, Completed: true, Header: contract.KVHeader{MemberID: 99}}, Result: contract.KVResult{Request: (&fakeMemberPreparation{}).Snapshot(), Attempted: true, WriteOutcomeUnknown: true, Native: []byte{8}, Header: contract.KVHeader{MemberID: 99}}, Error: "missing original ACK"}
		recoveryEvidence = contract.KVRecovery{MemberResults: []contract.KVMemberResult{r.Clone()}}
		return r, errors.New("missing original ACK")
	}
	b, err := f.BindMember(context.Background(), o, target)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Lease.Close()
	p, err := b.Lease.PrepareDefragment()
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		r   contract.KVMemberResult
		err error
	}
	done := make(chan outcome, 1)
	go func() { r, err := b.Lease.ExecuteMember(context.Background(), p); done <- outcome{r, err} }()
	kvAwait(t, started)
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	close(release)
	got := <-done
	if !errors.Is(got.err, contract.ErrRevoked) || !got.r.Result.WriteOutcomeUnknown || got.r.Scope.Original.Generation != old.Scope().Generation || got.r.DirectoryCheck.Scope.Generation != old.Scope().Generation || got.r.Before.Header.MemberID != 99 {
		t.Fatal("late original receipt retagged/lost", got.err, got.r.Scope.Original)
	}
	fresh, _ := m.KVRequests(1)
	recovered, err := fresh.RecoverKV(old)
	if err != nil || recovered.Lease == nil || len(recovered.MemberResults) != 1 {
		t.Fatal("pure original recovery", err)
	}
	defer recovered.Lease.Close()
	r := recovered.MemberResults[0]
	if r.Scope.Original.Generation != old.Scope().Generation || r.Scope.Directory.Scope.Generation != old.Scope().Generation || r.DirectoryCheck.Scope.Generation != old.Scope().Generation || recovered.Lease.Scope().Generation <= old.Scope().Generation || recoveryEvidence.MemberResults[0].Scope.Original.Generation != 1 || facets[0].last.executes.Load() != 1 {
		t.Fatal("recovery mutated provider/old publication or replayed")
	}
	r.Before.Native[0] = 11
	if recoveryEvidence.MemberResults[0].Before.Native[0] != 0 {
		t.Fatal("recovery evidence aliases raw")
	}
	newO, err := recovered.Lease.(contract.KVMembers).ObserveMembers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer newO.Close()
	if newO.Snapshot().DocumentID != 37 {
		t.Fatal("recovery lost original document ID")
	}
}
