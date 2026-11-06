package sqlserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) DataReads() contract.DataReads {
	return contract.DataReads{Loader: s.load, AdvancedLoader: s.page, RowReader: s.readRow, TableMetadata: s.tableMetadata, TableInfo: s.tableInfo, Count: s.count, Chunk: s.chunk}
}
func (s *Session) DataEdits() contract.DataEdits {
	return contract.DataEdits{Saver: s.save, Verify: s.verify}
}
func (s *Session) DataFilterModes() []string {
	return []string{"equal", "not_equal", "contains", "not_contains", "like", "not_like", "less", "greater", "is_null", "is_not_null"}
}
func (s *Session) tableMetadata(ctx context.Context, id int64, schema, name string) (*contract.TableStructure, error) {
	child, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.resolve(child, s.db, id, schema, name, contract.ObjectRef{})
}
func (s *Session) resolve(ctx context.Context, c queryer, id int64, schema, name string, o contract.ObjectRef) (*contract.TableStructure, error) {
	ref := s.Namespace(schema).Object("table", name)
	if id != s.ProfileID() {
		return nil, connections.ErrInvalid
	}
	if o.ProfileID != 0 {
		if o.Namespace() != ref.Namespace() || o.Name != name {
			return nil, connections.ErrInvalid
		}
		ref = o
	} else {
		if err := s.check(ref); err != nil {
			return nil, err
		}
		rows, err := c.QueryContext(ctx, `SELECT o.type FROM sys.objects o JOIN sys.schemas s ON s.schema_id=o.schema_id WHERE s.name=@p1 AND o.name=@p2 AND o.type IN ('U','V')`, schema, name)
		if err != nil {
			return nil, safeError(err)
		}
		var typ string
		if !rows.Next() {
			rows.Close()
			return nil, connections.ErrNotFound
		}
		err = rows.Scan(&typ)
		rows.Close()
		if err != nil {
			return nil, safeError(err)
		}
		if strings.TrimSpace(typ) == "V" {
			ref.Kind = "view"
		}
	}
	return s.structure(ctx, c, ref)
}
func (s *Session) metadataSeal(t *contract.TableStructure) string {
	raw, _ := json.Marshal(t)
	h := sha256.Sum256(append([]byte(s.scope), raw...))
	return hex.EncodeToString(h[:])
}
func fingerprint(name string) string {
	return "CONVERT(varchar(64),HASHBYTES('SHA2_256',CONVERT(varbinary(max)," + quote(name) + ")),2)"
}
func projection(col contract.StructureColumn, bounded bool) string {
	q := quote(col.Name)
	if col.DataType == "uniqueidentifier" {
		return "CONVERT(nvarchar(36)," + q + ")"
	}
	if !bounded {
		return q
	}
	if col.ValueKind == contract.Binary {
		return "SUBSTRING(" + q + ",1,1024)"
	}
	if col.ValueKind == contract.Text {
		return "SUBSTRING(CONVERT(nvarchar(max)," + q + "),1,1024)"
	}
	return q
}
func dataQuery(t *contract.TableStructure, o contract.DataOptions, columns []string, paged bool) (string, []any, error) {
	if o.After != nil || o.UseKeyset {
		return "", nil, contract.ErrUnsupported
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
	if len(columns) == 0 || len(columns) > 512 {
		return "", nil, contract.ErrUnsupported
	}
	var fields []string
	for _, name := range columns {
		c, ok := known[name]
		if !ok {
			return "", nil, connections.ErrInvalid
		}
		fields = append(fields, projection(c, paged)+" AS "+quote(name))
	}
	if paged {
		for _, c := range t.Columns {
			fields = append(fields, fingerprint(c.Name))
		}
		for _, c := range t.Columns {
			fields = append(fields, truncationFlag(c))
		}
	}
	query := "SELECT " + strings.Join(fields, ",") + " FROM " + qualified(t.Object)
	expr, err := contract.EffectiveDataExpression(o)
	if err != nil {
		return "", nil, err
	}
	var args []any
	where, err := contract.CompileFilter(expr, func(f contract.DataFilterRule) (string, error) {
		c, ok := known[f.Column]
		if !ok || !comparable(c) {
			return "", contract.ErrUnsupported
		}
		op := map[string]string{"": "=", "equal": "=", "not_equal": "<>", "less": "<", "greater": ">", "like": "LIKE", "not_like": "NOT LIKE", "contains": "LIKE", "not_contains": "NOT LIKE", "is_null": "IS NULL", "is_not_null": "IS NOT NULL"}[f.Mode]
		if op == "" {
			return "", contract.ErrUnsupported
		}
		q := quote(f.Column) + " " + op
		if f.Mode == "is_null" || f.Mode == "is_not_null" {
			return q, nil
		}
		if strings.Contains(op, "LIKE") && c.ValueKind != contract.Text {
			return "", contract.ErrUnsupported
		}
		v := f.Value
		if f.Mode == "contains" || f.Mode == "not_contains" {
			v = "%" + strings.NewReplacer("~", "~~", "%", "~%", "_", "~_", "[", "~[").Replace(v) + "%"
		}
		args = append(args, v)
		q += fmt.Sprintf(" @p%d", len(args))
		if f.Mode == "contains" || f.Mode == "not_contains" {
			q += " ESCAPE N'~'"
		}
		return q, nil
	})
	if err != nil {
		return "", nil, err
	}
	if where != "" {
		query += " WHERE " + where
	}
	sorts := append([]contract.DataSortRule(nil), o.Sorts...)
	if len(sorts) == 0 && o.SortColumn != "" {
		sorts = append(sorts, contract.DataSortRule{Column: o.SortColumn, Descending: o.SortDescending})
	}
	if len(sorts) > contract.DataMaxSortRules {
		return "", nil, connections.ErrInvalid
	}
	var order []string
	used := map[string]bool{}
	for _, sort := range sorts {
		c, ok := known[sort.Column]
		if !ok || !comparable(c) || used[sort.Column] {
			return "", nil, contract.ErrUnsupported
		}
		used[sort.Column] = true
		direction := " ASC"
		if sort.Descending {
			direction = " DESC"
		}
		order = append(order, quote(sort.Column)+direction)
	}
	for _, key := range keyColumns(t) {
		if !used[key] {
			order = append(order, quote(key)+" ASC")
		}
	}
	if len(order) == 0 && paged {
		order = []string{"(SELECT NULL)"}
	}
	if len(order) > 0 {
		query += " ORDER BY " + strings.Join(order, ",")
	}
	if paged {
		if o.Offset < 0 || o.Limit < 1 || o.Limit > contract.DataMaxPageSize {
			return "", nil, contract.ErrDataPage
		}
		query += fmt.Sprintf(" OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", o.Offset, o.Limit+1)
	}
	return query, args, nil
}
func (s *Session) load(ctx context.Context, id int64, schema, name string, offset int64, limit int) (*contract.DataPage, error) {
	return s.page(ctx, id, schema, name, contract.DataOptions{Offset: offset, Limit: limit})
}
func (s *Session) page(ctx context.Context, id int64, schema, name string, o contract.DataOptions) (*contract.DataPage, error) {
	child, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.pageWith(child, s.db, id, schema, name, o)
}
func (s *Session) pageWith(ctx context.Context, c queryer, id int64, schema, name string, o contract.DataOptions) (*contract.DataPage, error) {
	start := time.Now()
	t, err := s.resolve(ctx, c, id, schema, name, o.Object)
	if err != nil {
		return nil, err
	}
	query, args, err := dataQuery(t, o, nil, true)
	if err != nil {
		return nil, err
	}
	rows, err := c.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, safeError(err)
	}
	defer rows.Close()
	editable := t.Editable
	page := &contract.DataPage{Object: t.Object, Editable: &editable, Database: schema, Table: name, ColumnMetadata: t.Columns, Offset: o.Offset, Limit: o.Limit, StableOrder: len(keyColumns(t)) > 0, QuerySQL: query}
	for _, col := range t.Columns {
		page.Columns = append(page.Columns, col.Name)
		page.ColumnKinds = append(page.ColumnKinds, col.DataType)
	}
	budget := 16 << 20
	for rows.Next() {
		n := len(t.Columns)
		raw := make([]any, n*3)
		dest := make([]any, len(raw))
		for i := range raw {
			dest[i] = &raw[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return nil, safeError(err)
		}
		if len(page.Rows) >= o.Limit {
			page.HasMore = true
			continue
		}
		cells := make([]contract.Cell, n)
		previews := make([]contract.DataCell, n)
		fp := make([]*string, n)
		for i, col := range t.Columns {
			cells[i] = cell(raw[i], col.DataType)
			if flag, ok := raw[2*n+i].(int64); ok && flag != 0 {
				cells[i].Truncated = true
			}
			value := cells[i].Clone()
			previews[i] = contract.DataCell{Value: &value, Text: value.Display(), Null: value.Kind == contract.Null, Truncated: value.Truncated}
			if raw[n+i] != nil {
				v := fmt.Sprint(raw[n+i])
				fp[i] = &v
			}
			budget -= len(value.Text) + len(value.Bytes) + 128
		}
		if budget < 0 {
			return nil, contract.ErrDataPage
		}
		page.Rows = append(page.Rows, previews)
		snap := s.snapshot(t, cells, fp, "fingerprints")
		if snap != nil {
			page.RowRefs = append(page.RowRefs, &snap.Ref)
		} else {
			page.RowRefs = append(page.RowRefs, nil)
		}
		if !t.Editable {
			snap = nil
		}
		page.RowSnapshots = append(page.RowSnapshots, snap)
	}
	page.CompletedAt = time.Now()
	page.Duration = time.Since(start)
	return page, safeError(rows.Err())
}
func (s *Session) snapshot(t *contract.TableStructure, cells []contract.Cell, fp []*string, mode string) *contract.RowSnapshot {
	keys := keyColumns(t)
	if len(keys) == 0 {
		return nil
	}
	snap := &contract.RowSnapshot{Mode: mode, Metadata: s.metadataSeal(t), Fingerprints: fp, Ref: contract.RowRef{Object: t.Object, Database: t.Object.Schema, Table: t.Name, Columns: keys}}
	for i, col := range t.Columns {
		snap.Columns = append(snap.Columns, col.Name)
		for _, key := range keys {
			if col.Name == key {
				if cells[i].Truncated || cells[i].Kind == contract.Null {
					return nil
				}
			}
		}
	}
	for _, key := range keys {
		for i, col := range t.Columns {
			if col.Name == key {
				v := cells[i].Clone()
				snap.Ref.TypedValues = append(snap.Ref.TypedValues, v)
				raw := []byte(v.Text)
				if v.Kind == contract.Binary {
					raw = bytes.Clone(v.Bytes)
				}
				snap.Ref.Values = append(snap.Ref.Values, raw)
			}
		}
	}
	if mode == "values" {
		for _, v := range cells {
			snap.Original = append(snap.Original, v.Clone())
		}
	}
	return snap
}
func (s *Session) tableInfo(ctx context.Context, id int64, schema, name string) (*contract.TableInfo, error) {
	t, err := s.tableMetadata(ctx, id, schema, name)
	if err != nil {
		return nil, err
	}
	return &contract.TableInfo{Database: schema, Name: name, Kind: t.Kind, Engine: t.Engine, Comment: t.Comment}, nil
}
func (s *Session) count(ctx context.Context, id int64, schema, name string) (int64, error) {
	child, done, err := s.request(ctx)
	if err != nil {
		return 0, err
	}
	defer done()
	t, err := s.resolve(child, s.db, id, schema, name, contract.ObjectRef{})
	if err != nil {
		return 0, err
	}
	rows, err := s.db.QueryContext(child, "SELECT COUNT_BIG(*) FROM "+qualified(t.Object))
	if err != nil {
		return 0, safeError(err)
	}
	defer rows.Close()
	var count int64
	if !rows.Next() {
		return 0, safeError(rows.Err())
	}
	err = rows.Scan(&count)
	return count, safeError(err)
}

func truncationFlag(c contract.StructureColumn) string {
	if c.ValueKind == contract.Binary {
		return "CASE WHEN DATALENGTH(" + quote(c.Name) + ")>1024 THEN 1 ELSE 0 END"
	}
	if c.ValueKind == contract.Text {
		return "CASE WHEN DATALENGTH(CONVERT(nvarchar(max)," + quote(c.Name) + "))>2048 THEN 1 ELSE 0 END"
	}
	return "0"
}
