package rqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const maxDataColumns = 512

func (s *Session) DataReads() contract.DataReads {
	return contract.DataReads{Loader: s.loadData, AdvancedLoader: s.pageData, RowReader: s.readDataRow, TableMetadata: s.dataMetadata, TableInfo: s.dataTableInfo, Count: s.countData, Chunk: s.readDataChunk}
}

func (s *Session) DataFilterModes() []string {
	return []string{"equal", "not_equal", "contains", "not_contains", "like", "not_like", "less", "greater", "is_null", "is_not_null"}
}

func (s *Session) dataObject(ctx context.Context, id int64, database, name string, o contract.ObjectRef) (contract.ObjectRef, error) {
	if database != "main" || id != s.ProfileID() || !validName(name) {
		return o, connections.ErrInvalid
	}
	if o != (contract.ObjectRef{}) {
		if o.ProfileID != id || o.Catalog != "main" || o.Schema != "" || o.Name != name {
			return o, connections.ErrInvalid
		}
		return o, s.checkObject(o)
	}
	r, err := s.request(ctx, "/db/query", []any{namedStatement("SELECT type FROM main.sqlite_schema WHERE name=CAST(? AS TEXT) AND type IN ('table','view')", name)}, false, false)
	if err != nil {
		return o, err
	}
	rows, err := decodedRows(r, 0, 1)
	if err != nil || len(rows) != 1 {
		return o, connections.ErrNotFound
	}
	kind, err := cellText(rows[0][0])
	if err != nil || kind != "table" && kind != "view" {
		return o, errIncomplete
	}
	return contract.ObjectRef{ProfileID: id, Catalog: "main", Kind: kind, Name: name}, nil
}

func (s *Session) dataTable(ctx context.Context, id int64, database, name string, o contract.ObjectRef) (tableSnapshot, error) {
	o, err := s.dataObject(ctx, id, database, name, o)
	if err != nil {
		return tableSnapshot{}, err
	}
	t, err := s.table(ctx, o)
	if err == nil && (len(t.structure.Columns) == 0 || len(t.structure.Columns) > maxDataColumns) {
		err = contract.ErrUnsupported
	}
	return t, err
}

func (s *Session) dataMetadata(ctx context.Context, id int64, database, name string) (*contract.TableStructure, error) {
	t, err := s.dataTable(ctx, id, database, name, contract.ObjectRef{})
	return t.structure, err
}

func (s *Session) dataTableInfo(ctx context.Context, id int64, database, name string) (*contract.TableInfo, error) {
	t, err := s.dataMetadata(ctx, id, database, name)
	if err != nil {
		return nil, err
	}
	return &contract.TableInfo{Database: "main", Name: name, Kind: t.Kind, Engine: t.Engine}, nil
}

func dataLiteral(value string) string {
	return "CAST(X'" + hex.EncodeToString([]byte(value)) + "' AS TEXT)"
}

func schemaGuard(t tableSnapshot) string {
	return "(SELECT schema_version FROM pragma_schema_version)=" + strconv.FormatInt(t.version, 10)
}

func dataWhere(t tableSnapshot, o contract.DataOptions, masked bool) (string, error) {
	columns := map[string]bool{}
	for _, c := range t.structure.Columns {
		columns[c.Name] = true
	}
	expr, err := contract.EffectiveDataExpression(o)
	if err != nil {
		return "", err
	}
	return contract.CompileFilter(expr, func(f contract.DataFilterRule) (string, error) {
		if !columns[f.Column] || len(f.Value) > contract.DataEditMaxBytes || !utf8.ValidString(f.Value) {
			return "", connections.ErrInvalid
		}
		column := quote(f.Column)
		op := map[string]string{"": "=", "equal": "=", "not_equal": "<>", "contains": "LIKE", "not_contains": "NOT LIKE", "like": "LIKE", "not_like": "NOT LIKE", "less": "<", "greater": ">", "is_null": "IS NULL", "is_not_null": "IS NOT NULL"}[f.Mode]
		if op == "" {
			return "", contract.ErrUnsupported
		}
		if f.Mode == "is_null" || f.Mode == "is_not_null" {
			return column + " " + op, nil
		}
		value := f.Value
		if f.Mode == "contains" || f.Mode == "not_contains" {
			value = "%" + strings.NewReplacer("~", "~~", "%", "~%", "_", "~_").Replace(value) + "%"
		}
		literal := dataLiteral(value)
		if masked {
			literal = "?"
		}
		out := column + " " + op + " " + literal
		if f.Mode == "contains" || f.Mode == "not_contains" {
			out += " ESCAPE '~'"
		}
		return out, nil
	})
}

