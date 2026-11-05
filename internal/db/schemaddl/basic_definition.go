package schemaddl

import (
	"errors"
	"strings"
)

type BasicIndexColumn struct {
	Name       string
	Descending bool
}

// BasicDefinition handles ordinary column indexes and table constraints.
// Expressions, predicates, opclasses and other advanced clauses stay raw.
type BasicDefinition struct {
	Kind, Name, Schema, Table         string
	Unique                            bool
	Using                             string
	Columns                           []BasicIndexColumn
	ReferencedSchema, ReferencedTable string
	ReferencedColumns                 []string
	OnDelete, OnUpdate, Expression    string
}

func ParseBasicDefinition(engine, kind, raw string) (BasicDefinition, error) {
	f := BasicDefinition{Kind: kind}
	if strings.Contains(raw, "--") || strings.Contains(raw, "/*") {
		return f, ErrUnsupported
	}
	var ts []token
	var err error
	if engine == "postgresql" {
		ts, err = postgresLex(raw)
	} else if engine == "sqlite" {
		ts, err = lex(raw)
	} else {
		return f, ErrUnsupported
	}
	if err != nil || single(ts, false) != nil {
		return f, ErrUnsupported
	}
	if engine == "postgresql" {
		for i := range ts {
			if ts[i].kind == 'w' {
				ts[i].text = strings.ToLower(ts[i].text)
			}
		}
	}
	if len(ts) > 0 && ts[len(ts)-1].text == ";" {
		ts = ts[:len(ts)-1]
	}
	i := 0
	word := func(w string) bool {
		if i < len(ts) && ts[i].word(w) {
			i++
			return true
		}
		return false
	}
	if kind == "index" {
		if !word("CREATE") {
			return f, ErrUnsupported
		}
		f.Unique = word("UNIQUE")
		if !word("INDEX") {
			return f, ErrUnsupported
		}
		var indexSchema string
		indexSchema, f.Name, i, err = qualifiedName(ts, i)
		if err != nil || !word("ON") {
			return f, ErrUnsupported
		}
		f.Schema, f.Table, i, err = qualifiedName(ts, i)
		if err != nil {
			return f, ErrUnsupported
		}
		if indexSchema != "" && f.Schema != "" && indexSchema != f.Schema {
			return f, ErrUnsupported
		}
		if f.Schema == "" {
			f.Schema = indexSchema
		}
		if word("USING") {
			if engine != "postgresql" || !word("BTREE") {
				return f, ErrUnsupported
			}
			f.Using = "btree"
		}
	} else {
		if word("CONSTRAINT") {
			if i >= len(ts) || !identifierToken(ts[i]) {
				return f, ErrUnsupported
			}
			f.Name = ts[i].text
			i++
		}
		switch {
		case word("PRIMARY"):
			if !word("KEY") {
				return f, ErrUnsupported
			}
			f.Kind = "primary"
		case word("UNIQUE"):
			f.Kind = "unique"
		case word("FOREIGN"):
			if !word("KEY") {
				return f, ErrUnsupported
			}
			f.Kind = "foreign"
		case word("CHECK"):
			if i >= len(ts) || ts[i].text != "(" || ts[len(ts)-1].text != ")" {
				return f, ErrUnsupported
			}
			f.Kind = "check"
			f.Expression = raw[ts[i].end:ts[len(ts)-1].start]
			return f, nil
		default:
			return f, ErrUnsupported
		}
	}
	readColumns := func(desc bool) ([]BasicIndexColumn, error) {
		if i >= len(ts) || ts[i].text != "(" {
			return nil, ErrUnsupported
		}
		i++
		var out []BasicIndexColumn
		for {
			if i >= len(ts) || !identifierToken(ts[i]) {
				return nil, ErrUnsupported
			}
			c := BasicIndexColumn{Name: ts[i].text}
			i++
			if desc {
				c.Descending = word("DESC")
				if !c.Descending {
					word("ASC")
				}
			}
			out = append(out, c)
			if i >= len(ts) {
				return nil, ErrUnsupported
			}
			if ts[i].text == ")" {
				i++
				return out, nil
			}
			if ts[i].text != "," {
				return nil, ErrUnsupported
			}
			i++
		}
	}
	f.Columns, err = readColumns(f.Kind == "index")
	if err != nil {
		return f, err
	}
	if f.Kind == "foreign" {
		if !word("REFERENCES") {
			return f, ErrUnsupported
		}
		f.ReferencedSchema, f.ReferencedTable, i, err = qualifiedName(ts, i)
		if err != nil {
			return f, ErrUnsupported
		}
		cols, err := readColumns(false)
		if err != nil {
			return f, err
		}
		for _, c := range cols {
			f.ReferencedColumns = append(f.ReferencedColumns, c.Name)
		}
		for i < len(ts) && word("ON") {
			deletion := word("DELETE")
			if !deletion && !word("UPDATE") {
				return f, ErrUnsupported
			}
			action := ""
			for _, a := range []string{"NO ACTION", "RESTRICT", "CASCADE", "SET NULL", "SET DEFAULT"} {
				start := i
				ok := true
				for _, w := range strings.Fields(a) {
					if !word(w) {
						ok = false
						break
					}
				}
				if ok {
					action = a
					break
				}
				i = start
			}
			if action == "" {
				return f, ErrUnsupported
			}
			if deletion {
				if f.OnDelete != "" {
					return f, ErrUnsupported
				}
				f.OnDelete = action
			} else {
				if f.OnUpdate != "" {
					return f, ErrUnsupported
				}
				f.OnUpdate = action
			}
		}
	}
	if i != len(ts) {
		return f, ErrUnsupported
	}
	return f, nil
}

