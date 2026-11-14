package localdb

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSelectDBProfileRestartAndForeignEnvelopeRejection(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.SelectDB)
	p.Name, p.Database = "SelectDB persisted", "warehouse"
	p.SelectDBOptions.Catalog = "lake`quoted"
	p.SelectDBOptions.CAFile = filepath.Join(t.TempDir(), "ca.pem")
	p.SelectDBOptions.ServerName = "server"
	saved, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	store = NewProfileStore(reopened)
	got, err := store.Get(ctx, saved.ID)
	if err != nil || got.Kind != connections.SelectDB || got.Database != p.Database || got.SelectDBOptions != p.SelectDBOptions || got.DorisOptions != (connections.DorisOptions{}) {
		t.Fatal("SelectDB restart changed original product or catalog")
	}
	for _, raw := range []string{
		`{"version":1,"kind":"selectdb","selectdb":{"version":1,"catalog":"internal"},"doris":{}}`,
		`{"version":1,"kind":"selectdb","selectdb":{"version":1,"catalog":"internal"},"consul":null}`,
		`{"version":1,"kind":"selectdb","selectdb":{"version":1,"catalog":"internal","catalog":"lake"}}`,
		`{"version":1,"kind":"selectdb","selectdb":{"version":1,"catalog":"internal"},"password":"private"}`,
		`{"version":1,"kind":"doris","selectdb":{"version":1,"catalog":"internal"}}`,
	} {
		if _, err = reopened.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", raw, saved.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = store.Get(ctx, saved.ID); err == nil {
			t.Fatal("foreign or ambiguous SelectDB envelope accepted")
		}
	}
	other, _ := connections.Defaults(connections.Doris)
	other.Name = "Doris"
	other, err = store.Create(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"version":1,"kind":"doris","doris":{"version":1,"catalog":"internal"},"selectdb":{"version":1,"catalog":"internal"}}`
	if _, err = reopened.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", raw, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(ctx, other.ID); err == nil {
		t.Fatal("SelectDB envelope accepted on Doris")
	}
}
