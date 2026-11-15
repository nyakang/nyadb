package questdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) DataRuntime() contract.DataRuntime {
	return contract.DataRuntime{PageCancellationAcknowledged: true, Loader: s.load, AdvancedLoader: s.page, TableMetadata: s.tableMetadata, TableInfo: s.tableInfo, Count: s.count, Exporter: s.export, JSONL: s.jsonl, FilterModes: []string{"equal", "not_equal", "less", "greater", "like", "is_null", "is_not_null"}, Capabilities: s.Capabilities(), ReadScopeHint: "QuestDB 分页读取当前可见数据；跨页不提供一致快照。"}
}
func (s *Session) tableMetadata(ctx context.Context, id int64, database, table string) (*contract.TableStructure, error) {
	return s.TableMetadata(ctx, contract.ObjectRef{ProfileID: id, Catalog: database, Kind: "TABLE", Name: table})
}
func (s *Session) resolve(ctx context.Context, id int64, database, table string, o contract.ObjectRef) (*contract.TableStructure, error) {
	if o.ProfileID == 0 {
		return s.tableMetadata(ctx, id, database, table)
	}
	if o.ProfileID != id || o.Catalog != database || o.Name != table {
		return nil, connections.ErrInvalid
	}
	return s.TableMetadata(ctx, o)
}
func (s *Session) load(ctx context.Context, id int64, database, table string, offset int64, limit int) (*contract.DataPage, error) {
	return s.page(ctx, id, database, table, contract.DataOptions{Offset: offset, Limit: limit})
}

