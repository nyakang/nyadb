package localdb

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestRedisOptionsRoundTripAndLegacyCompatibility(t *testing.T) {
	store, _, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.Redis)
	p.Name = "Redis saved"
	p.TLSMode = "verify-full"
	p.RedisOptions.CAFile = "/tmp/ca.crt"
	p.RedisOptions.ServerName = "redis.example"
	p.RedisOptions.MaxConnections = 3
	saved, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, saved.ID)
	if err != nil || got.RedisOptions != saved.RedisOptions {
		t.Fatal("options roundtrip", err)
	}
	if _, err = db.Exec("UPDATE connection_profiles SET mysql_advanced_json='' WHERE id=?", saved.ID); err != nil {
		t.Fatal(err)
	}
	legacy, err := store.Get(ctx, saved.ID)
	if err != nil || legacy.RedisOptions != connections.DefaultRedisOptions() {
		t.Fatal("empty legacy options", err)
	}
	wrong := connections.Profile{Kind: connections.PostgreSQL}
	if decodeEngineOptions(&wrong, `{"version":1,"kind":"postgresql","redis":{"version":1}}`) == nil {
		t.Fatal("cross-engine options accepted")
	}
}
