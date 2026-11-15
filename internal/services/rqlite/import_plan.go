package rqlite

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/importspec"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

type importKey struct{ columns, collations []string }
type importRow struct {
	number                 int64
	edit                   contract.DataEdit
	names, values, updates []string
	where                  string
}
type importBatch struct {
	statements             []any
	guards                 []int
	mutations              [][]int
	states, version, clear int
	rows                   []importRow
	create                 bool
}

func importMatchKey(t tableSnapshot, r contract.DataImportRequest) (importKey, error) {
	if !importspec.NeedsKey(r.Mode) {
		return importKey{}, nil
	}
	wanted := slices.Clone(r.MatchColumns)
	slices.Sort(wanted)
	for _, key := range t.importKeys {
		cols := slices.Clone(key.columns)
		slices.Sort(cols)
		if slices.Equal(cols, wanted) {
			return key, nil
		}
	}
	return importKey{}, contract.ErrDataReadOnly
}

func planImportRow(t tableSnapshot, r contract.DataImportRequest, number int64, edit contract.DataEdit, key importKey) (out importRow, err error) {
	one := r
	one.NewTable = false
	one.Columns = nil
	one.ContinueOnError = false
	one.Edits = []contract.DataEdit{edit}
	if err = importspec.Validate("sqlite", t.structure.Columns, one); err != nil {
		return
	}
	one, err = importspec.Own(one)
	if err != nil {
		return
	}
	out.number, out.edit = number, one.Edits[0]
	strict := false
	if parsed, e := schemaddl.ParseSQLite(t.structure.Definition); e == nil {
		strict = strings.Contains(strings.ToUpper(parsed.Options), "STRICT")
	}
	literals := map[string]string{}
	for _, col := range t.structure.Columns {
		v, ok := out.edit.Values[col.Name]
		if !ok {
			continue
		}
		literal, e := importValueLiteral(col, v, strict)
		if e != nil {
			return out, e
		}
		literals[col.Name] = literal
		out.names = append(out.names, quote(col.Name))
		out.values = append(out.values, literal)
		if !slices.Contains(r.MatchColumns, col.Name) {
			out.updates = append(out.updates, quote(col.Name)+"="+literal)
		}
	}
	for i, col := range key.columns {
		literal, ok := literals[col]
		if !ok {
			return out, contract.ErrDataReadOnly
		}
		if i >= len(key.collations) || !validName(key.collations[i]) {
			return out, contract.ErrUnsupported
		}
		if out.where != "" {
			out.where += " AND "
		}
		out.where += quote(col) + " COLLATE " + quote(key.collations[i]) + "=" + literal
	}
	return
}

// All state and count checks live inside the one native HTTP transaction.
// No earlier SELECT or later read is used to acknowledge a mutation.
func planImportBatch(t tableSnapshot, r contract.DataImportRequest, rows []importRow, first bool) (b importBatch, err error) {
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return
	}
	guard := "temp." + quote("nyadb_import_guard_"+hex.EncodeToString(nonce[:]))
	state := "temp." + quote("nyadb_import_state_"+hex.EncodeToString(nonce[:]))
	b.clear = -1
	b.rows = rows
	b.create = first && r.NewTable
	b.statements = []any{"CREATE TEMP TABLE " + strings.TrimPrefix(guard, "temp.") + "(ok INTEGER NOT NULL CHECK(ok=1))", "CREATE TEMP TABLE " + strings.TrimPrefix(state, "temp.") + "(slot INTEGER PRIMARY KEY,found INTEGER NOT NULL CHECK(found IN (0,1)))"}
	addGuard := func(expression string) {
		b.guards = append(b.guards, len(b.statements))
		b.statements = append(b.statements, "INSERT INTO "+guard+" SELECT "+expression)
	}
	addGuard(schemaGuard(t))
	if b.create {
		ddl, e := importspec.DDL("sqlite", "main", t.structure.Name, r.Columns)
		if e != nil {
			return b, e
		}
		b.statements = append(b.statements, ddl)
	}
	table := "main." + quote(t.structure.Name)
	if first && r.Mode == "replace" {
		b.clear = len(b.statements)
		b.statements = append(b.statements, "DELETE FROM "+table)
	}
	for slot, row := range rows {
		found := "0"
		if row.where != "" {
			found = "(SELECT count(*) FROM " + table + " WHERE " + row.where + ")"
		}
		b.guards = append(b.guards, len(b.statements))
		b.statements = append(b.statements, fmt.Sprintf("INSERT INTO %s VALUES(%d,%s)", state, slot, found))
		captured := fmt.Sprintf("(SELECT found FROM %s WHERE slot=%d)", state, slot)
		indices := []int{}
		mutation := func(sql, expected string) {
			indices = append(indices, len(b.statements))
			b.statements = append(b.statements, sql)
			addGuard("changes()=" + expected)
		}
		insert := func(condition string) string {
			return "INSERT INTO " + table + "(" + strings.Join(row.names, ",") + ") SELECT " + strings.Join(row.values, ",") + " WHERE " + condition
		}
		update := func() string {
			if len(row.updates) == 0 {
				return "UPDATE " + table + " SET " + row.names[0] + "=" + row.names[0] + " WHERE 0"
			}
			return "UPDATE " + table + " SET " + strings.Join(row.updates, ",") + " WHERE " + row.where + " AND " + captured + "=1"
		}
		switch r.Mode {
		case "append", "replace":
			mutation(insert("1"), "1")
		case "insert_missing":
			mutation(insert(captured+"=0"), "1-"+captured)
		case "update":
			expected := captured
			if len(row.updates) == 0 {
				expected = "0"
			}
			mutation(update(), expected)
		case "delete":
			mutation("DELETE FROM "+table+" WHERE "+row.where+" AND "+captured+"=1", captured)
		case "upsert":
			expected := captured
			if len(row.updates) == 0 {
				expected = "0"
			}
			mutation(update(), expected)
			mutation(insert(captured+"=0"), "1-"+captured)
		default:
			return b, contract.ErrUnsupported
		}
		b.mutations = append(b.mutations, indices)
	}
	b.states = len(b.statements)
	b.statements = append(b.statements, "SELECT slot,found FROM "+state+" ORDER BY slot")
	b.version = len(b.statements)
	b.statements = append(b.statements, "SELECT schema_version FROM pragma_schema_version", "DROP TABLE "+state, "DROP TABLE "+guard)
	encoded, e := json.Marshal(b.statements)
	if e != nil || len(encoded) > maxRequestBytes || len(b.statements) > 1024 {
		return b, contract.ErrDataEditLimit
	}
	return b, nil
}
