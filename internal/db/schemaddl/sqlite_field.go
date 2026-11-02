package schemaddl

import (
	"errors"
	"strconv"
	"strings"
)

// SQLiteField is the structured form for common column attributes. Definition
// remains lossless; unknown attributes make the common form read-only instead
// of granting permission to silently discard them.
type SQLiteField struct {
	Name, Type, Collation, Default                      string
	NotNull, Primary, AutoIncrement, Unique, HasDefault bool
	Unsupported                                         bool
	Definition                                          string
}

func fieldAttribute(t token) bool {
	for _, word := range []string{"CONSTRAINT", "PRIMARY", "NOT", "NULL", "UNIQUE", "CHECK", "DEFAULT", "COLLATE", "REFERENCES", "GENERATED", "AS"} {
		if t.word(word) {
			return true
		}
	}
	return false
}
func ParseSQLiteField(c Clause) (SQLiteField, error) {
	f := SQLiteField{Name: c.Name, Definition: c.SQL}
	if c.Kind != "column" {
		return f, errors.New("expected a column")
	}
	ts, err := lex(c.SQL)
	if err != nil {
		return f, err
	}
	if err = single(ts, false); err != nil {
		return f, err
	}
	at, depth := 0, 0
	for at < len(ts) {
		if depth == 0 && fieldAttribute(ts[at]) {
			break
		}
		if ts[at].kind == 'p' && ts[at].text == "(" {
			depth++
		}
		if ts[at].kind == 'p' && ts[at].text == ")" {
			depth--
		}
		at++
	}
	if at > 0 {
		f.Type = strings.TrimSpace(c.SQL[:ts[at-1].end])
	}
	if strings.Contains(c.SQL, "/*") || strings.Contains(c.SQL, "--") {
		f.Unsupported = true
	}
	seen := map[string]bool{}
	for at < len(ts) {
		t := ts[at]
		key := strings.ToUpper(t.text)
		if seen[key] {
			f.Unsupported = true
			break
		}
		seen[key] = true
		switch {
		case t.word("NOT") && at+1 < len(ts) && ts[at+1].word("NULL"):
			f.NotNull = true
			at += 2
		case t.word("NULL"):
			at++
		case t.word("PRIMARY") && at+1 < len(ts) && ts[at+1].word("KEY"):
			f.Primary = true
			at += 2
			if at < len(ts) && ts[at].word("AUTOINCREMENT") {
				f.AutoIncrement = true
				at++
			}
		case t.word("UNIQUE"):
			f.Unique = true
			at++
		case t.word("COLLATE") && at+1 < len(ts) && identifierToken(ts[at+1]):
			f.Collation = ts[at+1].text
			at += 2
		case t.word("DEFAULT"):
			first := at + 1
			at = first
			depth = 0
			for at < len(ts) {
				if depth == 0 && at > first && fieldAttribute(ts[at]) {
					break
				}
				if ts[at].kind == 'p' && ts[at].text == "(" {
					depth++
				}
				if ts[at].kind == 'p' && ts[at].text == ")" {
					depth--
				}
				at++
			}
			if at == first {
				f.Unsupported = true
				break
			}
			f.Default, f.HasDefault = strings.TrimSpace(c.SQL[ts[first].start:ts[at-1].end]), true
			if err = validateSQLiteDefault(f.Default); err != nil {
				f.Unsupported = true
			}
		default:
			f.Unsupported = true
			return f, nil
		}
	}
	return f, nil
}

func validateSQLiteDefault(value string) error {
	ts, err := lex(value)
	if err != nil {
		return err
	}
	if err = single(ts, false); err != nil {
		return err
	}
	if len(ts) == 0 {
		return errors.New("default expression cannot be empty")
	}
	for _, t := range ts {
		if t.kind == 'p' && t.text == ";" {
			return errors.New("default contains a statement delimiter")
		}
	}
	if ts[0].kind == 'p' && ts[0].text == "(" && ts[len(ts)-1].kind == 'p' && ts[len(ts)-1].text == ")" {
		depth := 0
		for i, t := range ts {
			if t.kind == 'p' && t.text == "(" {
				depth++
			}
			if t.kind == 'p' && t.text == ")" {
				depth--
			}
			if depth == 0 && i != len(ts)-1 {
				return errors.New("default has multiple expressions")
			}
		}
		return nil
	}
	if len(ts) == 1 && (ts[0].kind == 's' || ts[0].kind == 'i' || ts[0].word("NULL") || ts[0].word("TRUE") || ts[0].word("FALSE") || ts[0].word("CURRENT_DATE") || ts[0].word("CURRENT_TIME") || ts[0].word("CURRENT_TIMESTAMP")) {
		return nil
	}
	if len(ts) == 2 && ts[0].word("X") && ts[1].kind == 's' {
		return nil
	}
	if _, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
		return nil
	}
	return errors.New("use a SQL literal or one parenthesized default expression")
}

func (f SQLiteField) SQL() (string, error) {
	if err := Identifier(f.Name); err != nil {
		return "", err
	}
	if f.Unsupported {
		return "", errors.New("column has unsupported attributes; preserve its complete definition")
	}
	typeTokens, err := lex(f.Type)
	if err != nil {
		return "", err
	}
	for _, t := range typeTokens {
		if fieldAttribute(t) {
			return "", errors.New("type contains a column attribute")
		}
	}
	if f.AutoIncrement && (!f.Primary || !strings.EqualFold(strings.TrimSpace(f.Type), "INTEGER")) {
		return "", errors.New("AUTOINCREMENT requires INTEGER PRIMARY KEY")
	}
	var parts []string
	if f.Type != "" {
		parts = append(parts, strings.TrimSpace(f.Type))
	}
	if f.Primary {
		parts = append(parts, "PRIMARY KEY")
		if f.AutoIncrement {
			parts = append(parts, "AUTOINCREMENT")
		}
	}
	if f.NotNull {
		parts = append(parts, "NOT NULL")
	}
	if f.Unique {
		parts = append(parts, "UNIQUE")
	}
	if f.HasDefault {
		if err := validateSQLiteDefault(f.Default); err != nil {
			return "", err
		}
		parts = append(parts, "DEFAULT "+strings.TrimSpace(f.Default))
	}
	if f.Collation != "" {
		if err := Identifier(f.Collation); err != nil {
			return "", err
		}
		parts = append(parts, "COLLATE "+Quote(f.Collation))
	}
	body := strings.Join(parts, " ")
	if _, err := CreateSQLite(Table{Name: "form", Clauses: []Clause{{ID: "field", Kind: "column", Name: f.Name, SQL: body}}}, "main", "form"); err != nil {
		return "", err
	}
	return body, nil
}
