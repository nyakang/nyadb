package databend

import (
	"fmt"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func dataSource(m *contract.TableStructure, rowID, complete bool, fields ...string) (string, string, error) {
	if !complete {
		return qualified(m.Object) + " AS " + quoteName(dataReadAlias), "", nil
	}
	used := map[string]bool{}
	var columns, sizes []string
	for _, c := range m.Columns {
		used[c.Name] = true
		columns = append(columns, quoteName(c.Name))
		if len(fields) > 0 && !slices.Contains(fields, c.Name) {
			continue
		}
		text, err := dataText(c, quoteName(c.Name))
		if err != nil {
			return "", "", err
		}
		size := "octet_length(" + text + ")"
		if c.ValueKind == contract.Binary {
			size = "CAST(" + size + "/2 AS UInt64)"
		}
		sizes = append(sizes, "COALESCE("+size+",0)")
	}
	name := "__nyadb_complete_size"
	for used[name] {
		name += "_"
	}
	if rowID {
		columns = append(columns, `"_row_id"`)
	}
	columns = append(columns, strings.Join(sizes, "+")+" AS "+quoteName(name))
	return "(SELECT " + strings.Join(columns, ",") + " FROM " + qualified(m.Object) + ") AS " + quoteName(dataReadAlias), name, nil
}

func dataQuery(m *contract.TableStructure, p *dataProjection, o contract.DataOptions, source string, paged bool) (string, string, error) {
	if o.UseKeyset || o.After != nil || o.Condition != nil {
		return "", "", contract.ErrUnsupported
	}
	if o.Object.ProfileID != 0 && o.Object != m.Object {
		return "", "", connections.ErrInvalid
	}
	known := map[string]contract.StructureColumn{}
	for _, c := range m.Columns {
		known[c.Name] = c
	}
	native, display := "SELECT "+strings.Join(p.selects, ",")+" FROM "+source, "SELECT "+strings.Join(p.selects, ",")+" FROM "+source
	filters := slices.Clone(o.Filters)
	if o.FilterColumn != "" {
		filters = append(filters, contract.DataFilterRule{Column: o.FilterColumn, Mode: o.FilterMode, Value: o.FilterValue})
	}
	if len(filters) > 64 {
		return "", "", connections.ErrInvalid
	}
	var actual, templates []string
	for _, f := range filters {
		c, ok := known[f.Column]
		if !ok {
			return "", "", connections.ErrInvalid
		}
		column := quoteName(dataReadAlias) + "." + quoteName(f.Column)
		op := ""
		switch f.Mode {
		case "", "equal":
			op = "="
		case "not_equal":
			op = "<>"
		case "less":
			op = "<"
		case "greater":
			op = ">"
		case "like":
			op = "LIKE"
		case "is_null":
			op = "IS NULL"
		case "is_not_null":
			op = "IS NOT NULL"
		default:
			return "", "", contract.ErrUnsupported
		}
		if op == "IS NULL" || op == "IS NOT NULL" {
			actual = append(actual, column+" "+op)
			templates = append(templates, column+" "+op)
			continue
		}
		if c.ValueKind == contract.JSON || c.ValueKind == contract.Binary || strings.HasPrefix(strings.ToUpper(c.Type), "TUPLE(") || op == "LIKE" && c.ValueKind != contract.Text {
			return "", "", contract.ErrUnsupported
		}
		t, err := parseDataType(c.Type)
		if err != nil {
			return "", "", err
		}
		literal, err := dataLiteral(t, contract.DataValue{Kind: c.ValueKind, Text: f.Value})
		if err != nil {
			return "", "", err
		}
		actual = append(actual, column+" "+op+" "+literal)
		templates = append(templates, column+" "+op+" ?")
	}
	if len(actual) > 0 {
		native += " WHERE " + strings.Join(actual, " AND ")
		display += " WHERE " + strings.Join(templates, " AND ")
	}
	sorts := slices.Clone(o.Sorts)
	if len(sorts) == 0 && o.SortColumn != "" {
		sorts = append(sorts, contract.DataSortRule{Column: o.SortColumn, Descending: o.SortDescending})
	}
	if len(sorts) > 64 {
		return "", "", connections.ErrInvalid
	}
	var order []string
	for _, sort := range sorts {
		c, ok := known[sort.Column]
		if !ok {
			return "", "", connections.ErrInvalid
		}
		if c.ValueKind == contract.JSON || c.ValueKind == contract.Binary || strings.HasPrefix(strings.ToUpper(c.Type), "TUPLE(") {
			return "", "", contract.ErrUnsupported
		}
		direction := " ASC"
		if sort.Descending {
			direction = " DESC"
		}
		order = append(order, quoteName(dataReadAlias)+"."+quoteName(c.Name)+direction)
	}
	if len(order) > 0 {
		suffix := " ORDER BY " + strings.Join(order, ",")
		native += suffix
		display += suffix
	}
	if paged {
		if o.Offset < 0 || o.Limit < 1 || o.Limit > contract.DataMaxPageSize {
			return "", "", connections.ErrInvalid
		}
		suffix := fmt.Sprintf(" LIMIT %d OFFSET %d", o.Limit+1, o.Offset)
		native += suffix
		display += suffix
	}
	if len(native) > 8<<20 {
		return "", "", contract.ErrDataPage
	}
	return native, display, nil
}
