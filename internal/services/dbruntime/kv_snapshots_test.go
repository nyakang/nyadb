package dbruntime_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type fakeSnapshotFacet struct {
	base     *kvFakeLease
	document uint64
	calls    atomic.Int32
	last     contract.KVSnapshotResult
	execute  func(context.Context, contract.KVSnapshotPreparation, contract.KVSnapshotOutputs) (contract.KVSnapshotResult, error)
}
type fakeSnapshotPlan struct {
	request contract.KVSnapshotRequest
	scope   contract.KVSnapshotScope
	used    atomic.Bool
}

func (p *fakeSnapshotPlan) Snapshot() contract.KVSnapshotRequest { return p.request }
func (p *fakeSnapshotPlan) Scope() contract.KVSnapshotScope      { return p.scope.Clone() }
func (f *fakeSnapshotFacet) PrepareSnapshot(r contract.KVSnapshotRequest) (contract.KVSnapshotPreparation, error) {
	return &fakeSnapshotPlan{request: r, scope: contract.KVSnapshotScope{Original: f.base.Scope(), DocumentID: f.document}}, nil
}
func (f *fakeSnapshotFacet) ExecuteSnapshot(ctx context.Context, p contract.KVSnapshotPreparation, o contract.KVSnapshotOutputs) (contract.KVSnapshotResult, error) {
	native := p.(*fakeSnapshotPlan)
	if !native.used.CompareAndSwap(false, true) {
		return contract.KVSnapshotResult{}, contract.ErrKVPlanUsed
	}
	f.calls.Add(1)
	if f.execute != nil {
		r, e := f.execute(ctx, p, o)
		f.last = r.Clone()
		return r, e
	}
	body := []byte{0, 255, 31}
	o.Snapshot.Write(body)
	o.Journal.Write([]byte{1, 255})
	f.last = contract.KVSnapshotResult{Scope: p.Scope(), Request: p.Snapshot(), Attempted: true, Ended: true, Completed: true, SourceComplete: true, EOF: true, ChecksumMatched: true, OutputAcknowledged: true, JournalComplete: true, SnapshotBytesWritten: 3, JournalBytesWritten: 2, Checksum: []byte{0, 255}, Before: contract.KVResult{NativeCaptured: true, Native: []byte{0, 255}, Header: f.base.scope.Header}}
	return f.last.Clone(), nil
}
func (f *fakeSnapshotFacet) LastSnapshotResult() contract.KVSnapshotResult { return f.last.Clone() }

type snapshotKvFakeLease struct {
	*kvFakeLease
	*fakeSnapshotFacet
}
type snapshotKvAuthFakeLease struct {
	*kvAuthFakeLease
	*fakeSnapshotFacet
}
type snapshotKvBrowserFakeLease struct {
	*kvBrowserFakeLease
	*fakeSnapshotFacet
}
type snapshotKvAuthBrowserFakeLease struct {
	*kvAuthBrowserFakeLease
	*fakeSnapshotFacet
}
type snapshotKvMaintenanceFakeLease struct {
	*kvMaintenanceFakeLease
	*fakeSnapshotFacet
}
type snapshotKvMaintenanceAuthFakeLease struct {
	*kvMaintenanceAuthFakeLease
	*fakeSnapshotFacet
}
type snapshotKvMaintenanceBrowserFakeLease struct {
	*kvMaintenanceBrowserFakeLease
	*fakeSnapshotFacet
}
type snapshotKvMaintenanceAuthBrowserFakeLease struct {
	*kvMaintenanceAuthBrowserFakeLease
	*fakeSnapshotFacet
}
type snapshotFakeMembersLease struct {
	*fakeMembersLease
	*fakeSnapshotFacet
}
type snapshotFakeMembersAuthLease struct {
	*fakeMembersAuthLease
	*fakeSnapshotFacet
}
type snapshotFakeMembersBrowseLease struct {
	*fakeMembersBrowseLease
	*fakeSnapshotFacet
}
type snapshotFakeMembersAuthBrowseLease struct {
	*fakeMembersAuthBrowseLease
	*fakeSnapshotFacet
}
type snapshotFakeMembersMaintenanceLease struct {
	*fakeMembersMaintenanceLease
	*fakeSnapshotFacet
}
type snapshotFakeMembersMaintenanceAuthLease struct {
	*fakeMembersMaintenanceAuthLease
	*fakeSnapshotFacet
}
type snapshotFakeMembersMaintenanceBrowseLease struct {
	*fakeMembersMaintenanceBrowseLease
	*fakeSnapshotFacet
}
type snapshotFakeMembersMaintenanceAuthBrowseLease struct {
	*fakeMembersMaintenanceAuthBrowseLease
	*fakeSnapshotFacet
}

