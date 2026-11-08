package meilisearch

import (
	"context"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestMeilisearchNativeKeysConfiguredIndexScopeAndLeaseLimit(t *testing.T) {
	cfg, secret := nativeFixture(t)
	c := nativeOpen(t)
	cfg.IndexUID = nativeIndex(t, c)
	s := testSession(t, cfg, secret)
	global := s.Namespace("").Object("keys", "keys")
	qualified := s.Namespace(cfg.IndexUID).Object("keys", "keys")
	for _, o := range []contract.ObjectRef{global, qualified} {
		if s.SupportsKeysDocument(o) {
			t.Fatal("configured index scope expanded into global key management")
		}
		if _, err := s.ReserveKeysDocument(o); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal("scoped profile used administrator fallback", err)
		}
	}
	cfg.IndexUID = ""
	fresh := testSession(t, cfg, secret)
	leases := make([]contract.KeysDocumentLease, 0, 32)
	defer func() {
		for _, lease := range leases {
			_ = lease.Close()
		}
	}()
	for i := 0; i < 32; i++ {
		lease, err := fresh.ReserveKeysDocument(fresh.Namespace("").Object("keys", "keys"))
		if err != nil {
			t.Fatal("original bounded key reservation", err)
		}
		leases = append(leases, lease)
	}
	if _, err := fresh.ReserveKeysDocument(global); !errors.Is(err, contract.ErrSQLSessionLimit) {
		t.Fatal("key documents bypassed physical executor limit", err)
	}
	_ = leases[0].Close()
	if lease, err := fresh.ReserveKeysDocument(global); err != nil {
		t.Fatal("key close failed to release executor slot", err)
	} else {
		_ = lease.Close()
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := leases[1].List(context.Background(), 0, 20); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old key executor followed session close", err)
	}
	t.Log("explicit IndexUID blocks global API key administration even with original administrator credential; 32 physical reservations shared with REST/tasks, idempotent close releases only original slot")
}
