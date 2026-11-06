package localdb

import (
	"context"
	"github.com/nyakang/nyadb/internal/services/connections"
	"reflect"
	"testing"
)

func TestOracleOptionsPersistAndRejectCrossEngineEnvelopes(t *testing.T) {
	store, _, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.Oracle)
	p.Name = "Oracle saved"
	p.Username = "tenant"
	p.OracleOptions.ConnectionType = "sid"
	p.OracleOptions.MaxConnections = 3
	p.OracleOptions.SQLMaxConnections = 5
	saved, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, saved.ID)
	if err != nil || !reflect.DeepEqual(got.OracleOptions, saved.OracleOptions) {
		t.Fatal(got, err)
	}
	duplicate, err := connections.NewService(store).Duplicate(ctx, saved.ID, "duplicate")
	if err != nil || duplicate.OracleOptions != saved.OracleOptions {
		t.Fatal(duplicate, err)
	}
	if _, err = db.Exec("UPDATE connection_profiles SET mysql_advanced_json='' WHERE id=?", saved.ID); err != nil {
		t.Fatal(err)
	}
	legacy, err := store.Get(ctx, saved.ID)
	if err != nil || legacy.OracleOptions.MaxConnections != 8 {
		t.Fatal(legacy, err)
	}
	wrong := connections.Profile{Kind: connections.PostgreSQL}
	if err = decodeEngineOptions(&wrong, `{"version":1,"kind":"postgresql","oracle":{"version":1}}`); err == nil {
		t.Fatal("Oracle envelope accepted for PostgreSQL")
	}
}