func withFakeSnapshot(l contract.KVRequestLease, f *fakeSnapshotFacet) contract.KVRequestLease {
	switch b := l.(type) {
	case *kvFakeLease:
		return &snapshotKvFakeLease{b, f}
	case *kvAuthFakeLease:
		return &snapshotKvAuthFakeLease{b, f}
	case *kvBrowserFakeLease:
		return &snapshotKvBrowserFakeLease{b, f}
	case *kvAuthBrowserFakeLease:
		return &snapshotKvAuthBrowserFakeLease{b, f}
	case *kvMaintenanceFakeLease:
		return &snapshotKvMaintenanceFakeLease{b, f}
	case *kvMaintenanceAuthFakeLease:
		return &snapshotKvMaintenanceAuthFakeLease{b, f}
	case *kvMaintenanceBrowserFakeLease:
		return &snapshotKvMaintenanceBrowserFakeLease{b, f}
	case *kvMaintenanceAuthBrowserFakeLease:
		return &snapshotKvMaintenanceAuthBrowserFakeLease{b, f}
	case *fakeMembersLease:
		return &snapshotFakeMembersLease{b, f}
	case *fakeMembersAuthLease:
		return &snapshotFakeMembersAuthLease{b, f}
	case *fakeMembersBrowseLease:
		return &snapshotFakeMembersBrowseLease{b, f}
	case *fakeMembersAuthBrowseLease:
		return &snapshotFakeMembersAuthBrowseLease{b, f}
	case *fakeMembersMaintenanceLease:
		return &snapshotFakeMembersMaintenanceLease{b, f}
	case *fakeMembersMaintenanceAuthLease:
		return &snapshotFakeMembersMaintenanceAuthLease{b, f}
	case *fakeMembersMaintenanceBrowseLease:
		return &snapshotFakeMembersMaintenanceBrowseLease{b, f}
	case *fakeMembersMaintenanceAuthBrowseLease:
		return &snapshotFakeMembersMaintenanceAuthBrowseLease{b, f}
	}
	panic("missing fake snapshot combination")
}
func TestKVSnapshotOriginalThirtyTwoFacetCombinationsAndPureRecovery(t *testing.T) {
	for bits := 0; bits < 32; bits++ {
		t.Run(strconv.Itoa(bits), func(t *testing.T) {
			maintenance, auth, browse := bits&1 != 0, bits&2 != 0, bits&4 != 0
			members := bits&8 != 0
			snapshots := bits&16 != 0
			var snapshotFacets []*fakeSnapshotFacet
			m := dbruntime.New()
			defer m.Close()
			h := kvHandleFor(1)
			var bases []*kvFakeLease
			var originals []contract.KVRequestLease
			authMethods := []string{"/etcdserverpb.Auth/AuthStatus"}
			h.reserve = func(document uint64) (contract.KVRequestLease, error) {
				base := &kvFakeLease{scope: h.scope.Clone()}
				bases = append(bases, base)
				var lease contract.KVRequestLease = base
				if browse {
					b := &kvBrowserFakeLease{kvFakeLease: base}
					if auth {
						a := &kvAuthBrowserFakeLease{b, authMethods}
						lease = a
						if maintenance {
							lease = &kvMaintenanceAuthBrowserFakeLease{a}
						}
					} else {
						lease = b
						if maintenance {
							lease = &kvMaintenanceBrowserFakeLease{b}
						}
					}
				} else if auth {
					a := &kvAuthFakeLease{base, authMethods}
					lease = a
					if maintenance {
						lease = &kvMaintenanceAuthFakeLease{a}
					}
				} else if maintenance {
					lease = &kvMaintenanceFakeLease{base}
				}
				if members {
					lease = withFakeMembers(lease, &fakeMemberFacet{base: base, document: document})
				}
				if snapshots {
					f := &fakeSnapshotFacet{base: base, document: document}
					snapshotFacets = append(snapshotFacets, f)
					lease = withFakeSnapshot(lease, f)
				}
				originals = append(originals, lease)
				return lease, nil
			}
			h.recover = func(previous contract.KVRequestLease) (contract.KVRecovery, error) {
				if previous != originals[0] {
					t.Fatal("recovery unwrapped another raw facet/lease")
				}
				previous.Close()
				fresh, _ := h.reserve(1)
				return contract.KVRecovery{Lease: fresh}, nil
			}
			m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
			if err := m.Connect(context.Background(), profile(1), ""); err != nil {
				t.Fatal(err)
			}
			provider, _ := m.KVRequests(1)
			bound, err := provider.ReserveKV(1)
			if err != nil {
				t.Fatal(err)
			}
			check := func(lease contract.KVRequestLease) {
				inventory, hasMaintenance := lease.(contract.KVMaintenance)
				_, hasAuth := lease.(contract.KVAuthManagement)
				_, hasBrowse := lease.(contract.KVLeaseBrowsing)
				memberFacet, hasMembers := lease.(contract.KVMembers)
				snapshotFacet, hasSnapshots := lease.(contract.KVSnapshots)
				if hasMaintenance != maintenance || hasAuth != auth || hasBrowse != browse || hasMembers != members || hasSnapshots != snapshots {
					t.Fatal("binding changed original optional facets")
				}

				if hasSnapshots {
					req := contract.KVSnapshotRequest{BodyBytes: 1<<53 + 1, JournalBytes: 1<<54 + 1, Frames: 3}
					plan, e := snapshotFacet.PrepareSnapshot(req)
					if e != nil || plan.Snapshot() != req || plan.Scope().Original.Generation != m.Generation(1) || plan.Scope().DocumentID != 1 {
						t.Fatal("original snapshot plan lost scope", e)
					}
					var body, journal bytes.Buffer
					result, e := snapshotFacet.ExecuteSnapshot(context.Background(), plan, contract.KVSnapshotOutputs{Snapshot: &body, Journal: &journal})
					if e != nil || !result.Completed || result.Scope.Original.Generation != m.Generation(1) || result.Scope.DocumentID != 1 || !bytes.Equal(body.Bytes(), []byte{0, 255, 31}) || !bytes.Equal(journal.Bytes(), []byte{1, 255}) {
						t.Fatal("original snapshot mapping changed", e)
					}
					result.Checksum[0] = 9
					result.Scope.Original.Prefix[0] = 9
					if snapshotFacet.LastSnapshotResult().Checksum[0] != 0 || snapshotFacet.LastSnapshotResult().Scope.Original.Prefix[0] != 0 {
						t.Fatal("snapshot receipt aliases caller")
					}
					if _, e := snapshotFacet.ExecuteSnapshot(context.Background(), plan, contract.KVSnapshotOutputs{Snapshot: &body, Journal: &journal}); !errors.Is(e, contract.ErrKVPlanUsed) {
						t.Fatal("snapshot plan replayed")
					}
				}
				if hasMembers {
					o, err := memberFacet.ObserveMembers(context.Background())
					if err != nil || o.Snapshot().DocumentID != 1 {
						t.Fatal("members facet lost original document", err)
					}
					o.Close()
				}
				if hasMaintenance {
					got := inventory.MaintenanceMethods()
					if !slices.Equal(got, maintenanceInventory) {
						t.Fatal("original maintenance inventory changed")
					}
					got[0] = "foreign"
					if !slices.Equal(inventory.MaintenanceMethods(), maintenanceInventory) {
						t.Fatal("maintenance inventory aliases caller")
					}
				}
			}
			check(bound)
			if snapshots && snapshotFacets[0].calls.Load() != 1 {
				t.Fatal("original snapshot dispatched unexpectedly")
			}
			recovery, err := provider.RecoverKV(bound)
			if err != nil || len(bases) != 2 || bases[0].executes.Load() != 0 || bases[1].executes.Load() != 0 || recovery.Lease.Scope().Generation != m.Generation(1) {
				t.Fatal("recovery performed maintenance or lost original publication", err)
			}
			if snapshots && (len(snapshotFacets) != 2 || snapshotFacets[0].calls.Load() != 1 || snapshotFacets[1].calls.Load() != 0) {
				t.Fatal("snapshot recovery performed IO")
			}
			check(recovery.Lease)
			recovery.Lease.Close()
		})
	}
}

