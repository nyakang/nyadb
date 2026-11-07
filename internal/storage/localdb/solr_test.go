package localdb

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSolrProfilesRestartUpdateAndCopyWithoutMigration(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	var saved []connections.Profile
	for _, auth := range []string{"basic", "none"} {
		p, _ := connections.Defaults(connections.Solr)
		p.Name = "native " + auth
		p.SolrOptions.Auth = auth
		p.SolrOptions.ContextPath = "/gateway/solr"
		if auth == "basic" {
			p.Username = "reader"
		}
		if auth != "basic" {
			p.Username = ""
		}
		p.SolrOptions.CAFile = filepath.Join(t.TempDir(), "ca.pem")
		p.SolrOptions.ServerName = "localhost"
		p.Notes = "native REST"
		got, err := store.Create(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		got.Host, got.Port = "::1", 8990
		got, err = store.Update(ctx, got)
		if err != nil {
			t.Fatal(err)
		}
		saved = append(saved, got)
		copy := got
		copy.ID, copy.Name = 0, "copy "+auth
		copy, err = store.Create(ctx, copy)
		if err != nil || copy.ID == got.ID || copy.SolrOptions != got.SolrOptions {
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
		if err != nil || got.Kind != connections.Solr || got.Host != "::1" || got.Port != 8990 || got.SolrOptions != original.SolrOptions || got.Username != original.Username || got.Notes != original.Notes {
			t.Fatal("restart changed native profile", err)
		}
	}
	var after int
	if err := reopened.QueryRow("PRAGMA user_version").Scan(&after); err != nil || after != version {
		t.Fatal("unexpected schema migration")
	}
}

func TestSolrSavedOptionsRejectAmbiguousAuthentication(t *testing.T) {
	store, _, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.Solr)
	p.Name = "strict auth"
	p.SolrOptions.Auth, p.Username = "basic", "reader"
	p, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"", `{}`, `{"version":2,"kind":"solr","solr":{"version":1,"auth":"basic","context_path":"/solr"}}`,
		`{"version":1,"kind":"solr","solr":{}}`,
		`{"version":1,"kind":"solr","solr":{"version":1}}`,
		`{"version":1,"kind":"solr","solr":{"version":1,"auth":"basic","context_path":"/solr","auth":"none"}}`,
		`{"version":1,"kind":"solr","solr":{"version":1,"auth":"basic","context_path":"/solr","Auth":"none"}}`,
		`{"version":1,"kind":"solr","solr":{"version":1,"auth":"basic","context_path":"/solr"},"Solr":{"version":1,"auth":"basic","context_path":"/solr"}}`,
		`{"version":1,"kind":"solr","solr":{"version":1,"auth":"basic","context_path":"/solr"},"postgresql":{}}`,
		`{"version":1,"kind":"solr","solr":{"version":1,"auth":"basic","context_path":"/solr","password":"private"}}`,
		`{"version":1,"kind":"solr","solr":{"version":1,"auth":"api_key","context_path":"/solr"}}`,
		`{"version":1,"kind":"solr","solr":{"version":1,"auth":"basic","context_path":"/solr"},"easysearch":{"version":1,"auth":"basic"}}`,
		`{"version":1,"kind":"solr","solr":{"version":1,"auth":"basic"}}`,
		`{"version":1,"kind":"solr","solr":{"version":1,"auth":"basic","context_path":"/solr","Context_Path":"/other"}}`,
		`{"version":1,"kind":"solr","solr":{"version":1,"auth":"basic","context_path":"/solr"}} {}`,
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
	if _, err = db.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", `{"version":1,"kind":"postgresql","postgresql":{"version":1},"solr":{"version":1,"auth":"basic","context_path":"/solr"}}`, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(ctx, other.ID); err == nil {
		t.Fatal("foreign Solr options accepted")
	}
}
