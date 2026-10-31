package mysqlddl

import (
	"strings"
	"testing"
)

const publicDDL = "CREATE TABLE `items` (\n" +
	"`id` bigint NOT NULL AUTO_INCREMENT,\n" +
	"`code` varchar(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin DEFAULT '' COMMENT 'a,b',\n" +
	"`total` decimal(65,30) NOT NULL DEFAULT '9007199254740993.0000000000000000001',\n" +
	"`generated` bigint GENERATED ALWAYS AS ((`id` + 1)) STORED,\n" +
	"PRIMARY KEY (`id`),\nKEY `idx_code` (`code`(10) DESC),\n" +
	"CONSTRAINT `fk_id` FOREIGN KEY (`id`) REFERENCES `other_db`.`parents` (`id`) ON DELETE RESTRICT,\n" +
	"CONSTRAINT `ck_total` CHECK ((`total` >= 0))\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin COMMENT='table'"

func TestFieldDefaultsGenerationAndPrefixSurviveSurgicalPlanning(t *testing.T) {
	table, e := Parse("db", "items", publicDDL)
	if e != nil {
		t.Fatal(e)
	}
	f, e := ParseColumn(table.Clauses[1].SQL)
	if e != nil || f.DefaultKind != LiteralDefault || f.Default != "" || f.Comment != "a,b" {
		t.Fatalf("lost field properties: %+v %v", f, e)
	}
	f.Type = "varchar(200)"
	raw, e := f.SQL()
	if e != nil {
		t.Fatal(e)
	}
	draft := Draft{Table: table.Clone()}
	draft.Table.Clauses[1].SQL = raw
	p, e := Build(Snapshot{Database: "db", Table: "items", DDL: publicDDL}, draft)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Statements) != 1 || !strings.Contains(p.Statements[0], "CHANGE COLUMN `code`") || strings.Contains(p.Statements[0], "DROP INDEX") {
		t.Fatalf("unexpected plan: %+v", p)
	}
	if draft.Table.Clauses[5].SQL != table.Clauses[5].SQL || draft.Table.Clauses[3].SQL != table.Clauses[3].SQL {
		t.Fatal("unmodified prefix/generated definition changed")
	}
	for _, test := range []struct {
		raw   string
		kind  DefaultKind
		value string
	}{{"`a` text", NoDefault, ""}, {"`a` text DEFAULT NULL", NullDefault, ""}, {"`a` text DEFAULT 'NULL'", LiteralDefault, "NULL"}, {"`a` timestamp DEFAULT CURRENT_TIMESTAMP(6)", ExpressionDefault, "CURRENT_TIMESTAMP(6)"}} {
		f, e := ParseColumn(test.raw)
		if e != nil || f.DefaultKind != test.kind || f.Default != test.value {
			t.Fatalf("default mode lost: %+v %v", f, e)
		}
	}
}
func TestUnknownDefinitionsAndPartitionsBlockLossyEdits(t *testing.T) {
	raw := "CREATE TABLE `t` (`id` int NOT NULL,`secret` int /*!80023 INVISIBLE */) ENGINE=InnoDB PARTITION BY HASH (`id`) PARTITIONS 2"
	table, e := Parse("db", "t", raw)
	if e != nil {
		t.Fatal(e)
	}
	draft := Draft{Table: table.Clone()}
	f, _ := ParseColumn(draft.Table.Clauses[1].SQL)
	if !f.Unsupported {
		t.Fatal("executable version attribute discarded")
	}
	draft.Table.Clauses[1].SQL = "`secret` bigint"
	if _, e = Build(Snapshot{Database: "db", Table: "t", DDL: raw}, draft); e == nil {
		t.Fatal("unsupported field rewritten")
	}
	draft = Draft{Table: table.Clone()}
	draft.Table.Clauses = draft.Table.Clauses[1:]
	if _, e = Build(Snapshot{Database: "db", Table: "t", DDL: raw}, draft); e == nil {
		t.Fatal("partition dependency dropped")
	}
	options, e := SetOption(table.Options, "COMMENT", "new 'comment'")
	if e != nil || !strings.Contains(options, "PARTITION BY HASH (`id`) PARTITIONS 2") {
		t.Fatalf("partition lost: %s %v", options, e)
	}
	draft = Draft{Table: table.Clone()}
	draft.Table.Options = options
	p, e := Build(Snapshot{Database: "db", Table: "t", DDL: raw}, draft)
	if e != nil || len(p.Statements) != 1 || !strings.Contains(p.Statements[0], "COMMENT='new ''comment''' ") {
		if e != nil || !strings.Contains(strings.Join(p.Statements, ""), "COMMENT='new ''comment'''") {
			t.Fatalf("surgical option edit failed: %+v %v", p, e)
		}
	}
}
func TestConstraintsAreMergedAndMissingDependenciesRejected(t *testing.T) {
	table, _ := Parse("db", "items", publicDDL)
	draft := Draft{Table: table.Clone()}
	draft.Table.Clauses[5].SQL = "UNIQUE KEY `idx_code` (`code`(12) ASC)"
	draft.Table.Clauses[7].SQL = "CONSTRAINT `ck_total` CHECK ((`total` > 0))"
	p, e := Build(Snapshot{Database: "db", Table: "items", DDL: publicDDL}, draft)
	if e != nil || len(p.Statements) != 1 || !strings.Contains(p.Statements[0], "DROP INDEX `idx_code`") || !strings.Contains(p.Statements[0], "DROP CHECK `ck_total`") {
		t.Fatalf("constraints not merged: %+v %v", p, e)
	}
	draft.Table.Clauses = append(draft.Table.Clauses[:1], draft.Table.Clauses[2:]...)
	if _, e = Build(Snapshot{Database: "db", Table: "items", DDL: publicDDL}, draft); e == nil {
		t.Fatal("dangling index reference accepted")
	}
}
func TestTriggerQualificationFingerprintAndVerification(t *testing.T) {
	table, _ := Parse("db", "items", publicDDL)
	trigger := Trigger{Name: "audit", SQL: "CREATE TRIGGER `audit` BEFORE INSERT ON `items` FOR EACH ROW SET NEW.`code` = 'NULL'"}
	p, e := Build(Snapshot{Database: "db", Table: "items", DDL: publicDDL}, Draft{Table: table, Triggers: []Trigger{trigger}})
	if e != nil || len(p.Statements) != 1 || !strings.Contains(p.Statements[0], "TRIGGER `db`.`audit`") || !strings.Contains(p.Statements[0], "ON `db`.`items`") {
		t.Fatalf("unqualified trigger: %+v %v", p, e)
	}
	got := trigger
	got.SQL = strings.Replace(got.SQL, "CREATE TRIGGER", "CREATE DEFINER=`user`@`localhost` TRIGGER", 1)
	if !triggerEquivalent(trigger, got, "db", "items") {
		t.Fatal("server-inserted definer not normalized")
	}
	got.SQL = strings.Replace(got.SQL, "'NULL'", "'null'", 1)
	if triggerEquivalent(trigger, got, "db", "items") {
		t.Fatal("literal case difference ignored")
	}
	original := Snapshot{DDL: publicDDL, Triggers: []Trigger{trigger}}
	other := original
	other.Triggers = []Trigger{{Name: trigger.Name, SQL: trigger.SQL, SQLMode: "NO_BACKSLASH_ESCAPES"}}
	if original.Fingerprint() == other.Fingerprint() {
		t.Fatal("trigger SQL mode absent from fingerprint")
	}
}
func TestNewTableVerificationAllowsServerDefaultsButChecksOrderAndValues(t *testing.T) {
	table, _ := Parse("db", "t", "CREATE TABLE `t` (`id` bigint NOT NULL, `value` text DEFAULT 'Case') ENGINE=InnoDB")
	draft := Draft{Table: table, New: true}
	actual := Snapshot{Database: "db", Table: "t", DDL: "CREATE TABLE `t` (`id` bigint NOT NULL, `value` text DEFAULT 'Case') ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"}
	if e := Verify(draft, actual); e != nil {
		t.Fatal(e)
	}
	actual.DDL = strings.Replace(actual.DDL, "'Case'", "'case'", 1)
	if Verify(draft, actual) == nil {
		t.Fatal("literal default difference ignored")
	}
	actual.DDL = "CREATE TABLE `t` (`value` text DEFAULT 'Case', `id` bigint NOT NULL) ENGINE=InnoDB"
	if Verify(draft, actual) == nil {
		t.Fatal("field order not verified")
	}
}

