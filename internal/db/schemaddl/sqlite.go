package schemaddl

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
)

func ParseSQLite(ddl string) (Table, error) {
	ts, err := lex(ddl)
	if err != nil {
		return Table{}, err
	}
	if err = single(ts, false); err != nil {
		return Table{}, err
	}
	if len(ts) < 5 || !ts[0].word("CREATE") || !ts[1].word("TABLE") {
		return Table{}, ErrUnsupported
	}
	at := 2
	if at+2 < len(ts) && ts[at].word("IF") && ts[at+1].word("NOT") && ts[at+2].word("EXISTS") {
		at += 3
	}
	_, name, at, err := qualifiedName(ts, at)
	if err != nil || at >= len(ts) || ts[at].text != "(" {
		return Table{}, errors.New("only complete CREATE TABLE definitions can be edited")
	}
	t := Table{Name: name}
	depth, first, closeAt := 1, at+1, -1
	for i := at + 1; i < len(ts); i++ {
		if ts[i].kind == 'p' && ts[i].text == "(" {
			depth++
		}
		if ts[i].kind == 'p' && ts[i].text == ")" {
			depth--
		}
		if depth == 0 || depth == 1 && ts[i].kind == 'p' && ts[i].text == "," {
			if first == i {
				return Table{}, errors.New("empty table clause")
			}
			part := ts[first:i]
			c := Clause{ID: fmt.Sprintf("clause:%d", len(t.Clauses)), Kind: "column", Name: part[0].text}
			if part[0].word("CONSTRAINT") || part[0].word("PRIMARY") || part[0].word("UNIQUE") || part[0].word("CHECK") || part[0].word("FOREIGN") {
				c.Kind, c.Name, c.SQL = "constraint", "", strings.TrimSpace(ddl[part[0].start:ts[i].start])
			} else {
				if !identifierToken(part[0]) {
					return Table{}, errors.New("invalid column name")
				}
				c.SQL = strings.TrimSpace(ddl[part[0].end:ts[i].start])
			}
			t.Clauses = append(t.Clauses, c)
			first = i + 1
			if depth == 0 {
				closeAt = i
				break
			}
		}
	}
	if closeAt < 0 {
		return Table{}, errors.New("missing CREATE TABLE closing parenthesis")
	}
	end := len(ddl)
	if ts[len(ts)-1].text == ";" && ts[len(ts)-1].kind == 'p' {
		end = ts[len(ts)-1].start
	}
	t.Options = strings.TrimSpace(ddl[ts[closeAt].end:end])
	if err = validateSQLiteTable(t); err != nil {
		return Table{}, err
	}
	return t, nil
}

