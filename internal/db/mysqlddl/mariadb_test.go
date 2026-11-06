package mysqlddl

import (
	"strings"
	"testing"
)

func TestMariaDBCheckRemovalUsesProductSyntax(t *testing.T) {
	original := Snapshot{Engine: "mariadb", Database: "db", Table: "t", DDL: "CREATE TABLE `t` (`id` int, CONSTRAINT `ck` CHECK (`id` > 0)) ENGINE=InnoDB"}
	table, err := Parse("db", "t", original.DDL)
	if err != nil {
		t.Fatal(err)
	}
	table.Clauses = table.Clauses[:1]
	plan, err := Build(original, Draft{Table: table})
	if err != nil || len(plan.Statements) != 1 || !strings.Contains(plan.Statements[0], "DROP CONSTRAINT `ck`") {
		t.Fatalf("MariaDB check removal: %+v %v", plan, err)
	}
	original.Engine = ""
	plan, err = Build(original, Draft{Table: table})
	if err != nil || !strings.Contains(plan.Statements[0], "DROP CHECK `ck`") {
		t.Fatal("legacy MySQL check removal changed")
	}
}

func TestMariaDBDesignerRejectsMySQLOnlyConstraintSemantics(t *testing.T) {
	for _, extra := range []string{"CONSTRAINT `ck` CHECK (`id` > 0) NOT ENFORCED", "KEY `idx` (`id`) INVISIBLE"} {
		table, err := Parse("db", "t", "CREATE TABLE `t` (`id` int, "+extra+") ENGINE=InnoDB")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = Build(Snapshot{Engine: "mariadb", Database: "db", Table: "t"}, Draft{New: true, Table: table}); err == nil {
			t.Fatalf("MySQL-only semantics accepted: %s", extra)
		}
	}
}

func TestMariaDBDesignerVerifiesIntegerWidthsWithoutHidingTypeChanges(t *testing.T) {
	desiredTable, err := Parse("db", "t", "CREATE TABLE `t` (`id` bigint NOT NULL, `n` int unsigned) ENGINE=InnoDB")
	if err != nil {
		t.Fatal(err)
	}
	desired := Draft{New: true, Table: desiredTable}
	actual := Snapshot{Engine: "mariadb", Database: "db", Table: "t", DDL: "CREATE TABLE `t` (`id` bigint(20) NOT NULL, `n` int(10) unsigned DEFAULT NULL) ENGINE=InnoDB"}
	if err = Verify(desired, actual); err != nil {
		t.Fatal("MariaDB display-width normalization rejected", err)
	}
	for _, change := range []string{"bigint(20) unsigned", "int(11)", "bigint(20) zerofill"} {
		changed := actual
		changed.DDL = strings.Replace(actual.DDL, "bigint(20)", change, 1)
		if err = Verify(desired, changed); err == nil {
			t.Fatal("meaningful field change hidden", change)
		}
	}
	actual.Engine = ""
	if err = Verify(desired, actual); err == nil {
		t.Fatal("MariaDB normalization changed legacy MySQL verification")
	}
}

func TestMariaDBDesignerVerifiesJSONAliasWithExactCheckAndCollation(t *testing.T) {
	table, err := Parse("db", "t", "CREATE TABLE `t` (`document` json, `active` boolean NOT NULL DEFAULT 1) ENGINE=InnoDB")
	if err != nil {
		t.Fatal(err)
	}
	desired := Draft{New: true, Table: table}
	actual := Snapshot{Engine: "mariadb", Database: "db", Table: "t", DDL: "CREATE TABLE `t` (`document` longtext CHARACTER SET utf8mb4 COLLATE utf8mb4_bin DEFAULT NULL CHECK (json_valid(`document`)), `active` tinyint(1) NOT NULL DEFAULT 1) ENGINE=InnoDB"}
	if err = Verify(desired, actual); err != nil {
		t.Fatal("MariaDB aliases rejected", err)
	}
	for _, change := range []struct{ from, to string }{
		{" CHECK (json_valid(`document`))", ""},
		{"json_valid(`document`)", "json_valid(`other`)"},
		{"utf8mb4_bin", "utf8mb4_general_ci"},
		{"tinyint(1)", "smallint(6)"},
		{"CHECK (json_valid(`document`))", "CHECK (json_valid(`document`) OR 1)"},
	} {
		changed := actual
		changed.DDL = strings.Replace(actual.DDL, change.from, change.to, 1)
		if err = Verify(desired, changed); err == nil {
			t.Fatalf("changed alias semantics verified: %s", change.to)
		}
	}
}
