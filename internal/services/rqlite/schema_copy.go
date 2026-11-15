package rqlite

import (
	"errors"
	"fmt"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

type schemaCopyProgram struct {
	withData                  bool
	beforeSQL, dataSQL        []string
	beforeGuards, afterGuards []string
	cleanupSQL                []string
}

// Full type/value frames stay on the original writer. Balanced concatenation
// avoids a linear expression-depth limit at 2000 columns. GROUP BY counts keep
// duplicate rows rather than accepting set equality as complete data equality.
func schemaCopyToken(columns []string) string {
	parts := make([]string, len(columns))
	for i, c := range columns {
		value := dataBytes(c)
		parts[i] = "(CASE WHEN typeof(" + c + ")='null' THEN 'N;' ELSE substr(typeof(" + c + "),1,1)||length(" + value + ")||':'||hex(" + value + ")||';' END)"
	}
	for len(parts) > 1 {
		var next []string
		for i := 0; i < len(parts); i += 2 {
			if i+1 == len(parts) {
				next = append(next, parts[i])
			} else {
				next = append(next, "("+parts[i]+"||"+parts[i+1]+")")
			}
		}
		parts = next
	}
	return parts[0]
}

func schemaCopyGroups(columns []string, from string) string {
	// Resolve aggregation only against materialized value frames. An original
	// column named token must never capture GROUP BY and hide different rows.
	return "WITH frames AS MATERIALIZED (SELECT " + schemaCopyToken(columns) + " COLLATE BINARY AS token FROM " + from + ") SELECT token,count(*) AS n FROM frames GROUP BY token COLLATE BINARY"
}

func schemaCopyEquality(snapshot, current string) string {
	return "(WITH current AS MATERIALIZED (" + current + ") SELECT NOT EXISTS(SELECT token,n FROM " + snapshot + " EXCEPT SELECT token,n FROM current) AND NOT EXISTS(SELECT token,n FROM current EXCEPT SELECT token,n FROM " + snapshot + "))"
}

func copyColumnPresent(columns []string, name string) bool {
	for _, c := range columns {
		if sqliteSchemaIdentity(c) == sqliteSchemaIdentity(name) {
			return true
		}
	}
	return false
}

func buildSchemaCopyProgram(original contract.SchemaDesignSnapshot, desired schemaddl.Table, r contract.SchemaDesignWriteReceipt, scope string) (*schemaCopyProgram, error) {
	p := &schemaCopyProgram{withData: r.WithData}
	source, destination := "main."+schemaddl.Quote(original.Target.Object.Name), "main."+schemaddl.Quote(desired.Name)
	var names, columns []string
	for _, c := range original.Schema.Table.Columns() {
		names, columns = append(names, c.Name), append(columns, schemaddl.Quote(c.Name))
	}
	if len(columns) == 0 {
		return nil, contract.ErrUnsupported
	}
	identity := schemaddl.SQLiteRowIDColumn(original.Schema.Table)
	if identity != "" {
		p.beforeGuards = append(p.beforeGuards,
			"(SELECT count(*) FROM pragma_table_xinfo("+schemaTextLiteral(original.Target.Object.Name)+") WHERE pk>0)=1",
			"NOT EXISTS(SELECT 1 FROM pragma_index_list("+schemaTextLiteral(original.Target.Object.Name)+") WHERE origin='pk')")
	} else if !schemaddl.SQLiteWithoutRowID(original.Schema.Table) {
		for _, alias := range []string{"_rowid_", "rowid", "oid"} {
			if !copyColumnPresent(names, alias) {
				identity = alias
				break
			}
		}
	}
	if r.WithData && r.PreserveRowID && !schemaddl.SQLiteWithoutRowID(original.Schema.Table) && identity == "" {
		return nil, errors.New("hidden rowid is inaccessible; explicitly allow new rowids to copy this table's data")
	}
	identityColumns := append([]string(nil), columns...)
	if identity != "" && !copyColumnPresent(names, identity) {
		identityColumns = append([]string{schemaddl.Quote(identity)}, identityColumns...)
	}
	stem := fmt.Sprintf("__nyadb_copy_%s_%d", scope[:16], r.Sequence)
	capture := func(suffix string, values []string, from string) string {
		name := "temp." + schemaddl.Quote(stem+suffix)
		p.beforeSQL = append(p.beforeSQL, "CREATE TEMP TABLE "+name+"(token TEXT COLLATE BINARY NOT NULL,n INTEGER NOT NULL)", "INSERT INTO "+name+" "+schemaCopyGroups(values, from))
		p.cleanupSQL = append(p.cleanupSQL, "DROP TABLE "+name)
		return name
	}
	rows := capture("_source", identityColumns, source)
	p.afterGuards = append(p.afterGuards, schemaCopyEquality(rows, schemaCopyGroups(identityColumns, source)))
	if r.WithData {
		writable, err := schemaddl.SQLiteWritableColumns(original.Schema.Table)
		if err != nil {
			return nil, err
		}
		var insertNames, insert []string
		for _, c := range writable {
			insertNames, insert = append(insertNames, c.Name), append(insert, schemaddl.Quote(c.Name))
		}
		if r.PreserveRowID && identity != "" && !copyColumnPresent(insertNames, identity) {
			insert = append([]string{schemaddl.Quote(identity)}, insert...)
		}
		if len(insert) == 0 {
			return nil, contract.ErrUnsupported
		}
		fields := strings.Join(insert, ",")
		if len(insert) <= 2000 {
			p.dataSQL = append(p.dataSQL, "INSERT INTO "+destination+" ("+fields+") SELECT "+fields+" FROM "+source)
		} else {
			// SQLite permits 2000 declared columns plus a hidden rowid in VALUES,
			// but a 2001-expression SELECT exceeds its native result-column limit.
			// A private TEMP queue feeds complete VALUES on the original writer;
			// no original or copied user trigger runs during this transfer.
			queueName := schemaddl.Quote(stem + "_rows")
			queue := "temp." + queueName
			trigger := schemaddl.Quote(stem + "_insert")
			p.beforeSQL = append(p.beforeSQL, "CREATE TEMP TABLE "+queue+"(source_rowid INTEGER PRIMARY KEY)")
			values := []string{"NEW.source_rowid"}
			for _, c := range writable {
				values = append(values, "(SELECT copied_source."+schemaddl.Quote(c.Name)+" FROM "+source+" AS copied_source WHERE copied_source."+schemaddl.Quote(identity)+"=NEW.source_rowid)")
			}
			p.dataSQL = append(p.dataSQL,
				"CREATE TEMP TRIGGER "+trigger+" AFTER INSERT ON "+queueName+" BEGIN INSERT INTO "+schemaddl.Quote(desired.Name)+" ("+fields+") VALUES ("+strings.Join(values, ",")+"); END",
				"INSERT INTO "+queue+"(source_rowid) SELECT "+schemaddl.Quote(identity)+" FROM "+source,
				"DROP TRIGGER temp."+trigger,
				"DROP TABLE "+queue)
		}
		verifyColumns := identityColumns
		if !r.PreserveRowID && len(identityColumns) != len(columns) {
			verifyColumns = columns
			rows = capture("_values", columns, source)
		}
		p.afterGuards = append(p.afterGuards, schemaCopyEquality(rows, schemaCopyGroups(verifyColumns, destination)))
	}
	p.beforeGuards = append(p.beforeGuards, "NOT EXISTS(SELECT 1 FROM main.sqlite_schema WHERE name COLLATE NOCASE="+schemaTextLiteral(desired.Name)+")")
	for _, d := range desired.Definitions {
		p.beforeGuards = append(p.beforeGuards, "NOT EXISTS(SELECT 1 FROM main.sqlite_schema WHERE name COLLATE NOCASE="+schemaTextLiteral(d.Name)+")")
	}
	sequence := false
	for _, e := range original.Schema.SQLite.Entries {
		sequence = sequence || e.Type == "table" && e.Name == "sqlite_sequence"
	}
	if sequence {
		dest, src := schemaTextLiteral(desired.Name), schemaTextLiteral(original.Target.Object.Name)
		p.beforeGuards = append(p.beforeGuards, "NOT EXISTS(SELECT 1 FROM main.sqlite_sequence WHERE name COLLATE NOCASE="+dest+")")
		seqColumns := []string{"_rowid_", "name", "seq"}
		saved := capture("_sequences", seqColumns, "main.sqlite_sequence")
		p.afterGuards = append(p.afterGuards, schemaCopyEquality(saved, schemaCopyGroups(seqColumns, "main.sqlite_sequence WHERE name IS NULL OR name<>"+dest+" COLLATE BINARY")))
		if r.WithData && schemaddl.SQLiteAutoIncrement(original.Schema.Table) {
			p.beforeGuards = append(p.beforeGuards,
				"(SELECT count(*) FROM main.sqlite_sequence WHERE name="+src+")<=1",
				"NOT EXISTS(SELECT 1 FROM main.sqlite_sequence WHERE name="+src+" AND typeof(seq)<>'integer')")
			p.dataSQL = append(p.dataSQL,
				"DELETE FROM main.sqlite_sequence WHERE name="+dest,
				"INSERT INTO main.sqlite_sequence(name,seq) SELECT "+dest+",seq FROM main.sqlite_sequence WHERE name="+src)
			p.afterGuards = append(p.afterGuards,
				"(SELECT count(*) FROM main.sqlite_sequence WHERE name="+dest+")=(SELECT count(*) FROM main.sqlite_sequence WHERE name="+src+")",
				"NOT EXISTS(SELECT 1 FROM main.sqlite_sequence WHERE name="+dest+" AND (typeof(seq)<>'integer' OR seq IS NOT (SELECT seq FROM main.sqlite_sequence WHERE name="+src+")))")
		} else {
			p.afterGuards = append(p.afterGuards, "NOT EXISTS(SELECT 1 FROM main.sqlite_sequence WHERE name="+dest+")")
		}
	}
	return p, nil
}
