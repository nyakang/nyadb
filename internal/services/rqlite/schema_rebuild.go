package rqlite

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

type schemaRebuildTable struct {
	table                 schemaddl.Table
	source, saved         string
	identity              string
	columns, savedColumns []string
	parents               []string
}

type schemaRebuildProgram struct {
	tables               []schemaRebuildTable
	beforeSQL, changeSQL []string
	checks               []schemaRebuildCheck
	cleanupSQL           []string
	sequenceBefore       bool
	sequenceAfter        bool
}

type schemaRebuildCheck struct {
	sql, condition string
	data           bool
}

func schemaRebuildIdentity(table schemaddl.Table) (string, error) {
	if schemaddl.SQLiteWithoutRowID(table) {
		return "", nil
	}
	if name := schemaddl.SQLiteRowIDColumn(table); name != "" {
		return name, nil
	}
	var names []string
	for _, c := range table.Columns() {
		names = append(names, c.Name)
	}
	for _, name := range []string{"_rowid_", "rowid", "oid"} {
		if !copyColumnPresent(names, name) {
			return name, nil
		}
	}
	return "", errors.Join(contract.ErrUnsupported, errors.New("original physical rowid is inaccessible; reconstruction cannot preserve it"))
}

// A raw TEMP snapshot has no declared affinity. A private queue avoids a
// 2001-expression SELECT when all 2000 columns and hidden rowid are retained.
func schemaRebuildTransfer(source, destination string, sourceColumns, destinationColumns []string, identity, stem string) ([]string, error) {
	if len(sourceColumns) == 0 || len(sourceColumns) != len(destinationColumns) {
		return nil, contract.ErrUnsupported
	}
	if len(sourceColumns) <= 2000 {
		return []string{"INSERT INTO " + destination + " (" + strings.Join(destinationColumns, ",") + ") SELECT " + strings.Join(sourceColumns, ",") + " FROM " + source}, nil
	}
	if len(sourceColumns) != 2001 || identity == "" {
		return nil, contract.ErrUnsupported
	}
	queueName, triggerName := schemaddl.Quote(stem+"_queue"), schemaddl.Quote(stem+"_transfer")
	queue := "temp." + queueName
	values := make([]string, len(sourceColumns))
	for i, c := range sourceColumns {
		values[i] = "(SELECT captured_source." + c + " FROM " + source + " AS captured_source WHERE captured_source." + schemaddl.Quote(identity) + "=NEW.source_rowid)"
	}
	_, destinationName, ok := strings.Cut(destination, ".")
	if !ok {
		return nil, contract.ErrUnsupported
	}
	return []string{
		"CREATE TEMP TABLE " + queue + "(source_rowid INTEGER PRIMARY KEY)",
		"CREATE TEMP TRIGGER " + triggerName + " AFTER INSERT ON " + queueName + " BEGIN INSERT INTO " + destinationName + " (" + strings.Join(destinationColumns, ",") + ") VALUES (" + strings.Join(values, ",") + "); END",
		"INSERT INTO " + queue + "(source_rowid) SELECT " + schemaddl.Quote(identity) + " FROM " + source,
		"DROP TRIGGER temp." + triggerName,
		"DROP TABLE " + queue,
	}, nil
}

func schemaRebuildDeleteOrder(tables []schemaRebuildTable) []schemaRebuildTable {
	remaining := slices.Clone(tables)
	var out []schemaRebuildTable
	for len(remaining) > 0 {
		at := -1
		for i, candidate := range remaining {
			incoming := false
			for j, child := range remaining {
				if i == j {
					continue
				}
				for _, parent := range child.parents {
					incoming = incoming || sqliteSchemaIdentity(parent) == sqliteSchemaIdentity(candidate.table.Name)
				}
			}
			if !incoming {
				at = i
				break
			}
		}
		if at < 0 {
			at = 0 // Deferred cyclic enforcement still belongs to SQLite.
		}
		out = append(out, remaining[at])
		remaining = append(remaining[:at], remaining[at+1:]...)
	}
	return out
}

