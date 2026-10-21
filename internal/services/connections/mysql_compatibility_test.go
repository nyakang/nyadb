package connections

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestCompatibilityDefaultsAndPersistence(t *testing.T) {
	store, _ := openStore(t)
	a := DefaultMySQLAdvanced()
	c := &a.Compatibility
	if c.Enabled || c.ForceLowerCase || c.ForceSQLMode || c.ForceNDB ||
		c.ForceDatabaseMethod || c.ForceViewMethod ||
		c.LowerCaseTableNames != "0" || c.SQLMode != DefaultMySQLSQLMode ||
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
	p, err := store.Create(context.Background(), Profile{
		Name: "compatible", Kind: MySQL, Host: "localhost", Port: 3306, MySQLAdvanced: a,
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

func TestCompatibilityRejectsInvalidChoices(t *testing.T) {
	store, _ := openStore(t)
	for _, change := range []func(*MySQLCompatibility){
		func(c *MySQLCompatibility) { c.LowerCaseTableNames = "3" },
		func(c *MySQLCompatibility) { c.NDBSupport = "unknown" },
		func(c *MySQLCompatibility) { c.DatabaseMethod = "random" },
		func(c *MySQLCompatibility) { c.ViewMethod = "random" },
		func(c *MySQLCompatibility) { c.SQLMode = "bad\x00mode" },
	} {
		a := DefaultMySQLAdvanced()
		change(&a.Compatibility)
		_, err := store.Create(context.Background(), Profile{
			Name: "bad", Kind: MySQL, Host: "localhost", Port: 3306, MySQLAdvanced: a,
		})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid compatibility accepted: %+v (%v)", a.Compatibility, err)
		}
	}
}

func TestPreCompatibilityProfilesReceiveDefaults(t *testing.T) {
	store, _ := openStore(t)
	p, err := store.Create(context.Background(), Profile{
		Name: "prior", Kind: MySQL, Host: "localhost", Port: 3306,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulates an advanced JSON from schema v2 before Compatibility existed.
	_, err = store.db.Exec("UPDATE connection_profiles SET mysql_advanced_json = ? WHERE id = ?",
		`{"clientDriver":"默认","charset":"自动","keepAliveSeconds":240,"connectTimeout":true,"connectTimeoutSeconds":30,"readTimeoutSeconds":30,"writeTimeout":true,"writeTimeoutSeconds":30,"maxSessions":10}`, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), p.ID)
	if err != nil || got.MySQLAdvanced.Compatibility != DefaultMySQLCompatibility() {
		t.Fatalf("backward-compatible defaults not applied: %+v %v", got.MySQLAdvanced.Compatibility, err)
	}
}
