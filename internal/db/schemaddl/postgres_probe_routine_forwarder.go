package schemaddl

// PostgreSQLProbeRoutineSignature binds each executable node in a parsed core
// SQL return body. The caller must compare these types with the actual pg_proc
// row and approve its C/internal implementation before invoking the probe.
type PostgreSQLProbeRoutineSignature struct {
	OID, Result int64
	Arguments   []int64
}

// PostgreSQLProbeRoutineForwarder supports only nested ordinary function calls,
// bound external parameters and a native int4 zero constant (round/trunc). It
// rejects SQL text bodies, relations, coercions, operators and new node fields.
func PostgreSQLProbeRoutineForwarder(raw string, arguments []int64, result int64) ([]PostgreSQLProbeRoutineSignature, error) {
	core := func(oid int64) bool { return oid > 0 && oid < 16384 }
	if len(arguments) > 8 || !core(result) {
		return nil, ErrUnsupported
	}
	for _, typ := range arguments {
		if !core(typ) {
			return nil, ErrUnsupported
		}
	}
	expr, err := pgProbeReturnExpression(raw)
	if err != nil {
		return nil, err
	}
	var out []PostgreSQLProbeRoutineSignature
	nodes := 0
	collation := func(values []*pgNodeToken) bool {
		return pgProbeNodeAtom(values, "0") || pgProbeNodeAtom(values, "100")
	}
	var walk func(*pgNodeToken, int) (int64, error)
	walk = func(node *pgNodeToken, depth int) (int64, error) {
		nodes++
		if depth > 8 || nodes > 64 {
			return 0, ErrUnsupported
		}
		name, _, err := pgNodeFields(node)
		if err != nil {
			return 0, ErrUnsupported
		}
		fields, err := pgProbeNodeFields(node, name)
		if err != nil {
			return 0, err
		}
		switch name {
		case "PARAM":
			if !pgProbeNodeCheck(fields, map[string]string{":paramkind": "0", ":paramtypmod": "-1"}, map[string]bool{":paramid": true, ":paramtype": true, ":paramcollid": true}, true) || !collation(fields[":paramcollid"]) {
				return 0, ErrUnsupported
			}
			id, err := pgNodeInteger(fields[":paramid"])
			if err != nil || id <= 0 || id > int64(len(arguments)) {
				return 0, ErrUnsupported
			}
			typ, err := pgNodeInteger(fields[":paramtype"])
			if err != nil || typ != arguments[id-1] {
				return 0, ErrUnsupported
			}
			return typ, nil
		case "CONST":
			if !pgProbeNodeCheck(fields, map[string]string{":consttype": "23", ":consttypmod": "-1", ":constcollid": "0", ":constlen": "4", ":constbyval": "true", ":constisnull": "false"}, map[string]bool{":constvalue": true}, true) {
				return 0, ErrUnsupported
			}
			v := fields[":constvalue"]
			if len(v) != 2 || !pgProbeNodeAtom(v[:1], "4") || v[1].kind != '[' || len(v[1].children) != 4 && len(v[1].children) != 8 {
				return 0, ErrUnsupported
			}
			for _, b := range v[1].children {
				if !pgProbeNodeAtom([]*pgNodeToken{b}, "0") {
					return 0, ErrUnsupported
				}
			}
			return 23, nil
		case "FUNCEXPR":
			if !pgProbeNodeCheck(fields, map[string]string{":funcretset": "false", ":funcvariadic": "false", ":funcformat": "0"}, map[string]bool{":funcid": true, ":funcresulttype": true, ":funccollid": true, ":inputcollid": true, ":args": true}, true) || !collation(fields[":funccollid"]) || !collation(fields[":inputcollid"]) {
				return 0, ErrUnsupported
			}
			oid, err := pgNodeInteger(fields[":funcid"])
			if err != nil || !core(oid) {
				return 0, ErrUnsupported
			}
			typ, err := pgNodeInteger(fields[":funcresulttype"])
			if err != nil || !core(typ) {
				return 0, ErrUnsupported
			}
			args := fields[":args"]
			if len(args) != 1 || args[0].kind != '(' || len(args[0].children) > 8 {
				return 0, ErrUnsupported
			}
			call := PostgreSQLProbeRoutineSignature{OID: oid, Result: typ}
			for _, child := range args[0].children {
				arg, err := walk(child, depth+1)
				if err != nil {
					return 0, err
				}
				call.Arguments = append(call.Arguments, arg)
			}
			out = append(out, call)
			return typ, nil
		default:
			return 0, ErrUnsupported
		}
	}
	actual, err := walk(expr, 0)
	if err != nil || actual != result || len(out) == 0 {
		return nil, ErrUnsupported
	}
	return out, nil
}
