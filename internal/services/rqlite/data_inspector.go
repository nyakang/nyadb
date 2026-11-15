package rqlite

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func dataCellLiteral(v contract.Cell) (string, string, error) {
	if v.Truncated || len(v.Text)+len(v.Bytes) > contract.DataEditMaxBytes {
		return "", "", connections.ErrInvalid
	}
	switch v.Kind {
	case contract.Text:
		if !utf8.ValidString(v.Text) || len(v.Bytes) != 0 {
			return "", "", connections.ErrInvalid
		}
		return dataLiteral(v.Text), "text", nil
	case contract.Binary:
		if v.Text != "" {
			return "", "", connections.ErrInvalid
		}
		return "X'" + hex.EncodeToString(v.Bytes) + "'", "blob", nil
	case contract.Integer:
		n, err := strconv.ParseInt(v.Text, 10, 64)
		if err != nil || len(v.Bytes) != 0 {
			return "", "", connections.ErrInvalid
		}
		return strconv.FormatInt(n, 10), "integer", nil
	case contract.Floating:
		n, err := strconv.ParseFloat(v.Text, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || len(v.Bytes) != 0 {
			return "", "", connections.ErrInvalid
		}
		return "CAST(" + dataLiteral(strconv.FormatFloat(n, 'g', -1, 64)) + " AS REAL)", "real", nil
	default:
		return "", "", contract.ErrUnsupported
	}
}

func dataKeyWhere(t tableSnapshot, ref contract.RowRef) (string, error) {
	if ref.Object != t.structure.Object || ref.Database != "main" || ref.Table != t.structure.Name || len(t.key) == 0 || !slices.Equal(ref.Columns, t.key) || len(ref.TypedValues) != len(t.key) || len(ref.Values) != 0 && len(ref.Values) != len(t.key) {
		return "", connections.ErrInvalid
	}
	terms := make([]string, len(t.key))
	for i, key := range t.key {
		v := ref.TypedValues[i]
		literal, typ, err := dataCellLiteral(v)
		if err != nil {
			return "", err
		}
		legacy := []byte(v.Text)
		if v.Kind == contract.Binary {
			legacy = v.Bytes
		}
		if len(ref.Values) != 0 && !bytes.Equal(ref.Values[i], legacy) {
			return "", connections.ErrInvalid
		}
		column := quote(key)
		terms[i] = "(typeof(" + column + ")='" + typ + "' AND " + dataBytes(column) + "=" + dataBytes(literal) + ")"
	}
	return strings.Join(terms, " AND "), nil
}

func (s *Session) readDataRow(ctx context.Context, id int64, database, name string, ref contract.RowRef) (*contract.DataRow, error) {
	t, err := s.dataTable(ctx, id, database, name, ref.Object)
	if err != nil {
		return nil, err
	}
	where, err := dataKeyWhere(t, ref)
	if err != nil {
		return nil, err
	}
	fields := make([]string, len(t.structure.Columns))
	for i, c := range t.structure.Columns {
		fields[i] = fmt.Sprintf("%s AS c%d", quote(c.Name), i)
	}
	source := "SELECT " + strings.Join(fields, ",") + " FROM main." + quote(name) + " WHERE " + where + " LIMIT 2"
	rows, err := s.readDataProjection(ctx, t, source, contract.DataEditMaxBytes, 2, true)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, contract.ErrDataConflict
	}
	out := &contract.DataRow{Columns: t.structure.Columns, Values: map[string]contract.DataValue{}, Snapshot: s.dataSnapshot(t, rows[0])}
	if out.Snapshot == nil {
		return nil, contract.ErrDataReadOnly
	}
	for i, c := range t.structure.Columns {
		v := rows[0].cells[i].Clone()
		out.Values[c.Name] = contract.DataValue{Kind: v.Kind, Text: v.Text, Bytes: v.Bytes, Binary: v.Kind == contract.Binary, Null: v.Kind == contract.Null}
	}
	return out, nil
}

// Offsets and TotalUnits count original UTF-8 bytes, including NUL. Native
// substr(TEXT) would stop at NUL, so even TEXT is sliced as BLOB here.
func (s *Session) readDataChunk(ctx context.Context, id int64, database, name, field string, ref contract.RowRef, offset int64, limit int) (*contract.FieldChunk, error) {
	if offset < 0 || offset > 1<<50 || limit < 1 || limit > 1024 {
		return nil, connections.ErrInvalid
	}
	t, err := s.dataTable(ctx, id, database, name, ref.Object)
	if err != nil {
		return nil, err
	}
	known := false
	for _, c := range t.structure.Columns {
		known = known || c.Name == field
	}
	if !known {
		return nil, connections.ErrInvalid
	}
	where, err := dataKeyWhere(t, ref)
	if err != nil {
		return nil, err
	}
	column := quote(field)
	b := dataBytes(column)
	query := fmt.Sprintf("SELECT %s,typeof(%s),hex(substr(%s,%d,%d)),length(%s) FROM main.%s WHERE %s LIMIT 2", schemaGuard(t), column, b, offset+1, limit, b, quote(name), where)
	r, err := s.request(ctx, "/db/query", []any{query}, false, false)
	if err != nil {
		return nil, err
	}
	rows, err := decodedRows(r, 0, 4)
	if err != nil || len(rows) != 1 {
		return nil, contract.ErrDataConflict
	}
	flag, err := cellInt(rows[0][0])
	if err != nil || flag != 1 {
		return nil, contract.ErrDataConflict
	}
	typ, err := cellText(rows[0][1])
	if err != nil {
		return nil, err
	}
	out := &contract.FieldChunk{Offset: offset, NextOffset: offset, Binary: typ == "blob", Null: typ == "null"}
	if out.Null {
		if rows[0][3].Kind != contract.Null {
			return nil, errIncomplete
		}
		return out, nil
	}
	out.TotalUnits, err = cellInt(rows[0][3])
	if err != nil || out.TotalUnits < 0 {
		return nil, errIncomplete
	}
	encoded, err := cellText(rows[0][2])
	if err != nil || len(encoded) > limit*2 {
		return nil, errIncomplete
	}
	data, err := hex.DecodeString(encoded)
	if err != nil || int64(len(data)) != min(int64(limit), max(0, out.TotalUnits-offset)) {
		return nil, errIncomplete
	}
	if out.Binary {
		out.Text = "0x" + hex.EncodeToString(data)
	} else {
		if len(data) > 0 && offset+int64(len(data)) < out.TotalUnits {
			start := len(data) - 1
			for start > 0 && !utf8.RuneStart(data[start]) {
				start--
			}
			if !utf8.FullRune(data[start:]) {
				data = data[:start]
			}
		}
		if !utf8.Valid(data) || len(data) == 0 && offset < out.TotalUnits {
			return nil, contract.ErrUnsupported
		}
		out.Text = string(data)
	}
	out.NextOffset = offset + int64(len(data))
	out.HasMore = out.NextOffset < out.TotalUnits
	return out, nil
}
