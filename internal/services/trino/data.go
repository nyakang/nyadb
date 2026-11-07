package trino

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) DataReads() contract.DataReads {
	return contract.DataReads{Loader: s.load, AdvancedLoader: s.page, TableMetadata: func(ctx context.Context, id int64, db, table string) (*contract.TableStructure, error) {
		return s.resolve(ctx, id, db, table, contract.ObjectRef{})
	}, TableInfo: s.tableInfo, Count: s.count}
}
func (s *Session) DataExports() contract.DataExports {
	return contract.DataExports{Formats: []string{"TXT", "CSV", "TSV", "JSON", "XML", "HTML"}, Exporter: s.export, JSONL: s.jsonl}
}
func (s *Session) DataFilterModes() []string {
	return []string{"equal", "not_equal", "less", "greater", "like", "is_null", "is_not_null"}
}
func filterLiteral(value string, col contract.StructureColumn) (string, error) {
	switch col.ValueKind {
	case contract.Text:
		return quoteText(value), nil
	case contract.Integer:
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return "", connections.ErrInvalid
		}
		return value, nil
	case contract.Decimal:
		if !decimalText.MatchString(value) {
			return "", connections.ErrInvalid
		}
		return "CAST(" + quoteText(value) + " AS " + col.Type + ")", nil
	case contract.Floating:
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return "", connections.ErrInvalid
		}
		if !decimalText.MatchString(value) && !strings.ContainsAny(value, "eE") {
			return "", contract.ErrUnsupported
		}
		return "CAST(" + quoteText(value) + " AS " + col.Type + ")", nil
	case contract.Boolean:
		if value != "true" && value != "false" {
			return "", connections.ErrInvalid
		}
		return value, nil
	case contract.DateTime:
		return "CAST(" + quoteText(value) + " AS " + col.Type + ")", nil
	default:
		return "", contract.ErrUnsupported
	}
}
func dataQuery(t *contract.TableStructure, o contract.DataOptions, fields []string, paged bool) (string, []string, error) {
	if o.UseKeyset || o.After != nil || o.Condition != nil {
		return "", nil, contract.ErrUnsupported
	}
	known := map[string]contract.StructureColumn{}
	for _, c := range t.Columns {
		known[c.Name] = c
	}
	if len(fields) == 0 {
		for _, c := range t.Columns {
			fields = append(fields, c.Name)
		}
	}
	if len(fields) == 0 || len(fields) > 512 {
		return "", nil, connections.ErrInvalid
	}
	selected := make([]string, len(fields))
	for i, f := range fields {
		if _, ok := known[f]; !ok {
			return "", nil, connections.ErrInvalid
		}
		selected[i] = quoteName(f)
	}
	q := "SELECT " + strings.Join(selected, ",") + " FROM " + qualified(t.Object)
	filters := append([]contract.DataFilterRule(nil), o.Filters...)
	if o.FilterColumn != "" {
		filters = append(filters, contract.DataFilterRule{Column: o.FilterColumn, Mode: o.FilterMode, Value: o.FilterValue})
	}
	if len(filters) > 64 {
		return "", nil, connections.ErrInvalid
	}
	var where []string
	var args []string
	for _, f := range filters {
		c, ok := known[f.Column]
		if !ok {
			return "", nil, connections.ErrInvalid
		}
		if c.ValueKind == contract.JSON || c.ValueKind == contract.Binary {
			return "", nil, contract.ErrUnsupported
		}
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
			if c.ValueKind != contract.Text {
				return "", nil, contract.ErrUnsupported
			}
			op = "LIKE"
		case "is_null":
			op = "IS NULL"
		case "is_not_null":
			op = "IS NOT NULL"
		default:
			return "", nil, contract.ErrUnsupported
		}
		clause := quoteName(c.Name) + " " + op
		if op != "IS NULL" && op != "IS NOT NULL" {
			v, err := filterLiteral(f.Value, c)
			if err != nil {
				return "", nil, err
			}
			clause += " ?"
			args = append(args, v)
		}
		where = append(where, clause)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	sorts := append([]contract.DataSortRule(nil), o.Sorts...)
	if len(sorts) == 0 && o.SortColumn != "" {
		sorts = append(sorts, contract.DataSortRule{Column: o.SortColumn, Descending: o.SortDescending})
	}
	if len(sorts) > 64 {
		return "", nil, connections.ErrInvalid
	}
	var order []string
	for _, r := range sorts {
		c, ok := known[r.Column]
		if !ok {
			return "", nil, connections.ErrInvalid
		}
		if c.ValueKind == contract.JSON || c.ValueKind == contract.Binary {
			return "", nil, contract.ErrUnsupported
		}
		direction := " ASC"
		if r.Descending {
			direction = " DESC"
		}
		order = append(order, quoteName(r.Column)+direction)
	}
	if len(order) > 0 {
		q += " ORDER BY " + strings.Join(order, ",")
	}
	if paged {
		if o.Offset < 0 || o.Limit < 1 || o.Limit > 1000 {
			return "", nil, connections.ErrInvalid
		}
		q += fmt.Sprintf(" OFFSET %d LIMIT %d", o.Offset, o.Limit+1)
	}
	if len(q) > 1<<20 {
		return "", nil, ErrBounds
	}
	return q, args, nil
}
func (s *Session) load(ctx context.Context, id int64, db, table string, offset int64, limit int) (*contract.DataPage, error) {
	return s.page(ctx, id, db, table, contract.DataOptions{Offset: offset, Limit: limit})
}
func (s *Session) page(ctx context.Context, id int64, db, table string, o contract.DataOptions) (*contract.DataPage, error) {
	start := time.Now()
	t, err := s.resolve(ctx, id, db, table, o.Object)
	if err != nil {
		return nil, err
	}
	q, args, err := dataQuery(t, o, nil, true)
	if err != nil {
		return nil, err
	}
	ctx, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	c, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	editable := false
	p := &contract.DataPage{Object: t.Object, Editable: &editable, Database: db, Table: table, ColumnMetadata: t.Columns, Offset: o.Offset, Limit: o.Limit, QuerySQL: q}
	for _, col := range t.Columns {
		p.Columns = append(p.Columns, col.Name)
		p.ColumnKinds = append(p.ColumnKinds, col.Type)
	}
	budget := previewBytes
	_, err = c.Run(ctx, bindQuery(q, args), func(cols []Column, row []json.RawMessage) error {
		if len(cols) != len(t.Columns) {
			return ErrProtocol
		}
		cells := make([]contract.DataCell, len(row))
		size := len(row) * 64
		for i, v := range row {
			if cols[i].Name != t.Columns[i].Name {
				return ErrProtocol
			}
			value, e := nativeCell(v, cols[i].Type)
			if e != nil {
				return e
			}
			value = preview(value)
			cells[i] = contract.DataCell{Value: &value, Text: value.Display(), Null: value.Kind == contract.Null, Truncated: value.Truncated}
			size += len(value.Text) + len(value.Bytes)
		}
		budget -= size
		if budget < 0 {
			return ErrBounds
		}
		if len(p.Rows) >= o.Limit {
			p.HasMore = true
			return nil
		}
		p.Rows = append(p.Rows, cells)
		return nil
	})
	if err != nil {
		return nil, err
	}
	p.CompletedAt = time.Now()
	p.Duration = time.Since(start)
	return p, nil
}
func (s *Session) count(ctx context.Context, id int64, db, table string) (int64, error) {
	t, err := s.resolve(ctx, id, db, table, contract.ObjectRef{})
	if err != nil {
		return 0, err
	}
	ctx, done, err := s.request(ctx)
	if err != nil {
		return 0, err
	}
	defer done()
	c, err := s.open(ctx)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	rows, err := metadataRows(ctx, c, "SELECT COUNT(*) FROM "+qualified(t.Object))
	if err != nil {
		return 0, err
	}
	if len(rows) != 1 || len(rows[0]) != 1 {
		return 0, ErrProtocol
	}
	return strconv.ParseInt(rows[0][0].Text, 10, 64)
}
func (s *Session) tableInfo(ctx context.Context, id int64, db, table string) (*contract.TableInfo, error) {
	t, err := s.resolve(ctx, id, db, table, contract.ObjectRef{})
	if err != nil {
		return nil, err
	}
	return &contract.TableInfo{Database: db, Name: table, Kind: t.Kind, Engine: t.Engine, Comment: t.Comment}, nil
}
func (s *Session) export(ctx context.Context, id int64, db, table string, o contract.DataOptions, fields []string, sink func([]contract.DataValue) error) error {
	if sink == nil {
		return connections.ErrInvalid
	}
	t, err := s.resolve(ctx, id, db, table, o.Object)
	if err != nil {
		return err
	}
	q, args, err := dataQuery(t, o, fields, false)
	if err != nil {
		return err
	}
	ctx, done, err := s.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	c, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Run(ctx, bindQuery(q, args), func(cols []Column, row []json.RawMessage) error {
		values := make([]contract.DataValue, len(row))
		for i, v := range row {
			cell, e := nativeCell(v, cols[i].Type)
			if e != nil {
				return e
			}
			values[i] = contract.DataValue{Kind: cell.Kind, Text: cell.Text, Bytes: cell.Bytes, Null: cell.Kind == contract.Null, Binary: cell.Kind == contract.Binary}
		}
		return sink(values)
	})
	return err
}
func (s *Session) jsonl(ctx context.Context, o contract.ObjectRef, w io.Writer) error {
	if w == nil {
		return connections.ErrInvalid
	}
	if err := s.checkObject(o); err != nil {
		return err
	}
	ctx, done, err := s.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	c, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	encoder := json.NewEncoder(w)
	_, err = c.Run(ctx, "SELECT * FROM "+qualified(o), func(cols []Column, row []json.RawMessage) error {
		values := make([]contract.Cell, len(row))
		for i, v := range row {
			cell, e := nativeCell(v, cols[i].Type)
			if e != nil {
				return e
			}
			values[i] = cell
		}
		return encoder.Encode(struct {
			Object  contract.ObjectRef `json:"object"`
			Columns []contract.Column  `json:"columns"`
			Values  []contract.Cell    `json:"values"`
		}{o, nativeColumns(cols), values})
	})
	return err
}

// Bind using native EXECUTE IMMEDIATE; the stored query excludes filter values.
func bindQuery(q string, args []string) string {
	if len(args) == 0 {
		return q
	}
	return "EXECUTE IMMEDIATE " + quoteText(q) + " USING " + strings.Join(args, ",")
}
