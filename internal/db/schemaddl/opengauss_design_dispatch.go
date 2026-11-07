package schemaddl

import "strings"

// Native index selection differs from PostgreSQL: column tables default to
// psort, remap btree/gin to cbtree/cgin, and use cbtree for unique constraints.
// Ustore accepts only ubtree. These rules precede server expression parsing.
func openGaussDesignStorage(t Table, defaultUstore bool) (string, error) {
	if t.OpenGauss == nil || t.Postgres != nil {
		return "", ErrUnsupported
	}
	if _, _, err := pgParameters(t.OpenGauss.Parameters); err != nil {
		return "", err
	}
	orientation, storage := "row", "astore"
	if defaultUstore {
		storage = "ustore"
	}
	seen := map[string]bool{}
	for _, raw := range t.OpenGauss.Parameters {
		key, value, _ := strings.Cut(raw, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "orientation" && key != "storage_type" {
			continue
		}
		if seen[key] {
			return "", ErrUnsupported
		}
		seen[key] = true
		ts, err := postgresFragment(strings.TrimSpace(value))
		if err != nil || len(ts) != 1 || ts[0].kind != 'w' && ts[0].kind != 's' {
			return "", ErrUnsupported
		}
		v := strings.ToLower(ts[0].text)
		if ts[0].kind == 's' {
			v = strings.Trim(v, "'")
		}
		if key == "orientation" {
			orientation = v
		} else {
			storage = v
		}
	}
	if orientation != "row" && orientation != "column" || storage != "astore" && storage != "ustore" {
		return "", ErrUnsupported
	}
	if orientation == "column" {
		if seen["storage_type"] {
			return "", ErrUnsupported
		}
		return "column", nil
	}
	return storage, nil
}

// OpenGaussDesignImplicitIndexMethods includes constraint and internal indexes
// even when the draft contains no explicit CREATE INDEX definition.
func OpenGaussDesignImplicitIndexMethods(t Table, defaultUstore bool) ([]string, error) {
	storage, err := openGaussDesignStorage(t, defaultUstore)
	if err != nil {
		return nil, err
	}
	switch storage {
	case "column":
		return []string{"psort", "cbtree"}, nil
	case "ustore":
		return []string{"ubtree"}, nil
	default:
		return []string{"btree"}, nil
	}
}

func OpenGaussIndexProbeLayout(d Definition, schema string, t Table, defaultUstore bool) (PostgreSQLProbeIndexLayout, error) {
	layout, err := PostgreSQLIndexProbeLayout(d, schema, t.Name)
	if err != nil {
		return layout, err
	}
	storage, err := openGaussDesignStorage(t, defaultUstore)
	if err != nil {
		return layout, err
	}
	raw, err := DefinitionPostgreSQL(d, schema, t.Name)
	if err != nil {
		return layout, err
	}
	ts, err := postgresTokens(raw, true)
	if err != nil {
		return layout, err
	}
	explicit := false
	for _, tok := range ts {
		if tok.text == "(" {
			break
		}
		explicit = explicit || tok.word("USING")
	}
	if !explicit {
		switch storage {
		case "column":
			layout.Method = "psort"
		case "ustore":
			layout.Method = "ubtree"
		}
	}
	switch storage {
	case "column":
		if layout.Method == "btree" {
			layout.Method = "cbtree"
		} else if layout.Method == "gin" {
			layout.Method = "cgin"
		}
		if layout.Method != "psort" && layout.Method != "cbtree" && layout.Method != "cgin" {
			return layout, ErrUnsupported
		}
	case "ustore":
		if layout.Method != "ubtree" {
			return layout, ErrUnsupported
		}
	default:
		if layout.Method != "btree" && layout.Method != "hash" && layout.Method != "gist" && layout.Method != "gin" {
			return layout, ErrUnsupported
		}
	}
	return layout, nil
}