func TestKVSnapshotWrapperRejectsCopiedAndForeignPlansBeforeRawDispatch(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	h := kvHandleFor(1)
	var facets []*fakeSnapshotFacet
	h.reserve = func(id uint64) (contract.KVRequestLease, error) {
		b := &kvFakeLease{scope: h.scope.Clone()}
		f := &fakeSnapshotFacet{base: b, document: id}
		facets = append(facets, f)
		return withFakeSnapshot(b, f), nil
	}
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
	if e := m.Connect(context.Background(), profile(1), ""); e != nil {
		t.Fatal(e)
	}
	provider, _ := m.KVRequests(1)
	original, _ := provider.ReserveKV(1)
	other, _ := provider.ReserveKV(2)
	f := original.(contract.KVSnapshots)
	plan, _ := f.PrepareSnapshot(contract.KVSnapshotRequest{BodyBytes: 100, JournalBytes: 1000, Frames: 10})
	copied := reflect.New(reflect.TypeOf(plan).Elem())
	copied.Elem().Set(reflect.ValueOf(plan).Elem())
	var body, journal bytes.Buffer
	outputs := contract.KVSnapshotOutputs{Snapshot: &body, Journal: &journal}
	if _, e := f.ExecuteSnapshot(context.Background(), copied.Interface().(contract.KVSnapshotPreparation), outputs); !errors.Is(e, contract.ErrKVPlanUsed) {
		t.Fatal("reflection copy gained snapshot authority")
	}
	if _, e := other.(contract.KVSnapshots).ExecuteSnapshot(context.Background(), plan, outputs); !errors.Is(e, contract.ErrKVPlanUsed) {
		t.Fatal("foreign snapshot plan dispatched")
	}
	if facets[0].calls.Load() != 0 || facets[1].calls.Load() != 0 {
		t.Fatal("rejected plan reached raw executor")
	}
	if _, e := f.ExecuteSnapshot(context.Background(), plan, outputs); e != nil {
		t.Fatal("rejected copies consumed original plan", e)
	}
}

