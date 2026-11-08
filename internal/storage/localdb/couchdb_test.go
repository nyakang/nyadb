package localdb

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestCouchDBProfilesRestartUpdateAndCopyWithoutMigration(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	var saved []connections.Profile
	for _, auth := range []string{"basic", "none"} {
		p, _ := connections.Defaults(connections.CouchDB)
		p.Name = "native " + auth
		p.TLSMode = "verify-full"
		p.CouchDBOptions.Auth = auth
		p.CouchDBOptions.ContextPath = "/gateway/couchdb"
		if auth == "basic" {
			p.Username = " 原生用户🐱 "
		}
		if auth != "basic" {
			p.Username = ""
		}
		p.CouchDBOptions.CAFile = filepath.Join(t.TempDir(), "ca.pem")
		p.CouchDBOptions.ServerName = "localhost"
		p.Notes = "native REST"
		got, err := store.Create(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		got.Host, got.Port = "::1", 6990
		got, err = store.Update(ctx, got)
		if err != nil {
			t.Fatal(err)
		}
		saved = append(saved, got)
		copy := got
		copy.ID, copy.Name = 0, "copy "+auth
		copy, err = store.Create(ctx, copy)
		if err != nil || copy.ID == got.ID || copy.CouchDBOptions != got.CouchDBOptions {
			t.Fatal("profile copy lost native settings", err)
		}
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
	for _, original := range saved {
		got, err := store.Get(ctx, original.ID)
		if err != nil || got.Kind != connections.CouchDB || got.Host != "::1" || got.Port != 6990 || got.CouchDBOptions != original.CouchDBOptions || got.Username != original.Username || got.Notes != original.Notes {
			t.Fatal("restart changed native profile", err)
		}
	}
	var after int
	if err := reopened.QueryRow("PRAGMA user_version").Scan(&after); err != nil || after != version {
		t.Fatal("unexpected schema migration")
	}
}

func TestCouchDBSavedOptionsRejectAmbiguousAuthentication(t *testing.T) {
	store, _, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.CouchDB)
	p.Name = "strict auth"
	p.CouchDBOptions.Auth, p.Username = "basic", "reader"
	p.TLSMode = "verify-full"
	p, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"version":1,"kind":"couchdb","couchdb":{"version":1,"auth":"basic","context_path":"/\ud800"}}`,
		`{"version":1,"kind":"couchdb","couchdb":{"version":1,"auth":"basic","context_path":"/\udc00"}}`,
		"", `{}`, `{"version":1,"kind":"couchdb","couchdb":{"version":1,"auth":null,"context_path":""}}`, `{"version":1,"kind":"couchdb","couchdb":{"version":1,"auth":"none","context_path":null}}`, `{"version":2,"kind":"couchdb","couchdb":{"version":1,"auth":"basic","context_path":"/couchdb"}}`,
		`{"version":1,"kind":"couchdb","couchdb":{}}`,
		`{"version":1,"kind":"couchdb","couchdb":{"version":1}}`,
		`{"version":1,"kind":"couchdb","couchdb":{"version":1,"auth":"basic","context_path":"/couchdb","auth":"none"}}`,
		`{"version":1,"kind":"couchdb","couchdb":{"version":1,"auth":"basic","context_path":"/couchdb","Auth":"none"}}`,
		`{"version":1,"kind":"couchdb","couchdb":{"version":1,"auth":"basic","context_path":"/couchdb"},"CouchDB":{"version":1,"auth":"basic","context_path":"/couchdb"}}`,
		`{"version":1,"kind":"couchdb","couchdb":{"version":1,"auth":"basic","context_path":"/couchdb"},"postgresql":{}}`,
		`{"version":1,"kind":"couchdb","couchdb":{"version":1,"auth":"basic","context_path":"/couchdb","password":"private"}}`,
		`{"version":1,"kind":"couchdb","couchdb":{"version":1,"auth":"api_key","context_path":"/couchdb"}}`,
		`{"version":1,"kind":"couchdb","couchdb":{"version":1,"auth":"basic","context_path":"/couchdb"},"easysearch":{"version":1,"auth":"basic"}}`,
		`{"version":1,"kind":"couchdb","couchdb":{"version":1,"auth":"basic"}}`,
		`{"version":1,"kind":"couchdb","couchdb":{"version":1,"auth":"basic","context_path":"/couchdb","Context_Path":"/other"}}`,
		`{"version":1,"kind":"couchdb","couchdb":{"version":1,"auth":"basic","context_path":"/couchdb"}} {}`,
	} {
		if _, err := db.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", raw, p.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Get(ctx, p.ID); err == nil {
			t.Fatal("ambiguous or foreign saved settings accepted")
		}
	}
	other, _ := connections.Defaults(connections.PostgreSQL)
	other.Name = "legacy unaffected"
	other, err = store.Create(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", "", other.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(ctx, other.ID); err != nil || got.PostgreSQLOptions.Version != 1 {
		t.Fatal("legacy PostgreSQL defaults changed", err)
	}
	if _, err = db.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", `{"version":1,"kind":"postgresql","postgresql":{"version":1},"couchdb":{"version":1,"auth":"basic","context_path":"/couchdb"}}`, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(ctx, other.ID); err == nil {
		t.Fatal("foreign CouchDB options accepted")
	}
}
