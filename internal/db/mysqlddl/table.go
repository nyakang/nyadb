// Package mysqlddl plans independent edits to public MySQL DDL. Unrecognized
// clauses remain verbatim; it never reconstructs a table from partial metadata.
package mysqlddl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nyakang/nyadb/internal/db/sqltext"
)

type Kind string

const (
	Column  Kind = "column"
	Primary Kind = "primary"
	Index   Kind = "index"
	Foreign Kind = "foreign"
	Check   Kind = "check"
	Unknown Kind = "unknown"
)

type Clause struct {
	ID, Name, SQL string
	Kind          Kind
}
type Trigger struct{ Name, SQL, SQLMode string }
type Snapshot struct {
	TriggersUnknown      bool
	Database, Table, DDL string
	SQLMode              string
	RuntimeGeneration    uint64
	Triggers             []Trigger
}

func (s Snapshot) Fingerprint() string {
	triggers := append([]Trigger(nil), s.Triggers...)

	b, _ := json.Marshal(struct {
		DDL             string
		Triggers        []Trigger
		TriggersUnknown bool
	}{s.DDL, triggers, s.TriggersUnknown})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type Table struct {
	Database, Name string
	Clauses        []Clause
	Options        string
}

func (t Table) Clone() Table { t.Clauses = append([]Clause(nil), t.Clauses...); return t }
func Name(token sqltext.Token) string {
	s := token.Text
	if strings.HasPrefix(s, "`") {
		return strings.ReplaceAll(s[1:len(s)-1], "``", "`")
	}
	return s
}
func significant(sql string) ([]sqltext.Token, error) {
	tokens, e := sqltext.Tokens(sql)
	if e != nil {
		return nil, e
	}
	out := tokens[:0]
	for _, t := range tokens {
		if !t.Comment {
			out = append(out, t)
		}
	}
	return out, nil
}
func Parse(database, table, ddl string) (Table, error) {
	tokens, e := significant(ddl)
	if e != nil {
		return Table{}, e
	}
	if len(tokens) < 5 || !sqltext.Word(tokens[0], "CREATE") || !sqltext.Word(tokens[1], "TABLE") {
		return Table{}, errors.New("a complete CREATE TABLE definition is required")
	}
	i := 2
	name := Name(tokens[i])
	i++
	if i < len(tokens) && tokens[i].Text == "." {
		if name != database {
			return Table{}, errors.New("CREATE TABLE database does not match target")
		}
		i++
		if i >= len(tokens) {
			return Table{}, errors.New("missing table name")
		}
		name = Name(tokens[i])
		i++
	}
	if name != table || i >= len(tokens) || tokens[i].Text != "(" {
		return Table{}, errors.New("CREATE TABLE identity does not match target")
	}
	start := tokens[i].End
	depth := 1
	t := Table{Database: database, Name: table}
	for i++; i < len(tokens); i++ {
		v := tokens[i]
		if v.Quoted {
			continue
		}
		switch v.Text {
		case "(":
			depth++
		case ")":
			depth--
		}
		if v.Text == "," && depth == 1 || depth == 0 {
			raw := strings.TrimSpace(ddl[start:v.Start])
			if raw == "" {
				return Table{}, errors.New("empty table clause")
			}
			c, e := ParseClause(raw)
			if e != nil {
				return Table{}, e
			}
			if c.Kind == Unknown {
				c.ID = fmt.Sprintf("unknown-%d", len(t.Clauses))
			}
			t.Clauses = append(t.Clauses, c)
			start = v.End
		}
		if depth == 0 {
			t.Options = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(ddl[v.End:]), ";"))
			if e = singleFragment(t.Options); e != nil {
				return Table{}, e
			}
			return t, Validate(t)
		}
	}
	return Table{}, errors.New("unterminated CREATE TABLE definition")
}
func ParseClause(raw string) (Clause, error) {
	tokens, e := significant(raw)
	if e != nil {
		return Clause{}, e
	}
	if len(tokens) < 2 {
		return Clause{}, errors.New("incomplete table clause")
	}
	if e = singleFragment(raw); e != nil {
		return Clause{}, e
	}
	c := Clause{SQL: raw}
	i := 0
	if sqltext.Word(tokens[0], "CONSTRAINT") {
		if len(tokens) < 4 {
			return c, errors.New("incomplete constraint")
		}
		c.Name = Name(tokens[1])
		i = 2
	}
	switch {
	case sqltext.Word(tokens[i], "PRIMARY"):
		c.Kind, c.Name = Primary, "PRIMARY"
	case sqltext.Word(tokens[i], "FOREIGN"):
		c.Kind = Foreign
	case sqltext.Word(tokens[i], "CHECK"):
		c.Kind = Check
	case sqltext.Word(tokens[i], "KEY") || sqltext.Word(tokens[i], "INDEX") || sqltext.Word(tokens[i], "UNIQUE") || sqltext.Word(tokens[i], "FULLTEXT") || sqltext.Word(tokens[i], "SPATIAL"):
		c.Kind = Index
		for ; i < len(tokens)-1; i++ {
			if sqltext.Word(tokens[i], "KEY") || sqltext.Word(tokens[i], "INDEX") {
				c.Name = Name(tokens[i+1])
				break
			}
		}
	case tokens[0].Quoted && strings.HasPrefix(tokens[0].Text, "`"):
		c.Kind, c.Name = Column, Name(tokens[0])
	default:
		c.Kind = Unknown
	}
	if c.Kind != Unknown && c.Name == "" {
		return c, errors.New("constraints and indexes require an explicit name")
	}
	c.ID = string(c.Kind) + ":" + c.Name
	return c, nil
}
func singleFragment(raw string) error {
	tokens, e := sqltext.Tokens(raw)
	if e != nil {
		return e
	}
	for _, t := range tokens {
		if !t.Quoted && !t.Comment && t.Text == ";" {
			return errors.New("unexpected statement delimiter in table definition")
		}
	}
	return nil
}
func Validate(t Table) error {
	if _, e := sqltext.Identifier(t.Database, true); e != nil {
		return e
	}
	if _, e := sqltext.Identifier(t.Name, true); e != nil {
		return e
	}
	seen := map[string]bool{}
	fields := 0
	for _, c := range t.Clauses {
		parsed, e := ParseClause(c.SQL)
		if e != nil {
			return e
		}
		if c.Kind != parsed.Kind || c.Name != parsed.Name {
			return errors.New("table clause identity changed without updating its draft")
		}
		if c.Kind == Unknown {
			continue
		}
		key := string(c.Kind) + ":" + strings.ToLower(c.Name)
		if seen[key] {
			return fmt.Errorf("duplicate table clause: %s", c.Name)
		}
		seen[key] = true
		if c.Kind == Column {
			fields++
			if _, e = ParseColumn(c.SQL); e != nil {
				return e
			}
		}
	}
	if fields == 0 {
		return errors.New("a table must contain at least one field")
	}
	fieldNames := map[string]bool{}
	for _, c := range t.Clauses {
		if c.Kind == Column {
			fieldNames[strings.ToLower(c.Name)] = true
		}
	}
	for _, c := range t.Clauses {
		raw := c.SQL
		if c.Kind == Column {
			f, _ := ParseColumn(raw)
			raw = f.Generation
		}
		if c.Kind == Unknown {
			continue
		}
		ts, _ := significant(raw)
		inside := c.Kind == Column
		for _, token := range ts {
			if c.Kind == Foreign && sqltext.Word(token, "REFERENCES") {
				break
			}
			if token.Text == "(" {
				inside = true
			}
			if inside && token.Quoted && strings.HasPrefix(token.Text, "`") && !fieldNames[strings.ToLower(Name(token))] {
				return fmt.Errorf("definition %s references a missing field %s", c.Name, Name(token))
			}
		}
	}
	return singleFragment(t.Options)
}
func (t Table) DDL() string {
	db, _ := sqltext.Identifier(t.Database, true)
	name, _ := sqltext.Identifier(t.Name, true)
	clauses := make([]string, len(t.Clauses))
	for i, c := range t.Clauses {
		clauses[i] = c.SQL
	}
	return "CREATE TABLE " + db + "." + name + " (\n  " + strings.Join(clauses, ",\n  ") + "\n) " + t.Options
}

// Canonical ignores whitespace and unquoted keyword case, preserving strings,
// identifiers and executable version comments. Used only for verification.
func Canonical(raw string) string {
	tokens, e := sqltext.Tokens(raw)
	if e != nil {
		return raw
	}
	var b strings.Builder
	for _, t := range tokens {
		if t.Comment && !strings.HasPrefix(t.Text, "/*!") {
			continue
		}
		v := t.Text
		if !t.Quoted && !t.Comment {
			v = strings.ToUpper(v)
		}
		fmt.Fprintf(&b, "%d:%s", len(v), v)
	}
	return b.String()
}
