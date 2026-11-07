package doris

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) DataReads() contract.DataReads {
	return contract.DataReads{Loader: s.load, AdvancedLoader: s.page, TableMetadata: s.tableMetadata, TableInfo: s.tableInfo, Count: s.count}
}
func (s *Session) DataExports() contract.DataExports {
	return contract.DataExports{Formats: []string{"TXT", "CSV", "TSV", "JSON", "XML", "HTML"}, Exporter: s.export, JSONL: s.jsonl}
}
func (s *Session) DataFilterModes() []string {
	return []string{"equal", "not_equal", "less", "greater", "like", "is_null", "is_not_null"}
}
func (s *Session) tableMetadata(ctx context.Context, id int64, database, table string) (*contract.TableStructure, error) {
	o := s.Namespace(database).Object("TABLE", table)
	o.ProfileID = id
	if err := s.checkObject(o); err != nil {
		return nil, err
	}
	child, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	f, err := s.open(child)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows, err := metadataRows(child, f.conn, "SELECT TABLE_TYPE FROM internal.information_schema.tables WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", database, table)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 || len(rows[0]) != 1 {
		return nil, connections.ErrNotFound
	}
	if rows[0][0].String == "VIEW" {
		o.Kind = "VIEW"
	}
	return tableMetadata(child, f.conn, o)
}

