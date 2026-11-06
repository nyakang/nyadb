package schemaddl

import (
	"errors"
	"slices"
	"strings"
)

type View struct {
	Schema, Name, SelectSQL string
	Columns                 []string
	Triggers                []Definition
	Options                 []string `json:",omitempty"`
}

func (v View) Clone() View {
	v.Columns = slices.Clone(v.Columns)
	v.Options = slices.Clone(v.Options)
	v.Triggers = slices.Clone(v.Triggers)
	for i := range v.Triggers {
		v.Triggers[i].Postgres = v.Triggers[i].Postgres.Clone()
	}
	return v
}

type ViewSnapshot struct {
	Engine, Revision, DDL string
	Exists                bool
	View                  View
}

func (s ViewSnapshot) Clone() ViewSnapshot { s.View = s.View.Clone(); return s }

type ViewPlan struct {
	Action  string
	Desired View
	SQL     []string
}

func ParseSQLiteView(ddl string) (View, error) {
	ts, err := lex(ddl)
	if err != nil {
		return View{}, err
	}
	if err = single(ts, false); err != nil {
		return View{}, err
	}
	if len(ts) < 5 || !ts[0].word("CREATE") || !ts[1].word("VIEW") {
		return View{}, ErrUnsupported
	}
	schema, name, at, err := qualifiedName(ts, 2)
	if err != nil {
		return View{}, err
	}
	v := View{Schema: schema, Name: name}
	if at < len(ts) && ts[at].kind == 'p' && ts[at].text == "(" {
		at++
		for {
			if at >= len(ts) || !identifierToken(ts[at]) {
				return View{}, errors.New("invalid declared view column")
			}
			v.Columns = append(v.Columns, ts[at].text)
			at++
			if at < len(ts) && ts[at].kind == 'p' && ts[at].text == "," {
				at++
				continue
			}
			if at >= len(ts) || ts[at].kind != 'p' || ts[at].text != ")" {
				return View{}, errors.New("invalid view column list")
			}
			at++
			break
		}
	}
	if at >= len(ts) || !ts[at].word("AS") || at+1 >= len(ts) {
		return View{}, errors.New("complete CREATE VIEW AS SELECT is required")
	}
	at++
	end := len(ddl)
	if last := ts[len(ts)-1]; last.kind == 'p' && last.text == ";" {
		end = last.start
	}
	v.SelectSQL = strings.TrimSpace(ddl[ts[at].start:end])
	if err = validateSQLiteView(v); err != nil {
		return View{}, err
	}
	return v, nil
}
func validateSQLiteView(v View) error {
	if len(v.Options) > 0 {
		return ErrUnsupported
	}
	if err := Identifier(v.Name); err != nil {
		return err
	}
	if strings.HasPrefix(asciiLower(v.Name), "sqlite_") {
		return ErrUnsupported
	}
	if v.Schema != "" {
		if err := Identifier(v.Schema); err != nil {
			return err
		}
	}
	if len(v.Columns) > 2000 || len(v.Triggers) > 1000 || len(v.SelectSQL) > 1<<20 {
		return errors.New("complete view definition exceeds limits")
	}
	names := map[string]bool{}
	for _, name := range v.Columns {
		if err := Identifier(name); err != nil {
			return err
		}
		key := asciiLower(name)
		if names[key] {
			return errors.New("duplicate declared view column")
		}
		names[key] = true
	}
	ts, err := lex(v.SelectSQL)
	if err != nil {
		return err
	}
	if err = single(ts, false); err != nil {
		return err
	}
	if len(ts) == 0 || (!ts[0].word("SELECT") && !ts[0].word("WITH") && !ts[0].word("VALUES")) {
		return errors.New("view body must be a SELECT, WITH or VALUES query")
	}
	for _, t := range ts {
		if t.kind == 'p' && t.text == ";" {
			return errors.New("statement delimiter in view query")
		}
	}
	return nil
}

