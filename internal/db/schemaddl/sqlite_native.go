package schemaddl

import (
	"errors"
	"reflect"
	"strings"
)

// PlanSQLiteNative uses native ALTER rather than an application shadow rebuild.
// Rebuild-only changes retain their separate migration requirement; they never
// become a lossy DROP/CREATE plan or a silently reordered retained column.
func PlanSQLiteNative(original Snapshot, desired Table, schema string, allowDrop bool) (Plan, error) {
	if desired.OpenGauss != nil {
		return Plan{}, ErrUnsupported
	}
	for _, c := range desired.Clauses {
		if c.OpenGauss != nil {
			return Plan{}, ErrUnsupported
		}
	}
	for _, d := range desired.Definitions {
		if d.OpenGauss != nil {
			return Plan{}, ErrUnsupported
		}
	}
	p, err := PlanSQLite(original, desired, schema, allowDrop)
	if err != nil || p.Mode != "rebuild" {
		return p, err
	}
	old := original.Table
	if old.Options != desired.Options || !reflect.DeepEqual(old.Definitions, desired.Definitions) {
		return Plan{}, ErrUnsupported
	}
	oldByID := map[string]Clause{}
	var originalConstraints, desiredConstraints []Clause
	for _, c := range old.Clauses {
		oldByID[c.ID] = c
		if c.Kind == "constraint" {
			originalConstraints = append(originalConstraints, c)
		}
	}
	for _, c := range desired.Clauses {
		if c.Kind == "constraint" {
			desiredConstraints = append(desiredConstraints, c)
		}
	}
	if !reflect.DeepEqual(originalConstraints, desiredConstraints) {
		return Plan{}, ErrUnsupported
	}
	kept := map[string]bool{}
	var added []Clause
	var retained []Clause
	newSeen := false
	for _, c := range desired.Columns() {
		if before, ok := oldByID[c.ID]; ok {
			if before != c || newSeen {
				return Plan{}, ErrUnsupported
			}
			kept[c.ID] = true
			retained = append(retained, c)
		} else {
			newSeen = true
			added = append(added, c)
		}
	}
	var expected []Clause
	for _, c := range old.Columns() {
		if kept[c.ID] {
			expected = append(expected, c)
		}
	}
	if !reflect.DeepEqual(expected, retained) {
		return Plan{}, ErrUnsupported
	}
	p.Mode = "native_columns"
	p.SQL = nil
	target := Quote(schema) + "." + Quote(old.Name)
	for _, name := range p.Dropped {
		p.SQL = append(p.SQL, "ALTER TABLE "+target+" DROP COLUMN "+Quote(name))
	}
	for _, c := range added {
		p.SQL = append(p.SQL, "ALTER TABLE "+target+" ADD COLUMN "+Quote(c.Name)+" "+c.SQL)
	}
	if len(p.SQL) == 0 {
		return Plan{}, ErrUnsupported
	}
	return p, nil
}

// SQLiteWritableColumns excludes generated fields without flattening the
// original clause or treating literals/comments as grammar keywords.
func SQLiteWritableColumns(t Table) ([]Clause, error) {
	var out []Clause
	for _, c := range t.Columns() {
		ts, err := lex(c.SQL)
		if err != nil {
			return nil, err
		}
		depth := 0
		generated := false
		for i, x := range ts {
			if depth == 0 && x.word("AS") && i+1 < len(ts) && ts[i+1].text == "(" {
				generated = true
				break
			}
			if x.kind == 'p' && x.text == "(" {
				depth++
			}
			if x.kind == 'p' && x.text == ")" {
				depth--
			}
		}
		if !generated {
			out = append(out, c)
		}
	}
	return out, nil
}

// sqliteRenameSQLMatches permits only the original native identifier rename.
// Literals, token order, operators and every other identifier stay complete.
// Quote normalization is allowed only for identifiers, never string values.
func sqliteRenameSQLMatches(before, after, source, destination string) bool {
	a, err := lex(before)
	if err != nil {
		return false
	}
	b, err := lex(after)
	if err != nil || len(a) != len(b) {
		return false
	}
	for i, x := range a {
		y := b[i]
		if x.kind == 's' || y.kind == 's' {
			if x.kind != y.kind || before[x.start:x.end] != after[y.start:y.end] {
				return false
			}
			continue
		}
		if identifierToken(x) && identifierToken(y) {
			if x.text == y.text {
				continue
			}
			if asciiLower(x.text) == asciiLower(source) && y.text == destination {
				continue
			}
			return false
		}
		if x.kind != y.kind || x.text != y.text {
			return false
		}
	}
	return true
}

// SQLiteNativeTableMatches verifies complete canonical clauses, options and
// definitions. Stable draft IDs are not native SQLite metadata. Native ADD
// places new columns before table constraints, which CreateSQLite also does.
func SQLiteNativeTableMatches(actual, desired Table, schema string) bool {
	sql, err := CreateSQLite(desired, schema, desired.Name)
	if err != nil {
		return false
	}
	parsed, err := ParseSQLite(sql)
	if err != nil || len(parsed.Clauses) != len(actual.Clauses) || len(desired.Definitions) != len(actual.Definitions) {
		return false
	}
	actual = actual.Clone()
	for i := range actual.Clauses {
		actual.Clauses[i].ID = parsed.Clauses[i].ID
	}
	definitions := map[string]string{}
	for _, d := range desired.Definitions {
		sql, err := DefinitionSQLite(d, schema, desired.Name)
		if err != nil {
			return false
		}
		definitions[d.Kind+":"+d.Name] = strings.TrimSuffix(strings.TrimSpace(sql), ";")
	}
	for _, d := range actual.Definitions {
		sql, err := DefinitionSQLite(d, schema, actual.Name)
		if err != nil || strings.TrimSuffix(strings.TrimSpace(sql), ";") != definitions[d.Kind+":"+d.Name] {
			return false
		}
	}
	actual.Definitions, parsed.Definitions = nil, nil
	return reflect.DeepEqual(actual, parsed)
}

