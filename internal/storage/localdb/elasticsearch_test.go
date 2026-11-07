package localdb

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestElasticsearchProfilesRestartUpdateAndCopyWithoutMigration(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	var saved []connections.Profile
	for _, auth := range []string{"basic", "api_key", "none"} {
		p, _ := connections.Defaults(connections.Elasticsearch)
		p.Name = "native " + auth
		p.ElasticsearchOptions.Auth = auth
		if auth != "basic" {
			p.Username = ""
		}
		p.ElasticsearchOptions.CAFile = filepath.Join(t.TempDir(), "ca.pem")
		p.ElasticsearchOptions.ServerName = "localhost"
		p.Notes = "native REST"
		got, err := store.Create(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		got.Host, got.Port = "::1", 9243
		got, err = store.Update(ctx, got)
		if err != nil {
			t.Fatal(err)
		}
		saved = append(saved, got)
		copy := got
		copy.ID, copy.Name = 0, "copy "+auth
		copy, err = store.Create(ctx, copy)
		if err != nil || copy.ID == got.ID || copy.ElasticsearchOptions != got.ElasticsearchOptions {
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
		if err != nil || got.Kind != connections.Elasticsearch || got.Host != "::1" || got.Port != 9243 || got.ElasticsearchOptions != original.ElasticsearchOptions || got.Username != original.Username || got.Notes != original.Notes {
			t.Fatal("restart changed native profile", err)
		}
	}
	var after int
	if err := reopened.QueryRow("PRAGMA user_version").Scan(&after); err != nil || after != version {
		t.Fatal("unexpected schema migration")
	}
}

func TestElasticsearchSavedOptionsRejectAmbiguousAuthentication(t *testing.T) {
	store, _, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.Elasticsearch)
	p.Name = "strict auth"
	p, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"", `{}`, `{"version":2,"kind":"elasticsearch","elasticsearch":{"version":1,"auth":"basic"}}`,
		`{"version":1,"kind":"elasticsearch","elasticsearch":{}}`,
		`{"version":1,"kind":"elasticsearch","elasticsearch":{"version":1}}`,
		`{"version":1,"kind":"elasticsearch","elasticsearch":{"version":1,"auth":"basic","auth":"none"}}`,
		`{"version":1,"kind":"elasticsearch","elasticsearch":{"version":1,"auth":"basic","Auth":"api_key"}}`,
		`{"version":1,"kind":"elasticsearch","elasticsearch":{"version":1,"auth":"basic"},"Elasticsearch":{"version":1,"auth":"basic"}}`,
		`{"version":1,"kind":"elasticsearch","elasticsearch":{"version":1,"auth":"basic"},"postgresql":{}}`,
		`{"version":1,"kind":"elasticsearch","elasticsearch":{"version":1,"auth":"basic","password":"private"}}`,
		`{"version":1,"kind":"elasticsearch","elasticsearch":{"version":1,"auth":"basic"}} {}`,
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
	if _, err = db.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", `{"version":1,"kind":"postgresql","postgresql":{"version":1},"elasticsearch":{"version":1,"auth":"basic"}}`, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(ctx, other.ID); err == nil {
		t.Fatal("foreign Elasticsearch options accepted")
	}
}