func buildSchemaRebuildProgram(original contract.SchemaDesignSnapshot, desired schemaddl.Table, stem string) (*schemaRebuildProgram, error) {
	p := &schemaRebuildProgram{}
	if !original.Schema.Exists || original.Schema.Table.Name != desired.Name {
		return nil, contract.ErrUnsupported
	}
	for _, e := range original.Schema.SQLite.Entries {
		if e.Type != "table" {
			continue
		}
		var table schemaddl.Table
		if e.Name == "sqlite_sequence" {
			p.sequenceBefore = true
			table = schemaddl.Table{Name: e.Name, Clauses: []schemaddl.Clause{{ID: "name", Kind: "column", Name: "name"}, {ID: "seq", Kind: "column", Name: "seq"}}}
		} else {
			var err error
			table, err = schemaddl.ParseSQLite(e.SQL)
			if err != nil {
				return nil, errors.Join(contract.ErrUnsupported, errors.New("original database includes a table whose complete values cannot be reconstructed"))
			}
		}
		if e.Name == original.Target.Object.Name {
			table = original.Schema.Table.Clone()
		}
		identity, err := schemaRebuildIdentity(table)
		if err != nil {
			return nil, err
		}
		parents, err := schemaddl.SQLiteReferencedTables(table)
		if err != nil {
			return nil, err
		}
		r := schemaRebuildTable{table: table, source: "main." + schemaddl.Quote(e.Name), saved: "temp." + schemaddl.Quote(fmt.Sprintf("%s_rows_%d", stem, len(p.tables))), identity: identity, parents: parents}
		for i, c := range table.Columns() {
			r.columns = append(r.columns, schemaddl.Quote(c.Name))
			r.savedColumns = append(r.savedColumns, schemaddl.Quote(fmt.Sprintf("c%d", i)))
		}
		p.tables = append(p.tables, r)
	}
	p.sequenceAfter = p.sequenceBefore || schemaddl.SQLiteAutoIncrement(desired)
	for i, r := range p.tables {
		p.beforeSQL = append(p.beforeSQL, "CREATE TEMP TABLE "+r.saved+" ("+strings.Join(r.savedColumns, ",")+")")
		source, destination := slices.Clone(r.columns), slices.Clone(r.savedColumns)
		if r.identity != "" {
			source, destination = append([]string{schemaddl.Quote(r.identity)}, source...), append([]string{"_rowid_"}, destination...)
		}
		transfer, err := schemaRebuildTransfer(r.source, r.saved, source, destination, r.identity, fmt.Sprintf("%s_capture_%d", stem, i))
		if err != nil {
			return nil, err
		}
		p.beforeSQL = append(p.beforeSQL, transfer...)
	}
	p.changeSQL = append(p.changeSQL, "PRAGMA defer_foreign_keys=ON")
	for _, e := range original.Schema.SQLite.Entries {
		if e.Type == "trigger" {
			p.changeSQL = append(p.changeSQL, "DROP TRIGGER main."+schemaddl.Quote(e.Name))
		}
	}
	for _, e := range original.Schema.SQLite.Entries {
		if e.Type == "view" {
			p.changeSQL = append(p.changeSQL, "DROP VIEW main."+schemaddl.Quote(e.Name))
		}
	}
	// Every original row is already saved. Ignore automatic FK UPDATE actions
	// only while emptying the tables, so SET NULL/DEFAULT cycles cannot violate
	// immediate value constraints. Full restored values and the FK graph are
	// checked before deferred counters are cleared. Original programs are off.
	var suspend []string
	for i, r := range p.tables {
		if r.table.Name == "sqlite_sequence" {
			continue
		}
		name := schemaddl.Quote(fmt.Sprintf("%s_suspend_fk_%d", stem, i))
		p.changeSQL = append(p.changeSQL, "CREATE TEMP TRIGGER "+name+" BEFORE UPDATE ON "+r.source+" BEGIN SELECT RAISE(IGNORE); END")
		suspend = append(suspend, "DROP TRIGGER temp."+name)
	}
	for _, r := range schemaRebuildDeleteOrder(p.tables) {
		if r.table.Name != "sqlite_sequence" {
			p.changeSQL = append(p.changeSQL, "DELETE FROM "+r.source)
		}
	}
	p.changeSQL = append(p.changeSQL, suspend...)
	create, err := schemaddl.CreateSQLite(desired, "main", desired.Name)
	if err != nil {
		return nil, err
	}
	p.changeSQL = append(p.changeSQL, "DROP TABLE main."+schemaddl.Quote(desired.Name), create)
	for i, r := range p.tables {
		if r.table.Name == "sqlite_sequence" {
			continue
		}
		table := r.table
		if table.Name == desired.Name {
			table = desired
		}
		writable, err := schemaddl.SQLiteWritableColumns(table)
		if err != nil {
			return nil, err
		}
		identity, err := schemaRebuildIdentity(table)
		if err != nil {
			return nil, err
		}
		var source, destination []string
		if identity != "" {
			source, destination = append(source, "_rowid_"), append(destination, schemaddl.Quote(identity))
		}
		for _, c := range writable {
			if c.Name == identity {
				continue // The original physical rowid already supplies this alias.
			}
			for j, before := range r.table.Columns() {
				if c.ID == before.ID {
					source, destination = append(source, r.savedColumns[j]), append(destination, schemaddl.Quote(c.Name))
					break
				}
			}
		}
		transfer, err := schemaRebuildTransfer(r.saved, r.source, source, destination, "_rowid_", fmt.Sprintf("%s_restore_%d", stem, i))
		if err != nil {
			return nil, err
		}
		p.changeSQL = append(p.changeSQL, transfer...)
	}
	newSequence := schemaddl.SQLiteAutoIncrement(desired) && !schemaddl.SQLiteAutoIncrement(original.Schema.Table)
	savedNewSequence := "temp." + schemaddl.Quote(stem+"_new_sequence")
	if newSequence {
		p.changeSQL = append(p.changeSQL, "CREATE TEMP TABLE "+savedNewSequence+"(saved_name,saved_value)", "INSERT INTO "+savedNewSequence+"(_rowid_,saved_name,saved_value) SELECT _rowid_,name,seq FROM main.sqlite_sequence WHERE name="+schemaTextLiteral(desired.Name))
		p.cleanupSQL = append(p.cleanupSQL, "DROP TABLE "+savedNewSequence)
	}
	for _, r := range p.tables {
		if r.table.Name != "sqlite_sequence" {
			continue
		}
		from := r.saved
		if schemaddl.SQLiteAutoIncrement(original.Schema.Table) && !schemaddl.SQLiteAutoIncrement(desired) {
			from += " WHERE c0 IS NULL OR c0<>" + schemaTextLiteral(desired.Name) + " COLLATE BINARY"
		}
		p.changeSQL = append(p.changeSQL, "DELETE FROM main.sqlite_sequence", "INSERT INTO main.sqlite_sequence(_rowid_,name,seq) SELECT _rowid_,c0,c1 FROM "+from)
		if newSequence {
			p.changeSQL = append(p.changeSQL, "INSERT INTO main.sqlite_sequence(_rowid_,name,seq) SELECT _rowid_,saved_name,saved_value FROM "+savedNewSequence)
		}
	}
	for _, d := range desired.Definitions {
		if d.Kind == "index" {
			sql, err := schemaddl.DefinitionSQLite(d, "main", desired.Name)
			if err != nil {
				return nil, err
			}
			p.changeSQL = append(p.changeSQL, sql)
		}
	}
	for _, e := range original.Schema.SQLite.Entries {
		if e.Type == "view" {
			p.changeSQL = append(p.changeSQL, e.SQL)
		}
	}
	for _, e := range original.Schema.SQLite.Entries {
		if e.Type == "trigger" && e.Table != desired.Name {
			p.changeSQL = append(p.changeSQL, e.SQL)
		}
	}
	for _, d := range desired.Definitions {
		if d.Kind == "trigger" {
			sql, err := schemaddl.DefinitionSQLite(d, "main", desired.Name)
			if err != nil {
				return nil, err
			}
			p.changeSQL = append(p.changeSQL, sql)
		}
	}
	for i, r := range p.tables {
		var saved, current []string
		if r.identity != "" {
			identity := r.identity
			if r.table.Name == desired.Name {
				identity, err = schemaRebuildIdentity(desired)
				if err != nil {
					return nil, err
				}
			}
			saved, current = append(saved, "_rowid_"), append(current, schemaddl.Quote(identity))
		}
		for j, before := range r.table.Columns() {
			name := before.Name
			if r.table.Name == desired.Name {
				name = ""
				for _, after := range desired.Columns() {
					if after.ID == before.ID {
						name = after.Name
					}
				}
			}
			if name != "" {
				saved, current = append(saved, r.savedColumns[j]), append(current, schemaddl.Quote(name))
			}
		}
		frames := "temp." + schemaddl.Quote(fmt.Sprintf("%s_frames_%d", stem, i))
		from := r.saved
		if r.table.Name == "sqlite_sequence" && schemaddl.SQLiteAutoIncrement(original.Schema.Table) && !schemaddl.SQLiteAutoIncrement(desired) {
			from += " WHERE c0 IS NULL OR c0<>" + schemaTextLiteral(desired.Name) + " COLLATE BINARY"
		}
		groups := schemaCopyGroups(saved, from)
		if r.table.Name == "sqlite_sequence" && newSequence {
			groups = "SELECT token,n FROM (" + groups + ") UNION ALL SELECT token,n FROM (" + schemaCopyGroups([]string{"_rowid_", "saved_name", "saved_value"}, savedNewSequence) + ")"
		}
		p.checks = append(p.checks, schemaRebuildCheck{sql: "CREATE TEMP TABLE " + frames + "(token TEXT COLLATE BINARY NOT NULL,n INTEGER NOT NULL)"}, schemaRebuildCheck{sql: "INSERT INTO " + frames + " " + groups}, schemaRebuildCheck{condition: schemaCopyEquality(frames, schemaCopyGroups(current, r.source)), data: true})
		p.cleanupSQL = append(p.cleanupSQL, "DROP TABLE "+frames, "DROP TABLE "+r.saved)
	}
	if newSequence {
		p.checks = append(p.checks, schemaRebuildCheck{condition: "(SELECT count(*) FROM " + savedNewSequence + ")<=1 AND NOT EXISTS(SELECT 1 FROM " + savedNewSequence + " WHERE typeof(saved_value)<>'integer' OR saved_name IS NOT " + schemaTextLiteral(desired.Name) + ")"})
		if !p.sequenceBefore {
			frames := "temp." + schemaddl.Quote(stem+"_new_sequence_frames")
			p.checks = append(p.checks,
				schemaRebuildCheck{sql: "CREATE TEMP TABLE " + frames + "(token TEXT COLLATE BINARY NOT NULL,n INTEGER NOT NULL)"},
				schemaRebuildCheck{sql: "INSERT INTO " + frames + " " + schemaCopyGroups([]string{"_rowid_", "saved_name", "saved_value"}, savedNewSequence)},
				schemaRebuildCheck{condition: schemaCopyEquality(frames, schemaCopyGroups([]string{"_rowid_", "name", "seq"}, "main.sqlite_sequence")), data: true})
			p.cleanupSQL = append(p.cleanupSQL, "DROP TABLE "+frames)
		}
	}
	return p, nil
}

