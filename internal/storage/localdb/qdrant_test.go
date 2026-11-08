package localdb

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestQdrantProfilesRestartTLSAndCopyWithoutMigration(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	var saved []connections.Profile
	for _, auth := range []string{"none", "api_key"} {
		p, _ := connections.Defaults(connections.Qdrant)
		p.Name, p.TLSMode = "native "+auth, "verify-full"
		o := &p.QdrantOptions
		o.Auth, o.ContextPath = auth, "/gateway/qdrant"
		o.CAFile, o.ServerName = filepath.Join(t.TempDir(), "ca.pem"), "localhost"
		o.ClientCertFile, o.ClientKeyFile = filepath.Join(t.TempDir(), "client.pem"), filepath.Join(t.TempDir(), "client.key")
		o.ConnectTimeoutSeconds, o.RequestTimeoutSeconds = 12, 45
		got, err := store.Create(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		got.Host, got.Port, got.Notes = "::1", 6390, "原生配置🐱"
		got, err = store.Update(ctx, got)
		if err != nil {
			t.Fatal(err)
		}
		saved = append(saved, got)
		copy := got
		copy.ID, copy.Name = 0, "copy "+auth
		copy, err = store.Create(ctx, copy)
		if err != nil || copy.ID == got.ID || copy.QdrantOptions != got.QdrantOptions {
			t.Fatal("metadata copy changed native settings", err)
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
		if err != nil || got.QdrantOptions != original.QdrantOptions || got.Host != "::1" || got.Port != 6390 || got.Notes != original.Notes || got.Username != "" || got.Database != "" {
			t.Fatal("restart changed original configuration", err)
		}
	}
	var after int
	if err := reopened.QueryRow("PRAGMA user_version").Scan(&after); err != nil || after != version {
		t.Fatal("unexpected schema migration")
	}
}

func TestQdrantSavedSettingsRejectMissingAuthDeadlinesAndForeignFields(t *testing.T) {
	store, _, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.Qdrant)
	p.Name, p.QdrantOptions.Auth = "strict", "api_key"
	p, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := encodeProfileOptions(p)
	if err != nil {
		t.Fatal(err)
	}
	invalid := []string{"", "{}", valid + " {}"}
	for _, replacement := range [][2]string{
		{`"auth":"api_key",`, ``}, {`"auth":"api_key"`, `"auth":null`}, {`"auth":"api_key"`, `"auth":"api_key","Auth":"none"`}, {`"auth":"api_key"`, `"auth":"api_key","auth":"none"`}, {`"context_path":"",`, ``}, {`"context_path":""`, `"context_path":null`}, {`"context_path":""`, `"context_path":"/\ud800"`}, {`"context_path":""`, `"context_path":"/\udc00"`}, {`"request_timeout_seconds":40`, `"request_timeout_seconds":0`}, {`"request_timeout_seconds":40`, `"request_timeout_seconds":null`}, {`"request_timeout_seconds":40`, `"request_timeout_seconds":601`}, {`"connect_timeout_seconds":10,`, ``}, {`"auth":"api_key"`, `"auth":"api_key","api_key":"private"`}, {`"auth":"api_key"`, `"auth":"basic"`}, {`"kind":"qdrant"`, `"kind":"couchdb"`},
	} {
		raw := strings.Replace(valid, replacement[0], replacement[1], 1)
		if raw == valid {
			t.Fatal("invalid fixture replacement did not match", replacement[0])
		}
		invalid = append(invalid, raw)
	}
	for _, raw := range invalid {
		if _, err := db.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", raw, p.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Get(ctx, p.ID); err == nil {
			t.Fatal("damaged native settings accepted")
		}
	}
	other, _ := connections.Defaults(connections.PostgreSQL)
	other.Name = "legacy"
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
	foreign := strings.Replace(valid, `"kind":"qdrant"`, `"kind":"postgresql","postgresql":{"version":1}`, 1)
	if _, err = db.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", foreign, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(ctx, other.ID); err == nil {
		t.Fatal("Qdrant settings accepted on foreign engine")
	}
}
