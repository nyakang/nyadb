package tdengine

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) DataRuntime() contract.DataRuntime {
	return contract.DataRuntime{PageCancellationAcknowledged: true, Loader: s.load, AdvancedLoader: s.page, TableMetadata: s.tableMetadata, TableInfo: s.tableInfo, Count: s.count, Exporter: s.export, JSONL: s.jsonl, Capabilities: s.Capabilities(), FilterModes: []string{"equal", "not_equal", "less", "greater", "like", "is_null", "is_not_null"}, ReadScopeHint: "TDengine 分页读取当前可见数据；跨页不提供一致快照。"}
}

func filterLiteral(column contract.StructureColumn, value string) (string, error) {
	switch valueKind(column.DataType) {
	case contract.Integer:
		if strings.Contains(column.DataType, "UNSIGNED") {
			v, e := strconv.ParseUint(value, 10, 64)
			if e != nil {
				return "", connections.ErrInvalid
			}
			return strconv.FormatUint(v, 10), nil
		}
		v, e := strconv.ParseInt(value, 10, 64)
		if e != nil {
			return "", connections.ErrInvalid
		}
		return strconv.FormatInt(v, 10), nil
	case contract.Floating:
		v, e := strconv.ParseFloat(value, 64)
		if e != nil || math.IsInf(v, 0) || math.IsNaN(v) {
			return "", connections.ErrInvalid
		}
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	case contract.Boolean:
		if value != "true" && value != "false" {
			return "", connections.ErrInvalid
		}
		return value, nil
	case contract.DateTime:
		if _, e := time.Parse(time.RFC3339Nano, value); e != nil {
			return "", connections.ErrInvalid
		}
		return literal(value), nil
	case contract.Binary, contract.JSON, contract.Decimal:
		return "", contract.ErrUnsupported
	default:
		return literal(value), nil
	}
}

