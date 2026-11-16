package localdb

import (
	"context"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestZooKeeperProfilesRestartDuplicateAndPreserveOriginalScope(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.ZooKeeper)
	p.Name, p.Host, p.Username, p.TLSMode = "original ZooKeeper", "::1", "ops", "verify-full"
	a := &p.ZooKeeperOptions
	a.AdditionalServers, a.Chroot, a.Namespace, a.Prefix = "zk-2:2281", "/app", "/ns", "/logical"
	a.Auth, a.DriverVersion, a.DriverSHA256, a.DriverDirectory = "digest", "native-1", strings.Repeat("a", 64), t.TempDir()
	a.CAFile, a.ClientCertFile, a.ClientKeyFile = "/owned/ca.crt", "/owned/client.crt", "/owned/client.key"
	p, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := connections.NewService(store).Duplicate(ctx, p.ID, "copy")
	if err != nil || copy.ID == p.ID || copy.ZooKeeperOptions != p.ZooKeeperOptions || copy.Host != p.Host || copy.Username != p.Username {
		t.Fatal("duplicate lost original native settings", err)
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
	if err != nil || got.ZooKeeperOptions != p.ZooKeeperOptions || got.Host != p.Host || got.Port != p.Port || got.Username != p.Username || got.Database != "" {
		t.Fatal("restart lost ZooKeeper identity or driver pin", err)
	}
	var after int
	if err := reopened.QueryRow("PRAGMA user_version").Scan(&after); err != nil || after != version {
		t.Fatal("native product changed the storage schema version", err)
	}
}

func TestZooKeeperSavedOptionsRejectAmbiguitySecretsAndForeignEngine(t *testing.T) {
	store, _, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.ZooKeeper)
	p.Name = "strict"
	p, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := encodeProfileOptions(p)
	if err != nil {
		t.Fatal(err)
	}
	invalid := []string{"", "{}", "null", valid + " {}"}
	for _, pair := range [][2]string{
		{`"version":1`, `"version":null`}, {`"version":1`, `"version":1,"version":1`}, {`"version":1`, `"version":1,"Version":1`},
		{`"kind":"zookeeper"`, `"kind":null`}, {`"zookeeper":`, `"ZooKeeper":`},
		{`"auth":"none",`, ``}, {`"auth":"none"`, `"auth":null`}, {`"auth":"none"`, `"auth":"none","Auth":"digest"`}, {`"auth":"none"`, `"auth":"none","auth":"digest"`},
		{`"prefix":"/",`, ``}, {`"prefix":"/"`, `"prefix":null`}, {`"prefix":"/"`, `"prefix":"/\ud800"`},
		{`"session_timeout_ms":30000,`, ``}, {`"session_timeout_ms":30000`, `"session_timeout_ms":3e4`}, {`"connection_timeout_ms":15000`, `"connection_timeout_ms":null`},
		{`"max_buffer_size":33554432,`, ``}, {`"max_documents":8`, `"max_documents":33`},
		{`"auth":"none"`, `"auth":"none","password":"private"`}, {`"auth":"none"`, `"auth":"none","endpoint_routes":{}`},
		{`"auth":"none"`, `"auth":"none","driver_version":"owned"`}, {`"auth":"none"`, `"auth":"none","driver_directory":"relative"`},
		{`"auth":"none"`, `"auth":"none","client_cert_file":"/client.crt"`}, {`"kind":"zookeeper"`, `"kind":"zookeeper","etcd":{"version":1}`},
	} {
		raw := strings.Replace(valid, pair[0], pair[1], 1)
		if raw == valid {
			t.Fatal("mutation did not match", pair[0])
		}
		invalid = append(invalid, raw)
	}
	for _, raw := range invalid {
		if _, err := db.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", raw, p.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Get(ctx, p.ID); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("damaged native settings accepted or secret exposed", raw)
		}
	}
	for _, kind := range []connections.Kind{connections.MySQL, connections.PostgreSQL, connections.Etcd, connections.Consul, connections.TDengine, connections.QuestDB, connections.H2} {
		other, _ := connections.Defaults(kind)
		other.Name = string(kind)
		if kind == connections.H2 {
			other.Database = "/owned/main"
		}
		other, err = store.Create(ctx, other)
		if err != nil {
			t.Fatal(err)
		}
		original, err := encodeProfileOptions(other)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"zookeeper", "ZooKeeper"} {
			foreign := strings.TrimSuffix(original, "}") + `,"` + field + `":null}`
			if _, err := db.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", foreign, other.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Get(ctx, other.ID); err == nil {
				t.Fatal("foreign ZooKeeper options accepted", kind)
			}
		}
	}
	legacy, _ := connections.Defaults(connections.PostgreSQL)
	legacy.Name = "legacy PG"
	legacy, err = store.Create(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE connection_profiles SET mysql_advanced_json='' WHERE id=?", legacy.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(ctx, legacy.ID); err != nil || got.PostgreSQLOptions.Version != 1 {
		t.Fatal("legacy engine defaults changed", err)
	}
}