// VerifySQLiteNativeCatalog requires the original unselected catalog to remain
// complete. Native renames may change only the selected identity and matching
// identifier tokens; roots, NULLness and literal bytes are preserved.
func VerifySQLiteNativeCatalog(original Snapshot, desired Table, actual Snapshot, p Plan) error {
	if original.SQLite == nil || actual.SQLite == nil || original.SQLite.Validate() != nil || actual.SQLite.Validate() != nil || actual.Engine != "sqlite" || actual.Table.Name != desired.Name || !actual.Exists {
		return ErrConflict
	}
	rename := p.Mode == "rename_table" || p.Mode == "rename_column"
	if !rename && !SQLiteNativeTableMatches(actual.Table, desired, "main") {
		return errors.New("complete native table differs from captured desired schema")
	}
	if rename {
		if len(original.Table.Clauses) != len(actual.Table.Clauses) || len(original.Table.Definitions) != len(actual.Table.Definitions) || original.Table.Options != actual.Table.Options {
			return ErrConflict
		}
		definitions := map[string]Definition{}
		for _, d := range actual.Table.Definitions {
			definitions[d.Kind+":"+d.Name] = d
		}
		for _, d := range original.Table.Definitions {
			a, ok := definitions[d.Kind+":"+d.Name]
			if !ok || !sqliteRenameSQLMatches(d.SQL, a.SQL, p.RenameFrom, p.RenameTo) {
				return ErrConflict
			}
		}
		for i, c := range original.Table.Clauses {
			a := actual.Table.Clauses[i]
			name := c.Name
			if p.Mode == "rename_column" && c.Kind == "column" && c.Name == p.RenameFrom {
				name = p.RenameTo
			}
			if a.Kind != c.Kind || a.Name != name || !sqliteRenameSQLMatches(c.SQL, a.SQL, p.RenameFrom, p.RenameTo) {
				return ErrConflict
			}
		}
	}
	current := map[string]SQLiteCatalogEntry{}
	for _, e := range actual.SQLite.Entries {
		current[e.Type+":"+e.Name] = e
	}
	expectedDefinitions := map[string]bool{}
	for _, d := range desired.Definitions {
		expectedDefinitions[d.Kind+":"+d.Name] = true
	}
	selected := original.Table.Name
	for _, e := range original.SQLite.Entries {
		name, table := e.Name, e.Table
		if p.Mode == "rename_table" && table == selected {
			table = desired.Name
			if e.Type == "table" && name == selected {
				name = desired.Name
			}
			prefix := "sqlite_autoindex_" + selected + "_"
			if e.Type == "index" && e.SQLNull && strings.HasPrefix(name, prefix) {
				name = "sqlite_autoindex_" + desired.Name + "_" + strings.TrimPrefix(name, prefix)
			}
		}
		key := e.Type + ":" + name
		a, exists := current[key]
		selectedDefinition := e.Table == selected && (e.Type == "index" || e.Type == "trigger") && !e.SQLNull
		if p.Mode == "definitions" && selectedDefinition {
			if !expectedDefinitions[key] {
				if exists {
					return ErrConflict
				}
				continue
			}
			if !exists || a.Table != table || a.SQLNull {
				return ErrConflict
			}
			delete(current, key)
			continue
		}
		if !exists || a.Type != e.Type || a.Table != table || a.Root != e.Root || a.SQLNull != e.SQLNull {
			return ErrConflict
		}
		if rename {
			if !sqliteRenameSQLMatches(e.SQL, a.SQL, p.RenameFrom, p.RenameTo) {
				return ErrConflict
			}
		} else if !(e.Type == "table" && e.Name == selected && p.Mode == "native_columns") && a.SQL != e.SQL {
			return ErrConflict
		}
		delete(current, key)
	}
	// Creation can introduce only its table, implicit indexes, requested
	// definitions and SQLite's native sequence catalog when AUTOINCREMENT needs it.
	for key, e := range current {
		if e.Table == desired.Name && ((p.Mode == "create" && e.Type == "table" && e.Name == desired.Name) || (p.Mode == "create" && e.Type == "index" && e.SQLNull) || expectedDefinitions[key] && (p.Mode == "create" || p.Mode == "definitions")) {
			continue
		}
		if p.Mode == "create" && SQLiteAutoIncrement(desired) && e.Type == "table" && e.Name == "sqlite_sequence" && e.Table == "sqlite_sequence" && e.SQL == "CREATE TABLE sqlite_sequence(name,seq)" && !e.SQLNull {
			continue
		}
		return ErrConflict
	}
	return nil
}

func VerifySQLiteNativeDrop(original, actual Snapshot) error {
	if original.SQLite == nil || actual.SQLite == nil || !original.Exists || actual.Exists || actual.Engine != "sqlite" || actual.Table.Name != original.Table.Name || original.SQLite.Validate() != nil || actual.SQLite.Validate() != nil {
		return ErrConflict
	}
	var expected []SQLiteCatalogEntry
	for _, e := range original.SQLite.Entries {
		if e.Table != original.Table.Name {
			expected = append(expected, e)
		}
	}
	if len(expected) == 0 && len(actual.SQLite.Entries) == 0 {
		return nil
	}
	if !reflect.DeepEqual(expected, actual.SQLite.Entries) {
		return ErrConflict
	}
	return nil
}