func dataSource(t tableSnapshot, o contract.DataOptions, masked bool) (string, error) {
	if o.After != nil {
		return "", connections.ErrInvalid
	}
	order, err := dataOrder(t, o)
	if err != nil {
		return "", err
	}
	return dataOrderedSource(t, o, masked, order, "")
}

func dataOrderedSource(t tableSnapshot, o contract.DataOptions, masked bool, terms []dataOrderTerm, seek string) (string, error) {
	if o.Offset < 0 || o.Limit < 1 || o.Limit > contract.DataMaxPageSize {
		return "", contract.ErrDataPage
	}
	out, err := dataOrderedSelect(t, o, masked, terms, seek)
	if err != nil {
		return "", err
	}
	out += fmt.Sprintf(" LIMIT %d", o.Limit+1)
	if o.After == nil {
		out += fmt.Sprintf(" OFFSET %d", o.Offset)
	}
	return out, nil
}

func dataOrderedSelect(t tableSnapshot, o contract.DataOptions, masked bool, terms []dataOrderTerm, seek string) (string, error) {
	where, err := dataWhere(t, o, masked)
	if err != nil {
		return "", err
	}
	fields := make([]string, len(t.structure.Columns))
	for i, c := range t.structure.Columns {
		fields[i] = fmt.Sprintf("%s AS c%d", quote(c.Name), i)
	}
	var order []string
	for _, term := range terms {
		direction := " ASC"
		if term.Descending {
			direction = " DESC"
		}
		order = append(order, term.expression()+direction)
	}
	if seek != "" {
		if where != "" {
			where = "(" + where + ") AND "
		}
		where += seek
	}
	out := "SELECT " + strings.Join(fields, ",") + " FROM main." + quote(t.structure.Name)
	if where != "" {
		out += " WHERE " + where
	}
	if len(order) > 0 {
		out += " ORDER BY " + strings.Join(order, ",")
	}
	return out, nil
}

func (s *Session) readDataProjection(ctx context.Context, t tableSnapshot, source string, limit, maxRows int, full bool) ([]dataRow, error) {
	// This leading observation and the projected rows share one native read
	// transaction. A schema change after metadata capture cannot attach old
	// columns or row identities to the new relation.
	r, err := s.request(ctx, "/db/query", []any{"SELECT " + schemaGuard(t), dataProjection(source, len(t.structure.Columns), limit, full)}, true, false)
	if err != nil {
		return nil, err
	}
	guard, err := decodedRows(r, 0, 1)
	if err != nil || len(guard) != 1 {
		return nil, contract.ErrDataConflict
	}
	flag, err := cellInt(guard[0][0])
	if err != nil || flag != 1 {
		return nil, contract.ErrDataConflict
	}
	r.Results = r.Results[1:]
	return decodeDataRows(r, len(t.structure.Columns), limit, maxRows, full)
}

func (s *Session) dataSnapshot(t tableSnapshot, row dataRow) *contract.RowSnapshot {
	if len(t.key) == 0 {
		return nil
	}
	out := &contract.RowSnapshot{Mode: "fingerprints", Metadata: s.dataMetadataSeal(t), Ref: contract.RowRef{Object: t.structure.Object, Database: "main", Table: t.structure.Name, Columns: append([]string(nil), t.key...)}}
	for i, c := range t.structure.Columns {
		out.Columns = append(out.Columns, c.Name)
		if row.fp[i] != nil {
			fp := *row.fp[i]
			out.Fingerprints = append(out.Fingerprints, &fp)
		} else {
			out.Fingerprints = append(out.Fingerprints, nil)
		}
	}
	for _, key := range t.key {
		for i, c := range t.structure.Columns {
			if c.Name != key {
				continue
			}
			v := row.cells[i].Clone()
			if v.Truncated || v.Kind == contract.Null {
				return nil
			}
			out.Ref.TypedValues = append(out.Ref.TypedValues, v)
			if v.Kind == contract.Binary {
				out.Ref.Values = append(out.Ref.Values, bytes.Clone(v.Bytes))
			} else {
				out.Ref.Values = append(out.Ref.Values, []byte(v.Text))
			}
		}
	}
	return out
}

func (s *Session) dataMetadataSeal(t tableSnapshot) string {
	raw, _ := json.Marshal(struct {
		Scope     string
		Version   int64
		Structure *contract.TableStructure
	}{s.dataScope, t.version, t.structure})
	seal := sha256.Sum256(raw)
	return hex.EncodeToString(seal[:])
}

