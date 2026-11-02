package schemaddl

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"
)

// CopySQLite preserves complete clauses/options and explicit definitions.
// Only CREATE identity and ON target are rewritten for indexes/triggers;
// trigger bodies retain their original explicit object references. Optional
// self-FK rebinding affects only the REFERENCES parent identifier.
func CopySQLite(original Table, schema, destination string, rebindSelf bool) (Table, error) {
	if err := validateSQLiteTable(original); err != nil {
		return Table{}, err
	}
	if err := Identifier(schema); err != nil {
		return Table{}, err
	}
	if err := Identifier(destination); err != nil {
		return Table{}, err
	}
	if asciiLower(original.Name) == asciiLower(destination) {
		return Table{}, errors.New("copy destination equals the original table")
	}
	copied := original.Clone()
	copied.Name = destination
	if rebindSelf {
		for i := range copied.Clauses {
			var err error
			copied.Clauses[i].SQL, err = sqliteCopyReferences(copied.Clauses[i].SQL, original.Name, destination)
			if err != nil {
				return Table{}, err
			}
		}
	}
	for i, d := range original.Definitions {
		name := sqliteCopyDefinitionName(destination, d.Name)
		var err error
		copied.Definitions[i], err = sqliteCopyDefinition(d, schema, original.Name, destination, name)
		if err != nil {
			return Table{}, err
		}
	}
	if err := validateSQLiteTable(copied); err != nil {
		return Table{}, err
	}
	return copied, nil
}

func sqliteCopyDefinitionName(destination, original string) string {
	name := destination + "_" + original
	if len(name) <= 1024 {
		return name
	}
	digest := sha256.Sum256([]byte(name))
	suffix := "_" + hex.EncodeToString(digest[:8])
	prefix := name[:1024-len(suffix)]
	for !utf8.ValidString(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix + suffix
}

type sqliteCopyEdit struct {
	start, end  int
	replacement string
}

func sqliteCopyEdits(src string, edits []sqliteCopyEdit) string {
	// Callers append edits in lexer order. Strings/comments and all other
	// bytes remain untouched; replacements never participate in another scan.
	var b strings.Builder
	at := 0
	for _, e := range edits {
		b.WriteString(src[at:e.start])
		b.WriteString(e.replacement)
		at = e.end
	}
	b.WriteString(src[at:])
	return b.String()
}
func sqliteCopyReferences(src, original, destination string) (string, error) {
	ts, err := lex(src)
	if err != nil {
		return "", err
	}
	depth := 0
	var edits []sqliteCopyEdit
	for i := 0; i < len(ts); i++ {
		t := ts[i]
		if t.kind == 'p' && t.text == "(" {
			depth++
		}
		if t.kind == 'p' && t.text == ")" {
			depth--
		}
		if depth != 0 || !t.word("REFERENCES") {
			continue
		}
		schema, parent, end, err := qualifiedName(ts, i+1)
		if err != nil || schema != "" {
			return "", errors.New("SQLite foreign key parent must be an unqualified identifier")
		}
		if asciiLower(parent) == asciiLower(original) {
			edits = append(edits, sqliteCopyEdit{ts[i+1].start, ts[end-1].end, Quote(destination)})
		}
		i = end - 1
	}
	return sqliteCopyEdits(src, edits), nil
}
func sqliteCopyDefinition(d Definition, schema, original, destination, name string) (Definition, error) {
	if _, err := DefinitionSQLite(d, schema, original); err != nil {
		return Definition{}, err
	}
	ts, err := lex(d.SQL)
	if err != nil {
		return Definition{}, err
	}
	at := 2
	if d.Kind == "index" && ts[1].word("UNIQUE") {
		at++
	}
	start := at
	_, _, end, err := qualifiedName(ts, at)
	if err != nil {
		return Definition{}, err
	}
	edits := []sqliteCopyEdit{{ts[start].start, ts[end-1].end, Quote(schema) + "." + Quote(name)}}
	on := -1
	for i := end; i < len(ts); i++ {
		if ts[i].word("ON") {
			on = i
			break
		}
		if ts[i].word("BEGIN") {
			break
		}
	}
	if on < 0 {
		return Definition{}, errors.New("copied definition has no ON target")
	}
	_, _, end, err = qualifiedName(ts, on+1)
	if err != nil {
		return Definition{}, err
	}
	edits = append(edits, sqliteCopyEdit{ts[on+1].start, ts[end-1].end, Quote(destination)})
	copied := Definition{Name: name, Kind: d.Kind, SQL: sqliteCopyEdits(d.SQL, edits)}
	if _, err = DefinitionSQLite(copied, schema, destination); err != nil {
		return Definition{}, err
	}
	return copied, nil
}

// CopySQLiteSQL returns a complete immutable schema preview. Data selection,
// rowid/sequence verification and transaction control belong to the adapter.
func CopySQLiteSQL(copied Table, schema string) ([]string, error) {
	create, err := CreateSQLite(copied, schema, copied.Name)
	if err != nil {
		return nil, err
	}
	sql := []string{create}
	for _, d := range copied.Definitions {
		statement, err := DefinitionSQLite(d, schema, copied.Name)
		if err != nil {
			return nil, err
		}
		sql = append(sql, statement)
	}
	return sql, nil
}
