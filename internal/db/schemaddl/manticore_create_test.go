package schemaddl

import (
	"fmt"
	"strings"
	"testing"
)

func TestManticoreCreateBindsAbsentNameAndNativeTypes(t *testing.T) {
	original := Snapshot{Engine: "manticoresearch", Table: Table{Name: "original"}}
	desired := Table{Name: "original", Clauses: []Clause{{ID: "id", Kind: "column", Name: "id", SQL: "bigint"}}}
	for _, typ := range []string{"integer", "bigint", "float", "bool", "timestamp", "string", "multi", "multi64", "json", "text"} {
		desired.Clauses = append(desired.Clauses, Clause{ID: "new:" + typ, Kind: "column", Name: "value_" + typ, SQL: typ})
	}
	p, err := PlanManticoreRT(original, desired, false)
	if err != nil || p.Mode != "native_create" || len(p.SQL) != 1 || !strings.HasPrefix(p.SQL[0], "CREATE TABLE `original` (`id` bigint,") || !strings.Contains(p.SQL[0], "`value_string` string attribute") || p.ScopeHint == "" {
		t.Fatal(p, err)
	}
	for _, mode := range []string{"existing", "foreign", "ddl", "original-options", "original-clauses", "wrong-target", "mixed-table", "mixed-field", "unsafe-table", "unsafe-column", "unsafe-type", "new-options", "missing-id", "id-type", "duplicate-name", "duplicate-id", "constraint", "column-properties", "table-properties", "definition", "too-many"} {
		t.Run(mode, func(t *testing.T) {
			o, d := original.Clone(), desired.Clone()
			switch mode {
			case "existing":
				o.Exists = true
			case "foreign":
				o.Engine = "sqlite"
			case "ddl":
				o.DDL = p.SQL[0]
			case "original-options":
				o.Table.Options = "type='pq'"
			case "original-clauses":
				o.Table.Clauses = d.Clauses
			case "wrong-target":
				d.Name = "other"
			case "mixed-table":
				o.Table.Name, d.Name = "Original", "Original"
			case "mixed-field":
				d.Clauses[1].Name = "Value_integer"
			case "unsafe-table":
				o.Table.Name, d.Name = "bad`;DROP TABLE victim", "bad`;DROP TABLE victim"
			case "unsafe-column":
				d.Clauses[1].Name = "field`"
			case "unsafe-type":
				d.Clauses[1].SQL = "integer); DROP TABLE victim"
			case "new-options":
				d.Options = "type='pq'"
			case "missing-id":
				d.Clauses = d.Clauses[1:]
			case "id-type":
				d.Clauses[0].SQL = "integer"
			case "duplicate-name":
				d.Clauses[1].Name = "id"
			case "duplicate-id":
				d.Clauses[1].ID = d.Clauses[0].ID
			case "constraint":
				d.Clauses[1].Kind = "constraint"
			case "column-properties":
				d.Clauses[1].Postgres = &PostgreSQLColumnProperties{}
			case "table-properties":
				d.Postgres = &PostgreSQLTableProperties{}
			case "definition":
				d.Definitions = []Definition{{Name: "foreign", Kind: "index", SQL: "CREATE INDEX foreign"}}
			case "too-many":
				for i := len(d.Clauses); i <= 512; i++ {
					name := fmt.Sprintf("field_%d", i)
					d.Clauses = append(d.Clauses, Clause{ID: name, Kind: "column", Name: name, SQL: "integer"})
				}
			}
			if got, err := PlanManticoreRT(o, d, false); err == nil {
				t.Fatal("unrepresented native creation admitted", got)
			}
		})
	}
}
