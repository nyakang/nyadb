package localdb

import (
	"context"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestConsulProfilesRestartDuplicateAndExactOriginalScope(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.Consul)
	p.Name, p.Host, p.Port, p.TLSMode = "original Consul", "::1", 18501, "verify-full"
	o := &p.ConsulOptions
	o.Auth, o.Datacenter, o.Consistency, o.Prefix = "token", "dc1", "consistent", " /key//../\n/ "
	o.CAFile, o.ServerName, o.ClientCertFile, o.ClientKeyFile = "/owned/ca.crt", "localhost", "/owned/client.crt", "/owned/client.key"
	p, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := connections.NewService(store).Duplicate(ctx, p.ID, "copy")
	if err != nil || copy.ID == p.ID || copy.ConsulOptions != p.ConsulOptions || copy.Username != "" || copy.Database != "" {
		t.Fatal("duplicate lost Consul scope", err)
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
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
	got, err := NewProfileStore(reopened).Get(ctx, p.ID)
	if err != nil || got.ConsulOptions != p.ConsulOptions || got.Host != "::1" || got.Port != 18501 || got.Username != "" || got.Database != "" {
		t.Fatal("restart lost original Consul identity", err)
	}
	var after int
	if err := reopened.QueryRow("PRAGMA user_version").Scan(&after); err != nil || after != version {
		t.Fatal("Consul profile unexpectedly changed schema version", err)
	}
}

func TestConsulSavedOptionsRejectAmbiguitySecretsAndForeignEngine(t *testing.T) {
	store, _, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.Consul)
	p.Name = "strict"
	p, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := encodeProfileOptions(p)
	if err != nil {
		t.Fatal(err)
	}
	invalid := []string{"", "{}", valid + " {}"}
	for _, pair := range [][2]string{
		{`"auth":"none",`, ``}, {`"auth":"none"`, `"auth":null`}, {`"auth":"none"`, `"auth":"none","Auth":"token"`}, {`"auth":"none"`, `"auth":"none","auth":"token"`},
		{`"datacenter":"",`, ``}, {`"datacenter":""`, `"datacenter":null`}, {`"prefix":"",`, ``}, {`"prefix":""`, `"prefix":null`}, {`"prefix":""`, `"prefix":"/\ud800"`},
		{`"consistency":"default",`, ``}, {`"consistency":"default"`, `"consistency":"serializable"`}, {`"consistency":"default"`, `"consistency":null`},
		{`"connect_timeout_seconds":10,`, ``}, {`"request_timeout_seconds":30`, `"request_timeout_seconds":null`}, {`"request_timeout_seconds":30`, `"request_timeout_seconds":601`}, {`"request_timeout_seconds":30`, `"request_timeout_seconds":3e1`},
		{`"auth":"none"`, `"auth":"none","token":"private"`}, {`"kind":"consul"`, `"kind":"consul","etcd":{"version":1}`},
		{`"version":1`, `"version":1,"Version":1`}, {`"version":1`, `"version":1,"version":1`},
	} {
		raw := strings.Replace(valid, pair[0], pair[1], 1)
		if raw == valid {
			t.Fatal("invalid replacement did not match")
		}
		invalid = append(invalid, raw)
	}
	for _, raw := range invalid {
		if _, err := db.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", raw, p.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Get(ctx, p.ID); err == nil {
			t.Fatal("damaged original Consul settings accepted")
		}
	}
	other, _ := connections.Defaults(connections.PostgreSQL)
	other.Name = "legacy"
	other, err = store.Create(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE connection_profiles SET mysql_advanced_json='' WHERE id=?", other.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(ctx, other.ID); err != nil || got.PostgreSQLOptions.Version != 1 {
		t.Fatal("legacy configuration defaults changed", err)
	}
	foreign := strings.Replace(valid, `"kind":"consul"`, `"kind":"postgresql","postgresql":{"version":1}`, 1)
	if _, err = db.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", foreign, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, other.ID); err == nil {
		t.Fatal("foreign Consul options accepted")
	}
}
