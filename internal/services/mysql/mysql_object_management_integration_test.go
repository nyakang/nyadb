package mysql

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLObjectManagementCoreFixture(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_CORE_IT") != "1" {
		t.Skip("requires explicit disposable MySQL core fixture")
	}
	port, e := strconv.ParseUint(os.Getenv("NYADB_MYSQL_CORE_PORT"), 10, 16)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	m := NewMySQLSessionManager()
	defer m.Close()
	settings := connections.ActiveSettings{ProfileID: 918, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_core", Database: "nyadb_core_it", MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	settings.MySQLAdvanced.InitialSQLID = "fixture-initial"
	settings.MySQLAdvanced.Compression = true
	settings.MySQLAdvanced.Charset = "utf8mb4"
	settings.MySQLAdvanced.AutoConnect = true
	settings.MySQLAdvanced.UseCustomDatabaseList = true
	settings.MySQLAdvanced.CustomDatabases = "nyadb_core_it"
	m.InitialSQLLoader = func(context.Context, string) (string, error) {
		return "SET @nyadb_initialized=COALESCE(@nyadb_initialized,0)+1", nil
	}
	if e = m.Connect(ctx, settings, ""); e != nil {
		t.Fatal(e)
	}
	names, e := m.Databases(ctx, 918)
	if e != nil || len(names) != 1 || names[0] != "nyadb_core_it" {
		t.Fatalf("custom database filtering failed: %v %v", names, e)
	}
	if e = m.ReserveSQLTab(918, 1); e != nil {
		t.Fatal(e)
	}
	defer m.ReleaseSQLTab(918, 1)
	for i := 0; i < 2; i++ {
		result, e := m.ExecuteSQLTab(ctx, 918, 1, "nyadb_core_it", "SELECT @nyadb_initialized")
		if e != nil || len(result.Rows) != 1 || result.Rows[0][0] != "1" {
			t.Fatalf("physical SQL lease initialization was skipped or replayed: %+v %v", result, e)
		}
	}
	gen := m.SessionGeneration(918)
	for _, kind := range []string{"VIEW", "FUNCTION", "PROCEDURE", "EVENT"} {
		name := "nyadb_object_" + strings.ToLower(kind)
		ref := ObjectIdentity{Database: settings.Database, Name: name, Kind: strings.ToLower(kind)}
		raw := ""
		switch kind {
		case "VIEW":
			raw = "CREATE SQL SECURITY INVOKER VIEW `nyadb_core_it`.`" + name + "` AS SELECT 'NULL' AS value"
		case "FUNCTION":
			raw = "CREATE FUNCTION `nyadb_core_it`.`" + name + "`() RETURNS BIGINT DETERMINISTIC COMMENT 'literal NULL; preserved' RETURN 1"
		case "PROCEDURE":
			raw = "CREATE PROCEDURE `nyadb_core_it`.`" + name + "`() BEGIN /* preserve punctuation ; 'NULL' */ SELECT 'a;b' AS value; SELECT 2 AS value; END"
		case "EVENT":
			raw = "CREATE EVENT `nyadb_core_it`.`" + name + "` ON SCHEDULE EVERY 1 DAY ON COMPLETION PRESERVE DISABLE COMMENT 'literal NULL; preserved' DO SELECT 1"
		}
		createDefinition := ObjectDefinition{ObjectIdentity: ref, Generation: gen}
		// Explicitly empty is a stored mode, not permission to replace it with
		// a fresh physical connection's strict default on create/copy/alter.
		createDefinition.SQLModeKnown = kind == "PROCEDURE"
		plan, e := BuildObjectPlan(createDefinition, "create", raw)
		if e != nil {
			t.Fatal(e)
		}
		out := m.ApplyObject(ctx, 918, plan)
		if !out.Verified || out.Error != nil {
			t.Fatalf("%s create: %+v", kind, out)
		}
		original := *out.ActualDefinition
		if kind == "PROCEDURE" && (!original.SQLModeKnown || original.SQLMode != "" || !strings.Contains(original.SQL, "/* preserve punctuation ; 'NULL' */")) {
			t.Fatalf("procedure comments or explicitly empty stored mode were lost: %+v", original)
		}
		cloneSQL, e := CloneObjectSQL(original, name+"_copy")
		if e != nil {
			t.Fatal(e)
		}
		cloneref := ref
		cloneref.Name += "_copy"
		cloneDefinition := original
		cloneDefinition.ObjectIdentity = cloneref
		cloneplan, e := BuildObjectPlan(cloneDefinition, "create", cloneSQL)
		if e != nil {
			t.Fatal(e)
		}
		copyout := m.ApplyObject(ctx, 918, cloneplan)
		if !copyout.Verified || copyout.Error != nil {
			t.Fatalf("%s copy: %+v", kind, copyout)
		}
		drop, e := BuildObjectPlan(*copyout.ActualDefinition, "drop", "")
		if e != nil {
			t.Fatal(e)
		}
		if out = m.ApplyObject(ctx, 918, drop); !out.Verified || out.Error != nil {
			t.Fatalf("%s clone drop: %+v", kind, out)
		}
		edit := original.SQL
		if kind == "VIEW" {
			edit = strings.Replace(edit, "'NULL'", "'edited'", 1)
		} else if kind == "FUNCTION" {
			edit = strings.Replace(edit, "RETURN 1", "RETURN 2", 1)
		} else if kind == "PROCEDURE" {
			edit = strings.Replace(edit, "'a;b'", "'edited'", 1)
		} else {
			edit = "ALTER EVENT `nyadb_core_it`.`" + name + "` ON SCHEDULE EVERY 2 DAY DISABLE DO SELECT 2"
		}
		alter, e := BuildObjectPlan(original, "alter", edit)
		if e != nil {
			t.Fatal(e)
		}
		out = m.ApplyObject(ctx, 918, alter)
		if !out.Verified || out.Error != nil {
			t.Fatalf("%s alter: %+v", kind, out)
		}
		if kind != "EVENT" {
			conflict := m.ApplyObject(ctx, 918, alter)
			if conflict.State != "not_executed" || conflict.Error == nil {
				t.Fatalf("external change not blocked: %+v", conflict)
			}
		}
		drop, e = BuildObjectPlan(*out.ActualDefinition, "drop", "")
		if e != nil {
			t.Fatal(e)
		}
		out = m.ApplyObject(ctx, 918, drop)
		if !out.Verified || out.Error != nil {
			t.Fatalf("%s drop: %+v", kind, out)
		}
	}
	databaseRef := ObjectIdentity{Database: "nyadb_object_database", Kind: "database"}
	databasePlan, e := BuildObjectPlan(ObjectDefinition{ObjectIdentity: databaseRef, Generation: gen}, "create", "CREATE DATABASE `nyadb_object_database` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin")
	if e != nil {
		t.Fatal(e)
	}
	databaseOut := m.ApplyObject(ctx, 918, databasePlan)
	if !databaseOut.Verified || databaseOut.Error != nil {
		t.Fatalf("database create: %+v", databaseOut)
	}
	databaseAlter, e := BuildObjectPlan(*databaseOut.ActualDefinition, "alter", "ALTER DATABASE `nyadb_object_database` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	if e != nil {
		t.Fatal(e)
	}
	databaseOut = m.ApplyObject(ctx, 918, databaseAlter)
	if !databaseOut.Verified || databaseOut.Error != nil || !strings.Contains(databaseOut.ActualDefinition.SQL, "utf8mb4_unicode_ci") {
		t.Fatalf("database alter: %+v", databaseOut)
	}
	databaseDrop, e := BuildObjectPlan(*databaseOut.ActualDefinition, "drop", "")
	if e != nil {
		t.Fatal(e)
	}
	databaseOut = m.ApplyObject(ctx, 918, databaseDrop)
	if !databaseOut.Verified || databaseOut.Error != nil {
		t.Fatalf("database drop: %+v", databaseOut)
	}
	managed, _, cancelRequest, e := m.requestContext(ctx, 918, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer cancelRequest()
	for _, stmt := range []string{
		"CREATE TABLE nyadb_core_it.nyadb_copy_source (id BIGINT NOT NULL PRIMARY KEY,parent BIGINT, doubled BIGINT GENERATED ALWAYS AS (id * 2) STORED,CONSTRAINT nyadb_self_fk FOREIGN KEY(parent) REFERENCES nyadb_copy_source(id),CONSTRAINT nyadb_positive CHECK (id > 0)) ENGINE=InnoDB",
		"CREATE TRIGGER nyadb_core_it.z_nyadb_copy_first BEFORE INSERT ON nyadb_core_it.nyadb_copy_source FOR EACH ROW SET NEW.parent=NULL",
		"CREATE TRIGGER nyadb_core_it.a_nyadb_copy_second BEFORE INSERT ON nyadb_core_it.nyadb_copy_source FOR EACH ROW SET NEW.parent=NEW.id",
		"INSERT INTO nyadb_core_it.nyadb_copy_source (id,parent) VALUES(1,NULL),(2,1)",
	} {
		if _, e = managed.db.ExecContext(ctx, stmt); e != nil {
			t.Fatal(e)
		}
	}
	if e = m.ChangeTable(ctx, 918, MySQLTableChange{Action: MySQLTableCopyData, Database: "nyadb_core_it", Table: "nyadb_copy_source", NewName: "nyadb_copy_target"}); e != nil {
		t.Fatal(e)
	}
	copied, e := m.TableDesign(ctx, 918, "nyadb_core_it", "nyadb_copy_target")
	if e != nil {
		t.Fatal(e)
	}
	if len(copied.Triggers) != 2 || !strings.Contains(copied.DDL, "REFERENCES `nyadb_copy_target`") {
		t.Fatalf("copy structure lost: %+v", copied)
	}
	var count int
	if e = managed.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM nyadb_core_it.nyadb_copy_target WHERE doubled=id*2").Scan(&count); e != nil || count != 2 {
		t.Fatalf("copy data lost: %d %v", count, e)
	}
	if _, e = managed.db.ExecContext(ctx, "INSERT INTO nyadb_core_it.nyadb_copy_target (id,parent) VALUES(3,NULL)"); e != nil {
		t.Fatal(e)
	}
	var copiedParent int
	if e = managed.db.QueryRowContext(ctx, "SELECT parent FROM nyadb_core_it.nyadb_copy_target WHERE id=3").Scan(&copiedParent); e != nil || copiedParent != 3 {
		t.Fatalf("trigger ACTION_ORDER changed during copy: %d %v", copiedParent, e)
	}
	if _, e = managed.db.ExecContext(ctx, "DROP TABLE nyadb_core_it.nyadb_copy_target,nyadb_core_it.nyadb_copy_source"); e != nil {
		t.Fatal(e)
	}
	adminSettings := connections.ActiveSettings{ProfileID: 919, Host: "127.0.0.1", Port: uint16(port), Username: "root", Database: "nyadb_core_it", MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	if e = m.Connect(ctx, adminSettings, ""); e != nil {
		t.Fatal(e)
	}
	accountUser := MySQLAccount{User: "nyadb_fixture_user", Host: "localhost"}
	accountRole := MySQLAccount{User: "nyadb_fixture_role", Host: "localhost"}
	accountChanges := []AccountChange{
		{Action: "create_user", Account: accountUser, Password: "disposable-password"},
		{Action: "create_role", Account: accountRole},
		{Action: "grant", Account: accountRole, Database: "nyadb_core_it", Privileges: []string{"SELECT", "INSERT"}},
		{Action: "grant_role", Account: accountUser, Role: accountRole},
		{Action: "password", Account: accountUser, Password: "another-disposable-password"},
		{Action: "lock", Account: accountUser},
		{Action: "unlock", Account: accountUser},
	}
	adminGen := m.SessionGeneration(919)
	changed := m.ChangeAccounts(ctx, 919, adminGen, accountChanges)
	if changed.Error != nil || !changed.Verified || len(changed.Steps) != len(accountChanges) {
		t.Fatalf("user and role management failed: %+v", changed)
	}
	grants, e := m.AccountGrants(ctx, 919, accountUser)
	if e != nil || len(grants) < 2 {
		t.Fatalf("grants readback failed: %v %v", grants, e)
	}
	cleanupAccounts := m.ChangeAccounts(ctx, 919, adminGen, []AccountChange{{Action: "revoke_role", Account: accountUser, Role: accountRole}, {Action: "drop", Account: accountUser}, {Action: "drop_role", Account: accountRole}})
	if cleanupAccounts.Error != nil || !cleanupAccounts.Verified {
		t.Fatalf("account cleanup failed: %+v", cleanupAccounts)
	}
	account := m.ChangeAccounts(ctx, 918, gen, []AccountChange{{Action: "create_user", Account: MySQLAccount{User: "nyadb_forbidden", Host: "localhost"}, Password: "private"}})
	if account.Error == nil || account.State != "not_executed" {
		t.Fatalf("limited fixture user must report account permission rejection: %+v", account)
	}
}
