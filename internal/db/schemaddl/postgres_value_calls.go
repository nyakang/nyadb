package schemaddl

import "sort"

// PostgreSQLValueExpressionRoutines collects dispatch OIDs from a captured
// pg_attrdef tree. It is repeatability evidence only: it never grants probe
// callback permission or resolves user SQL. Sequence expressions and unknown
// expression node kinds cannot acquire a repeatable value baseline.
func PostgreSQLValueExpressionRoutines(raw string) ([]int64, error) {
	if len(raw) > 1<<20 {
		return nil, ErrUnsupported
	}
	ts, err := pgParseNode(raw)
	if err != nil || len(ts) != 1 || ts[0].kind != '{' {
		return nil, ErrUnsupported
	}
	seen := map[int64]bool{}
	var walk func(*pgNodeToken) error
	walk = func(t *pgNodeToken) error {
		if t.kind == '{' {
			name, fields, err := pgNodeFields(t)
			// PostgreSQL 14 emits ARRAY for ArrayExpr in pg_node_tree;
			// later versions emit ARRAYEXPR. Traverse both identically so
			// nested dispatch cannot disappear behind the legacy spelling.
			if name == "ARRAY" {
				name = "ARRAYEXPR"
			}
			if err != nil || !pgKnownExpressionNode(name) || name == "NEXTVALUEEXPR" || name == "AGGREF" || name == "WINDOWFUNC" || name == "RANGETBLENTRY" {
				return ErrUnsupported
			}
			required := ""
			switch name {
			case "FUNCEXPR":
				required = ":funcid"
			case "OPEXPR", "DISTINCTEXPR", "NULLIFEXPR", "SCALARARRAYOPEXPR":
				required = ":opfuncid"
			case "SQLVALUEFUNCTION":
				required = ":op"
			}
			found := required == ""
			for _, f := range fields {
				if f.name == required {
					found = true
				}
				if f.name == ":funcid" && name == "FUNCEXPR" || f.name == ":opfuncid" && (name == "OPEXPR" || name == "DISTINCTEXPR" || name == "NULLIFEXPR" || name == "SCALARARRAYOPEXPR") {
					oid, err := pgNodeInteger(f.values)
					if err != nil || oid <= 0 || oid >= 16384 {
						return ErrUnsupported
					}
					seen[oid] = true
				}
				if name == "SQLVALUEFUNCTION" && f.name == ":op" {
					op, err := pgNodeInteger(f.values)
					if err != nil || op < 0 || op > 14 {
						return ErrUnsupported
					}
				}
				for _, child := range f.values {
					if err := walk(child); err != nil {
						return err
					}
				}
			}
			if !found {
				return ErrUnsupported
			}
			return nil
		}
		for _, child := range t.children {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(ts[0]); err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(seen))
	for oid := range seen {
		out = append(out, oid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}