func validateSQLiteTable(t Table) error {
	if t.Postgres != nil {
		return errors.New("PostgreSQL table properties cannot be applied to SQLite")
	}
	if err := Identifier(t.Name); err != nil {
		return err
	}
	if strings.HasPrefix(strings.ToLower(t.Name), "sqlite_") {
		return errors.New("SQLite reserved object name")
	}
	if len(t.Clauses) == 0 || len(t.Clauses) > 2000 || len(t.Definitions) > 1000 {
		return errors.New("table definition count exceeded")
	}
	ids, names := map[string]bool{}, map[string]bool{}
	definitionBytes := len(t.Name) + len(t.Options)
	for _, c := range t.Clauses {
		if c.Postgres != nil {
			return errors.New("PostgreSQL column properties cannot be applied to SQLite")
		}
		definitionBytes += len(c.Name) + len(c.SQL) + 8
		if definitionBytes > 1<<20 {
			return errors.New("complete table definition exceeds size limit")
		}
		if c.ID == "" || len(c.ID) > 256 || ids[c.ID] {
			return errors.New("missing or duplicated stable clause ID")
		}
		ids[c.ID] = true
		if c.Kind == "column" {
			if err := Identifier(c.Name); err != nil {
				return err
			}
			// SQLite folds ASCII identifier characters only.
			key := asciiLower(c.Name)
			if names[key] {
				return errors.New("duplicated column name")
			}
			names[key] = true
		} else if c.Kind != "constraint" {
			return errors.New("unknown table clause kind")
		}
		ts, err := lex(c.SQL)
		if err != nil {
			return err
		}
		if err = single(ts, false); err != nil {
			return err
		}
		for _, x := range ts {
			if x.kind == 'p' && x.text == ";" {
				return errors.New("statement delimiter in table clause")
			}
		}
		depth := 0
		for _, x := range ts {
			if x.kind == 'p' && x.text == "(" {
				depth++
			}
			if x.kind == 'p' && x.text == ")" {
				depth--
			}
			if depth == 0 && x.kind == 'p' && x.text == "," {
				return errors.New("multiple clauses in a field definition")
			}
		}
		if c.Kind == "constraint" && (len(ts) == 0 || !(ts[0].word("CONSTRAINT") || ts[0].word("PRIMARY") || ts[0].word("UNIQUE") || ts[0].word("CHECK") || ts[0].word("FOREIGN"))) {
			return errors.New("invalid table constraint")
		}
	}
	if len(names) == 0 {
		return errors.New("table needs a column")
	}
	for _, d := range t.Definitions {
		if d.Postgres != nil {
			return errors.New("PostgreSQL definition properties cannot be applied to SQLite")
		}
		definitionBytes += len(d.Name) + len(d.SQL)
		if definitionBytes > 16<<20 {
			return errors.New("complete index/trigger definitions exceed size limit")
		}
	}
	ts, err := lex(t.Options)
	if err != nil {
		return err
	}
	strict, without := false, false
	for i := 0; i < len(ts); i++ {
		if ts[i].word("STRICT") && !strict {
			strict = true
		} else if ts[i].word("WITHOUT") && !without && i+1 < len(ts) && ts[i+1].word("ROWID") {
			without = true
			i++
		} else {
			return errors.New("unsupported SQLite table option")
		}
		if i+1 < len(ts) {
			i++
			if ts[i].text != "," || i+1 == len(ts) {
				return errors.New("invalid SQLite table options")
			}
		}
	}
	return nil
}
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

func CreateSQLite(t Table, schema, name string) (string, error) {
	if err := validateSQLiteTable(t); err != nil {
		return "", err
	}
	if err := Identifier(schema); err != nil {
		return "", err
	}
	if err := Identifier(name); err != nil {
		return "", err
	}
	var parts []string
	for _, c := range t.Clauses {
		if c.Kind == "column" {
			parts = append(parts, Quote(c.Name)+" "+c.SQL)
		}
	}
	// SQLite requires all columns before table constraints. UI additions keep
	// stable IDs in the draft without needing to move existing constraint rows.
	for _, c := range t.Clauses {
		if c.Kind == "constraint" {
			parts = append(parts, c.SQL)
		}
	}
	sql := "CREATE TABLE " + Quote(schema) + "." + Quote(name) + " (\n  " + strings.Join(parts, "\n, ") + "\n)"
	if t.Options != "" {
		sql += " " + t.Options
	}
	if len(sql) > 1<<20 {
		return "", errors.New("table SQL size exceeded")
	}
	return sql, nil
}

// DefinitionSQLite binds exactly one complete index/trigger definition to its
// original namespace and table. Only the CREATE object name is requalified;
// strings and trigger bodies are never text-replaced.
func DefinitionSQLite(d Definition, schema, table string) (string, error) {
	if err := Identifier(d.Name); err != nil {
		return "", err
	}
	if strings.HasPrefix(strings.ToLower(d.Name), "sqlite_") {
		return "", errors.New("SQLite reserved definition name")
	}
	ts, err := lex(d.SQL)
	if err != nil {
		return "", err
	}
	if err = single(ts, d.Kind == "trigger"); err != nil {
		return "", err
	}
	if len(ts) < 5 || !ts[0].word("CREATE") {
		return "", errors.New("expected CREATE definition")
	}
	at := 1
	if ts[at].word("UNIQUE") {
		if d.Kind != "index" {
			return "", errors.New("UNIQUE requires an index")
		}
		at++
	}
	if at >= len(ts) || !(d.Kind == "index" && ts[at].word("INDEX") || d.Kind == "trigger" && ts[at].word("TRIGGER")) {
		return "", ErrUnsupported
	}
	at++
	if at+2 < len(ts) && ts[at].word("IF") && ts[at+1].word("NOT") && ts[at+2].word("EXISTS") {
		return "", errors.New("IF NOT EXISTS cannot verify a changed definition")
	}
	start := at
	objectSchema, name, at, err := qualifiedName(ts, at)
	if err != nil || name != d.Name || objectSchema != "" && objectSchema != schema {
		return "", errors.New("definition identity differs from original scope")
	}
	nameEnd := ts[at-1].end
	on := -1
	for i := at; i < len(ts); i++ {
		if ts[i].word("ON") {
			on = i
			break
		}
		if ts[i].word("BEGIN") {
			break
		}
	}
	if on < 0 {
		return "", errors.New("definition has no target table")
	}
	targetSchema, target, _, err := qualifiedName(ts, on+1)
	if err != nil || target != table || targetSchema != "" && targetSchema != schema {
		return "", errors.New("definition target differs from original table")
	}
	return d.SQL[:ts[start].start] + Quote(schema) + "." + Quote(d.Name) + d.SQL[nameEnd:], nil
}

