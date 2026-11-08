package localdb

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestCassandraPrivateProfilesRestartUpdateDuplicateAndLegacyCompatibility(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	var saved []connections.Profile
	for _, auth := range []string{"password", "none"} {
		p, _ := connections.Defaults(connections.Cassandra)
		p.Name = "native " + auth
		p.Database = "MixedCase"
		p.CassandraOptions.Auth = auth
		if auth == "password" {
			p.Username = " native 用户 "
		}
		p.CassandraOptions.CAFile = filepath.Join(t.TempDir(), "ca.pem")
		p.CassandraOptions.ServerName = "localhost"
		p.Notes = "native CQL"
		p, err := store.Create(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		p.Host, p.Port = "::1", 9043
		p, err = store.Update(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		saved = append(saved, p)
		copy, err := connections.NewService(store).Duplicate(ctx, p.ID, "copy "+auth)
		if err != nil || copy.ID == p.ID || copy.CassandraOptions != p.CassandraOptions || copy.Username != p.Username {
			t.Fatal("copy lost public native settings")
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
		if err != nil || got.CassandraOptions != original.CassandraOptions || got.Username != original.Username || got.Database != "MixedCase" || got.Host != "::1" || got.Port != 9043 || got.Notes != original.Notes {
			t.Fatal("restart lost native settings", err)
		}
	}
	var after int
	if err := reopened.QueryRow("PRAGMA user_version").Scan(&after); err != nil || after != version {
		t.Fatal("unexpected schema migration")
	}
}

func TestCassandraSavedOptionsRejectAuthenticationAmbiguityAndForeignFields(t *testing.T) {
	store, _, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.Cassandra)
	p.Name = "strict"
	p, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"", `{}`, `{"version":1,"kind":"cassandra"}`, `{"version":1,"kind":"cassandra","cassandra":{}}`,
		`{"version":1,"kind":"cassandra","cassandra":{"version":1}}`,
		`{"version":1,"kind":"cassandra","cassandra":{"version":2,"auth":"none"}}`,
		`{"version":1,"kind":"cassandra","cassandra":{"version":1,"auth":"none","auth":"password"}}`,
		`{"version":1,"kind":"cassandra","cassandra":{"version":1,"auth":"none","Auth":"password"}}`,
		`{"version":1,"kind":"cassandra","cassandra":{"version":1,"auth":"none"},"Cassandra":{"version":1,"auth":"none"}}`,
		`{"version":1,"kind":"cassandra","cassandra":{"version":1,"auth":"none","password":"private"}}`,
		`{"version":1,"kind":"cassandra","cassandra":{"version":1,"auth":"basic"}}`,
		`{"version":1,"kind":"cassandra","cassandra":{"version":1,"auth":"none"},"postgresql":{"version":1}}`,
		`{"version":1,"kind":"cassandra","cassandra":{"version":1,"auth":"none"},"solr":{"version":1,"auth":"none"}}`,
		`{"version":1,"kind":"cassandra","cassandra":{"version":1,"auth":"none"}} {}`,
	} {
		if _, err := db.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", raw, p.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Get(ctx, p.ID); err == nil {
			t.Fatal("ambiguous native options accepted")
		}
	}
	other, _ := connections.Defaults(connections.PostgreSQL)
	other.Name = "legacy"
	other, err = store.Create(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE connection_profiles SET mysql_advanced_json='' WHERE id=?", other.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(ctx, other.ID); err != nil || got.PostgreSQLOptions.Version != 1 {
		t.Fatal("legacy default changed")
	}
	if _, err := db.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", `{"version":1,"kind":"postgresql","postgresql":{"version":1},"cassandra":{"version":1,"auth":"none"}}`, other.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(ctx, other.ID); err == nil || strings.Contains(fmt.Sprint(got), "private") {
		t.Fatal("foreign Cassandra options accepted")
	}
}
