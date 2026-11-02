package mysqlddl

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/sqltext"
)

type IndexColumn struct {
	Name       string
	Prefix     int
	Descending bool
}

// Constraint covers properties that can be edited without losing public DDL.
// Executable comments, functional indexes and unknown options remain locked.
type Constraint struct {
	Kind                                     Kind
	Name, Symbol, IndexKind, Method, Comment string
	Columns                                  []IndexColumn
	Visible                                  bool
	ReferencedDatabase, ReferencedTable      string
	ReferencedColumns                        []string
	OnDelete, OnUpdate                       string
	Expression                               string
	Enforced                                 bool
	Unsupported                              bool
}

func (c Constraint) Clone() Constraint {
	c.Columns = append([]IndexColumn(nil), c.Columns...)
	c.ReferencedColumns = append([]string(nil), c.ReferencedColumns...)
	return c
}

func ParseConstraint(raw string) (Constraint, error) {
	clause, err := ParseClause(raw)
	if err != nil {
		return Constraint{}, err
	}
	c := Constraint{Kind: clause.Kind, Name: clause.Name, IndexKind: "INDEX", Visible: true, Enforced: true}
	if c.Kind == Column || c.Kind == Unknown {
		return c, errors.New("a recognized constraint or index is required")
	}
	tokens, err := sqltext.Tokens(raw)
	if err != nil {
		return c, err
	}
	for _, token := range tokens {
		if token.Comment {
			c.Unsupported = true
		}
	}
	ts, _ := significant(raw)
	i := 0
	if sqltext.Word(ts[i], "CONSTRAINT") {
		c.Symbol = Name(ts[i+1])
		i = 2
	}
	word := func(value string) bool { return i < len(ts) && sqltext.Word(ts[i], value) }
	identifier := func() (string, bool) {
		if i >= len(ts) || !ts[i].Quoted || !strings.HasPrefix(ts[i].Text, "`") {
			return "", false
		}
		v := Name(ts[i])
		i++
		return v, true
	}
	method := func() bool {
		if !word("USING") {
			return true
		}
		i++
		if i >= len(ts) || !word("BTREE") && !word("HASH") || c.Method != "" {
			return false
		}
		c.Method = strings.ToUpper(ts[i].Text)
		i++
		return true
	}
	if c.Kind == Check {
		if !word("CHECK") {
			return c, errors.New("CHECK definition required")
		}
		i++
		if i >= len(ts) || ts[i].Text != "(" {
			return c, errors.New("check expression required")
		}
		end, e := balanced(ts, i)
		if e != nil {
			return c, e
		}
		c.Expression = raw[ts[i].End:ts[end-1].Start]
		i = end
		if word("NOT") {
			i++
			c.Enforced = false
		}
		if word("ENFORCED") {
			i++
		} else if !c.Enforced {
			c.Unsupported = true
		}
		c.Unsupported = c.Unsupported || i != len(ts)
		return c, nil
	}
	if c.Kind == Primary || c.Kind == Foreign {
		i++
		if !word("KEY") {
			return c, errors.New("KEY definition required")
		}
		i++
		if c.Kind == Foreign && i < len(ts) && ts[i].Text != "(" {
			// An independent foreign-key index name is uncommon and remains
			// verbatim rather than silently disappearing from the form.
			c.Unsupported = true
			return c, nil
		}
	} else {
		for _, kind := range []string{"UNIQUE", "FULLTEXT", "SPATIAL"} {
			if word(kind) {
				c.IndexKind = kind
				i++
				break
			}
		}
		if word("KEY") || word("INDEX") {
			i++
		}
		if name, ok := identifier(); !ok || name != c.Name {
			c.Unsupported = true
			return c, nil
		}
	}
	if !method() {
		c.Unsupported = true
		return c, nil
	}
	if i >= len(ts) || ts[i].Text != "(" {
		return c, errors.New("index fields required")
	}
	i++
	for i < len(ts) && ts[i].Text != ")" {
		name, ok := identifier()
		if !ok {
			c.Unsupported = true
			return c, nil // Expression indexes are retained without rewriting.
		}
		field := IndexColumn{Name: name}
		if i < len(ts) && ts[i].Text == "(" {
			if i+2 >= len(ts) || ts[i+2].Text != ")" {
				return c, errors.New("invalid index prefix")
			}
			field.Prefix, err = strconv.Atoi(ts[i+1].Text)
			if err != nil || field.Prefix <= 0 {
				return c, errors.New("index prefix must be positive")
			}
			i += 3
		}
		if word("ASC") || word("DESC") {
			field.Descending = word("DESC")
			i++
		}
		c.Columns = append(c.Columns, field)
		if i < len(ts) && ts[i].Text == "," {
			i++
		} else {
			break
		}
	}
	if i >= len(ts) || ts[i].Text != ")" || len(c.Columns) == 0 {
		return c, errors.New("unterminated or empty index fields")
	}
	i++
	if c.Kind == Foreign {
		if !word("REFERENCES") {
			return c, errors.New("foreign key target required")
		}
		i++
		name, ok := identifier()
		if !ok {
			c.Unsupported = true
			return c, nil
		}
		c.ReferencedTable = name
		if i < len(ts) && ts[i].Text == "." {
			i++
			c.ReferencedDatabase = name
			name, ok = identifier()
			if !ok {
				return c, errors.New("referenced table required")
			}
			c.ReferencedTable = name
		}
		if i >= len(ts) || ts[i].Text != "(" {
			return c, errors.New("referenced fields required")
		}
		i++
		for i < len(ts) && ts[i].Text != ")" {
			name, ok := identifier()
			if !ok {
				c.Unsupported = true
				return c, nil
			}
			c.ReferencedColumns = append(c.ReferencedColumns, name)
			if i < len(ts) && ts[i].Text == "," {
				i++
			} else {
				break
			}
		}
		if i >= len(ts) || ts[i].Text != ")" || len(c.ReferencedColumns) == 0 {
			return c, errors.New("unterminated referenced fields")
		}
		i++
		for word("ON") {
			i++
			destination := &c.OnDelete
			if word("UPDATE") {
				destination = &c.OnUpdate
			} else if !word("DELETE") {
				break
			}
			if *destination != "" {
				c.Unsupported = true
				break
			}
			i++
			for _, action := range []string{"RESTRICT", "CASCADE", "SET NULL", "NO ACTION"} {
				parts := strings.Fields(action)
				matched := i+len(parts) <= len(ts)
				for j, p := range parts {
					if !matched || !sqltext.Word(ts[i+j], p) {
						matched = false
						break
					}
				}
				if matched {
					*destination = action
					i += len(parts)
					break
				}
			}
			if *destination == "" {
				c.Unsupported = true
				break
			}
		}
	} else {
		for i < len(ts) {
			switch {
			case word("USING"):
				if !method() {
					c.Unsupported = true
					return c, nil
				}
			case word("COMMENT"):
				i++
				if i >= len(ts) || !strings.HasPrefix(ts[i].Text, "'") {
					return c, errors.New("index comment must be a literal")
				}
				c.Comment = decodeLiteral(ts[i].Text)
				i++
			case word("VISIBLE") || word("INVISIBLE"):
				c.Visible = word("VISIBLE")
				i++
			default:
				c.Unsupported = true
				return c, nil
			}
		}
	}
	c.Unsupported = c.Unsupported || i != len(ts)
	return c, nil
}