func dataQuery(t *contract.TableStructure, o contract.DataOptions, columns []string, paged bool) (string, error) {
	if o.UseKeyset || o.After != nil || o.Condition != nil {
		return "", contract.ErrUnsupported
	}
	known := map[string]contract.StructureColumn{}
	for _, c := range t.Columns {
		known[c.Name] = c
	}
	if len(columns) == 0 {
		for _, c := range t.Columns {
			columns = append(columns, c.Name)
		}
	}
	if len(columns) == 0 {
		return "", contract.ErrUnsupported
	}
	parts := []string{}
	for _, name := range columns {
		if _, ok := known[name]; !ok {
			return "", connections.ErrInvalid
		}
		parts = append(parts, quote(name))
	}
	q := "SELECT " + strings.Join(parts, ",") + " FROM " + qualified(t.Database, t.Name)
	filters := o.Filters
	if len(filters) == 0 && o.FilterColumn != "" {
		filters = []contract.DataFilterRule{{Column: o.FilterColumn, Mode: o.FilterMode, Value: o.FilterValue}}
	}
	conditions := []string{}
	for _, f := range filters {
		col, ok := known[f.Column]
		if !ok {
			return "", connections.ErrInvalid
		}
		mode := f.Mode
		if mode == "" {
			mode = "equal"
		}
		name := quote(f.Column)
		if mode == "is_null" || mode == "is_not_null" {
			operator := " IS NULL"
			if mode == "is_not_null" {
				operator = " IS NOT NULL"
			}
			conditions = append(conditions, name+operator)
			continue
		}
		operator := map[string]string{"equal": "=", "not_equal": "!=", "less": "<", "greater": ">", "like": " LIKE "}[mode]
		if operator == "" {
			return "", contract.ErrUnsupported
		}
		if mode == "like" && valueKind(col.DataType) != contract.Text {
			return "", contract.ErrUnsupported
		}
		v, e := filterLiteral(col, f.Value)
		if e != nil {
			return "", e
		}
		conditions = append(conditions, name+operator+v)
	}
	if len(conditions) > 0 {
		q += " WHERE " + strings.Join(conditions, " AND ")
	}
	sorts := o.Sorts
	if len(sorts) == 0 && o.SortColumn != "" {
		sorts = []contract.DataSortRule{{Column: o.SortColumn, Descending: o.SortDescending}}
	}
	parts = nil
	for _, sort := range sorts {
		if _, ok := known[sort.Column]; !ok {
			return "", connections.ErrInvalid
		}
		direction := " ASC"
		if sort.Descending {
			direction = " DESC"
		}
		parts = append(parts, quote(sort.Column)+direction)
	}
	if len(parts) > 0 {
		q += " ORDER BY " + strings.Join(parts, ",")
	}
	if paged {
		if o.Offset < 0 || o.Limit < 1 || o.Limit > 1000 || o.Offset > math.MaxInt64-int64(o.Limit)-1 {
			return "", connections.ErrInvalid
		}
		q += " LIMIT " + strconv.Itoa(o.Limit+1) + " OFFSET " + strconv.FormatInt(o.Offset, 10)
	}
	return q, nil
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
	q, err := dataQuery(t, o, nil, true)
	if err != nil {
		return nil, err
	}
	var set contract.ResultSet
	err = s.withConnection(ctx, func(child context.Context, w *wire) error {
		var e error
		budget := 32 << 20
		set, _, e = w.query(child, q, nil, &budget)
		for _, reason := range set.Truncation {
			if reason != "cell" && e == nil {
				e = contract.ErrUnsupported
			}
		}
		return e
	})
	if err != nil {
		return nil, err
	}
	editable := false
	page := &contract.DataPage{Database: db, Table: table, Offset: o.Offset, Limit: o.Limit, ColumnMetadata: t.Columns, Editable: &editable, HasMore: len(set.Rows) > o.Limit, Object: t.Object, LiveRead: true, PagingMode: "offset", QuerySQL: q, CompletedAt: time.Now(), Duration: time.Since(start)}
	for _, c := range set.Columns {
		page.Columns = append(page.Columns, c.Name)
		page.ColumnKinds = append(page.ColumnKinds, c.DatabaseType)
	}
	if page.HasMore {
		set.Rows = set.Rows[:o.Limit]
	}
	for _, row := range set.Rows {
		cells := make([]contract.DataCell, len(row))
		for i, v := range row {
			copy := v.Clone()
			cells[i] = contract.DataCell{Value: &copy, Text: v.Display(), Null: v.Kind == contract.Null, Truncated: v.Truncated}
		}
		page.Rows = append(page.Rows, cells)
	}
	return page, nil
}
func (s *Session) count(ctx context.Context, id int64, db, table string) (int64, error) {
	if _, err := s.resolve(ctx, id, db, table, contract.ObjectRef{}); err != nil {
		return 0, err
	}
	var count int64
	err := s.withConnection(ctx, func(child context.Context, w *wire) error {
		r, e := metadataRead(child, w, "SELECT count(*) FROM "+qualified(db, table))
		if e != nil {
			return e
		}
		if len(r.Rows) != 1 || len(r.Rows[0]) != 1 {
			return contract.ErrUnsupported
		}
		count, e = strconv.ParseInt(r.Rows[0][0].Text, 10, 64)
		return e
	})
	return count, err
}
func (s *Session) tableInfo(ctx context.Context, id int64, db, table string) (*contract.TableInfo, error) {
	t, err := s.tableMetadata(ctx, id, db, table)
	if err != nil {
		return nil, err
	}
	return &contract.TableInfo{Database: db, Name: table, Kind: t.Kind, Engine: "TDengine"}, nil
}
func (s *Session) export(ctx context.Context, id int64, db, table string, o contract.DataOptions, columns []string, sink func([]contract.DataValue) error) error {
	if sink == nil {
		return connections.ErrInvalid
	}
	t, err := s.resolve(ctx, id, db, table, o.Object)
	if err != nil {
		return err
	}
	q, err := dataQuery(t, o, columns, false)
	if err != nil {
		return err
	}
	return s.withConnection(ctx, func(child context.Context, w *wire) error {
		budget := 0
		_, _, e := w.query(child, q, func(_ []contract.Column, row []contract.Cell) error {
			values := make([]contract.DataValue, len(row))
			for i, v := range row {
				values[i] = contract.DataValue{Kind: v.Kind, Text: v.Text, Bytes: v.Bytes, Null: v.Kind == contract.Null, Binary: v.Kind == contract.Binary}
			}
			return sink(values)
		}, &budget)
		return e
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
