package localdb

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func savedDamengProfile(t *testing.T) connections.Profile {
	t.Helper()
	p, _ := connections.Defaults(connections.Dameng)
	p.Name, p.Database, p.Username = "DM8 persisted", " Catalog ", " User "
	p.DamengOptions.LibraryFile = filepath.Join(t.TempDir(), "libdmdpi.so")
	p.DamengOptions.DefaultSchema = " Schema ' \" "
	p.DamengOptions.CAFile = filepath.Join(t.TempDir(), "ca.pem")
	p.DamengOptions.CertificateFile = filepath.Join(t.TempDir(), "client.pem")
	p.DamengOptions.KeyFile = filepath.Join(t.TempDir(), "client.key")
	p.DamengOptions.ServerName = "dm.example"
	p.DamengOptions.MaxConnections, p.DamengOptions.SQLMaxConnections = 2, 3
	return p
}

func TestDamengRestartDuplicateUpdateAndLegacyCompatibility(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	p := savedDamengProfile(t)
	saved, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	service := connections.NewService(store)
	copy, err := service.Duplicate(ctx, saved.ID, "DM8 copy")
	if err != nil || copy.ID == saved.ID || copy.DamengOptions != saved.DamengOptions || copy.Username != saved.Username || copy.Database != saved.Database {
		t.Fatal("duplicate changed original native settings", err)
	}
	copy.DamengOptions.DefaultSchema, copy.DamengOptions.SQLMaxConnections = "Other schema", 4
	copy, err = service.Update(ctx, copy)
	if err != nil {
		t.Fatal(err)
	}
	invalid := copy
	invalid.DamengOptions.LibraryFile = "relative.so"
	if _, err = service.Update(ctx, invalid); err == nil {
		t.Fatal("invalid update accepted")
	}
	for _, kind := range []connections.Kind{connections.MySQL, connections.PostgreSQL, connections.SQLite} {
		legacy, _ := connections.Defaults(kind)
		legacy.Name = "legacy " + string(kind)
		if kind == connections.SQLite {
			legacy.Database = filepath.Join(t.TempDir(), "user.sqlite")
		}
		old, err := store.Create(ctx, legacy)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec("UPDATE connection_profiles SET mysql_advanced_json='' WHERE id=?", old.ID); err != nil {
			t.Fatal(err)
		}
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
	if err != nil || !reflect.DeepEqual(got, saved) {
		t.Fatal("restart lost original profile identity or native settings", err)
	}
	got, err = store.Get(ctx, copy.ID)
	if err != nil || !reflect.DeepEqual(got, copy) {
		t.Fatal("failed update altered saved settings", err)
	}
	profiles, err := store.List(ctx)
	if err != nil || len(profiles) != 5 {
		t.Fatal("legacy profiles failed to decode", err)
	}
	for _, profile := range profiles {
		if profile.Kind == connections.PostgreSQL && profile.PostgreSQLOptions != connections.DefaultPostgreSQLOptions() || profile.Kind == connections.MySQL && profile.MySQLAdvanced != connections.DefaultMySQLAdvanced() || profile.Kind == connections.SQLite && profile.SQLiteOptions.Version != 1 {
			t.Fatal("legacy defaults changed")
		}
	}
}

func TestDamengStoredOptionsRejectMissingCorruptAndForeignConfiguration(t *testing.T) {
	p := savedDamengProfile(t)
	raw, err := encodeProfileOptions(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, corrupt := range []string{
		"", "{}", "null", raw + "{}",
		strings.Replace(raw, `"kind":"dameng"`, `"kind":"oracle"`, 1),
		strings.Replace(raw, `"version":1`, `"version":2`, 1),
		strings.Replace(raw, `"dameng":{`, `"dameng":null,"ignored":{`, 1),
		strings.TrimSuffix(raw, "}") + `,"postgresql":{}}`,
		strings.TrimSuffix(raw, "}") + `,"password":"private"}`,
	} {
		got := p
		if err = decodeEngineOptions(&got, corrupt); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("corrupt/foreign native options accepted or echoed")
		}
	}
	for _, kind := range []connections.Kind{connections.PostgreSQL, connections.Kingbase, connections.SQLite, connections.Oracle, connections.Redis, connections.ClickHouse} {
		foreign, _ := connections.Defaults(kind)
		foreignRaw, err := encodeProfileOptions(foreign)
		if err != nil {
			t.Fatal(err)
		}
		foreignRaw = strings.TrimSuffix(foreignRaw, "}") + `,"dameng":{}}`
		if err = decodeEngineOptions(&foreign, foreignRaw); err == nil {
			t.Fatal("DM8 saved options accepted by another engine", kind)
		}
	}
}

func TestDamengURIStorageNeverContainsTemporaryCredentials(t *testing.T) {
	store, path, db := openStore(t)
	p := savedDamengProfile(t)
	q := url.Values{"library_file": {p.DamengOptions.LibraryFile}, "sslmode": {p.TLSMode}, "schema": {p.DamengOptions.DefaultSchema}, "ca_file": {p.DamengOptions.CAFile}, "certificate_file": {p.DamengOptions.CertificateFile}, "key_file": {p.DamengOptions.KeyFile}}
	u := url.URL{Scheme: "dameng", Host: p.Host, Path: "/" + p.Database, User: url.UserPassword(p.Username, "private-secret-sentinel"), RawQuery: q.Encode()}
	imported, err := connections.ParseURI(u.String())
	if err != nil || imported.Password != "private-secret-sentinel" {
		t.Fatal("native URI with temporary credentials failed to import", err)
	}
	if _, err := store.Create(context.Background(), imported.Profile); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(imported)
	if err != nil || strings.Contains(string(raw), imported.Password) {
		t.Fatal("temporary credential serialized", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), imported.Password) {
		t.Fatal("temporary credential reached configuration SQLite", err)
	}
}