func (c Constraint) SQL(table Table) (string, error) {
	if c.Unsupported {
		return "", errors.New("unsupported constraint properties must remain verbatim")
	}
	quote := func(name string) (string, error) {
		if !sqltext.ValidName(name) || len(name) > 256 {
			return "", errors.New("constraint identifiers must be nonempty and contain no control characters")
		}
		return sqltext.Identifier(name, true)
	}
	name, err := quote(c.Name)
	if err != nil {
		return "", err
	}
	var prefix string
	switch c.Kind {
	case Primary:
		prefix = "PRIMARY KEY"
	case Index:
		switch c.IndexKind {
		case "INDEX", "":
			prefix = "KEY " + name
		case "UNIQUE", "FULLTEXT", "SPATIAL":
			prefix = c.IndexKind + " KEY " + name
		default:
			return "", errors.New("unsupported index type")
		}
	case Foreign:
		prefix = "CONSTRAINT " + name + " FOREIGN KEY"
	case Check:
		prefix = "CONSTRAINT " + name + " CHECK"
	default:
		return "", errors.New("unsupported constraint kind")
	}
	if c.Kind == Primary && c.Symbol != "" {
		symbol, err := quote(c.Symbol)
		if err != nil {
			return "", err
		}
		prefix = "CONSTRAINT " + symbol + " " + prefix
	}
	if c.Kind == Index && c.Symbol != "" {
		symbol, err := quote(c.Symbol)
		if err != nil {
			return "", err
		}
		prefix = "CONSTRAINT " + symbol + " " + prefix
	}
	if c.Kind == Check {
		if strings.TrimSpace(c.Expression) == "" {
			return "", errors.New("check expression required")
		}
		prefix += " (" + c.Expression + ")"
		if !c.Enforced {
			prefix += " NOT ENFORCED"
		}
	} else {
		if len(c.Columns) == 0 {
			return "", errors.New("at least one constraint field required")
		}
		fields := map[string]Field{}
		for _, clause := range table.Clauses {
			if clause.Kind == Column {
				f, _ := ParseColumn(clause.SQL)
				fields[strings.ToLower(clause.Name)] = f
			}
		}
		var columns []string
		seen := map[string]bool{}
		for _, column := range c.Columns {
			field, ok := fields[strings.ToLower(column.Name)]
			if !ok || seen[strings.ToLower(column.Name)] {
				return "", fmt.Errorf("constraint references a missing or repeated field: %s", column.Name)
			}
			seen[strings.ToLower(column.Name)] = true
			v, err := quote(column.Name)
			if err != nil {
				return "", err
			}
			if column.Prefix < 0 || column.Prefix > 65535 || c.Kind == Foreign && (column.Prefix != 0 || column.Descending) {
				return "", errors.New("invalid constraint prefix or order")
			}
			if column.Prefix > 0 {
				typeName := strings.ToUpper(field.Type)
				if !strings.Contains(typeName, "CHAR") && !strings.Contains(typeName, "TEXT") && !strings.Contains(typeName, "BINARY") && !strings.Contains(typeName, "BLOB") {
					return "", errors.New("prefix indexes require text or binary fields")
				}
				v += fmt.Sprintf("(%d)", column.Prefix)
			}
			if column.Descending {
				v += " DESC"
			}
			if c.Kind == Foreign && (c.OnDelete == "SET NULL" || c.OnUpdate == "SET NULL") && !field.Nullable {
				return "", errors.New("SET NULL requires nullable source fields")
			}
			columns = append(columns, v)
		}
		prefix += " (" + strings.Join(columns, ", ") + ")"
		if c.Kind == Foreign {
			if len(c.ReferencedColumns) != len(c.Columns) {
				return "", errors.New("source and referenced field counts differ")
			}
			if err := validateSelfReference(c, table); err != nil {
				return "", err
			}
			target, err := quote(c.ReferencedTable)
			if err != nil {
				return "", err
			}
			referencedDatabase := c.ReferencedDatabase
			if referencedDatabase == "" {
				referencedDatabase = table.Database
			}
			db, err := quote(referencedDatabase)
			if err != nil {
				return "", err
			}
			target = db + "." + target
			var referenced []string
			for _, column := range c.ReferencedColumns {
				v, err := quote(column)
				if err != nil {
					return "", err
				}
				referenced = append(referenced, v)
			}
			prefix += " REFERENCES " + target + " (" + strings.Join(referenced, ", ") + ")"
			for _, action := range []struct{ label, value string }{{"DELETE", c.OnDelete}, {"UPDATE", c.OnUpdate}} {
				if action.value == "" {
					continue
				}
				switch action.value {
				case "RESTRICT", "CASCADE", "SET NULL", "NO ACTION":
					prefix += " ON " + action.label + " " + action.value
				default:
					return "", errors.New("unsupported foreign key action")
				}
			}
		} else {
			if c.Method != "" {
				if c.Method != "BTREE" && c.Method != "HASH" {
					return "", errors.New("unsupported index method")
				}
				prefix += " USING " + c.Method
			}
			if c.Comment != "" {
				prefix += " COMMENT " + Literal(c.Comment)
			}
			if !c.Visible {
				if c.Kind == Primary {
					return "", errors.New("primary keys cannot be invisible")
				}
				prefix += " INVISIBLE"
			}
		}
	}
	parsed, err := ParseConstraint(prefix)
	if err != nil || parsed.Unsupported {
		return "", errors.New("constraint form generated an unsupported SQL definition")
	}
	test := table.Clone()
	clause, err := ParseClause(prefix)
	if err != nil {
		return "", err
	}
	for i, v := range test.Clauses {
		if v.Kind == clause.Kind && v.Name == clause.Name {
			test.Clauses = append(test.Clauses[:i], test.Clauses[i+1:]...)
			break
		}
	}
	test.Clauses = append(test.Clauses, clause)
	if err := Validate(test); err != nil {
		return "", err
	}
	return prefix, nil
}