func CreateSQLiteView(v View, schema string) (string, error) {
	if err := validateSQLiteView(v); err != nil {
		return "", err
	}
	if err := Identifier(schema); err != nil {
		return "", err
	}
	if v.Schema != "" && v.Schema != schema {
		return "", errors.New("view definition belongs to another namespace")
	}
	sql := "CREATE VIEW " + Quote(schema) + "." + Quote(v.Name)
	if len(v.Columns) > 0 {
		var columns []string
		for _, name := range v.Columns {
			columns = append(columns, Quote(name))
		}
		sql += " (" + strings.Join(columns, ",") + ")"
	}
	// A newline prevents a trailing line comment from swallowing a delimiter
	// when preview statements are joined for display.
	return sql + " AS\n" + v.SelectSQL + "\n", nil
}

func PlanSQLiteView(original ViewSnapshot, action string, desired View, schema string) (ViewPlan, error) {
	p := ViewPlan{Action: action}
	if original.Engine != "sqlite" || original.Revision == "" {
		return p, errors.New("complete original SQLite view snapshot is required")
	}
	if err := Identifier(schema); err != nil {
		return p, err
	}
	if err := Identifier(original.View.Name); err != nil {
		return p, err
	}
	if action != "create" && !original.Exists || action == "create" && original.Exists {
		return p, errors.New("view existence differs from requested operation")
	}
	if action == "drop" {
		p.Desired = original.View.Clone()
		p.SQL = []string{"DROP VIEW " + Quote(schema) + "." + Quote(original.View.Name)}
		return p, nil
	}
	if action != "create" && action != "alter" && action != "rename" {
		return p, ErrUnsupported
	}
	desired = desired.Clone()
	if action == "create" && len(desired.Triggers) != 0 {
		return p, errors.New("create view does not accept hidden trigger operations")
	}
	if action != "create" && Signature(desired.Triggers) != Signature(original.View.Triggers) {
		return p, errors.New("view triggers must be preserved in a separate view edit")
	}
	if action == "alter" && desired.Name != original.View.Name {
		return p, errors.New("rename the view separately from changing its query")
	}
	if action == "rename" {
		if asciiLower(desired.Name) == asciiLower(original.View.Name) {
			return p, errors.New("rename destination equals the original view")
		}
		if desired.SelectSQL != original.View.SelectSQL || Signature(desired.Columns) != Signature(original.View.Columns) {
			return p, errors.New("rename must preserve the original view query and columns")
		}
	}
	statement, err := CreateSQLiteView(desired, schema)
	if err != nil {
		return p, err
	}
	if original.Exists {
		p.SQL = append(p.SQL, "DROP VIEW "+Quote(schema)+"."+Quote(original.View.Name))
	}
	p.SQL = append(p.SQL, statement)
	for i, trigger := range desired.Triggers {
		if trigger.Kind != "trigger" || trigger.Postgres != nil {
			return p, ErrUnsupported
		}
		definition, err := sqliteCopyDefinition(trigger, schema, original.View.Name, desired.Name, trigger.Name)
		if err != nil {
			return p, err
		}
		desired.Triggers[i] = definition
		p.SQL = append(p.SQL, definition.SQL)
	}
	desired.Schema = schema
	p.Desired = desired
	return p, nil
}

func SQLiteViewMatches(actual, desired View, schema string) bool {
	if actual.Name != desired.Name || Signature(actual.Columns) != Signature(desired.Columns) || strings.TrimSpace(actual.SelectSQL) != strings.TrimSpace(desired.SelectSQL) || len(actual.Triggers) != len(desired.Triggers) {
		return false
	}
	want := map[string]string{}
	for _, d := range desired.Triggers {
		sql, err := DefinitionSQLite(d, schema, desired.Name)
		if err != nil {
			return false
		}
		want[d.Name] = sql
	}
	for _, d := range actual.Triggers {
		sql, err := DefinitionSQLite(d, schema, actual.Name)
		if err != nil || want[d.Name] != sql {
			return false
		}
	}
	return true
}
