package schemaddl

// PostgreSQLProbeOperatorForwarder accepts only a parsed SELECT/RETURN of one
// operator applied to the two reversed external parameters. It does not parse
// SQL through the server or authorize the referenced catalog implementations.
// This narrow shape supports PostgreSQL's core commuted SQL operators without
// admitting arbitrary SQL-language functions into the probe callback policy.
func PostgreSQLProbeOperatorForwarder(raw string, left, right, result int64) (operator, routine int64, err error) {
	if left <= 0 || right <= 0 || result <= 0 {
		return 0, 0, ErrUnsupported
	}
	expr, err := pgProbeReturnExpression(raw)
	if err != nil {
		return 0, 0, ErrUnsupported
	}
	op, err := pgProbeNodeFields(expr, "OPEXPR")
	if err != nil || !pgProbeNodeCheck(op, map[string]string{":opretset": "false", ":opcollid": "0", ":inputcollid": "0"}, map[string]bool{":opno": true, ":opfuncid": true, ":opresulttype": true, ":args": true}, true) {
		return 0, 0, ErrUnsupported
	}
	operator, err = pgNodeInteger(op[":opno"])
	if err != nil || operator <= 0 {
		return 0, 0, ErrUnsupported
	}
	routine, err = pgNodeInteger(op[":opfuncid"])
	if err != nil || routine <= 0 {
		return 0, 0, ErrUnsupported
	}
	returns, err := pgNodeInteger(op[":opresulttype"])
	if err != nil || returns != result || len(op[":args"]) != 1 {
		return 0, 0, ErrUnsupported
	}
	args := op[":args"][0]
	if args.kind != '(' || len(args.children) != 2 {
		return 0, 0, ErrUnsupported
	}
	for i, typ := range []int64{right, left} {
		param, err := pgProbeNodeFields(args.children[i], "PARAM")
		id := "2"
		if i == 1 {
			id = "1"
		}
		if err != nil || !pgProbeNodeCheck(param, map[string]string{":paramkind": "0", ":paramid": id, ":paramtypmod": "-1", ":paramcollid": "0"}, map[string]bool{":paramtype": true}, true) {
			return 0, 0, ErrUnsupported
		}
		if actual, err := pgNodeInteger(param[":paramtype"]); err != nil || actual != typ {
			return 0, 0, ErrUnsupported
		}
	}
	return operator, routine, nil
}

func pgProbeNodeFields(node *pgNodeToken, want string) (map[string][]*pgNodeToken, error) {
	name, values, err := pgNodeFields(node)
	if err != nil || name != want {
		return nil, ErrUnsupported
	}
	out := map[string][]*pgNodeToken{}
	for _, field := range values {
		out[field.name] = field.values
	}
	return out, nil
}

func pgProbeNodeAtom(values []*pgNodeToken, want string) bool {
	return len(values) == 1 && values[0].kind == 'a' && values[0].atom == want
}

func pgProbeNodeCheck(values map[string][]*pgNodeToken, required map[string]string, nested map[string]bool, locations bool) bool {
	for key, want := range required {
		if !pgProbeNodeAtom(values[key], want) {
			return false
		}
	}
	for key, value := range values {
		if _, ok := required[key]; ok || nested[key] {
			continue
		}
		if locations && (key == ":location" || key == ":stmt_location" || key == ":stmt_len") {
			if _, err := pgNodeInteger(value); err == nil {
				continue
			}
		}
		return false
	}
	return true
}

// The surrounding query must have one return expression and no relation,
// predicate, CTE, utility operation or additional executable query property.
// The caller separately validates that sole expression's permitted node kinds.
func pgProbeReturnExpression(raw string) (*pgNodeToken, error) {
	if len(raw) > 1<<20 {
		return nil, ErrUnsupported
	}
	ts, err := pgParseNode(raw)
	if err != nil || len(ts) != 1 {
		return nil, ErrUnsupported
	}
	query, err := pgProbeNodeFields(ts[0], "QUERY")
	if err != nil {
		return nil, ErrUnsupported
	}
	queryRequired := map[string]string{":commandType": "1", ":querySource": "0", ":canSetTag": "true", ":utilityStmt": "<>", ":resultRelation": "0", ":rtable": "<>", ":cteList": "<>"}
	// Optional version-dependent query fields must be inert. New properties are
	// rejected until their semantics have been reviewed rather than discarded.
	neutral := map[string]string{
		":hasAggs": "false", ":hasWindowFuncs": "false", ":hasTargetSRFs": "false",
		":hasSubLinks": "false", ":hasDistinctOn": "false", ":hasRecursive": "false",
		":hasModifyingCTE": "false", ":hasForUpdate": "false", ":hasRowSecurity": "false",
		":hasGroupRTE": "false", ":isReturn": "true", ":rteperminfos": "<>",
		":mergeActionList": "<>", ":mergeTargetRelation": "0", ":mergeJoinCondition": "<>",
		":override": "0", ":onConflict": "<>", ":returningList": "<>",
		":returningOldAlias": "<>", ":returningNewAlias": "<>",
		":groupClause": "<>", ":groupDistinct": "false", ":groupingSets": "<>",
		":havingQual": "<>", ":windowClause": "<>", ":distinctClause": "<>",
		":sortClause": "<>", ":limitOffset": "<>", ":limitCount": "<>", ":limitOption": "0",
		":rowMarks": "<>", ":setOperations": "<>", ":constraintDeps": "<>", ":withCheckOptions": "<>",
	}
	for key, want := range neutral {
		if _, ok := query[key]; ok {
			queryRequired[key] = want
		}
	}
	if !pgProbeNodeCheck(query, queryRequired, map[string]bool{":jointree": true, ":targetList": true}, true) || len(query[":jointree"]) != 1 || len(query[":targetList"]) != 1 {
		return nil, ErrUnsupported
	}
	from, err := pgProbeNodeFields(query[":jointree"][0], "FROMEXPR")
	if err != nil || !pgProbeNodeCheck(from, map[string]string{":fromlist": "<>", ":quals": "<>"}, nil, false) {
		return nil, ErrUnsupported
	}
	list := query[":targetList"][0]
	if list.kind != '(' || len(list.children) != 1 {
		return nil, ErrUnsupported
	}
	target, err := pgProbeNodeFields(list.children[0], "TARGETENTRY")
	if err != nil || !pgProbeNodeCheck(target, map[string]string{":resno": "1", ":resname": "<>", ":ressortgroupref": "0", ":resorigtbl": "0", ":resorigcol": "0", ":resjunk": "false"}, map[string]bool{":expr": true}, false) || len(target[":expr"]) != 1 {
		return nil, ErrUnsupported
	}
	return target[":expr"][0], nil
}
