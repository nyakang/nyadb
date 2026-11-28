package schemaddl

import (
	"reflect"
	"strings"
	"testing"
)

func manticoreOriginal(t *testing.T) Snapshot {
	t.Helper()
	ddl := `CREATE TABLE rt_original (
 id bigint,
 title text,
 qty integer,
 tag string attribute
 ) min_word_len='2' exceptions='escaped\'comma,paren)'`
	table, e := ParseManticoreRT(ddl, "rt_original")
	if e != nil {
		t.Fatal(e)
	}
	return Snapshot{Engine: "manticoresearch", Exists: true, DDL: ddl, Table: table}
}
func TestManticoreNativeDeclarationOptionsAndSingleColumnPlans(t *testing.T) {
	original := manticoreOriginal(t)
	if !strings.Contains(original.Table.Options, `escaped\'comma,paren)`) {
		t.Fatal(original.Table)
	}
	for _, typ := range []string{"integer", "bigint", "float", "bool", "timestamp", "string", "multi", "multi64", "json", "text"} {
		t.Run(typ, func(t *testing.T) {
			desired := original.Table.Clone()
			desired.Clauses = append(desired.Clauses, Clause{ID: "new:field", Kind: "column", Name: "added", SQL: typ})
			p, e := PlanManticoreRT(original, desired, true)
			if e != nil || p.Mode != "native_add" || len(p.SQL) != 1 || !strings.HasPrefix(p.SQL[0], "ALTER TABLE `rt_original` ADD COLUMN `added` ") {
				t.Fatal(p, e)
			}
			actual := desired.Clone()
			actual.Clauses[len(actual.Clauses)-1].ID = "native:added"
			if typ == "string" {
				actual.Clauses[len(actual.Clauses)-1].SQL = "string attribute"
			}
			if typ == "text" {
				c := actual.Clauses[len(actual.Clauses)-1]
				actual.Clauses = append(actual.Clauses[:2:2], append([]Clause{c}, actual.Clauses[2:len(actual.Clauses)-1]...)...)
			}
			if e = VerifyManticoreRT(desired, actual); e != nil {
				t.Fatal(e)
			}
			actual.Options = ""
			if VerifyManticoreRT(desired, actual) == nil {
				t.Fatal("lost original options accepted")
			}
		})
	}
	desired := original.Table.Clone()
	desired.Clauses = append(desired.Clauses[:2:2], desired.Clauses[3:]...)
	p, e := PlanManticoreRT(original, desired, true)
	if e != nil || p.Mode != "native_drop" || !reflect.DeepEqual(p.SQL, []string{"ALTER TABLE `rt_original` DROP COLUMN `qty`"}) || !reflect.DeepEqual(p.Dropped, []string{"qty"}) {
		t.Fatal(p, e)
	}
	if _, e = PlanManticoreRT(original, desired, false); e == nil {
		t.Fatal("unapproved column removal")
	}
	p, e = PlanManticoreRT(original, original.Table.Clone(), true)
	if e != nil || p.Mode != "noop" || len(p.SQL) != 0 {
		t.Fatal(p, e)
	}
}
func TestManticorePlanRejectsUnrepresentedOrMultipleChanges(t *testing.T) {
	for _, mode := range []string{"rename", "option", "type", "move", "drop-id", "change-id", "duplicate-name", "duplicate-id", "extra-operation", "constraint", "foreign-properties", "unsafe-name", "unsafe-body", "new-attribute-position", "forged-original"} {
		t.Run(mode, func(t *testing.T) {
			original := manticoreOriginal(t)
			desired := original.Table.Clone()
			extra := Clause{ID: "new:added", Kind: "column", Name: "added", SQL: "integer"}
			switch mode {
			case "rename":
				desired.Name = "new_name"
			case "option":
				desired.Options = ""
			case "type":
				desired.Clauses[2].SQL = "bigint"
			case "move":
				desired.Clauses[2], desired.Clauses[3] = desired.Clauses[3], desired.Clauses[2]
			case "drop-id":
				desired.Clauses = desired.Clauses[1:]
			case "change-id":
				desired.Clauses[0].Name = "changed"
			case "duplicate-name":
				extra.Name = "QTY"
				desired.Clauses = append(desired.Clauses, extra)
			case "duplicate-id":
				extra.ID = desired.Clauses[1].ID
				desired.Clauses = append(desired.Clauses, extra)
			case "extra-operation":
				desired.Clauses = append(desired.Clauses[:3], extra)
			case "constraint":
				extra.Kind = "constraint"
				desired.Clauses = append(desired.Clauses, extra)
			case "foreign-properties":
				desired.Postgres = &PostgreSQLTableProperties{}
			case "unsafe-name":
				extra.Name = "a`;DROP TABLE victim"
				desired.Clauses = append(desired.Clauses, extra)
			case "unsafe-body":
				extra.SQL = "integer; DROP TABLE victim"
				desired.Clauses = append(desired.Clauses, extra)
			case "new-attribute-position":
				desired.Clauses = append(desired.Clauses[:2:2], append([]Clause{extra}, desired.Clauses[2:]...)...)
			case "forged-original":
				original.Table.Clauses[2].SQL = "bigint"
			}
			if p, e := PlanManticoreRT(original, desired, true); e == nil {
				t.Fatal(mode, p)
			}
		})
	}
}
func TestManticoreDeclarationRejectsAmbiguousOrUnsupportedSyntax(t *testing.T) {
	for _, ddl := range []string{
		"CREATE TABLE rt_original (id bigint, title text); DROP TABLE other",
		"CREATE TABLE rt_original (id bigint, title text) min_word_len='unterminated",
		"CREATE TABLE rt_original (id bigint, TITLE text, title text)",
		"CREATE TABLE rt_original (id bigint, title text indexed)",
		"CREATE TABLE rt_original (id bigint, value double)",
		"CREATE TABLE rt_original (id bigint, title text) trailing",
		"CREATE TABLE rt_original (id bigint, title text /* hidden */)",
		"CREATE TABLE rt_original (id bigint, title text,)",
		"CREATE TABLE rt_original (id bigint, title text) a='x' b=bare",
		"CREATE TABLE other (id bigint, title text)",
	} {
		if _, e := ParseManticoreRT(ddl, "rt_original"); e == nil {
			t.Fatal(ddl)
		}
	}
}

func TestManticoreTablePlansRequireCompleteOriginalRT(t *testing.T) {
	original := manticoreOriginal(t)
	for _, op := range []string{"drop", "truncate"} {
		p, e := PlanManticoreRTTableOperation(original, op)
		if e != nil || len(p.SQL) != 1 || p.ScopeHint == "" || !strings.HasSuffix(p.SQL[0], "TABLE `rt_original`") {
			t.Fatal(p, e)
		}
		for _, mode := range []string{"absent", "mode", "ddl", "table", "foreign"} {
			v := original.Clone()
			switch mode {
			case "absent":
				v.Exists = false
			case "mode":
				v.Engine = "mysql"
			case "ddl":
				v.DDL += "; DROP TABLE other"
			case "table":
				v.Table.Name = "other"
			case "foreign":
				v.Postgres = &PostgreSQLCatalog{}
			}
			if p, e = PlanManticoreRTTableOperation(v, op); e == nil {
				t.Fatal(mode, p)
			}
		}
	}
	for _, op := range []string{"delete", "clear", "rename", "DROP", "truncate; drop"} {
		if p, e := PlanManticoreRTTableOperation(original, op); e == nil {
			t.Fatal(p)
		}
	}
}
