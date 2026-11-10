package dbruntime_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type kvBrowserFakeLease struct {
	*kvFakeLease
	reads  atomic.Int32
	browse func(context.Context, contract.KVLeasePageRequest) (contract.KVLeasePage, error)
	detail func(context.Context, contract.KVLeaseDetailRequest) (contract.KVLeaseDetail, error)
}

func (l *kvBrowserFakeLease) BrowseLeases(ctx context.Context, r contract.KVLeasePageRequest) (contract.KVLeasePage, error) {
	l.reads.Add(1)
	if l.browse != nil {
		return l.browse(ctx, r)
	}
	return contract.KVLeasePage{Scope: l.Scope(), Request: r.Clone(), List: contract.KVResult{Attempted: true, Completed: true, Native: []byte{1, 2}}, Complete: true}, nil
}
func (l *kvBrowserFakeLease) ObserveLease(ctx context.Context, r contract.KVLeaseDetailRequest) (contract.KVLeaseDetail, error) {
	l.reads.Add(1)
	if l.detail != nil {
		return l.detail(ctx, r)
	}
	return contract.KVLeaseDetail{Scope: l.Scope(), Request: r, Result: contract.KVResult{Attempted: true, Completed: true, Native: []byte{3, 4}}}, nil
}

func TestKVLeaseBrowserOptionalFacetOriginalPublishedGenerationAndPureRecovery(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	var leases []*kvBrowserFakeLease
	var opens atomic.Int32
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		opens.Add(1)
		h := kvHandleFor(p.ID)
		h.reserve = func(uint64) (contract.KVRequestLease, error) {
			l := &kvBrowserFakeLease{kvFakeLease: &kvFakeLease{scope: h.scope.Clone()}}
			leases = append(leases, l)
			return l, nil
		}
		h.recover = func(old contract.KVRequestLease) (contract.KVRecovery, error) {
			_ = old.Close()
			fresh, _ := h.reserve(1)
			return contract.KVRecovery{Lease: fresh, Last: contract.KVResult{WriteOutcomeUnknown: true, Native: []byte{7, 8}}}, nil
		}
		return h, nil
	}))
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	p, _ := m.KVRequests(1)
	old, _ := p.ReserveKV(1)
	browser, ok := old.(contract.KVLeaseBrowsing)
	if !ok || leases[0].reads.Load() != 0 {
		t.Fatal("native facet lost or pure reserve performed a read")
	}
	page, err := browser.BrowseLeases(context.Background(), contract.KVLeasePageRequest{Limit: 1})
	if err != nil || page.Scope.Generation != m.Generation(1) || !page.Complete || !page.List.Completed {
		t.Fatal("published original page changed", err)
	}
	page.List.Native[0] = 9
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	freshProvider, _ := m.KVRequests(1)
	recovery, err := freshProvider.RecoverKV(old)
	if err != nil || !recovery.Last.WriteOutcomeUnknown || recovery.Lease.Scope().Generation != m.Generation(1) || leases[1].reads.Load() != 0 || opens.Load() != 2 {
		t.Fatal("browser recovery changed evidence, publication or performed a read", err)
	}
	if _, ok := recovery.Lease.(contract.KVLeaseBrowsing); !ok {
		t.Fatal("fresh lease lost optional facet")
	}
	if _, err := browser.ObserveLease(context.Background(), contract.KVLeaseDetailRequest{ID: 7}); !errors.Is(err, contract.ErrRevoked) || leases[0].reads.Load() != 1 {
		t.Fatal("old original facet dispatched after replacement", err)
	}
	if err := recovery.Lease.Close(); err != nil {
		t.Fatal(err)
	}
	_ = recovery.Lease.Close()
	if leases[1].closed.Load() != 1 {
		t.Fatal("browser close did not preserve original idempotent ownership")
	}
}

func TestKVLeaseBrowserLateOriginalReceiptsKeepOriginalPublishedScope(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	h := kvHandleFor(1)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	l := &kvBrowserFakeLease{kvFakeLease: &kvFakeLease{scope: h.scope.Clone()}}
	l.browse = func(_ context.Context, r contract.KVLeasePageRequest) (contract.KVLeasePage, error) {
		close(started)
		<-release
		return contract.KVLeasePage{Scope: h.scope.Clone(), Request: r.Clone(), List: contract.KVResult{Completed: true, Native: []byte{0, 255}}, Complete: true}, nil
	}
	h.reserve = func(uint64) (contract.KVRequestLease, error) { return l, nil }
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	p, _ := m.KVRequests(1)
	original, _ := p.ReserveKV(1)
	generation := original.Scope().Generation
	type completion struct {
		page contract.KVLeasePage
		err  error
	}
	out := make(chan completion, 1)
	go func() {
		page, err := original.(contract.KVLeaseBrowsing).BrowseLeases(context.Background(), contract.KVLeasePageRequest{Limit: 1})
		out <- completion{page, err}
	}()
	kvAwait(t, started)
	if err := m.Disconnect(1); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	got := <-out
	if !errors.Is(got.err, contract.ErrRevoked) || !got.page.Complete || !got.page.List.Completed || got.page.Scope.Generation != generation || got.page.List.Native[1] != 255 {
		t.Fatal("original late receipt erased or relabeled", got.err)
	}
}

func TestKVLeaseBrowserRejectsForeignOriginalScopeAndChangedRequest(t *testing.T) {
	for _, change := range []string{"namespace", "generation", "prefix", "header", "request"} {
		t.Run(change, func(t *testing.T) {
			m := dbruntime.New()
			defer m.Close()
			h := kvHandleFor(1)
			l := &kvBrowserFakeLease{kvFakeLease: &kvFakeLease{scope: h.scope.Clone()}}
			l.browse = func(_ context.Context, r contract.KVLeasePageRequest) (contract.KVLeasePage, error) {
				page := contract.KVLeasePage{Scope: h.scope.Clone(), Request: r.Clone(), List: contract.KVResult{Completed: true, Native: []byte{1}}, Complete: true}
				switch change {
				case "namespace":
					page.Scope.Namespace.ProfileID = 2
				case "generation":
					page.Scope.Generation = 9
				case "prefix":
					page.Scope.Prefix = []byte("foreign")
				case "header":
					page.Scope.Header.MemberID = 9
				case "request":
					page.Request.Limit++
				}
				return page, nil
			}
			h.reserve = func(uint64) (contract.KVRequestLease, error) { return l, nil }
			m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
			if err := m.Connect(context.Background(), profile(1), ""); err != nil {
				t.Fatal(err)
			}
			p, _ := m.KVRequests(1)
			lease, _ := p.ReserveKV(1)
			page, err := lease.(contract.KVLeaseBrowsing).BrowseLeases(context.Background(), contract.KVLeasePageRequest{Limit: 1})
			if !errors.Is(err, contract.ErrRevoked) || len(page.List.Native) != 1 {
				t.Fatal("foreign original page accepted or original bytes erased", err)
			}
		})
	}
}

func TestKVLeaseBrowserAvailabilityFollowsOriginalRawFacet(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	h := kvHandleFor(1)
	l := &kvFakeLease{scope: h.scope.Clone()}
	h.reserve = func(uint64) (contract.KVRequestLease, error) { return l, nil }
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	p, err := m.KVRequests(1)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := p.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lease.(contract.KVLeaseBrowsing); ok {
		t.Fatal("runtime published browsing for an unsupported raw lease")
	}
}
