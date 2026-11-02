package mysqlddl

import (
	"reflect"
	"strings"
	"testing"
)

func TestStructuredConstraintsRoundTripAndSurgicalPlan(t *testing.T) {
	table, err := Parse("db", "items", publicDDL)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		raw  string
		kind Kind
	}{
		{"PRIMARY KEY (`id`)", Primary},
		{"UNIQUE KEY `idx` USING BTREE (`code`(10) DESC, `id`) COMMENT 'prefix, exact' INVISIBLE", Index},
		{"CONSTRAINT `symbol` UNIQUE KEY `idx` (`code`)", Index},
		{"FULLTEXT KEY `idx` (`code`)", Index},
		{"CONSTRAINT `fk` FOREIGN KEY (`id`, `code`) REFERENCES `elsewhere`.`parents` (`ref_id`,`ref_code`) ON UPDATE CASCADE ON DELETE RESTRICT", Foreign},
		{"CONSTRAINT `ck` CHECK ((`total` >= 0 AND `code` <> 'Case')) NOT ENFORCED", Check},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			form, err := ParseConstraint(tc.raw)
			if err != nil || form.Unsupported || form.Kind != tc.kind {
				t.Fatalf("parse: %+v %v", form, err)
			}
			raw, err := form.SQL(table)
			if err != nil {
				t.Fatal(err)
			}
			again, err := ParseConstraint(raw)
			if err != nil || !reflect.DeepEqual(form, again) {
				t.Fatalf("round trip lost properties:\n%+v\n%+v\n%s\n%v", form, again, raw, err)
			}
			copy := form.Clone()
			if len(copy.Columns) > 0 {
				copy.Columns[0].Name = "changed"
				if form.Columns[0].Name == "changed" {
					t.Fatal("column list not cloned")
				}
			}
			if len(copy.ReferencedColumns) > 0 {
				copy.ReferencedColumns[0] = "changed"
				if form.ReferencedColumns[0] == "changed" {
					t.Fatal("references not cloned")
				}
			}
		})
	}
	form, err := ParseConstraint(table.Clauses[5].SQL)
	if err != nil {
		t.Fatal(err)
	}
	form.Name, form.IndexKind = "renamed_index", "UNIQUE"
	form.Columns[0].Prefix = 12
	raw, err := form.SQL(table)
	if err != nil {
		t.Fatal(err)
	}
	draft := Draft{Table: table.Clone()}
	clause, _ := ParseClause(raw)
	clause.ID = draft.Table.Clauses[5].ID
	draft.Table.Clauses[5] = clause
	plan, err := Build(Snapshot{Database: "db", Table: "items", DDL: publicDDL}, draft)
	if err != nil || len(plan.Statements) != 1 || !strings.Contains(plan.Statements[0], "DROP INDEX `idx_code`") || !strings.Contains(plan.Statements[0], "ADD UNIQUE KEY `renamed_index` (`code`(12) DESC)") || strings.Contains(plan.Statements[0], "CHANGE COLUMN") {
		t.Fatalf("surgical rename: %+v %v", plan, err)
	}
}

func TestStructuredConstraintValidationRejectsInvalidFieldsAndRules(t *testing.T) {
	table, _ := Parse("db", "items", publicDDL)
	index, _ := ParseConstraint("KEY `new_index` (`code`)")
	fk, _ := ParseConstraint("CONSTRAINT `fk_new` FOREIGN KEY (`id`) REFERENCES `parents` (`id`)")
	check, _ := ParseConstraint("CONSTRAINT `check_new` CHECK (`id` > 0)")
	for _, tc := range []struct {
		name   string
		form   Constraint
		change func(*Constraint)
	}{
		{"missing column", index, func(c *Constraint) { c.Columns[0].Name = "missing" }},
		{"duplicate column", index, func(c *Constraint) { c.Columns = append(c.Columns, c.Columns[0]) }},
		{"negative prefix", index, func(c *Constraint) { c.Columns[0].Prefix = -1 }},
		{"numeric prefix", index, func(c *Constraint) { c.Columns[0] = IndexColumn{Name: "id", Prefix: 2} }},
		{"wrong references", fk, func(c *Constraint) { c.ReferencedColumns = nil }},
		{"foreign prefix", fk, func(c *Constraint) { c.Columns[0].Prefix = 2 }},
		{"nonnullable SET NULL", fk, func(c *Constraint) { c.OnDelete = "SET NULL" }},
		{"foreign action", fk, func(c *Constraint) { c.OnUpdate = "SET DEFAULT" }},
		{"self field reference", fk, func(c *Constraint) { c.ReferencedTable = "items" }},
		{"empty check", check, func(c *Constraint) { c.Expression = "" }},
		{"check statement injection", check, func(c *Constraint) { c.Expression = "1); DROP TABLE `items`; SELECT (1" }},
		{"check escaped boundary", check, func(c *Constraint) { c.Expression = "1) ENFORCED EXTRA (1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.form.Clone()
			tc.change(&c)
			if raw, err := c.SQL(table); err == nil {
				t.Fatalf("invalid form accepted: %s", raw)
			}
		})
	}
}

