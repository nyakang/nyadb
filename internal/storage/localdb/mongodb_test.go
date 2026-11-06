package localdb

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMongoDBOptionsRoundTripAndLegacyCompatibility(t *testing.T) {
	store, _, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.MongoDB)
	p.Name = "MongoDB saved"
	p.MongoDBOptions.Seeds = "replica:27018"
	p.MongoDBOptions.ReplicaSet = "rs"
	p.MongoDBOptions.AuthSource = "auth"
	saved, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, saved.ID)
	if err != nil || got.MongoDBOptions != saved.MongoDBOptions {
		t.Fatal("options roundtrip", err)
	}
	if _, err = db.Exec("UPDATE connection_profiles SET mysql_advanced_json='' WHERE id=?", saved.ID); err != nil {
		t.Fatal(err)
	}
	legacy, err := store.Get(ctx, saved.ID)
	if err != nil || legacy.MongoDBOptions != connections.DefaultMongoDBOptions() {
		t.Fatal("empty legacy options", err)
	}
	wrong := connections.Profile{Kind: connections.PostgreSQL}
	if decodeEngineOptions(&wrong, `{"version":1,"kind":"postgresql","mongodb":{"version":1}}`) == nil {
		t.Fatal("cross-engine options accepted")
	}
}