func TestKVSnapshotLateOriginalCompletionAndRecoveryRetainOriginalPublication(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	var facets []*fakeSnapshotFacet
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) {
		h := kvHandleFor(1)
		h.reserve = func(id uint64) (contract.KVRequestLease, error) {
			b := &kvFakeLease{scope: h.scope.Clone()}
			f := &fakeSnapshotFacet{base: b, document: id}
			facets = append(facets, f)
			return withFakeSnapshot(b, f), nil
		}
		h.recover = func(old contract.KVRequestLease) (contract.KVRecovery, error) {
			r := old.(contract.KVSnapshots).LastSnapshotResult()
			old.Close()
			fresh, _ := h.reserve(37)
			return contract.KVRecovery{Lease: fresh, Last: contract.KVResult{WriteOutcomeUnknown: true, Native: []byte{0, 255}}, SnapshotResult: &r}, nil
		}
		return h, nil
	}))
	for i := 0; i < 2; i++ {
		if e := m.Connect(context.Background(), profile(1), ""); e != nil {
			t.Fatal(e)
		}
	}
	provider, _ := m.KVRequests(1)
	old, _ := provider.ReserveKV(37)
	defer old.Close()
	f := old.(contract.KVSnapshots)
	plan, _ := f.PrepareSnapshot(contract.KVSnapshotRequest{BodyBytes: 100, JournalBytes: 1000, Frames: 10})
	started, release := make(chan struct{}), make(chan struct{})
	facets[0].execute = func(_ context.Context, p contract.KVSnapshotPreparation, o contract.KVSnapshotOutputs) (contract.KVSnapshotResult, error) {
		close(started)
		<-release
		o.Snapshot.Write([]byte{0, 255})
		return contract.KVSnapshotResult{Scope: p.Scope(), Request: p.Snapshot(), Attempted: true, Ended: true, Completed: true, Checksum: []byte{0, 255}, NativeStatus: []byte{1, 255}}, nil
	}
	var body, journal bytes.Buffer
	type completed struct {
		r contract.KVSnapshotResult
		e error
	}
	done := make(chan completed, 1)
	go func() {
		r, e := f.ExecuteSnapshot(context.Background(), plan, contract.KVSnapshotOutputs{Snapshot: &body, Journal: &journal})
		done <- completed{r, e}
	}()
	kvAwait(t, started)
	if e := m.Connect(context.Background(), profile(1), ""); e != nil {
		t.Fatal(e)
	}
	freshProvider, _ := m.KVRequests(1)
	close(release)
	r := <-done
	if !errors.Is(r.e, contract.ErrRevoked) || !r.r.Completed || r.r.Scope.Original.Generation != old.Scope().Generation || r.r.Scope.Original.Generation == freshProvider.KVScope().Generation || r.r.Scope.DocumentID != 37 || !bytes.Equal(body.Bytes(), []byte{0, 255}) {
		t.Fatal("late original snapshot lost actual receipt or borrowed replacement scope", r.e)
	}
	recovery, e := freshProvider.RecoverKV(old)
	if e != nil || recovery.SnapshotResult == nil || recovery.SnapshotResult.Scope.Original.Generation != old.Scope().Generation || recovery.Lease.Scope().Generation != m.Generation(1) || !recovery.Last.WriteOutcomeUnknown || len(facets) != 2 || facets[0].calls.Load() != 1 || facets[1].calls.Load() != 0 {
		t.Fatal("snapshot recovery performed IO or rewrote original publication", e)
	}
	defer recovery.Lease.Close()
	if _, e := recovery.Lease.(contract.KVSnapshots).ExecuteSnapshot(context.Background(), plan, contract.KVSnapshotOutputs{Snapshot: &body, Journal: &journal}); !errors.Is(e, contract.ErrKVPlanUsed) {
		t.Fatal("recovery replayed old original Snapshot")
	}
}
