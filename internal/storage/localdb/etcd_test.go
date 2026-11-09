package localdb

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestEtcdProfilesRestartPrefixTLSCopyAndExactIdentity(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	var version int
	if e := db.QueryRow("PRAGMA user_version").Scan(&version); e != nil {
		t.Fatal(e)
	}
	var saved []connections.Profile
	for _, auth := range []string{"basic", "none", "certificate"} {
		p, _ := connections.Defaults(connections.Etcd)
		p.Name = "native " + auth
		p.TLSMode = "verify-full"
		p.EtcdOptions.Auth = auth
		if auth == "basic" {
			p.Username = " native 用户 "
		}
		o := &p.EtcdOptions
		o.PrefixEncoding, o.Prefix = "base64", "AP8gL0tleS8g"
		o.CAFile = filepath.Join(t.TempDir(), "ca.crt")
		o.ServerName = "localhost"
		if auth != "none" {
			o.ClientCertFile = filepath.Join(t.TempDir(), "client.crt")
			o.ClientKeyFile = filepath.Join(t.TempDir(), "client.key")
		}
		o.ConnectTimeoutSeconds, o.RequestTimeoutSeconds = 12, 45
		got, e := store.Create(ctx, p)
		if e != nil {
			t.Fatal(e)
		}
		got.Host, got.Port, got.Notes = "::1", 2389, "原生图配置🐱"
		got, e = store.Update(ctx, got)
		if e != nil {
			t.Fatal(e)
		}
		saved = append(saved, got)
		copy, e := connections.NewService(store).Duplicate(ctx, got.ID, "copy "+auth)
		if e != nil || copy.ID == got.ID || copy.EtcdOptions != got.EtcdOptions || copy.Username != got.Username || copy.Database != got.Database {
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
		if e != nil || got.EtcdOptions != original.EtcdOptions || got.Database != original.Database || got.Username != original.Username || got.Host != "::1" || got.Port != 2389 || got.Notes != original.Notes {
			t.Fatal("restart lost native configuration", e)
		}
	}
	var after int
	if e := reopened.QueryRow("PRAGMA user_version").Scan(&after); e != nil || after != version {
		t.Fatal("native profile required unexpected migration")
	}
}

func TestEtcdSavedOptionsRejectAmbiguousAuthPrefixDeadlinesAndSecrets(t *testing.T) {
	store, _, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.Etcd)
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
		{`"auth":"none",`, ``}, {`"auth":"none"`, `"auth":null`}, {`"auth":"none"`, `"auth":"none","Auth":"basic"`}, {`"auth":"none"`, `"auth":"none","auth":"basic"`},
		{`"prefix_encoding":"text",`, ``}, {`"prefix_encoding":"text"`, `"prefix_encoding":"raw"`}, {`"prefix":"",`, ``}, {`"prefix":""`, `"prefix":null`}, {`"prefix":""`, `"prefix":"/\ud800"`},
		{`"connect_timeout_seconds":10,`, ``}, {`"request_timeout_seconds":30`, `"request_timeout_seconds":null`}, {`"request_timeout_seconds":30`, `"request_timeout_seconds":0`}, {`"request_timeout_seconds":30`, `"request_timeout_seconds":601`}, {`"request_timeout_seconds":30`, `"request_timeout_seconds":3e1`},
		{`"auth":"none"`, `"auth":"none","password":"private"`}, {`"kind":"etcd"`, `"kind":"etcd","postgresql":{"version":1}`},
		{`"version":1`, `"version":1,"Version":1`}, {`"version":1`, `"version":1,"version":1`},
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
	foreign := strings.Replace(valid, `"kind":"etcd"`, `"kind":"postgresql","postgresql":{"version":1}`, 1)
	if _, e = db.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", foreign, other.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := store.Get(ctx, other.ID); e == nil {
		t.Fatal("foreign graph settings accepted")
	}
}
