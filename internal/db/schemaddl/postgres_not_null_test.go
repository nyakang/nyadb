package schemaddl

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPostgreSQL18NotNullBelongsToStableColumn(t *testing.T) {
	p, names := pgGraphFixture()
	p.ServerVersion = 180000
	p.Entries = append(p.Entries, pgRecord("constraint", 1100, 0, `{"oid":1100,"conrelid":700,"conname":"original_not_null","contype":"n","conkey":[3]}`))
	b, err := BuildPostgreSQLGraphBindings(p, names)
	if err != nil || b.Objects[PostgreSQLCatalogObject{2606, 1100}] != "@notnull:@column:value-stable" {
		t.Fatal("NOT NULL did not bind to its column", err)
	}
	q := p.Clone()
	pgGraphChange(t, q, "constraint", 1100, "conkey", []int{2})
	if _, err := BuildPostgreSQLGraphBindings(*q, names); err == nil {
		t.Fatal("dropped attribute gained a NOT NULL identity")
	}
	catalog := pgCatalogFixture()
	catalog.ServerVersion = 180000
	catalog.Entries = append(catalog.Entries, pgRecord("constraint", 55, 0, `{"relation_oid":16,"name":"named_nn","definition":"NOT NULL id","contype":"n"}`))
	table, err := catalog.Table()
	if err != nil {
		t.Fatal(err)
	}
	ddl, err := PostgreSQLStructuralDDL(table, "demo")
	if err != nil || strings.Contains(ddl, "named_nn") || strings.Count(ddl, "NOT NULL") != 1 {
		t.Fatal("duplicated NOT NULL declaration", ddl, err)
	}
}

func TestPostgreSQL18NotNullReadOnlyAndOriginalName(t *testing.T) {
	p := pgCatalogFixture()
	p.ServerVersion = 180000
	p.Entries = append(p.Entries, pgRecord("constraint", 55, 0, `{"conrelid":16,"conkey":[1],"conname":"named_nn","contype":"n","comment":"retained","convalidated":true,"conenforced":true,"conislocal":true,"coninhcount":0,"conparentid":0,"connoinherit":false}`))
	if err := PostgreSQLNotNullEditable(&p, "column:1"); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"convalidated", "conenforced", "conislocal"} {
		q := p.Clone()
		pgGraphChange(t, q, "constraint", 55, field, false)
		if !errors.Is(PostgreSQLNotNullEditable(q, "column:1"), ErrPostgreSQLNotNull) {
			t.Fatal("unsupported NOT NULL became editable", field)
		}
	}
	table, err := p.Table()
	if err != nil {
		t.Fatal(err)
	}
	q := p.Clone()
	pgGraphChange(t, q, "constraint", 55, "conname", "other")
	if pgVerifyNotNullNames(&p, q, table) == nil {
		t.Fatal("original name was discarded")
	}
	q = p.Clone()
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(q.Entries[len(q.Entries)-1].Data, &fields)
	fields["conkey"] = json.RawMessage(`[1,2]`)
	q.Entries[len(q.Entries)-1].Data, _ = json.Marshal(fields)
	if PostgreSQLNotNullEditable(q, "column:1") == nil {
		t.Fatal("ambiguous ownership accepted")
	}
}
