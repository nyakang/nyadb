package localdb

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestPrestoSQLPinnedOptionsRestartCloneAndRejectForeignOrSecretFields(t *testing.T) {
	store, path, db := openStore(t)
	p, _ := connections.Defaults(connections.PrestoSQL)
	p.Name = "original PrestoSQL"
	p.Database = `under_"quote`
	p.Username = " original user "
	p.PrestoSQLOptions.DriverVersion = "original-350"
	p.PrestoSQLOptions.DriverSHA256 = strings.Repeat("b", 64)
	p.PrestoSQLOptions.DriverDirectory = t.TempDir()
	p.PrestoSQLOptions.MaxQueryProcesses = 2
	p.PrestoSQLOptions.DefaultSchema = " schema "
	p.PrestoSQLOptions.JDBCParameters = "source=public"
	saved, err := store.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	store = NewProfileStore(reopened)
	got, err := store.Get(context.Background(), saved.ID)
	if err != nil || got.PrestoSQLOptions != p.PrestoSQLOptions || got.Database != p.Database || got.Username != p.Username {
		t.Fatal("restart changed original settings", err)
	}
	clone, err := connections.NewService(store).Duplicate(context.Background(), saved.ID, "clone")
	if err != nil || clone.ID == saved.ID || clone.PrestoSQLOptions != got.PrestoSQLOptions {
		t.Fatal("clone changed driver pin", err)
	}
	valid, err := encodeProfileOptions(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"", `{"version":1,"kind":"prestosql","prestosql":null}`, strings.Replace(valid, `"driver_version":`, `"password":"secret","driver_version":`, 1), strings.Replace(valid, `"prestosql":`, `"trino":`, 1), strings.Replace(valid, `"version":1`, `"version":1,"Version":1`, 1)} {
		original := got
		if err := decodeEngineOptions(&original, raw); err == nil {
			t.Fatal("empty/secret/foreign/duplicate saved envelope accepted")
		}
	}
	var encoded map[string]json.RawMessage
	if json.Unmarshal([]byte(valid), &encoded) != nil {
		t.Fatal("canonical public envelope missing")
	}
	foreign, _ := connections.Defaults(connections.MySQL)
	if err := decodeEngineOptions(&foreign, valid); err == nil {
		t.Fatal("PrestoSQL options migrated into another engine")
	}
}
