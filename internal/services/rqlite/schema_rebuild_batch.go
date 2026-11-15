package rqlite

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func planSchemaDocument(original contract.SchemaDesignSnapshot, desired schemaddl.Table, drop bool, guard, scope string) (schemaddl.Plan, error) {
	if err := schemaWriteBounds(original, &desired); err != nil {
		return schemaddl.Plan{}, err
	}
	plan, err := schemaddl.PlanSQLiteNative(original.Schema, desired, "main", drop)
	rebuild := errors.Is(err, schemaddl.ErrUnsupported)
	if err == nil && plan.Mode == "native_columns" {
		old := map[string]bool{}
		for _, c := range original.Schema.Table.Columns() {
			old[c.ID] = true
		}
		for _, c := range desired.Columns() {
			if !old[c.ID] && !schemaddl.SQLiteNativeAddition(c) {
				rebuild = true
			}
		}
	}
	if !rebuild {
		return plan, err
	}
	plan, err = schemaddl.PlanSQLite(original.Schema, desired, "main", drop)
	if err != nil || plan.Mode != "rebuild" {
		return plan, err
	}
	batch, err := schemaRebuildBatch(original, desired, guard, scope, 0, nil)
	if err != nil {
		return schemaddl.Plan{}, err
	}
	plan.SQL = slices.Clone(batch.statements)
	return plan, nil
}

func schemaRebuildBatch(original contract.SchemaDesignSnapshot, desired schemaddl.Table, guard, scope string, sequence uint64, viewChecks []string) (schemaBatch, error) {
	b := schemaBatch{guards: map[int]bool{}, dataGuards: map[int]bool{}}
	stem := fmt.Sprintf("__nyadb_rebuild_%s_%d", scope[:16], sequence)
	p, err := buildSchemaRebuildProgram(original, desired, stem)
	if err != nil {
		return b, err
	}
	name := schemaddl.Quote(stem + "_guard")
	table := "temp." + name
	b.statements = append(b.statements, "CREATE TEMP TABLE "+table+"(ok INTEGER NOT NULL CHECK(ok=1),original_rows INTEGER,schema_version INTEGER,type_hex TEXT,name_hex TEXT,table_hex TEXT,rootpage INTEGER,sql_hex TEXT,sql_null INTEGER,identity_phase TEXT,identity_count INTEGER,identity_rowid INTEGER,identity_name_type TEXT,identity_name_hex TEXT,identity_value_type TEXT,identity_value_hex TEXT,wfk INTEGER,wdefer INTEGER,wlegacy INTEGER,ctables INTEGER,cindexes INTEGER,ctriggers INTEGER,cviews INTEGER,cimplicit INTEGER)")
	addGuard := func(condition string, data bool) {
		at := len(b.statements)
		b.guards[at] = true
		if data {
			b.dataGuards[at] = true
		}
		b.statements = append(b.statements, "INSERT INTO "+table+"(ok) SELECT ("+condition+")")
	}
	b.guards[len(b.statements)] = true
	b.statements = append(b.statements, "INSERT INTO "+table+"(ok,original_rows,wfk,wdefer,wlegacy) SELECT ("+guard+"),(SELECT count(*) FROM main."+schemaddl.Quote(desired.Name)+"),(SELECT foreign_keys FROM pragma_foreign_keys),(SELECT defer_foreign_keys FROM pragma_defer_foreign_keys),(SELECT legacy_alter_table FROM pragma_legacy_alter_table)")
	addGuard("NOT EXISTS(SELECT 1 FROM temp.sqlite_schema WHERE type IN ('view','trigger'))", false)
	addGuard("(SELECT foreign_keys FROM pragma_foreign_keys)=1 AND (SELECT defer_foreign_keys FROM pragma_defer_foreign_keys)=0", false)
	addGuard("NOT EXISTS(SELECT 1 FROM pragma_foreign_key_check)", false)
	if p.sequenceBefore && schemaddl.SQLiteAutoIncrement(desired) && !schemaddl.SQLiteAutoIncrement(original.Schema.Table) {
		addGuard("NOT EXISTS(SELECT 1 FROM main.sqlite_sequence WHERE name="+schemaTextLiteral(desired.Name)+")", false)
	}
	if p.sequenceBefore && schemaddl.SQLiteAutoIncrement(original.Schema.Table) && schemaddl.SQLiteAutoIncrement(desired) {
		addGuard("(SELECT count(*) FROM main.sqlite_sequence WHERE name="+schemaTextLiteral(desired.Name)+")<=1 AND NOT EXISTS(SELECT 1 FROM main.sqlite_sequence WHERE name="+schemaTextLiteral(desired.Name)+" AND typeof(seq)<>'integer')", false)
	}
	before, err := schemaRebuildProgramChecks(p, original.Schema.Table, original.Schema.SQLite, viewChecks)
	if err != nil {
		return b, err
	}
	b.statements = append(b.statements, before...)
	b.statements = append(b.statements, (&schemaClearProgram{sequence: p.sequenceBefore}).capture(table, "before")...)
	b.statements = append(b.statements, p.beforeSQL...)
	b.statements = append(b.statements, p.changeSQL...)
	after, err := schemaRebuildProgramChecks(p, desired, original.Schema.SQLite, viewChecks)
	if err != nil {
		return b, err
	}
	b.statements = append(b.statements, after...)
	addGuard("NOT EXISTS(SELECT 1 FROM pragma_foreign_key_check)", false)
	for _, check := range p.checks {
		if check.sql != "" {
			b.statements = append(b.statements, check.sql)
		} else {
			addGuard(check.condition, check.data)
		}
	}
	b.statements = append(b.statements, p.cleanupSQL...)
	addGuard("(SELECT count(*) FROM main."+schemaddl.Quote(desired.Name)+")=(SELECT original_rows FROM "+table+" WHERE original_rows IS NOT NULL)", true)
	b.statements = append(b.statements, "PRAGMA defer_foreign_keys=OFF")
	addGuard("(SELECT foreign_keys FROM pragma_foreign_keys)=(SELECT wfk FROM "+table+" WHERE wfk IS NOT NULL) AND (SELECT defer_foreign_keys FROM pragma_defer_foreign_keys)=0 AND (SELECT legacy_alter_table FROM pragma_legacy_alter_table)=(SELECT wlegacy FROM "+table+" WHERE wlegacy IS NOT NULL)", false)
	b.statements = append(b.statements, (&schemaClearProgram{sequence: p.sequenceAfter}).capture(table, "after")...)
	b.identities = len(b.statements)
	b.statements = append(b.statements, "DELETE FROM "+name+" WHERE identity_phase IS NOT NULL RETURNING identity_phase,identity_count,identity_rowid,identity_name_type,identity_name_hex,identity_value_type,identity_value_hex")
	b.rebuildState = len(b.statements)
	b.statements = append(b.statements, "DELETE FROM "+name+" WHERE original_rows IS NOT NULL RETURNING original_rows,wfk,wdefer,wlegacy")
	b.statements = append(b.statements, "INSERT INTO "+table+"(ok,ctables,cindexes,ctriggers,cviews,cimplicit) SELECT 1,sum(type='table'),sum(type='index'),sum(type='trigger'),sum(type='view'),sum(type='index' AND sql IS NULL) FROM main.sqlite_schema")
	b.rebuildCounts = len(b.statements)
	b.statements = append(b.statements, "DELETE FROM "+name+" WHERE ctables IS NOT NULL RETURNING ctables,cindexes,ctriggers,cviews,cimplicit")
	b.statements = append(b.statements, "INSERT INTO "+table+"(ok,schema_version) SELECT 1,schema_version FROM pragma_schema_version", "INSERT INTO "+table+"(ok,type_hex,name_hex,table_hex,rootpage,sql_hex,sql_null) SELECT 1,hex(type),hex(name),hex(tbl_name),rootpage,hex(coalesce(sql,'')),sql IS NULL FROM main.sqlite_schema")
	b.catalog = len(b.statements)
	b.statements = append(b.statements, "DELETE FROM "+name+" WHERE schema_version IS NOT NULL RETURNING schema_version", "DELETE FROM "+name+" WHERE type_hex IS NOT NULL RETURNING type_hex,name_hex,table_hex,rootpage,sql_hex,sql_null", "DROP TABLE "+table)
	raw, err := json.Marshal(b.statements)
	if err != nil || len(raw) > maxRequestBytes || len(b.statements) > 1024 {
		return schemaBatch{}, errors.Join(contract.ErrUnsupported, errors.New("complete reconstruction request exceeds native bounds"))
	}
	return b, nil
}

