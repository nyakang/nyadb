package localdb

import (
	"context"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestCompatibilityDefaultsAndPersistence(t *testing.T) {
	store, _, _ := openStore(t)
	a := connections.DefaultMySQLAdvanced()
	c := &a.Compatibility
	if c.Enabled || c.ForceLowerCase || c.ForceSQLMode || c.ForceNDB ||
		c.ForceDatabaseMethod || c.ForceViewMethod ||
		c.LowerCaseTableNames != "0" || c.SQLMode != connections.DefaultMySQLSQLMode ||
		c.NDBSupport != "Not Support" || c.DatabaseMethod != "SHOW DATABASES Statement" ||
		c.ViewMethod != "SHOW FULL TABLES Statement" {
		t.Fatalf("unexpected compatibility defaults: %+v", c)
	}
	c.Enabled = true
	c.ForceLowerCase = true
	c.LowerCaseTableNames = "2"
	c.ForceSQLMode = true
	c.SQLMode = "STRICT_TRANS_TABLES"
	c.ForceNDB = true
	c.NDBSupport = "Support"
	c.ForceDatabaseMethod = true
	c.DatabaseMethod = "INFORMATION_SCHEMA"
	c.ForceViewMethod = true
	c.ViewMethod = "INFORMATION_SCHEMA"
	p, err := store.Create(context.Background(), connections.Profile{
		Name: "compatible", Kind: connections.MySQL, Host: "localhost", Port: 3306, MySQLAdvanced: a,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), p.ID)
	if err != nil || !reflect.DeepEqual(got.MySQLAdvanced.Compatibility, *c) {
		t.Fatalf("compatibility values lost: %+v %v", got.MySQLAdvanced.Compatibility, err)
	}
	got.MySQLAdvanced.Compatibility.Enabled = false
	updated, err := store.Update(context.Background(), got)
	if err != nil || updated.MySQLAdvanced.Compatibility.LowerCaseTableNames != "2" {
		t.Fatalf("disabled parent should preserve child values: %+v %v", updated.MySQLAdvanced.Compatibility, err)
	}
}

func TestPreCompatibilityProfilesReceiveDefaults(t *testing.T) {
	store, _, db := openStore(t)
	p, err := store.Create(context.Background(), connections.Profile{
		Name: "prior", Kind: connections.MySQL, Host: "localhost", Port: 3306,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulates an advanced JSON from schema v2 before Compatibility existed.
	_, err = db.Exec("UPDATE connection_profiles SET mysql_advanced_json = ? WHERE id = ?",
		`{"clientDriver":"默认","charset":"自动","keepAliveSeconds":240,"connectTimeout":true,"connectTimeoutSeconds":30,"readTimeoutSeconds":30,"writeTimeout":true,"writeTimeoutSeconds":30,"maxSessions":10}`, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), p.ID)
	if err != nil || got.MySQLAdvanced.Compatibility != connections.DefaultMySQLCompatibility() {
		t.Fatalf("backward-compatible defaults not applied: %+v %v", got.MySQLAdvanced.Compatibility, err)
	}
}
