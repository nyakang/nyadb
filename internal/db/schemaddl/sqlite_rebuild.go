package schemaddl

import (
	"errors"
	"strconv"
	"strings"
)

// SQLiteReferencedTables reads only top-level REFERENCES clauses. Keywords in
// defaults, CHECK expressions, literals and quoted names are not dependencies.
func SQLiteReferencedTables(table Table) ([]string, error) {
	var parents []string
	for _, c := range table.Clauses {
		ts, err := lex(c.SQL)
		if err != nil {
			return nil, err
		}
		depth := 0
		for i, x := range ts {
			if x.kind == 'p' && x.text == "(" {
				depth++
			}
			if x.kind == 'p' && x.text == ")" {
				depth--
			}
			if depth != 0 || !x.word("REFERENCES") {
				continue
			}
			schema, name, _, err := qualifiedName(ts, i+1)
			if err != nil || schema != "" {
				return nil, errors.New("SQLite FK parent must be an unqualified identifier")
			}
			parents = append(parents, name)
		}
	}
	return parents, nil
}

// SQLiteNativeAddition admits the subset that native ADD can safely represent
// on an existing populated table. Other additions require guarded rebuilding.
func SQLiteNativeAddition(c Clause) bool {
	f, err := ParseSQLiteField(c)
	if err != nil || f.Unsupported || f.Primary || f.Unique || f.NotNull {
		return false
	}
	if !f.HasDefault {
		return true
	}
	ts, err := lex(f.Default)
	if err != nil || len(ts) == 0 {
		return false
	}
	if len(ts) == 1 && (ts[0].kind == 's' || ts[0].word("NULL") || ts[0].word("TRUE") || ts[0].word("FALSE")) {
		return true
	}
	if first := f.Default[0]; first >= '0' && first <= '9' || first == '+' || first == '-' || first == '.' {
		if _, err := strconv.ParseFloat(f.Default, 64); err == nil {
			return true
		}
	}
	// The lexer keeps X and its complete quoted byte literal separate.
	return len(ts) == 2 && ts[0].word("X") && ts[1].kind == 's'
}

// SQLiteTriggerUpdateColumns retains every UPDATE OF header name. SQLite
// otherwise silently accepts nonexistent names and disables that program.
func SQLiteTriggerUpdateColumns(ddl string) ([]string, error) {
	ts, err := lex(ddl)
	if err != nil {
		return nil, err
	}
	for i := 0; i+1 < len(ts); i++ {
		if ts[i].word("ON") || ts[i].word("BEGIN") {
			break
		}
		if !ts[i].word("UPDATE") || !ts[i+1].word("OF") {
			continue
		}
		var columns []string
		for j := i + 2; j < len(ts) && !ts[j].word("ON"); j++ {
			if ts[j].kind == 'p' && ts[j].text == "," {
				continue
			}
			if !identifierToken(ts[j]) {
				return nil, ErrUnsupported
			}
			columns = append(columns, ts[j].text)
		}
		return columns, nil
	}
	return nil, nil
}

// VerifySQLiteRebuildCatalog allows physical roots to change only for the
// selected table/indexes. Every unselected catalog row remains byte-complete.
// Native result counts are checked separately by the original executor.
func VerifySQLiteRebuildCatalog(original Snapshot, desired Table, actual Snapshot) error {
	if original.SQLite == nil || actual.SQLite == nil || original.SQLite.Validate() != nil || actual.SQLite.Validate() != nil || !original.Exists || !actual.Exists || actual.Engine != "sqlite" || original.Table.Name != desired.Name || actual.Table.Name != desired.Name || !SQLiteNativeTableMatches(actual.Table, desired, "main") {
		return ErrConflict
	}
	current := map[string]SQLiteCatalogEntry{}
	for _, e := range actual.SQLite.Entries {
		current[e.Type+":"+e.Name] = e
	}
	for _, e := range original.SQLite.Entries {
		if e.Table == desired.Name {
			continue
		}
		key := e.Type + ":" + e.Name
		if a, ok := current[key]; !ok || a != e {
			return ErrConflict
		}
		delete(current, key)
	}
	definitions := map[string]bool{}
	for _, d := range desired.Definitions {
		definitions[d.Kind+":"+d.Name] = true
	}
	tableFound := false
	for key, e := range current {
		if e.Table != desired.Name {
			if SQLiteAutoIncrement(desired) && e.Type == "table" && e.Name == "sqlite_sequence" && e.Table == "sqlite_sequence" && e.Root > 0 && e.SQL == "CREATE TABLE sqlite_sequence(name,seq)" && !e.SQLNull {
				continue
			}
			return ErrConflict
		}
		switch e.Type {
		case "table":
			if e.Name != desired.Name || e.Root == 0 || e.SQLNull || tableFound {
				return ErrConflict
			}
			tableFound = true
		case "index":
			if e.Root == 0 {
				return ErrConflict
			}
			if e.SQLNull {
				prefix := "sqlite_autoindex_" + desired.Name + "_"
				n, err := strconv.Atoi(strings.TrimPrefix(e.Name, prefix))
				if !strings.HasPrefix(e.Name, prefix) || err != nil || n < 1 {
					return ErrConflict
				}
			} else if !definitions[key] {
				return ErrConflict
			}
		case "trigger":
			if e.Root != 0 || e.SQLNull || !definitions[key] {
				return ErrConflict
			}
		default:
			return ErrConflict
		}
	}
	if !tableFound {
		return ErrConflict
	}
	return nil
}