// Original view columns are read before dispatch and protected by the complete
// original catalog guard in the write. The preview remains entirely pure.
func (s *Session) schemaRebuildViewChecks(ctx context.Context, catalog *schemaddl.SQLiteCatalog) ([]string, error) {
	views := map[string]bool{}
	for _, e := range catalog.Entries {
		if e.Type == "view" {
			views[e.Name] = true
		}
	}
	needed := false
	for _, e := range catalog.Entries {
		needed = needed || e.Type == "trigger" && views[e.Table]
	}
	if !needed {
		return nil, nil
	}
	r, err := s.request(ctx, "/db/query", []any{"SELECT hex(s.name),x.cid,hex(x.name) FROM main.sqlite_schema s JOIN pragma_table_xinfo(s.name) x ON 1=1 WHERE s.type='view' ORDER BY s.name COLLATE BINARY,x.cid"}, true, false)
	if err != nil {
		return nil, err
	}
	rows, err := decodedRows(r, 0, 3)
	if err != nil {
		return nil, err
	}
	columns := map[string][]string{}
	for _, row := range rows {
		name, err := schemaCatalogText(row[0])
		if err != nil || !views[name] {
			return nil, errIncomplete
		}
		cid, err := cellInt(row[1])
		if err != nil || cid != int64(len(columns[name])) {
			return nil, errIncomplete
		}
		column, err := schemaCatalogText(row[2])
		if err != nil || schemaddl.Identifier(column) != nil {
			return nil, errIncomplete
		}
		columns[name] = append(columns[name], column)
	}
	var checks []string
	for _, e := range catalog.Entries {
		if e.Type != "trigger" || !views[e.Table] {
			continue
		}
		if len(columns[e.Table]) == 0 {
			return nil, errIncomplete
		}
		event, err := schemaddl.SQLiteTriggerEvent(e.SQL)
		if err != nil {
			return nil, err
		}
		target := "main." + schemaddl.Quote(e.Table)
		switch event {
		case "insert":
			checks = append(checks, "EXPLAIN INSERT INTO "+target+" DEFAULT VALUES")
		case "delete":
			checks = append(checks, "EXPLAIN DELETE FROM "+target+" WHERE 0")
		case "update":
			names, err := schemaddl.SQLiteTriggerUpdateColumns(e.SQL)
			if err != nil {
				return nil, err
			}
			if len(names) == 0 {
				names = columns[e.Table]
			}
			var updates []string
			for _, name := range names {
				if !copyColumnPresent(columns[e.Table], name) {
					return nil, errors.Join(contract.ErrUnsupported, errors.New("view trigger UPDATE OF references a missing original column"))
				}
				quoted := schemaddl.Quote(name)
				updates = append(updates, quoted+"="+quoted)
			}
			checks = append(checks, "EXPLAIN UPDATE "+target+" SET "+strings.Join(updates, ",")+" WHERE 0")
		}
	}
	return checks, nil
}