func (s *Session) resolve(ctx context.Context, id int64, database, table string, o contract.ObjectRef) (*contract.TableStructure, error) {
	if o.ProfileID == 0 {
		return s.tableMetadata(ctx, id, database, table)
	}
	if o.ProfileID != id || o.Namespace() != s.Namespace(database) || o.Name != table {
		return nil, connections.ErrInvalid
	}
	return s.TableMetadata(ctx, o)
}
func (s *Session) load(ctx context.Context, id int64, database, table string, offset int64, limit int) (*contract.DataPage, error) {
	return s.page(ctx, id, database, table, contract.DataOptions{Offset: offset, Limit: limit})
}
func dataQuery(t *contract.TableStructure, options contract.DataOptions, columns []string, paged bool) (string, []any, error) {
	if options.UseKeyset || options.After != nil || options.Condition != nil {
		return "", nil, contract.ErrUnsupported
	}
	known := map[string]bool{}
	filterable := map[string]bool{}
	for _, col := range t.Columns {
		known[col.Name] = true
		upper := strings.ToUpper(col.Type)
		filterable[col.Name] = col.ValueKind != contract.Binary && !strings.HasPrefix(upper, "ARRAY") && !strings.HasPrefix(upper, "MAP") && !strings.HasPrefix(upper, "STRUCT") && !strings.HasPrefix(upper, "VARIANT")
	}
	if len(columns) == 0 {
		for _, col := range t.Columns {
			columns = append(columns, col.Name)
		}
	}
	if len(columns) == 0 || len(columns) > 512 {
		return "", nil, contract.ErrUnsupported
	}
	fields := make([]string, len(columns))
	for i, col := range columns {
		if !known[col] {
			return "", nil, connections.ErrInvalid
		}
		fields[i] = quote(col)
	}
	query := "SELECT " + strings.Join(fields, ",") + " FROM " + qualified(t.Object)
	filters := append([]contract.DataFilterRule(nil), options.Filters...)
	if options.FilterColumn != "" {
		filters = append(filters, contract.DataFilterRule{Column: options.FilterColumn, Mode: options.FilterMode, Value: options.FilterValue})
	}
	var args []any
	var conditions []string
	if len(filters) > 64 {
		return "", nil, connections.ErrInvalid
	}
	for _, filter := range filters {
		if !known[filter.Column] {
			return "", nil, connections.ErrInvalid
		}
		if !filterable[filter.Column] {
			return "", nil, contract.ErrUnsupported
		}
		var op string
		switch filter.Mode {
		case "", "equal":
			op = "="
		case "not_equal":
			op = "!="
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
			return "", nil, contract.ErrUnsupported
		}
		clause := quote(filter.Column) + " " + op
		if op != "IS NULL" && op != "IS NOT NULL" {
			clause += " ?"
			args = append(args, filter.Value)
		}
		conditions = append(conditions, clause)
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	sorts := append([]contract.DataSortRule(nil), options.Sorts...)
	if len(sorts) == 0 && options.SortColumn != "" {
		sorts = append(sorts, contract.DataSortRule{Column: options.SortColumn, Descending: options.SortDescending})
	}
	if len(sorts) > 64 {
		return "", nil, connections.ErrInvalid
	}
	var order []string
	for _, sort := range sorts {
		if !known[sort.Column] {
			return "", nil, connections.ErrInvalid
		}
		if !filterable[sort.Column] {
			return "", nil, contract.ErrUnsupported
		}
		direction := " ASC"
		if sort.Descending {
			direction = " DESC"
		}
		order = append(order, quote(sort.Column)+direction)
	}
	if len(order) > 0 {
		query += " ORDER BY " + strings.Join(order, ",")
	}
	if paged {
		if options.Offset < 0 || options.Limit < 1 || options.Limit > 1000 {
			return "", nil, connections.ErrInvalid
		}
		query += fmt.Sprintf(" LIMIT %d OFFSET %d", options.Limit+1, options.Offset)
	}
	return query, args, nil
}
func (s *Session) page(ctx context.Context, id int64, database, table string, options contract.DataOptions) (*contract.DataPage, error) {
	start := time.Now()
	t, err := s.resolve(ctx, id, database, table, options.Object)
	if err != nil {
		return nil, err
	}
	query, args, err := dataQuery(t, options, nil, true)
	if err != nil {
		return nil, err
	}
	child, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	f, err := s.open(child)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	statement, err := f.conn.PrepareContext(child, query)
	if err != nil {
		return nil, safeError(err)
	}
	defer statement.Close()
	rows, err := statement.QueryContext(child, args...)
	if err != nil {
		return nil, safeError(err)
	}
	defer rows.Close()
	budget := previewBytes
	decls := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		decls[i] = c.Type
	}
	set, err := scanRows(rows, 0, nil, &budget, decls)
	if err != nil {
		return nil, safeError(err)
	}
	for _, reason := range set.Truncation {
		if reason == "bytes" {
			return nil, errors.New("Doris page preview exceeds 16 MiB; select a smaller page size")
		}
	}
	editable := false
	page := &contract.DataPage{Object: t.Object, Editable: &editable, Database: database, Table: table, ColumnMetadata: t.Columns, Offset: options.Offset, Limit: options.Limit, QuerySQL: query, CompletedAt: time.Now(), Duration: time.Since(start)}
	for _, col := range set.Columns {
		page.Columns = append(page.Columns, col.Name)
		page.ColumnKinds = append(page.ColumnKinds, col.DatabaseType)
	}
	page.HasMore = len(set.Rows) > options.Limit
	if page.HasMore {
		set.Rows = set.Rows[:options.Limit]
	}
	for _, row := range set.Rows {
		cells := make([]contract.DataCell, len(row))
		for i, c := range row {
			value := c.Clone()
			cells[i] = contract.DataCell{Value: &value, Text: c.Display(), Null: c.Kind == contract.Null, Truncated: c.Truncated}
		}
		page.Rows = append(page.Rows, cells)
	}
	return page, nil
}
func (s *Session) count(ctx context.Context, id int64, database, table string) (int64, error) {
	if err := s.checkObject(contract.ObjectRef{ProfileID: id, Catalog: "internal", Schema: database, Kind: "TABLE", Name: table}); err != nil {
		return 0, err
	}
	child, done, err := s.request(ctx)
	if err != nil {
		return 0, err
	}
	defer done()
	var count uint64
	f, err := s.open(child)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	err = f.conn.QueryRowContext(child, "SELECT count(*) FROM "+quote("internal")+"."+quote(database)+"."+quote(table)).Scan(&count)
	if err != nil {
		return 0, safeError(err)
	}
	if count > 1<<63-1 {
		return 0, contract.ErrUnsupported
	}
	return int64(count), nil
}
func (s *Session) tableInfo(ctx context.Context, id int64, database, table string) (*contract.TableInfo, error) {
	t, err := s.tableMetadata(ctx, id, database, table)
	if err != nil {
		return nil, err
	}
	return &contract.TableInfo{Database: database, Name: table, Kind: t.Kind, Engine: t.Engine, Comment: t.Comment}, nil
}
func (s *Session) export(ctx context.Context, id int64, database, table string, options contract.DataOptions, columns []string, sink func([]contract.DataValue) error) error {
	if sink == nil {
		return connections.ErrInvalid
	}
	t, err := s.resolve(ctx, id, database, table, options.Object)
	if err != nil {
		return err
	}
	query, args, err := dataQuery(t, options, columns, false)
	if err != nil {
		return err
	}
	child, done, err := s.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	f, err := s.open(child)
	if err != nil {
		return err
	}
	defer f.Close()
	statement, err := f.conn.PrepareContext(child, query)
	if err != nil {
		return safeError(err)
	}
	defer statement.Close()
	rows, err := statement.QueryContext(child, args...)
	if err != nil {
		return safeError(err)
	}
	defer rows.Close()
	budget := 0 // exporters retain no previews
	if len(columns) == 0 {
		for _, c := range t.Columns {
			columns = append(columns, c.Name)
		}
	}
	var decls []string
	for _, name := range columns {
		for _, c := range t.Columns {
			if c.Name == name {
				decls = append(decls, c.Type)
				break
			}
		}
	}
	_, err = scanRows(rows, 0, func(_ int, _ []contract.Column, cells []contract.Cell) error {
		values := make([]contract.DataValue, len(cells))
		for i, c := range cells {
			values[i] = contract.DataValue{Kind: c.Kind, Text: c.Text, Bytes: c.Bytes, Null: c.Kind == contract.Null, Binary: c.Kind == contract.Binary}
		}
		return sink(values)
	}, &budget, decls)
	return safeError(err)
}
func (s *Session) jsonl(ctx context.Context, o contract.ObjectRef, w io.Writer) error {
	if w == nil {
		return connections.ErrInvalid
	}
	encoder := json.NewEncoder(w)
	return s.export(ctx, o.ProfileID, o.Schema, o.Name, contract.DataOptions{Object: o}, nil, func(values []contract.DataValue) error {
		cells := make([]contract.Cell, len(values))
		for i, v := range values {
			cells[i] = contract.Cell{Kind: v.Kind, Text: v.Text, Bytes: v.Bytes}
		}
		return encoder.Encode(cells)
	})
}
