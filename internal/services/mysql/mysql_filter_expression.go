package mysql

import (
	"github.com/nyakang/nyadb/internal/db/contract"
	"strings"
)

func mysqlFilterWhere(columns []MySQLStructureColumn, filters []MySQLDataFilterRule, expr *contract.FilterExpression[contract.DataFilterRule]) (string, []any, error) {
	allowed := map[string]MySQLStructureColumn{}
	for _, col := range columns {
		allowed[col.Name] = col
	}
	if expr != nil {
		if _, err := expr.Rules(MySQLDataMaxFilterRules, 3); err != nil {
			return "", nil, ErrMySQLDataPage
		}
	}
	var args []any
	leaf := func(rule MySQLDataFilterRule) (string, error) {
		col, ok := allowed[rule.Column]
		if !ok || len(rule.Value) > 4096 {
			return "", ErrMySQLDataPage
		}
		q, err := quotedMySQLIdentifier(rule.Column)
		if err != nil {
			return "", err
		}
		switch rule.Mode {
		case "contains", "not_contains", "like", "not_like":
			if !mysqlDataTextType(strings.ToLower(col.DataType)) {
				return "", ErrMySQLDataPage
			}
			args = append(args, rule.Value)
			switch rule.Mode {
			case "contains":
				return "LOCATE(?, " + q + ") > 0", nil
			case "not_contains":
				return "LOCATE(?, " + q + ") = 0", nil
			case "like":
				args[len(args)-1] = "%" + rule.Value + "%"
				return q + " LIKE ?", nil
			default:
				args[len(args)-1] = "%" + rule.Value + "%"
				return q + " NOT LIKE ?", nil
			}
		case "equal", "not_equal", "less", "greater":
			args = append(args, rule.Value)
			return q + map[string]string{"equal": " = ", "not_equal": " <> ", "less": " < ", "greater": " > "}[rule.Mode] + "?", nil
		case "is_null":
			return q + " IS NULL", nil
		case "is_not_null":
			return q + " IS NOT NULL", nil
		default:
			return "", ErrMySQLDataPage
		}
	}
	if expr == nil {
		if len(filters) > MySQLDataMaxFilterRules {
			return "", nil, ErrMySQLDataPage
		}
		parts := []string{}
		for _, rule := range filters {
			part, err := leaf(rule)
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, part)
		}
		return strings.Join(parts, " AND "), args, nil
	}
	where, err := contract.CompileFilter(expr, leaf)
	return where, args, err
}
