package localdb

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestNeo4jProfilesRestartRoutingTLSCopyAndExactIdentity(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	var version int
	if e := db.QueryRow("PRAGMA user_version").Scan(&version); e != nil {
		t.Fatal(e)
	}
	var saved []connections.Profile
	for _, auth := range []string{"basic", "none"} {
		p, _ := connections.Defaults(connections.Neo4j)
		p.Name = "native " + auth
		p.TLSMode = "verify-full"
		p.Database = " ExactDatabase "
		p.Neo4jOptions.Auth = auth
		if auth == "none" {
			p.Username = ""
		} else {
			p.Username = " native 用户 "
		}
		o := &p.Neo4jOptions
		o.Protocol = "neo4j"
		o.RoutingContext = "region=local&policy=reader"
		o.CAFile = filepath.Join(t.TempDir(), "ca.crt")
		o.ClientCertFile = filepath.Join(t.TempDir(), "client.crt")
		o.ClientKeyFile = filepath.Join(t.TempDir(), "client.key")
		o.ConnectTimeoutSeconds, o.RequestTimeoutSeconds = 12, 45
		got, e := store.Create(ctx, p)
		if e != nil {
			t.Fatal(e)
		}
		got.Host, got.Port, got.Notes = "::1", 7689, "原生图配置🐱"
		got, e = store.Update(ctx, got)
		if e != nil {
			t.Fatal(e)
		}
		saved = append(saved, got)
		copy, e := connections.NewService(store).Duplicate(ctx, got.ID, "copy "+auth)
		if e != nil || copy.ID == got.ID || copy.Neo4jOptions != got.Neo4jOptions || copy.Username != got.Username || copy.Database != got.Database {
			t.Fatal("duplicate lost native identity", e)
		}
	}
	if e := db.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	store = NewProfileStore(reopened)
	for _, original := range saved {
		got, e := store.Get(ctx, original.ID)
		if e != nil || got.Neo4jOptions != original.Neo4jOptions || got.Database != original.Database || got.Username != original.Username || got.Host != "::1" || got.Port != 7689 || got.Notes != original.Notes {
			t.Fatal("restart lost native configuration", e)
		}
	}
	var after int
	if e := reopened.QueryRow("PRAGMA user_version").Scan(&after); e != nil || after != version {
		t.Fatal("native profile required unexpected migration")
	}
}

func TestNeo4jSavedOptionsRejectAmbiguousAuthRoutingDeadlinesAndSecrets(t *testing.T) {
	store, _, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.Neo4j)
	p.Name = "strict"
	p, e := store.Create(ctx, p)
	if e != nil {
		t.Fatal(e)
	}
	valid, e := encodeProfileOptions(p)
	if e != nil {
		t.Fatal(e)
	}
	invalid := []string{"", "{}", valid + " {}"}
	for _, pair := range [][2]string{
		{`"auth":"basic",`, ``}, {`"auth":"basic"`, `"auth":null`}, {`"auth":"basic"`, `"auth":"basic","Auth":"none"`}, {`"auth":"basic"`, `"auth":"basic","auth":"none"`},
		{`"protocol":"bolt",`, ``}, {`"protocol":"bolt"`, `"protocol":"https"`}, {`"routing_context":"",`, ``}, {`"routing_context":""`, `"routing_context":null`}, {`"routing_context":""`, `"routing_context":"/\ud800"`},
		{`"connect_timeout_seconds":10,`, ``}, {`"request_timeout_seconds":40`, `"request_timeout_seconds":null`}, {`"request_timeout_seconds":40`, `"request_timeout_seconds":0`}, {`"request_timeout_seconds":40`, `"request_timeout_seconds":601`}, {`"request_timeout_seconds":40`, `"request_timeout_seconds":4e1`},
		{`"auth":"basic"`, `"auth":"basic","password":"private"`}, {`"kind":"neo4j"`, `"kind":"neo4j","postgresql":{"version":1}`},
	} {
		raw := strings.Replace(valid, pair[0], pair[1], 1)
		if raw == valid {
			t.Fatal("invalid replacement did not match")
		}
		invalid = append(invalid, raw)
	}
	for _, raw := range invalid {
		if _, e := db.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", raw, p.ID); e != nil {
			t.Fatal(e)
		}
		if _, e := store.Get(ctx, p.ID); e == nil {
			t.Fatal("damaged native settings accepted")
		}
	}
	other, _ := connections.Defaults(connections.PostgreSQL)
	other.Name = "legacy"
	other, e = store.Create(ctx, other)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("UPDATE connection_profiles SET mysql_advanced_json='' WHERE id=?", other.ID); e != nil {
		t.Fatal(e)
	}
	if got, e := store.Get(ctx, other.ID); e != nil || got.PostgreSQLOptions.Version != 1 {
		t.Fatal("legacy PostgreSQL defaults changed", e)
	}
	foreign := strings.Replace(valid, `"kind":"neo4j"`, `"kind":"postgresql","postgresql":{"version":1}`, 1)
	if _, e = db.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", foreign, other.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := store.Get(ctx, other.ID); e == nil {
		t.Fatal("foreign graph settings accepted")
	}
}
