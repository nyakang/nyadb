package schemaddl

import (
	"sort"
	"strings"
)

func normalizeOpenGaussView(v View) (View, error) {
	v = v.Clone()
	if len(v.SelectSQL) > 1<<20 || len(v.Columns) > 10000 || len(v.Options) > 2 {
		return v, ErrUnsupported
	}
	ts, err := postgresTokens(v.SelectSQL, true)
	if err != nil {
		return v, err
	}
	end := len(ts)
	if end >= 3 && ts[end-1].word("OPTION") && ts[end-2].word("CHECK") {
		start := end - 3
		value := "cascaded"
		if ts[start].word("LOCAL") || ts[start].word("CASCADED") {
			value = strings.ToLower(ts[start].text)
			start--
		}
		if start < 0 || !ts[start].word("WITH") {
			return v, ErrUnsupported
		}
		v.Options = append(v.Options, "check_option="+value)
		v.SelectSQL = strings.TrimSpace(v.SelectSQL[:ts[start].start])
	}
	seen := map[string]bool{}
	for _, option := range v.Options {
		key, value, ok := strings.Cut(option, "=")
		if !ok || seen[key] {
			return v, ErrUnsupported
		}
		seen[key] = true
		if key == "security_barrier" && (value == "true" || value == "false") {
			continue
		}
		if key == "check_option" && (value == "local" || value == "cascaded") {
			continue
		}
		return v, ErrUnsupported
	}
	sort.Strings(v.Options)
	if len(v.Options) == 0 {
		v.Options = nil
	}
	return v, nil
}

func CreateOpenGaussView(v View, schema string) (string, error) {
	v, err := normalizeOpenGaussView(v)
	if err != nil {
		return "", err
	}
	options := v.Options
	v.Options = nil
	statement, err := CreatePostgreSQLView(v, schema)
	if err != nil {
		return "", err
	}
	if len(options) > 0 {
		// The body length locates the generated AS, even for identifiers which
		// contain AS/newlines. Never search and rewrite inside a quoted name.
		at := len(statement) - len(v.SelectSQL) - 1 - len(" AS\n")
		statement = statement[:at] + " WITH (" + strings.Join(options, ",") + ")" + statement[at:]
	}
	return statement, nil
}

func PlanOpenGaussView(original ViewSnapshot, action string, desired View, schema string) (ViewPlan, error) {
	if original.Engine != "opengauss" {
		return ViewPlan{}, ErrUnsupported
	}
	if original.View.Schema != schema || desired.Schema != "" && desired.Schema != schema || len(desired.Triggers) != 0 {
		return ViewPlan{}, ErrUnsupported
	}
	var err error
	desired, err = normalizeOpenGaussView(desired)
	if err != nil {
		return ViewPlan{}, err
	}
	original = original.Clone()
	before, err := normalizeOpenGaussView(original.View)
	if err != nil && original.Exists {
		return ViewPlan{}, err
	}
	if action == "rename" && Signature(before.Options) != Signature(desired.Options) {
		return ViewPlan{}, ErrUnsupported
	}
	original.Engine = "postgresql"
	original.View.Options = nil
	bare := desired.Clone()
	bare.Options = nil
	p, err := PlanPostgreSQLView(original, action, bare, schema)
	if err != nil {
		return p, err
	}
	p.Desired = desired
	if action == "create" || action == "alter" {
		statement, err := CreateOpenGaussView(desired, schema)
		if err != nil {
			return p, err
		}
		if action == "alter" {
			statement = strings.Replace(statement, "CREATE VIEW", "CREATE OR REPLACE VIEW", 1)
		}
		p.SQL = []string{statement}
	}
	return p, nil
}
