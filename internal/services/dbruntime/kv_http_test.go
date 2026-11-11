package dbruntime_test

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

func TestHTTPKVRecoveryRejectsChangedOriginalIdentityBeforeRawRecovery(t *testing.T) {
	identity := contract.KVHTTPIdentity{Protocol: "consul-http", Endpoint: "https://original", Datacenter: "dc1", Auth: "token", Consistency: "consistent", Configuration: "original-configuration", AgentID: "original-agent", AgentName: "original", AgentDatacenter: "dc1", AgentAvailable: true}
	for _, mutate := range []struct {
		name   string
		change func(*contract.KVHTTPIdentity)
	}{
		{"protocol", func(i *contract.KVHTTPIdentity) { i.Protocol = "foreign" }},
		{"endpoint", func(i *contract.KVHTTPIdentity) { i.Endpoint = "https://foreign" }},
		{"datacenter", func(i *contract.KVHTTPIdentity) { i.Datacenter = "foreign" }},
		{"auth", func(i *contract.KVHTTPIdentity) { i.Auth = "none" }},
		{"consistency", func(i *contract.KVHTTPIdentity) { i.Consistency = "stale" }},
		{"configuration", func(i *contract.KVHTTPIdentity) { i.Configuration = "foreign" }},
		{"agent", func(i *contract.KVHTTPIdentity) { i.AgentID = "foreign" }},
		{"availability", func(i *contract.KVHTTPIdentity) { i.AgentAvailable = false }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			m := dbruntime.New()
			defer m.Close()
			var opens, recoveries atomic.Int32
			var original *kvFakeLease
			m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) {
				h := kvHandleFor(1)
				h.scope.Header = contract.KVHeader{}
				h.scope.HTTP = identity
				h.scope.Version = "2.0.2"
				if opens.Add(1) > 1 {
					mutate.change(&h.scope.HTTP)
				}
				h.reserve = func(uint64) (contract.KVRequestLease, error) {
					l := &kvFakeLease{scope: h.scope}
					if original == nil {
						original = l
					}
					return l, nil
				}
				h.recover = func(contract.KVRequestLease) (contract.KVRecovery, error) {
					recoveries.Add(1)
					return contract.KVRecovery{}, nil
				}
				return h, nil
			}))
			if err := m.Connect(context.Background(), profile(1), ""); err != nil {
				t.Fatal(err)
			}
			provider, _ := m.KVRequests(1)
			old, _ := provider.ReserveKV(1)
			if err := m.Connect(context.Background(), profile(1), ""); err != nil {
				t.Fatal(err)
			}
			provider, _ = m.KVRequests(1)
			if _, err := provider.RecoverKV(old); !errors.Is(err, contract.ErrRevoked) || recoveries.Load() != 0 || original.closed.Load() != 0 {
				t.Fatal("foreign HTTP recovery altered original lease", err)
			}
		})
	}
}

func TestHTTPKVBoundResultKeepsNativeValuesAndIndependentUnknownRecovery(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	h := kvHandleFor(1)
	h.scope.Header = contract.KVHeader{}
	h.scope.HTTP = contract.KVHTTPIdentity{Protocol: "consul-http", Endpoint: "http://original"}
	index := uint64(math.MaxUint64)
	raw := contract.KVResult{HTTP: &contract.KVHTTPResult{Body: []byte{0, 255}, Header: map[string][]string{"X-Original": {"kept"}}, Metadata: contract.KVHTTPMetadata{Index: &index}, Entries: []contract.KVHTTPEntry{{Flags: math.MaxUint64, Value: []byte{0, 255}}}}}
	h.reserve = func(uint64) (contract.KVRequestLease, error) {
		return &kvFakeLease{scope: h.scope, execute: func(context.Context, contract.KVPreparation) (contract.KVResult, error) { return raw, nil }}, nil
	}
	h.recover = func(old contract.KVRequestLease) (contract.KVRecovery, error) {
		old.Close()
		fresh, _ := h.reserve(1)
		return contract.KVRecovery{Lease: fresh, Last: raw, Unknown: &raw}, nil
	}
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	provider, _ := m.KVRequests(1)
	l, _ := provider.ReserveKV(1)
	p, _ := l.Prepare(contract.KVRequest{})
	r, err := l.Execute(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	r.HTTP.Body[0] = 9
	r.HTTP.Header["X-Original"][0] = "changed"
	*r.HTTP.Metadata.Index = 1
	r.HTTP.Entries[0].Value[0] = 9
	if raw.HTTP.Body[0] != 0 || raw.HTTP.Header["X-Original"][0] != "kept" || *raw.HTTP.Metadata.Index != math.MaxUint64 || raw.HTTP.Entries[0].Value[0] != 0 {
		t.Fatal("runtime exposed mutable native storage")
	}
	recovery, err := provider.RecoverKV(l)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Lease.Close()
	recovery.Unknown.HTTP.Body[0] = 9
	recovery.Last.HTTP.Body[0] = 9
	if raw.HTTP.Body[0] != 0 || recovery.Lease.Scope().HTTP != h.scope.HTTP {
		t.Fatal("runtime recovery lost HTTP scope or independent bytes")
	}
}