func validateSelfReference(c Constraint, table Table) error {
	if c.Kind != Foreign || c.ReferencedTable != table.Name || c.ReferencedDatabase != "" && c.ReferencedDatabase != table.Database {
		return nil
	}
	for i, column := range c.Columns {
		if i < len(c.ReferencedColumns) && strings.EqualFold(column.Name, c.ReferencedColumns[i]) {
			return errors.New("a MySQL field cannot reference itself; select a distinct source or referenced field")
		}
	}
	return nil
}

func constraintEquivalent(want, got Clause, database string) bool {
	a, errA := ParseConstraint(want.SQL)
	b, errB := ParseConstraint(got.SQL)
	if errA != nil || errB != nil || a.Unsupported || b.Unsupported {
		return false
	}
	normalize := func(c *Constraint) {
		if c.Kind == Primary || c.Kind == Index {
			// MySQL's primary/unique CONSTRAINT symbol does not create an
			// independent stored constraint identity; the index name does.
			c.Symbol = ""
			if c.Method == "BTREE" {
				c.Method = ""
			}
		}
		if c.Kind == Foreign {
			if c.ReferencedDatabase == "" {
				c.ReferencedDatabase = database
			}
			if c.OnDelete == "" || c.OnDelete == "NO ACTION" {
				c.OnDelete = "RESTRICT"
			}
			if c.OnUpdate == "" || c.OnUpdate == "NO ACTION" {
				c.OnUpdate = "RESTRICT"
			}
			c.Symbol = c.Name
		}
		if c.Kind == Check {
			for {
				ts, err := significant(c.Expression)
				if err != nil || len(ts) < 2 || ts[0].Text != "(" {
					break
				}
				end, err := balanced(ts, 0)
				if err != nil || end != len(ts) {
					break
				}
				c.Expression = c.Expression[ts[0].End:ts[end-1].Start]
			}
			c.Expression = Canonical(c.Expression)
		}
	}
	normalize(&a)
	normalize(&b)
	return reflect.DeepEqual(a, b)
}