func (f BasicDefinition) SQL(engine string) (string, error) {
	validate := Identifier
	if engine == "postgresql" {
		validate = PostgreSQLIdentifier
	} else if engine != "sqlite" {
		return "", ErrUnsupported
	}
	if f.Name != "" {
		if err := validate(f.Name); err != nil {
			return "", err
		}
	}
	columns := make([]string, len(f.Columns))
	seen := map[string]bool{}
	for i, c := range f.Columns {
		if err := validate(c.Name); err != nil {
			return "", err
		}
		key := c.Name
		if engine == "sqlite" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			return "", errors.New("duplicate index or constraint column")
		}
		seen[key] = true
		columns[i] = Quote(c.Name)
		if c.Descending {
			if f.Kind != "index" {
				return "", ErrUnsupported
			}
			columns[i] += " DESC"
		}
	}
	if f.Kind != "check" && len(columns) == 0 {
		return "", errors.New("select at least one column")
	}
	list := "(" + strings.Join(columns, ", ") + ")"
	prefix := ""
	if f.Name != "" {
		prefix = "CONSTRAINT " + Quote(f.Name) + " "
	}
	switch f.Kind {
	case "index":
		if validate(f.Name) != nil || validate(f.Table) != nil || validate(f.Schema) != nil {
			return "", errors.New("invalid index identity")
		}
		unique := ""
		if f.Unique {
			unique = "UNIQUE "
		}
		name, target := Quote(f.Name), Quote(f.Schema)+"."+Quote(f.Table)
		if engine == "sqlite" {
			name = Quote(f.Schema) + "." + name
			target = Quote(f.Table)
		}
		using := ""
		if f.Using != "" {
			if engine != "postgresql" || f.Using != "btree" {
				return "", ErrUnsupported
			}
			using = " USING btree"
		}
		return "CREATE " + unique + "INDEX " + name + " ON " + target + using + " " + list, nil
	case "primary":
		return prefix + "PRIMARY KEY " + list, nil
	case "unique":
		return prefix + "UNIQUE " + list, nil
	case "check":
		if strings.TrimSpace(f.Expression) == "" {
			return "", errors.New("check expression is required")
		}
		raw := prefix + "CHECK (" + f.Expression + ")"
		var ts []token
		var err error
		if engine == "postgresql" {
			ts, err = postgresLex(raw)
		} else {
			ts, err = lex(raw)
		}
		if err != nil || single(ts, false) != nil {
			return "", errors.New("invalid check expression")
		}
		return raw, nil
	case "foreign":
		if len(f.ReferencedColumns) != len(columns) || validate(f.ReferencedTable) != nil {
			return "", errors.New("foreign key columns and target must match")
		}
		ref := Quote(f.ReferencedTable)
		if f.ReferencedSchema != "" {
			if engine != "postgresql" || validate(f.ReferencedSchema) != nil {
				return "", ErrUnsupported
			}
			ref = Quote(f.ReferencedSchema) + "." + ref
		}
		refs := make([]string, len(f.ReferencedColumns))
		for i, c := range f.ReferencedColumns {
			if validate(c) != nil {
				return "", errors.New("invalid referenced column")
			}
			refs[i] = Quote(c)
		}
		raw := prefix + "FOREIGN KEY " + list + " REFERENCES " + ref + " (" + strings.Join(refs, ", ") + ")"
		for _, action := range []struct{ label, value string }{{"DELETE", f.OnDelete}, {"UPDATE", f.OnUpdate}} {
			if action.value == "" {
				continue
			}
			valid := false
			for _, v := range []string{"NO ACTION", "RESTRICT", "CASCADE", "SET NULL", "SET DEFAULT"} {
				valid = valid || v == action.value
			}
			if !valid {
				return "", ErrUnsupported
			}
			raw += " ON " + action.label + " " + action.value
		}
		return raw, nil
	}
	return "", ErrUnsupported
}
