package mysqlddl

import (
	"strings"
	"testing"
)

func TestTiDBDesignerPreservesExecutableAttributes(t *testing.T) {
	original := Snapshot{Engine: "tidb", Database: "db", Table: "t", DDL: "CREATE TABLE `t` (`id` bigint NOT NULL /*T![auto_rand] AUTO_RANDOM(5) */, `note` varchar(20), PRIMARY KEY (`id`) /*T![clustered_index] CLUSTERED */, CONSTRAINT `ck` CHECK (`id` > 0)) ENGINE=InnoDB /*T! SHARD_ROW_ID_BITS=0 */"}
	table, err := Parse("db", "t", original.DDL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Build(original, Draft{Table: table}); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []string{
		strings.Replace(original.DDL, "AUTO_RANDOM(5)", "AUTO_RANDOM(6)", 1),
		strings.Replace(original.DDL, "CLUSTERED */", "NONCLUSTERED */", 1),
		strings.Replace(original.DDL, "SHARD_ROW_ID_BITS=0", "SHARD_ROW_ID_BITS=4", 1),
	} {
		next, e := Parse("db", "t", changed)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = Build(original, Draft{Table: next}); e == nil {
			t.Fatal("TiDB attributes changed", changed)
		}
		if e = Verify(Draft{Table: table}, Snapshot{Engine: "tidb", Database: "db", Table: "t", DDL: changed}); e == nil {
			t.Fatal("TiDB attribute loss verified")
		}
	}
	var kept []Clause
	for _, c := range table.Clauses {
		if c.Kind != Check {
			kept = append(kept, c)
		}
	}
	table.Clauses = kept
	plan, err := Build(original, Draft{Table: table})
	if err != nil || len(plan.Statements) != 1 || !strings.Contains(plan.Statements[0], "DROP CONSTRAINT `ck`") {
		t.Fatal(plan, err)
	}
}

func TestTiDBDesignerRejectsIgnoredIndexAndFieldSemantics(t *testing.T) {
	for _, clause := range []string{"KEY `ix` (`id` DESC)", "FULLTEXT KEY `ix` (`id`)", "KEY `ix` (`id`) USING HASH", "`geo` MULTIPOLYGON"} {
		table, err := Parse("db", "t", "CREATE TABLE `t` (`id` int, "+clause+") ENGINE=InnoDB")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = Build(Snapshot{Engine: "tidb", Database: "db", Table: "t"}, Draft{New: true, Table: table}); err == nil {
			t.Fatal("unsupported TiDB clause accepted", clause)
		}
	}
}

func TestTiDBDesignerVerifiesWidthsAndClusteredDefault(t *testing.T) {
	table, err := Parse("db", "t", "CREATE TABLE `t` (`id` bigint NOT NULL, PRIMARY KEY (`id`)) ENGINE=InnoDB")
	if err != nil {
		t.Fatal(err)
	}
	actual := Snapshot{Engine: "tidb", Database: "db", Table: "t", DDL: "CREATE TABLE `t` (`id` bigint(20) NOT NULL, PRIMARY KEY (`id`) /*T![clustered_index] CLUSTERED */) ENGINE=InnoDB"}
	if err = Verify(Draft{New: true, Table: table}, actual); err != nil {
		t.Fatal(err)
	}
	actual.DDL = strings.Replace(actual.DDL, "bigint(20)", "bigint(20) unsigned", 1)
	if err = Verify(Draft{New: true, Table: table}, actual); err == nil {
		t.Fatal("unsigned change hidden")
	}
}
