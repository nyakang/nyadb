package manticore

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type dataQuery struct{ sql, display string }

func captureOptions(o contract.DataOptions, object contract.ObjectRef) (contract.DataOptions, error) {
	if o.Object != (contract.ObjectRef{}) && o.Object != object {
		return o, connections.ErrInvalid
	}
	if o.UseKeyset || o.After != nil {
		return o, contract.ErrUnsupported
	}
	if o.FilterColumn != "" && o.FilterMode == "" {
		o.FilterMode = "equal"
	}
	if o.FilterColumn == "" && o.FilterMode == "" && o.FilterValue != "" {
		return o, connections.ErrInvalid
	}
	if _, e := contract.EffectiveDataExpression(o); e != nil {
		return o, connections.ErrInvalid
	}
	o.Filters = slices.Clone(o.Filters)
	o.Sorts = slices.Clone(o.Sorts)
	o.Condition = o.Condition.Clone(func(f contract.DataFilterRule) contract.DataFilterRule { return f })
	return o, nil
}
func nativeLiteral(col contract.StructureColumn, text string) (string, error) {
	if !utf8.ValidString(text) || len(text) > 1<<20 || strings.ContainsRune(text, 0) {
		return "", connections.ErrInvalid
	}
	switch strings.ToLower(col.DataType) {
	case "bigint":
		n, e := strconv.ParseInt(text, 10, 64)
		if e != nil {
			return "", connections.ErrInvalid
		}
		return strconv.FormatInt(n, 10), nil
	case "uint", "integer", "timestamp", "bool":
		n, e := strconv.ParseUint(text, 10, 32)
		if e != nil || col.DataType == "bool" && n > 1 {
			return "", connections.ErrInvalid
		}
		return strconv.FormatUint(n, 10), nil
	case "float", "double":
		n, e := strconv.ParseFloat(text, 64)
		if e != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return "", connections.ErrInvalid
		}
		return strconv.FormatFloat(n, 'g', -1, 64), nil
	case "string":
		// Native SphinxQL uses backslash escapes, independently of MySQL sql_mode.
		return "'" + strings.NewReplacer("\\", "\\\\", "'", "\\'", "\n", "\\n", "\r", "\\r").Replace(text) + "'", nil
	default:
		return "", contract.ErrUnsupported
	}
}
func selectData(m *contract.TableStructure, o contract.DataOptions, fields []string, offset int64, limit int) (dataQuery, error) {
	var result dataQuery
	if offset < 0 || limit < 1 || int64(limit) > math.MaxInt32 || offset > math.MaxInt32-int64(limit) {
		return result, contract.ErrUnsupported
	}
	known := map[string]contract.StructureColumn{}
	for _, c := range m.Columns {
		known[c.Name] = c
	}
	if len(fields) == 0 {
		for _, c := range m.Columns {
			fields = append(fields, c.Name)
		}
	}
	columns := make([]string, 0, len(fields))
	seen := map[string]bool{}
	for _, field := range fields {
		if _, ok := known[field]; !ok || seen[field] {
			return result, connections.ErrInvalid
		}
		seen[field] = true
		columns = append(columns, quote(field))
	}
	if len(columns) == 0 {
		return result, contract.ErrUnsupported
	}
	expression, e := contract.EffectiveDataExpression(o)
	if e != nil {
		return result, connections.ErrInvalid
	}
	compile := func(display bool) (string, error) {
		return contract.CompileFilter(expression, func(f contract.DataFilterRule) (string, error) {
			col, ok := known[f.Column]
			if !ok {
				return "", connections.ErrInvalid
			}
			op := ""
			switch f.Mode {
			case "equal":
				op = "="
			case "not_equal":
				op = "!="
			case "less":
				op = "<"
			case "greater":
				op = ">"
			default:
				return "", contract.ErrUnsupported
			}
			literal, e := nativeLiteral(col, f.Value)
			if e != nil {
				return "", e
			}
			if display {
				literal = "?"
			}
			return quote(col.Name) + op + literal, nil
		})
	}
	where, e := compile(false)
	if e != nil {
		return result, e
	}
	displayWhere, e := compile(true)
	if e != nil {
		return result, e
	}
	if len(o.Sorts) > 0 && o.SortColumn != "" {
		return result, connections.ErrInvalid
	}
	sorts := slices.Clone(o.Sorts)
	if o.SortColumn != "" {
		sorts = append(sorts, contract.DataSortRule{Column: o.SortColumn, Descending: o.SortDescending})
	}
	if len(sorts) > 32 {
		return result, connections.ErrInvalid
	}
	var order []string
	seen = map[string]bool{}
	for _, sort := range sorts {
		col, ok := known[sort.Column]
		if !ok || seen[sort.Column] {
			return result, connections.ErrInvalid
		}
		if _, e := nativeLiteral(col, sortProbeValue(col)); e != nil {
			return result, contract.ErrUnsupported
		}
		seen[sort.Column] = true
		dir := " ASC"
		if sort.Descending {
			dir = " DESC"
		}
		order = append(order, quote(col.Name)+dir)
	}
	// Native id provides deterministic tie breaking. This is not SQL primary-key
	// metadata and does not grant mutable row identities to the shared grid.
	if !seen["id"] {
		if _, ok := known["id"]; !ok {
			return result, contract.ErrUnsupported
		}
		order = append(order, quote("id")+" ASC")
	}
	prefix := "SELECT " + strings.Join(columns, ",") + " FROM " + quote(m.Object.Catalog) + "." + quote(m.Name)
	suffix := " ORDER BY " + strings.Join(order, ",") + fmt.Sprintf(" LIMIT %d,%d OPTION max_matches=%d", offset, limit, offset+int64(limit))
	result.sql = prefix
	result.display = prefix
	if where != "" {
		result.sql += " WHERE " + where
		result.display += " WHERE " + displayWhere
	}
	result.sql += suffix
	result.display += suffix
	if len(result.sql) > 8<<20 {
		return dataQuery{}, contract.ErrUnsupported
	}
	return result, nil
}
func sortProbeValue(c contract.StructureColumn) string {
	if c.DataType == "string" {
		return ""
	}
	return "0"
}