func (s *Session) loadData(ctx context.Context, id int64, database, name string, offset int64, limit int) (*contract.DataPage, error) {
	return s.pageData(ctx, id, database, name, contract.DataOptions{Offset: offset, Limit: limit})
}

func (s *Session) pageData(ctx context.Context, id int64, database, name string, o contract.DataOptions) (*contract.DataPage, error) {
	return s.pageDataScope(ctx, id, database, name, o, s.dataScope)
}

func (s *Session) pageDataScope(ctx context.Context, id int64, database, name string, o contract.DataOptions, scope string) (*contract.DataPage, error) {
	started := time.Now()
	t, err := s.dataTable(ctx, id, database, name, o.Object)
	if err != nil {
		return nil, err
	}
	order, err := dataOrder(t, o)
	if err != nil {
		return nil, err
	}
	seek, err := s.cursorWhere(scope, t, o, order, false)
	if err != nil {
		return nil, err
	}
	if o.After != nil {
		if o.Offset != 0 && o.Offset != o.After.Offset {
			return nil, connections.ErrInvalid
		}
		o.Offset = o.After.Offset
	}
	source, err := dataOrderedSource(t, o, false, order, seek)
	if err != nil {
		return nil, err
	}
	var rows []dataRow
	var cursor *contract.DataCursor
	keyset := o.UseKeyset && len(t.key) > 0
	if keyset {
		rows, cursor, err = s.readCursorPage(ctx, scope, t, o, source, order)
	} else {
		rows, err = s.readDataProjection(ctx, t, source, 1024, o.Limit+1, false)
	}
	if err != nil {
		return nil, err
	}
	editable := t.structure.Editable
	out := &contract.DataPage{Object: t.structure.Object, Editable: &editable, Database: database, Table: name, ColumnMetadata: t.structure.Columns, Offset: o.Offset, Limit: o.Limit, LiveRead: true, StableOrder: len(t.key) > 0, HasMore: len(rows) > o.Limit}
	out.KeysetAvailable, out.NextCursor = keyset, cursor
	out.CurrentCursor = o.After
	out.PagingMode = "offset"
	if keyset {
		out.PagingMode = "keyset"
	}
	// Values in generated literals stay out of presentation SQL.
	maskedSeek, err := s.cursorWhere(scope, t, o, order, true)
	if err != nil {
		return nil, err
	}
	out.QuerySQL, err = dataOrderedSource(t, o, true, order, maskedSeek)
	if err != nil {
		return nil, err
	}
	for _, c := range t.structure.Columns {
		out.Columns = append(out.Columns, c.Name)
		out.ColumnKinds = append(out.ColumnKinds, c.DataType)
	}
	for _, row := range rows[:min(len(rows), o.Limit)] {
		previews := make([]contract.DataCell, len(row.cells))
		for i, cell := range row.cells {
			v := cell.Clone()
			previews[i] = contract.DataCell{Value: &v, Text: v.Display(), Null: v.Kind == contract.Null, Truncated: v.Truncated}
		}
		out.Rows = append(out.Rows, previews)
		snapshot := s.dataSnapshot(t, row)
		out.RowSnapshots = append(out.RowSnapshots, snapshot)
		if snapshot == nil {
			out.RowRefs = append(out.RowRefs, nil)
		} else {
			ref := snapshot.Ref
			out.RowRefs = append(out.RowRefs, &ref)
		}
	}
	out.Duration, out.CompletedAt = time.Since(started), time.Now()
	return out, nil
}

func (s *Session) countData(ctx context.Context, id int64, database, name string) (int64, error) {
	return s.countDataObject(ctx, id, database, name, contract.ObjectRef{})
}

func (s *Session) countDataObject(ctx context.Context, id int64, database, name string, o contract.ObjectRef) (int64, error) {
	t, err := s.dataTable(ctx, id, database, name, o)
	if err != nil {
		return 0, err
	}
	r, err := s.request(ctx, "/db/query", []any{"SELECT " + schemaGuard(t) + ",count(*) FROM main." + quote(name)}, false, false)
	if err != nil {
		return 0, err
	}
	rows, err := decodedRows(r, 0, 2)
	if err != nil || len(rows) != 1 {
		return 0, contract.ErrDataConflict
	}
	flag, err := cellInt(rows[0][0])
	if err != nil || flag != 1 {
		return 0, contract.ErrDataConflict
	}
	n, err := cellInt(rows[0][1])
	if err != nil || n < 0 {
		return 0, errIncomplete
	}
	return n, nil
}

var _ contract.DataReadsProvider = (*Session)(nil)