func schemaRebuildProgramChecks(p *schemaRebuildProgram, desired schemaddl.Table, catalog *schemaddl.SQLiteCatalog, viewChecks []string) ([]string, error) {
	var sql []string
	for _, e := range catalog.Entries {
		if e.Type == "view" {
			sql = append(sql, "EXPLAIN SELECT * FROM main."+schemaddl.Quote(e.Name)+" LIMIT 0")
		}
	}
	for _, r := range p.tables {
		if r.table.Name == "sqlite_sequence" {
			continue
		}
		table := r.table
		if table.Name == desired.Name {
			table = desired
		}
		for _, e := range catalog.Entries {
			if e.Type == "trigger" && e.Table == table.Name && table.Name != desired.Name {
				if err := schemaddl.ValidateSQLiteTriggerColumns(schemaddl.Definition{Kind: "trigger", Name: e.Name, SQL: e.SQL}, table); err != nil {
					return nil, err
				}
			}
		}
		if table.Name == desired.Name {
			for _, d := range desired.Definitions {
				if err := schemaddl.ValidateSQLiteTriggerColumns(d, table); err != nil {
					return nil, err
				}
			}
		}
		writable, err := schemaddl.SQLiteWritableColumns(table)
		if err != nil || len(writable) == 0 {
			return nil, errors.Join(contract.ErrUnsupported, err)
		}
		var columns, nulls, updates []string
		for _, c := range writable {
			name := schemaddl.Quote(c.Name)
			columns, nulls, updates = append(columns, name), append(nulls, "NULL"), append(updates, name+"="+name)
		}
		sql = append(sql, "EXPLAIN INSERT INTO "+r.source+" ("+strings.Join(columns, ",")+") SELECT "+strings.Join(nulls, ",")+" WHERE 0", "EXPLAIN DELETE FROM "+r.source+" WHERE 0", "EXPLAIN UPDATE "+r.source+" SET "+strings.Join(updates, ",")+" WHERE 0")
	}
	return append(sql, viewChecks...), nil
}
