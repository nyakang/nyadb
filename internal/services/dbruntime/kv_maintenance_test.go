package dbruntime_test

import (
	"context"
	"slices"
	"strconv"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

var maintenanceInventory = []string{"/etcdserverpb.KV/Compact"}

type kvMaintenanceFakeLease struct{ *kvFakeLease }
type kvMaintenanceAuthFakeLease struct{ *kvAuthFakeLease }
type kvMaintenanceBrowserFakeLease struct{ *kvBrowserFakeLease }
type kvMaintenanceAuthBrowserFakeLease struct{ *kvAuthBrowserFakeLease }

func (*kvMaintenanceFakeLease) MaintenanceMethods() []string            { return maintenanceInventory }
func (*kvMaintenanceAuthFakeLease) MaintenanceMethods() []string        { return maintenanceInventory }
func (*kvMaintenanceBrowserFakeLease) MaintenanceMethods() []string     { return maintenanceInventory }
func (*kvMaintenanceAuthBrowserFakeLease) MaintenanceMethods() []string { return maintenanceInventory }

func TestKVMaintenanceOriginalSixteenFacetCombinationsAndPureRecovery(t *testing.T) {
	for bits := 0; bits < 16; bits++ {
		t.Run(strconv.Itoa(bits), func(t *testing.T) {
			maintenance, auth, browse := bits&1 != 0, bits&2 != 0, bits&4 != 0
			members := bits&8 != 0
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
				if hasMaintenance != maintenance || hasAuth != auth || hasBrowse != browse || hasMembers != members {
					t.Fatal("binding changed original optional facets")
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
			recovery, err := provider.RecoverKV(bound)
			if err != nil || len(bases) != 2 || bases[0].executes.Load() != 0 || bases[1].executes.Load() != 0 || recovery.Lease.Scope().Generation != m.Generation(1) {
				t.Fatal("recovery performed maintenance or lost original publication", err)
			}
			check(recovery.Lease)
			recovery.Lease.Close()
		})
	}
}