func TestUnknownConstraintAttributesRetainVerbatimAndBlockRewrites(t *testing.T) {
	for _, raw := range []string{
		"KEY `idx` ((lower(`code`)))",
		"KEY `idx` (`code`) /*!80000 INVISIBLE */",
		"FULLTEXT KEY `idx` (`code`) WITH PARSER ngram",
		"CONSTRAINT `fk` FOREIGN KEY `fk_index` (`id`) REFERENCES `parents` (`id`)",
	} {
		form, err := ParseConstraint(raw)
		if err != nil || !form.Unsupported {
			t.Fatalf("unknown properties silently lost: %+v %v", form, err)
		}
		ddl := "CREATE TABLE `t` (`id` bigint NOT NULL, `code` varchar(100), " + raw + ") ENGINE=InnoDB"
		table, err := Parse("db", "t", ddl)
		if err != nil {
			t.Fatal(err)
		}
		draft := Draft{Table: table.Clone()}
		field, _ := ParseColumn(draft.Table.Clauses[0].SQL)
		field.Type = "int"
		draft.Table.Clauses[0].SQL, _ = field.SQL()
		if _, err := Build(Snapshot{Database: "db", Table: "t", DDL: ddl}, draft); err != nil {
			t.Fatalf("unrelated field edit blocked: %v", err)
		}
		if draft.Table.Clauses[2].SQL != raw {
			t.Fatal("unknown constraint changed")
		}
		draft.Table.Clauses[2].SQL += " VISIBLE"
		if _, err := Build(Snapshot{Database: "db", Table: "t", DDL: ddl}, draft); err == nil {
			t.Fatal("unknown constraint was rewritten")
		}
	}
}

func TestConstraintVerificationAllowsOnlyKnownServerNormalization(t *testing.T) {
	for _, tc := range []struct {
		want, got string
		equal     bool
	}{
		{"KEY `idx` (`code` ASC)", "KEY `idx` (`code`) USING BTREE", true},
		{"KEY `idx` (`code` DESC)", "KEY `idx` (`code`)", false},
		{"KEY `idx` (`code`(10))", "KEY `idx` (`code`(12))", false},
		{"KEY `idx` (`code`) COMMENT 'Case'", "KEY `idx` (`code`) COMMENT 'case'", false},
		{"CONSTRAINT `ck` CHECK (`id` >= 0)", "CONSTRAINT `ck` CHECK (((`id` >= 0)))", true},
		{"CONSTRAINT `ck` CHECK (`id` >= 0)", "CONSTRAINT `ck` CHECK ((`id` > 0))", false},
		{"CONSTRAINT `fk` FOREIGN KEY (`id`) REFERENCES `parents` (`id`) ON DELETE NO ACTION", "CONSTRAINT `fk` FOREIGN KEY (`id`) REFERENCES `db`.`parents` (`id`)", true},
		{"CONSTRAINT `fk` FOREIGN KEY (`id`) REFERENCES `parents` (`id`) ON DELETE CASCADE", "CONSTRAINT `fk` FOREIGN KEY (`id`) REFERENCES `db`.`parents` (`id`)", false},
	} {
		want, err := ParseClause(tc.want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseClause(tc.got)
		if err != nil {
			t.Fatal(err)
		}
		if equal := constraintEquivalent(want, got, "db"); equal != tc.equal {
			t.Fatalf("normalization %q / %q: got %t want %t", tc.want, tc.got, equal, tc.equal)
		}
	}
}
