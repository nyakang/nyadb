package mysql

import (
	"github.com/nyakang/nyadb/internal/db/mysqlddl"
	"strings"
	"testing"
)

func TestObjectPlanRequiresQualifiedIdentityAndCompleteOriginal(t *testing.T) {
	ref := ObjectIdentity{Database: "db", Name: "p", Kind: "procedure"}
	for _, sql := range []string{"CREATE TABLE db.p(id INT)", "CREATE PROCEDURE other.p() SELECT 1", "CREATE PROCEDURE db.q() SELECT 1"} {
		if _, e := BuildObjectPlan(ObjectDefinition{ObjectIdentity: ref}, "create", sql); e == nil {
			t.Fatalf("mismatched definition accepted: %s", sql)
		}
	}
	sql := "CREATE PROCEDURE `db`.`p`() BEGIN SELECT 'a;b'; SELECT 2; END"
	if _, e := BuildObjectPlan(ObjectDefinition{ObjectIdentity: ref}, "create", sql); e != nil {
		t.Fatal(e)
	}
	if _, e := BuildObjectPlan(ObjectDefinition{ObjectIdentity: ref}, "alter", sql); e == nil {
		t.Fatal("editing without full original must fail")
	}
	if _, e := BuildObjectPlan(ObjectDefinition{ObjectIdentity: ObjectIdentity{Database: "db", Name: "v", Kind: "view"}}, "create", "CREATE VIEW db.v AS SELECT 1; DROP TABLE db.t"); e == nil {
		t.Fatal("stacked view statement accepted")
	}
}
func TestCompleteCopyPreservesGeneratedForeignCheckAndTriggers(t *testing.T) {
	s := mysqlddl.Snapshot{Database: "db", Table: "source", DDL: "CREATE TABLE `source` (`id` BIGINT NOT NULL, `parent` BIGINT, `twice` BIGINT GENERATED ALWAYS AS (`id` * 2) STORED, PRIMARY KEY (`id`), CONSTRAINT `fk_source` FOREIGN KEY (`parent`) REFERENCES `source` (`id`), CONSTRAINT `chk_source` CHECK (`id` > 0)) ENGINE=InnoDB COMMENT='source'", Triggers: []mysqlddl.Trigger{{Name: "source_insert", SQL: "CREATE TRIGGER `source_insert` BEFORE INSERT ON `source` FOR EACH ROW SET NEW.parent = NEW.id"}}}
	table, triggers, e := CopyTableDefinition(s, "copy")
	if e != nil {
		t.Fatal(e)
	}
	ddl := table.DDL()
	for _, want := range []string{"GENERATED ALWAYS AS", "REFERENCES `copy`", "COMMENT='source'", "PRIMARY KEY"} {
		if !strings.Contains(ddl, want) {
			t.Errorf("lost %s: %s", want, ddl)
		}
	}
	if strings.Contains(ddl, "CONSTRAINT `fk_source`") || strings.Contains(ddl, "CONSTRAINT `chk_source`") {
		t.Fatal("constraint names must avoid schema collisions")
	}
	if len(triggers) != 1 || !strings.Contains(triggers[0].SQL, "ON `db`.`copy`") {
		t.Fatalf("trigger lost: %+v", triggers)
	}
	s.TriggersUnknown = true
	if _, _, e = CopyTableDefinition(s, "copy"); e == nil {
		t.Fatal("unknown triggers must block copy")
	}
}
func TestAccountSQLRedactsSecretsAndRejectsArbitraryPrivileges(t *testing.T) {
	stmt, args, e := accountChangeSQL(AccountChange{Action: "create_user", Account: MySQLAccount{User: "alice", Host: "localhost"}, Password: "private"})
	if e != nil || strings.Contains(stmt, "private") || len(args) != 1 || args[0] != "private" {
		t.Fatalf("unsafe password binding: %s %v", stmt, e)
	}
	if _, _, e := accountChangeSQL(AccountChange{Action: "grant", Account: MySQLAccount{User: "alice"}, Privileges: []string{"SELECT; DROP DATABASE db"}}); e == nil {
		t.Fatal("untrusted privilege accepted")
	}
}

func TestAccountPasswordLiteralUsesActualSQLMode(t *testing.T) {
	password := "x\\'; DELETE FROM t; --"
	normal, e := mysqlAccountPasswordLiteral(password, "")
	if e != nil {
		t.Fatal(e)
	}
	if normal != "'x\\\\''; DELETE FROM t; --'" {
		t.Fatalf("normal mode literal escaped incorrectly: %q", normal)
	}
	noBackslash, e := mysqlAccountPasswordLiteral(password, "NO_BACKSLASH_ESCAPES")
	if e != nil {
		t.Fatal(e)
	}
	if noBackslash != "'x\\''; DELETE FROM t; --'" {
		t.Fatalf("no-backslash literal escaped incorrectly: %q", noBackslash)
	}
	if _, e = mysqlAccountPasswordLiteral("bad\x00password", ""); e == nil {
		t.Fatal("NUL password must be rejected before execution")
	}
}