func TestTriggerFingerprintAndVerificationRetainExecutionOrder(t *testing.T) {
	a := Trigger{Name: "z_first", SQL: "CREATE TRIGGER z_first BEFORE INSERT ON items FOR EACH ROW SET NEW.id=1"}
	b := Trigger{Name: "a_second", SQL: "CREATE TRIGGER a_second BEFORE INSERT ON items FOR EACH ROW SET NEW.id=2"}
	original := Snapshot{Triggers: []Trigger{a, b}}
	reordered := Snapshot{Triggers: []Trigger{b, a}}
	if original.Fingerprint() == reordered.Fingerprint() {
		t.Fatal("execution order vanished from the structure fingerprint")
	}
	if triggerOrderEquivalent(original.Triggers, reordered.Triggers) {
		t.Fatal("execution order change was accepted")
	}
}

func TestTriggerReorderingIsBlockedBeforeExecution(t *testing.T) {
	table, e := Parse("db", "items", publicDDL)
	if e != nil {
		t.Fatal(e)
	}
	a := Trigger{Name: "z_first", SQL: "CREATE TRIGGER z_first BEFORE INSERT ON items FOR EACH ROW SET NEW.code='one'"}
	b := Trigger{Name: "a_second", SQL: "CREATE TRIGGER a_second BEFORE INSERT ON items FOR EACH ROW SET NEW.code='two'"}
	original := Snapshot{Database: "db", Table: "items", DDL: publicDDL, Triggers: []Trigger{a, b}}
	edited := a
	edited.SQL = "CREATE TRIGGER z_first BEFORE INSERT ON items FOR EACH ROW SET NEW.code='edited'"
	if _, e = Build(original, Draft{Table: table, Triggers: []Trigger{edited, b}}); e == nil {
		t.Fatal("editing the first trigger silently reordered its execution")
	}
	if _, e = Build(original, Draft{Table: table, Triggers: []Trigger{b, a}}); e == nil {
		t.Fatal("unsupported explicit trigger reordering was accepted")
	}
	last := b
	last.SQL = "CREATE TRIGGER a_second BEFORE INSERT ON items FOR EACH ROW SET NEW.code='edited'"
	if _, e = Build(original, Draft{Table: table, Triggers: []Trigger{a, last}}); e != nil {
		t.Fatalf("order-preserving last-trigger edit was rejected: %v", e)
	}
}