// ValidateSQLiteTriggerColumns catches SQLite's deliberately permissive
// UPDATE OF behavior: unknown column names otherwise silently disable a
// trigger instead of producing a CREATE/EXPLAIN error.
func ValidateSQLiteTriggerColumns(d Definition, table Table) error {
	if d.Kind != "trigger" {
		return nil
	}
	ts, err := lex(d.SQL)
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for _, col := range table.Columns() {
		names[asciiLower(col.Name)] = true
	}
	for i := 0; i+1 < len(ts); i++ {
		if ts[i].word("ON") || ts[i].word("BEGIN") {
			break
		}
		if !ts[i].word("UPDATE") || !ts[i+1].word("OF") {
			continue
		}
		for j := i + 2; j < len(ts) && !ts[j].word("ON"); j++ {
			if ts[j].kind == 'p' && ts[j].text == "," {
				continue
			}
			if !identifierToken(ts[j]) || !names[asciiLower(ts[j].text)] {
				return errors.New("trigger UPDATE OF references a missing column")
			}
		}
		break
	}
	return nil
}

func PlanSQLite(original Snapshot, desired Table, schema string, allowDrop bool) (Plan, error) {
	p := Plan{}
	if original.Engine != "sqlite" || original.Revision == "" {
		return p, errors.New("missing original SQLite snapshot")
	}
	if err := validateSQLiteTable(desired); err != nil {
		return p, err
	}
	create, err := CreateSQLite(desired, schema, desired.Name)
	if err != nil {
		return p, err
	}
	definitions := map[string]bool{}
	definitionTable := desired.Name
	if original.Exists && original.Table.Name != desired.Name {
		definitionTable = original.Table.Name
	}
	for _, d := range desired.Definitions {
		key := asciiLower(d.Name)
		if definitions[key] {
			return p, errors.New("duplicate index/trigger name")
		}
		definitions[key] = true
		if _, err = DefinitionSQLite(d, schema, definitionTable); err != nil {
			return p, err
		}
	}
	if !original.Exists {
		p.Mode, p.SQL = "create", []string{create}
		for _, d := range desired.Definitions {
			sql, _ := DefinitionSQLite(d, schema, desired.Name)
			p.SQL = append(p.SQL, sql)
		}
		return p, nil
	}
	if reflect.DeepEqual(original.Table, desired) {
		p.Mode = "noop"
		return p, nil
	}
	old := original.Table.Clone()
	old.Name = desired.Name
	if original.Table.Name != desired.Name {
		if !reflect.DeepEqual(old, desired) {
			return p, errors.New("rename the table before making other schema changes")
		}
		p.Mode, p.RenameFrom, p.RenameTo = "rename_table", original.Table.Name, desired.Name
		p.SQL = []string{"ALTER TABLE " + Quote(schema) + "." + Quote(original.Table.Name) + " RENAME TO " + Quote(desired.Name)}
		return p, nil
	}
	oldWithoutDefinitions, desiredWithoutDefinitions := old.Clone(), desired.Clone()
	oldWithoutDefinitions.Definitions, desiredWithoutDefinitions.Definitions = nil, nil
	if reflect.DeepEqual(oldWithoutDefinitions, desiredWithoutDefinitions) {
		p.Mode = "definitions"
		oldDefinitions, newDefinitions := map[string]Definition{}, map[string]Definition{}
		for _, definition := range old.Definitions {
			oldDefinitions[definition.Kind+":"+definition.Name] = definition
		}
		for _, definition := range desired.Definitions {
			newDefinitions[definition.Kind+":"+definition.Name] = definition
		}
		for _, definition := range old.Definitions {
			if newDefinition, ok := newDefinitions[definition.Kind+":"+definition.Name]; !ok || newDefinition != definition {
				if definition.Kind != "index" && definition.Kind != "trigger" {
					return p, ErrUnsupported
				}
				p.SQL = append(p.SQL, "DROP "+strings.ToUpper(definition.Kind)+" "+Quote(schema)+"."+Quote(definition.Name))
			}
		}
		for _, definition := range desired.Definitions {
			if oldDefinition, ok := oldDefinitions[definition.Kind+":"+definition.Name]; !ok || oldDefinition != definition {
				sql, _ := DefinitionSQLite(definition, schema, desired.Name)
				p.SQL = append(p.SQL, sql)
			}
		}
		return p, nil
	}
	oldCols := map[string]Clause{}
	for _, c := range old.Columns() {
		oldCols[c.ID] = c
	}
	kept := map[string]bool{}
	var renamed []Mapping
	for _, c := range desired.Columns() {
		if oc, ok := oldCols[c.ID]; ok {
			kept[c.ID] = true
			m := Mapping{c.ID, oc.Name, c.Name}
			p.Mappings = append(p.Mappings, m)
			if oc.Name != c.Name {
				renamed = append(renamed, m)
			}
		}
	}
	for _, c := range old.Columns() {
		if !kept[c.ID] {
			p.Dropped = append(p.Dropped, c.Name)
		}
	}
	if len(p.Dropped) > 0 && !allowDrop {
		return p, errors.New("column removal needs explicit data-loss confirmation")
	}
	if len(renamed) > 0 {
		if len(renamed) != 1 {
			return p, errors.New("rename one column at a time")
		}
		for i := range old.Clauses {
			if old.Clauses[i].ID == renamed[0].ID {
				old.Clauses[i].Name = renamed[0].Destination
			}
		}
		if !reflect.DeepEqual(old, desired) {
			return p, errors.New("rename the column before making other schema changes")
		}
		p.Mode, p.RenameFrom, p.RenameTo = "rename_column", renamed[0].Source, renamed[0].Destination
		p.SQL = []string{"ALTER TABLE " + Quote(schema) + "." + Quote(desired.Name) + " RENAME COLUMN " + Quote(p.RenameFrom) + " TO " + Quote(p.RenameTo)}
		return p, nil
	}
	if SQLiteWithoutRowID(old) != SQLiteWithoutRowID(desired) {
		return p, errors.New("changing rowid mode requires a separate data migration")
	}
	p.Mode = "rebuild"
	// Preview uses a clearly reserved placeholder. The executor chooses a unique
	// shadow name under its lock and never executes this caller-visible SQL.
	shadow := "__nyadb_rebuild_preview"
	create, _ = CreateSQLite(desired, schema, shadow)
	p.SQL = []string{create, "-- Copy retained columns and verify typed values, row count and row identity", "DROP TABLE " + Quote(schema) + "." + Quote(desired.Name), "ALTER TABLE " + Quote(schema) + "." + Quote(shadow) + " RENAME TO " + Quote(desired.Name)}
	for _, d := range desired.Definitions {
		sql, _ := DefinitionSQLite(d, schema, desired.Name)
		p.SQL = append(p.SQL, sql)
	}
	return p, nil
}
func SQLiteWithoutRowID(t Table) bool {
	ts, _ := lex(t.Options)
	for _, t := range ts {
		if t.word("WITHOUT") {
			return true
		}
	}
	return false
}
func SQLiteAutoIncrement(t Table) bool {
	for _, c := range t.Columns() {
		ts, _ := lex(c.SQL)
		for _, t := range ts {
			if t.word("AUTOINCREMENT") {
				return true
			}
		}
	}
	return false
}