func verifySchemaRebuildState(native receipt, batch schemaBatch, actual *schemaddl.SQLiteCatalog, original contract.SchemaDesignSnapshot, desired schemaddl.Table) (int64, error) {
	if batch.rebuildState < 0 || batch.rebuildState >= len(native.Results) || !slices.Equal(native.Results[batch.rebuildState].Columns, []string{"original_rows", "wfk", "wdefer", "wlegacy"}) {
		return 0, errIncomplete
	}
	rows, err := decodedRows(native, batch.rebuildState, 4)
	if err != nil || len(rows) != 1 {
		return 0, errIncomplete
	}
	n, err := cellInt(rows[0][0])
	if err != nil || n < 0 {
		return 0, errIncomplete
	}
	for i := 1; i < 4; i++ {
		value, err := cellInt(rows[0][i])
		if err != nil || i == 1 && value != 1 || i == 2 && value != 0 || i == 3 && value != 0 && value != 1 {
			return 0, errIncomplete
		}
	}
	if batch.rebuildCounts < 0 || batch.rebuildCounts >= len(native.Results) || !slices.Equal(native.Results[batch.rebuildCounts].Columns, []string{"ctables", "cindexes", "ctriggers", "cviews", "cimplicit"}) {
		return 0, errIncomplete
	}
	counts, err := decodedRows(native, batch.rebuildCounts, 5)
	if err != nil || len(counts) != 1 {
		return 0, errIncomplete
	}
	var expected [5]int64
	for _, e := range actual.Entries {
		switch e.Type {
		case "table":
			expected[0]++
		case "index":
			expected[1]++
			if e.SQLNull {
				expected[4]++
			}
		case "trigger":
			expected[2]++
		case "view":
			expected[3]++
		}
	}
	for i, want := range expected {
		got, err := cellInt(counts[0][i])
		if err != nil || got != want {
			return 0, errIncomplete
		}
	}
	return n, verifySchemaRebuildIdentities(native, batch.identities, original, desired, n)
}

func verifySchemaRebuildIdentities(native receipt, index int, original contract.SchemaDesignSnapshot, desired schemaddl.Table, originalRows int64) error {
	if err := verifySchemaClearIdentities(native, index); err != nil {
		return err
	}
	type frame struct{ nameType, nameHex, valueType, valueHex string }
	before, after := map[int64]frame{}, map[int64]frame{}
	for _, raw := range native.Results[index].Values {
		row, err := native.Results[index].cells(raw)
		if err != nil {
			return errIncomplete
		}
		if row[1].Kind != contract.Null {
			continue
		}
		id, _ := cellInt(row[2])
		f := frame{row[3].Text, strings.ToUpper(row[4].Text), row[5].Text, strings.ToUpper(row[6].Text)}
		if row[0].Text == "before" {
			before[id] = f
		} else {
			after[id] = f
		}
	}
	selectedHex := strings.ToUpper(hex.EncodeToString([]byte(desired.Name)))
	removed := schemaddl.SQLiteAutoIncrement(original.Schema.Table) && !schemaddl.SQLiteAutoIncrement(desired)
	added := !schemaddl.SQLiteAutoIncrement(original.Schema.Table) && schemaddl.SQLiteAutoIncrement(desired)
	for id, f := range before {
		if removed && f.nameType == "text" && f.nameHex == selectedHex {
			continue
		}
		if a, ok := after[id]; !ok || a != f {
			return errIncomplete
		}
		delete(after, id)
	}
	if !added {
		if len(after) != 0 {
			return errIncomplete
		}
		return nil
	}
	if len(after) > 1 || originalRows > 0 && len(after) != 1 {
		return errIncomplete
	}
	for _, f := range after {
		if f.nameType != "text" || f.nameHex != selectedHex || f.valueType != "integer" {
			return errIncomplete
		}
	}
	return nil
}
