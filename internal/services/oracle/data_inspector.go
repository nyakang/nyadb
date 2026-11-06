package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) readRow(ctx context.Context, id int64, schema, name string, ref contract.RowRef) (*contract.DataRow, error) {
	child, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	tx, err := s.db.BeginTx(child, &sql.TxOptions{Isolation: sql.LevelDefault})
	if err != nil {
		return nil, safeError(err)
	}
	defer tx.Rollback()
	t, err := s.resolve(child, tx, id, schema, name, ref.Object)
	if err != nil {
		return nil, err
	}
	return s.rowWith(child, tx, t, ref, true)
}
func (s *Session) rowWith(ctx context.Context, c queryer, t *contract.TableStructure, ref contract.RowRef, full bool) (*contract.DataRow, error) {
	row, _, err := s.rowWithPreviews(ctx, c, t, ref, full)
	return row, err
}
func (s *Session) rowWithPreviews(ctx context.Context, c queryer, t *contract.TableStructure, ref contract.RowRef, full bool) (*contract.DataRow, []contract.DataCell, error) {
	var args []any
	where, err := keyWhere(t, ref, &args)
	if err != nil {
		return nil, nil, err
	}
	if full {
		var lengths []string
		for _, col := range t.Columns {
			lengths = append(lengths, "("+valueLength(col)+")*4")
		}
		rows, err := c.QueryContext(ctx, "SELECT "+strings.Join(lengths, "+")+" FROM "+qualified(t.Object)+" WHERE "+where, args...)
		if err != nil {
			return nil, nil, safeError(err)
		}
		var length int64
		if !rows.Next() {
			rows.Close()
			return nil, nil, contract.ErrDataConflict
		}
		err = rows.Scan(&length)
		rows.Close()
		if err != nil {
			return nil, nil, safeError(err)
		}
		if length > contract.DataEditMaxBytes {
			return nil, nil, contract.ErrDataEditLimit
		}
	}
	var fields []string
	for _, col := range t.Columns {
		fields = append(fields, projection(col, !full))
	}
	for _, col := range t.Columns {
		if lob(col) && !s.crypto {
			fields = append(fields, "NULL")
		} else {
			fields = append(fields, fingerprint(col))
		}
	}
	for _, col := range t.Columns {
		if full {
			fields = append(fields, "0")
		} else {
			fields = append(fields, truncationFlag(col))
		}
	}
	rows, err := c.QueryContext(ctx, "SELECT "+strings.Join(fields, ",")+" FROM "+qualified(t.Object)+" WHERE "+where, args...)
	if err != nil {
		return nil, nil, safeError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil, contract.ErrDataConflict
	}
	n := len(t.Columns)
	raw := make([]any, n*3)
	dest := make([]any, len(raw))
	for i := range raw {
		dest[i] = &raw[i]
	}
	if err = rows.Scan(dest...); err != nil {
		return nil, nil, safeError(err)
	}
	result := &contract.DataRow{Columns: t.Columns, Values: map[string]contract.DataValue{}}
	cells := make([]contract.Cell, n)
	previews := make([]contract.DataCell, n)
	fp := make([]*string, n)
	budget := 0
	for i, col := range t.Columns {
		cells[i] = cell(raw[i], col.DataType)
		if flag := fmt.Sprint(raw[2*n+i]); flag == "1" {
			cells[i].Truncated = true
		}
		preview := previewCell(cells[i], 1024)
		copy := preview.Clone()
		previews[i] = contract.DataCell{Value: &copy, Text: preview.Display(), Null: preview.Kind == contract.Null, Truncated: preview.Truncated}
		v := cells[i]
		budget += len(v.Text) + len(v.Bytes)
		result.Values[col.Name] = contract.DataValue{Kind: v.Kind, Text: v.Text, Bytes: v.Bytes, Null: v.Kind == contract.Null, Binary: v.Kind == contract.Binary}
		if raw[n+i] != nil {
			value := fmt.Sprint(raw[n+i])
			fp[i] = &value
		}
	}
	if full && budget > contract.DataEditMaxBytes {
		return nil, nil, contract.ErrDataEditLimit
	}
	if rows.Next() {
		return nil, nil, contract.ErrDataConflict
	}
	if err = rows.Err(); err != nil {
		return nil, nil, safeError(err)
	}
	mode := "fingerprints"
	if full {
		mode = "values"
	}
	result.Snapshot = s.snapshot(t, cells, fp, mode)
	return result, previews, nil
}
func (s *Session) chunk(ctx context.Context, id int64, schema, name, field string, ref contract.RowRef, offset int64, limit int) (*contract.FieldChunk, error) {
	child, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.chunkWith(child, s.db, id, schema, name, field, ref, offset, limit)
}
func (s *Session) chunkWith(ctx context.Context, c queryer, id int64, schema, name, field string, ref contract.RowRef, offset int64, limit int) (*contract.FieldChunk, error) {
	if offset < 0 || offset > 1<<50 || limit < 1 || limit > 1024 {
		return nil, connections.ErrInvalid
	}
	t, err := s.resolve(ctx, c, id, schema, name, ref.Object)
	if err != nil {
		return nil, err
	}
	var col *contract.StructureColumn
	for i := range t.Columns {
		if t.Columns[i].Name == field {
			col = &t.Columns[i]
			break
		}
	}
	if col == nil {
		return nil, connections.ErrInvalid
	}
	var args []any
	where, err := keyWhere(t, ref, &args)
	if err != nil {
		return nil, err
	}
	binary := col.ValueKind == contract.Binary
	if !supported(*col) {
		return nil, contract.ErrUnsupported
	}
	expression := quote(field)
	if col.ValueKind != contract.Text && col.ValueKind != contract.Binary {
		expression = fieldText(*col)
	}
	length := "LENGTH(" + expression + ")"
	if col.DataType == "RAW" {
		length = "UTL_RAW.LENGTH(" + expression + ")"
	}
	if lob(*col) {
		length = "DBMS_LOB.GETLENGTH(" + expression + ")"
	}
	substring := fmt.Sprintf("SUBSTR(%s,%d,%d)", expression, offset+1, limit)
	if col.DataType == "RAW" {
		substring = fmt.Sprintf("CASE WHEN UTL_RAW.LENGTH(%s)>%d THEN UTL_RAW.SUBSTR(%s,%d,LEAST(%d,UTL_RAW.LENGTH(%s)-%d)) END", expression, offset, expression, offset+1, limit, expression, offset)
	}
	if lob(*col) {
		substring = fmt.Sprintf("DBMS_LOB.SUBSTR(%s,%d,%d)", expression, limit, offset+1)
	}
	query := fmt.Sprintf("SELECT %s,%s FROM %s WHERE %s", substring, length, qualified(t.Object), where)
	rows, err := c.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, safeError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, contract.ErrDataConflict
	}
	var value, total any
	if err = rows.Scan(&value, &total); err != nil {
		return nil, safeError(err)
	}
	result := &contract.FieldChunk{Offset: offset, NextOffset: offset, Binary: binary, Null: total == nil}
	if total != nil {
		result.TotalUnits, err = strconv.ParseInt(fmt.Sprint(total), 10, 64)
		if err != nil {
			return nil, contract.ErrUnsupported
		}
	}
	if value != nil {
		if binary {
			result.Text = "0x" + fmt.Sprintf("%x", value.([]byte))
		} else {
			result.Text = fmt.Sprint(value)
		}
		result.NextOffset = min(offset+int64(limit), result.TotalUnits)
		result.HasMore = result.NextOffset < result.TotalUnits
	}
	return result, safeError(rows.Err())
}