func filterArgument(col contract.StructureColumn, value string) (any, error) {
	switch valueKind(col.DataType) {
	case contract.Integer:
		v, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, connections.ErrInvalid
		}
		return v, nil
	case contract.Floating:
		v, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, connections.ErrInvalid
		}
		return v, nil
	case contract.Boolean:
		if value != "true" && value != "false" {
			return nil, connections.ErrInvalid
		}
		return value == "true", nil
	case contract.Binary:
		return nil, contract.ErrUnsupported
	}
	if col.DataType == "LONG256" || strings.HasPrefix(col.DataType, "GEOHASH") {
		return nil, contract.ErrUnsupported
	}
	return value, nil
}
func dataQuery(t *contract.TableStructure, options contract.DataOptions, columns []string, paged bool) (string, []any, error) {
	if options.UseKeyset || options.After != nil || options.Condition != nil {
		return "", nil, contract.ErrUnsupported
	}
	known := map[string]contract.StructureColumn{}
	for _, col := range t.Columns {
		known[col.Name] = col
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
		if _, ok := known[col]; !ok {
			return "", nil, connections.ErrInvalid
		}
		fields[i] = quote(col)
	}
	query := "SELECT " + strings.Join(fields, ",") + " FROM " + quote(t.Name)
	filters := append([]contract.DataFilterRule(nil), options.Filters...)
	if options.FilterColumn != "" {
		filters = append(filters, contract.DataFilterRule{Column: options.FilterColumn, Mode: options.FilterMode, Value: options.FilterValue})
	}
	if len(filters) > 64 {
		return "", nil, connections.ErrInvalid
	}
	var args []any
	var conditions []string
	for _, filter := range filters {
		col, ok := known[filter.Column]
		if !ok {
			return "", nil, connections.ErrInvalid
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
		condition := quote(col.Name) + " " + op
		if op != "IS NULL" && op != "IS NOT NULL" {
			if op == "LIKE" && valueKind(col.DataType) != contract.Text {
				return "", nil, contract.ErrUnsupported
			}
			value, err := filterArgument(col, filter.Value)
			if err != nil {
				return "", nil, err
			}
			args = append(args, value)
			parameter := fmt.Sprintf("$%d", len(args))
			if valueKind(col.DataType) == contract.DateTime || col.DataType == "UUID" || col.DataType == "IPv4" {
				parameter = "CAST(" + parameter + " AS " + col.DataType + ")"
			}
			condition += " " + parameter
		}
		conditions = append(conditions, condition)
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
		if _, ok := known[sort.Column]; !ok {
			return "", nil, connections.ErrInvalid
		}
		dir := " ASC"
		if sort.Descending {
			dir = " DESC"
		}
		order = append(order, quote(sort.Column)+dir)
	}
	if len(order) > 0 {
		query += " ORDER BY " + strings.Join(order, ",")
	}
	if paged {
		if options.Offset < 0 || options.Limit < 1 || options.Limit > 1000 || options.Offset > math.MaxInt64-int64(options.Limit)-1 {
			return "", nil, connections.ErrInvalid
		}
		query += fmt.Sprintf(" LIMIT %d,%d", options.Offset, options.Offset+int64(options.Limit)+1)
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
	var set contract.ResultSet
	err = s.withConnection(ctx, func(child context.Context, c *pgx.Conn) error {
		budget := previewBytes
		var e error
		set, e = execute(child, c, query, args, 0, nil, &budget)
		return e
	})
	if err != nil {
		return nil, err
	}
	for _, reason := range set.Truncation {
		if reason == "bytes" || reason == "rows" {
			return nil, errors.New("QuestDB page exceeds preview capacity; select a smaller page")
		}
	}
	editable := false
	page := &contract.DataPage{Object: t.Object, Editable: &editable, Database: database, Table: table, Offset: options.Offset, Limit: options.Limit, QuerySQL: query, ColumnMetadata: t.Columns, LiveRead: true, CompletedAt: time.Now(), Duration: time.Since(start)}
	if len(set.Columns) != len(t.Columns) {
		return nil, contract.ErrUnsupported
	}
	for _, col := range t.Columns {
		page.Columns = append(page.Columns, col.Name)
		page.ColumnKinds = append(page.ColumnKinds, col.DataType)
	}
	page.HasMore = len(set.Rows) > options.Limit
	if page.HasMore {
		set.Rows = set.Rows[:options.Limit]
	}
	for _, row := range set.Rows {
		cells := make([]contract.DataCell, len(row))
		for i, cell := range row {
			v := cell.Clone()
			cells[i] = contract.DataCell{Value: &v, Text: v.Display(), Null: v.Kind == contract.Null, Truncated: v.Truncated}
		}
		page.Rows = append(page.Rows, cells)
	}
	return page, nil
}
func (s *Session) count(ctx context.Context, id int64, database, table string) (int64, error) {
	if err := s.checkObject(contract.ObjectRef{ProfileID: id, Catalog: database, Kind: "TABLE", Name: table}); err != nil {
		return 0, err
	}
	var count int64
	err := s.withConnection(ctx, func(child context.Context, c *pgx.Conn) error {
		set, err := execute(child, c, "SELECT count(*) FROM "+quote(table), nil, 0, nil, nil)
		if err != nil {
			return err
		}
		if len(set.Rows) != 1 || len(set.Rows[0]) != 1 {
			return contract.ErrUnsupported
		}
		count, err = strconv.ParseInt(set.Rows[0][0].Text, 10, 64)
		return err
	})
	return count, err
}
func (s *Session) tableInfo(ctx context.Context, id int64, database, table string) (*contract.TableInfo, error) {
	t, err := s.tableMetadata(ctx, id, database, table)
	if err != nil {
		return nil, err
	}
	return &contract.TableInfo{Database: database, Name: table, Kind: t.Kind, Engine: "QuestDB"}, nil
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
	return s.withConnection(ctx, func(child context.Context, c *pgx.Conn) error {
		budget := 0
		_, err := execute(child, c, query, args, 0, func(_ int, _ []contract.Column, row []contract.Cell) error {
			values := make([]contract.DataValue, len(row))
			for i, v := range row {
				values[i] = contract.DataValue{Kind: v.Kind, Text: v.Text, Bytes: v.Bytes, Null: v.Kind == contract.Null, Binary: v.Kind == contract.Binary}
			}
			return sink(values)
		}, &budget)
		return err
	})
}
func (s *Session) jsonl(ctx context.Context, o contract.ObjectRef, writer io.Writer) error {
	if writer == nil {
		return connections.ErrInvalid
	}
	encoder := json.NewEncoder(writer)
	return s.export(ctx, o.ProfileID, o.Catalog, o.Name, contract.DataOptions{Object: o}, nil, func(values []contract.DataValue) error {
		row := make([]contract.Cell, len(values))
		for i, v := range values {
			row[i] = contract.Cell{Kind: v.Kind, Text: v.Text, Bytes: v.Bytes}
		}
		return encoder.Encode(row)
	})
}

var _ contract.MetadataProvider = (*Session)(nil)
var _ contract.QueryProvider = (*Session)(nil)
var _ contract.DataProvider = (*Session)(nil)
