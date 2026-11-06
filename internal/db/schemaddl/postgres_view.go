package schemaddl

import (
	"errors"
	"strings"
)

func ParsePostgreSQLView(ddl string) (View, error) {
	return parsePostgreSQLView(ddl, false)
}

func parsePostgreSQLView(ddl string, native bool) (View, error) {
	if native {
		// Accept one terminal delimiter (including following comments), but
		// keep embedded delimiters for the normal fragment check to reject.
		if len(ddl) > 1<<20 {
			return View{}, ErrUnsupported
		}
		ts, err := postgresLex(ddl)
		if err != nil {
			return View{}, err
		}
		if len(ts) > 0 {
			last := ts[len(ts)-1]
			if last.kind == 'p' && last.text == ";" {
				ddl = ddl[:last.start]
			}
		}
	}
	ts, err := postgresTokens(strings.TrimSpace(ddl), true)
	if err != nil {
		return View{}, err
	}
	if len(ts) < 5 || !ts[0].word("CREATE") {
		return View{}, ErrUnsupported
	}
	i := 1
	if ts[i].word("OR") {
		if len(ts) < 7 || !ts[i+1].word("REPLACE") {
			return View{}, ErrUnsupported
		}
		i += 2
	}
	if !ts[i].word("VIEW") {
		return View{}, ErrUnsupported
	}
	i++
	v := View{}
	if i >= len(ts) || !identifierToken(ts[i]) {
		return v, ErrUnsupported
	}
	v.Name = pgTokenName(ts[i])
	i++
	if i < len(ts) && ts[i].text == "." {
		i++
		if i >= len(ts) || !identifierToken(ts[i]) {
			return v, ErrUnsupported
		}
		v.Schema, v.Name = v.Name, pgTokenName(ts[i])
		i++
	}
	if i < len(ts) && ts[i].text == "(" {
		i++
		for {
			if i >= len(ts) || !identifierToken(ts[i]) {
				return v, ErrUnsupported
			}
			v.Columns = append(v.Columns, pgTokenName(ts[i]))
			i++
			if i < len(ts) && ts[i].text == "," {
				i++
				continue
			}
			if i >= len(ts) || ts[i].text != ")" {
				return v, ErrUnsupported
			}
			i++
			break
		}
	}
	if native && i < len(ts) && ts[i].word("WITH") {
		i++
		if i >= len(ts) || ts[i].text != "(" {
			return v, ErrUnsupported
		}
		i++
		for {
			if i+2 >= len(ts) || !identifierToken(ts[i]) || ts[i+1].text != "=" {
				return v, ErrUnsupported
			}
			v.Options = append(v.Options, pgTokenName(ts[i])+"="+strings.ToLower(pgTokenName(ts[i+2])))
			i += 3
			if i < len(ts) && ts[i].text == "," {
				i++
				continue
			}
			if i >= len(ts) || ts[i].text != ")" {
				return v, ErrUnsupported
			}
			i++
			break
		}
	}
	if i >= len(ts) || !ts[i].word("AS") || i+1 >= len(ts) {
		return v, ErrUnsupported
	}
	i++
	raw := strings.TrimSpace(ddl)
	v.SelectSQL = strings.TrimSpace(raw[ts[i].start:])
	v.SelectSQL = strings.TrimSuffix(v.SelectSQL, ";")
	if native {
		v, err = normalizeOpenGaussView(v)
		if err == nil {
			_, err = CreateOpenGaussView(v, v.Schema)
		}
	} else {
		_, err = CreatePostgreSQLView(v, v.Schema)
	}
	return v, err
}
func CreatePostgreSQLView(v View, schema string) (string, error) {
	if PostgreSQLIdentifier(v.Name) != nil || schema != "" && PostgreSQLIdentifier(schema) != nil || v.Schema != "" && v.Schema != schema || len(v.Triggers) > 0 || len(v.Options) > 0 {
		return "", ErrUnsupported
	}
	ts, err := postgresTokens(v.SelectSQL, true)
	if err != nil || len(ts) == 0 {
		return "", errors.Join(ErrUnsupported, err)
	}
	if !ts[0].word("SELECT") && !ts[0].word("WITH") && !ts[0].word("VALUES") {
		return "", ErrUnsupported
	}
	for _, t := range ts {
		if t.text == ";" && t.kind == 'p' {
			return "", ErrUnsupported
		}
	}
	name := Quote(v.Name)
	if schema != "" {
		name = Quote(schema) + "." + name
	}
	var cols []string
	seen := map[string]bool{}
	for _, n := range v.Columns {
		if PostgreSQLIdentifier(n) != nil || seen[n] {
			return "", ErrUnsupported
		}
		seen[n] = true
		cols = append(cols, Quote(n))
	}
	if len(cols) > 0 {
		name += " (" + strings.Join(cols, ",") + ")"
	}
	return "CREATE VIEW " + name + " AS\n" + v.SelectSQL + "\n", nil
}
func PlanPostgreSQLView(original ViewSnapshot, action string, desired View, schema string) (ViewPlan, error) {
	p := ViewPlan{Action: action, Desired: desired.Clone()}
	if original.Engine != "postgresql" || original.Revision == "" || PostgreSQLIdentifier(schema) != nil || PostgreSQLIdentifier(original.View.Name) != nil {
		return p, ErrUnsupported
	}
	if action == "create" && original.Exists || action != "create" && !original.Exists {
		return p, ErrConflict
	}
	target := Quote(schema) + "." + Quote(original.View.Name)
	switch action {
	case "drop":
		p.SQL = []string{"DROP VIEW " + target + " RESTRICT"}
	case "rename":
		if PostgreSQLIdentifier(desired.Name) != nil || desired.Name == original.View.Name || desired.SelectSQL != original.View.SelectSQL || Signature(desired.Columns) != Signature(original.View.Columns) {
			return p, ErrUnsupported
		}
		p.SQL = []string{"ALTER VIEW " + target + " RENAME TO " + Quote(desired.Name)}
	case "create", "alter":
		if desired.Name != original.View.Name {
			return p, ErrConflict
		}
		sql, e := CreatePostgreSQLView(desired, schema)
		if e != nil {
			return p, e
		}
		if action == "alter" {
			sql = strings.Replace(sql, "CREATE VIEW", "CREATE OR REPLACE VIEW", 1)
		}
		p.SQL = []string{sql}
	default:
		return p, ErrUnsupported
	}
	return p, nil
}
func ParseView(engine, ddl string) (View, error) {
	if engine == "opengauss" {
		return parsePostgreSQLView(ddl, true)
	}
	if engine == "postgresql" {
		return ParsePostgreSQLView(ddl)
	}
	return ParseSQLiteView(ddl)
}
func CreateView(engine string, v View, schema string) (string, error) {
	if engine == "opengauss" {
		return CreateOpenGaussView(v, schema)
	}
	if engine == "postgresql" {
		return CreatePostgreSQLView(v, schema)
	}
	return CreateSQLiteView(v, schema)
}
func PlanView(original ViewSnapshot, action string, desired View, schema string) (ViewPlan, error) {
	if original.Engine == "opengauss" {
		return PlanOpenGaussView(original, action, desired, schema)
	}
	if original.Engine == "postgresql" {
		return PlanPostgreSQLView(original, action, desired, schema)
	}
	return PlanSQLiteView(original, action, desired, schema)
}
