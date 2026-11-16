package dbruntime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/zookeeper"
)

func TestZooKeeperBuiltinProviderRequiresExplicitPinnedDriver(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	dbruntime.RegisterBuiltins(m, nil)
	p, _ := connections.Defaults(connections.ZooKeeper)
	p.ID, p.Name = 7, "original"
	if err := m.Connect(context.Background(), p, ""); !errors.Is(err, zookeeper.ErrInvalid) {
		t.Fatal("builtin native provider missing or implicit package selected", err)
	}
	if _, err := m.Session(p.ID); err == nil {
		t.Fatal("failed builtin open published a session")
	}
}

func TestZooKeeperRuntimeRejectsForeignIdentityOnReservationAndRecovery(t *testing.T) {
	changes := []struct {
		name  string
		apply func(*contract.KVZNodeIdentity)
	}{
		{"protocol", func(z *contract.KVZNodeIdentity) { z.Protocol = "other" }},
		{"ensemble", func(z *contract.KVZNodeIdentity) { z.ConnectString = "other:2181/app" }},
		{"root", func(z *contract.KVZNodeIdentity) { z.Root = "/other" }},
		{"username", func(z *contract.KVZNodeIdentity) { z.Username = "other" }},
		{"auth", func(z *contract.KVZNodeIdentity) { z.AuthScheme = "sasl_digest" }},
		{"configuration", func(z *contract.KVZNodeIdentity) { z.Configuration = "other" }},
		{"driver-version", func(z *contract.KVZNodeIdentity) { z.DriverVersion = "other" }},
		{"driver-hash", func(z *contract.KVZNodeIdentity) { z.DriverSHA256 = "other" }},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			m := dbruntime.New()
			defer m.Close()
			h := kvHandleFor(7)
			h.caps = contract.Capabilities{KeyValues: true, Export: true}
			h.scope.Header = contract.KVHeader{}
			h.scope.ZNode = contract.KVZNodeIdentity{Protocol: "zookeeper-v1", ConnectString: "zk-1:2181/app", Root: "/app/ns", Username: "ops", AuthScheme: "digest", Configuration: "original", DriverVersion: "native-1", DriverSHA256: "original-sha"}
			var captured *kvFakeLease
			h.reserve = func(uint64) (contract.KVRequestLease, error) {
				captured = &kvFakeLease{scope: h.scope.Clone()}
				return captured, nil
			}
			recoverCalls := 0
			h.recover = func(contract.KVRequestLease) (contract.KVRecovery, error) {
				recoverCalls++
				return contract.KVRecovery{}, nil
			}
			m.Register(connections.ZooKeeper, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
			p, _ := connections.Defaults(connections.ZooKeeper)
			p.ID, p.Name = 7, "original"
			if err := m.Connect(context.Background(), p, ""); err != nil {
				t.Fatal(err)
			}
			provider, err := m.KVRequests(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			published, err := m.Session(p.ID)
			if err != nil || published.Capabilities() != h.caps {
				t.Fatal("bound native receipt export capability was lost", err)
			}
			original, err := provider.ReserveKV(1)
			if err != nil || original.Scope().ZNode != h.scope.ZNode || original.Scope().Generation != m.Generation(p.ID) {
				t.Fatal("native identity or published generation lost", err)
			}
			defer original.Close()
			originalRaw := captured
			change.apply(&h.scope.ZNode)
			if _, err := provider.RecoverKV(original); !errors.Is(err, contract.ErrRevoked) || recoverCalls != 0 || originalRaw.closed.Load() != 0 {
				t.Fatal("incompatible recovery reached provider or closed original", err)
			}
			h.reserve = func(uint64) (contract.KVRequestLease, error) {
				captured = &kvFakeLease{scope: h.scope.Clone()}
				captured.scope.ZNode = original.Scope().ZNode
				return captured, nil
			}
			if _, err := provider.ReserveKV(2); !errors.Is(err, contract.ErrRevoked) || captured.closed.Load() != 1 || originalRaw.closed.Load() != 0 {
				t.Fatal("foreign reservation admitted or cleanup reached original", err)
			}
		})
	}
}

func TestZooKeeperRuntimeDoesNotExportWithoutNativeRequestProvider(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	m.Register(connections.ZooKeeper, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) {
		return &handle{id: 7, caps: contract.Capabilities{KeyValues: true, Export: true}}, nil
	}))
	p, _ := connections.Defaults(connections.ZooKeeper)
	p.ID, p.Name = 7, "missing features"
	s, err := m.ConnectSession(context.Background(), p, "")
	if err != nil || s.Capabilities().Export {
		t.Fatal("native export advertised without a request provider", err)
	}
}
