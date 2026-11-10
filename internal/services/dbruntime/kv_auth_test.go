package dbruntime_test

import (
	"context"
	"slices"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type kvAuthFakeLease struct {
	*kvFakeLease
	methods []string
}

func (l *kvAuthFakeLease) AuthMethods() []string { return l.methods }

type kvAuthBrowserFakeLease struct {
	*kvBrowserFakeLease
	methods []string
}

func (l *kvAuthBrowserFakeLease) AuthMethods() []string { return l.methods }

func TestKVAuthOriginalInventoryPreservesAllOptionalFacetCombinationsAndPureRecovery(t *testing.T) {
	for _, auth := range []bool{false, true} {
		for _, browser := range []bool{false, true} {
			t.Run(map[bool]string{false: "no-auth", true: "auth"}[auth]+"/"+map[bool]string{false: "no-browser", true: "browser"}[browser], func(t *testing.T) {
				m := dbruntime.New()
				defer m.Close()
				h := kvHandleFor(1)
				methods := []string{"/etcdserverpb.Auth/AuthStatus", "/etcdserverpb.Auth/UserAdd"}
				var bases []*kvFakeLease
				h.reserve = func(uint64) (contract.KVRequestLease, error) {
					base := &kvFakeLease{scope: h.scope.Clone()}
					bases = append(bases, base)
					if browser {
						b := &kvBrowserFakeLease{kvFakeLease: base}
						if auth {
							return &kvAuthBrowserFakeLease{b, methods}, nil
						}
						return b, nil
					}
					if auth {
						return &kvAuthFakeLease{base, methods}, nil
					}
					return base, nil
				}
				h.recover = func(old contract.KVRequestLease) (contract.KVRecovery, error) {
					_ = old.Close()
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
				inventory, hasAuth := bound.(contract.KVAuthManagement)
				_, hasBrowser := bound.(contract.KVLeaseBrowsing)
				if hasAuth != auth || hasBrowser != browser {
					t.Fatal("binding invented or dropped original optional facet")
				}
				if hasAuth {
					got := inventory.AuthMethods()
					if !slices.Equal(got, methods) {
						t.Fatal("original method inventory changed")
					}
					got[0] = "foreign"
					if !slices.Equal(inventory.AuthMethods(), methods) {
						t.Fatal("method inventory aliases caller")
					}
				}
				r, err := provider.RecoverKV(bound)
				if err != nil || len(bases) != 2 || bases[0].executes.Load() != 0 || bases[1].executes.Load() != 0 || r.Lease.Scope().Generation != m.Generation(1) {
					t.Fatal("recovery lost ownership, invented generation or performed Auth IO", err)
				}
				_, newAuth := r.Lease.(contract.KVAuthManagement)
				_, newBrowser := r.Lease.(contract.KVLeaseBrowsing)
				if newAuth != auth || newBrowser != browser {
					t.Fatal("recovery lost optional facets")
				}
				_ = r.Lease.Close()
			})
		}
	}
}
