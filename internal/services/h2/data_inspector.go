package h2

import (
	"context"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) readRow(ctx context.Context, id int64, schema, name string, ref contract.RowRef) (out *contract.DataRow, err error) {
	err = s.withFeature(ctx, func(ctx context.Context, session *agentbridge.Session) error {
		t, err := s.resolve(ctx, session, id, schema, name, ref.Object)
		if err != nil {
			return err
		}
		out, _, err = s.rowWith(ctx, session, t, ref, true)
		return err
	})
	return
}

func (s *Session) rowWith(ctx context.Context, session *agentbridge.Session, t *contract.TableStructure, ref contract.RowRef, full bool) (*contract.DataRow, []contract.DataCell, error) {
	where, err := keyWhere(t, ref)
	if err != nil {
		return nil, nil, err
	}
	limit := 1024
	if full {
		limit = contract.DataEditMaxBytes
	}
	var lengths, fields []string
	for _, c := range t.Columns {
		if !readable(c) {
			return nil, nil, contract.ErrUnsupported
		}
		lengths = append(lengths, "COALESCE("+valueLength(c)+",0)")
	}
	total := "(" + strings.Join(lengths, "+") + ")"
	for _, c := range t.Columns {
		field := projection(c, limit)
		if full {
			// Check and read in one native statement: a racing growth cannot
			// evade an earlier length check and allocate an oversized result.
			field = "CASE WHEN " + total + "<=" + strconv.Itoa(limit) + " THEN " + field + " ELSE NULL END"
		}
		fields = append(fields, field)
	}
	for _, c := range t.Columns {
		fields = append(fields, fingerprint(c))
	}
	for _, c := range t.Columns {
		fields = append(fields, valueLength(c))
	}
	fields = append(fields, total)
	q, err := s.query(ctx, session, "SELECT "+strings.Join(fields, ",")+" FROM "+qualified(t.Object)+" WHERE "+where+" LIMIT 2", 2)
	if err != nil {
		return nil, nil, err
	}
	n := len(t.Columns)
	if len(q.Sets) != 1 || len(q.Sets[0].Rows) != 1 || q.Truncated || len(q.Sets[0].Rows[0]) != n*3+1 {
		return nil, nil, contract.ErrDataConflict
	}
	raw := q.Sets[0].Rows[0]
	length, err := strconv.ParseInt(raw[n*3].Text, 10, 64)
	if err != nil || length < 0 {
		return nil, nil, contract.ErrUnsupported
	}
	if full && length > int64(limit) {
		return nil, nil, contract.ErrDataEditLimit
	}
	cells, previews, fp, err := rowCells(t, raw[:n*3], limit, full)
	if err != nil {
		return nil, nil, err
	}
	mode := "fingerprints"
	if full {
		mode = "values"
	}
	row := &contract.DataRow{Columns: t.Columns, Values: map[string]contract.DataValue{}, Snapshot: s.snapshot(t, cells, fp, mode)}
	if row.Snapshot == nil {
		return nil, nil, contract.ErrDataReadOnly
	}
	for i, c := range t.Columns {
		v := cells[i]
		row.Values[c.Name] = contract.DataValue{Kind: v.Kind, Text: v.Text, Bytes: v.Bytes, Null: v.Kind == contract.Null, Binary: v.Kind == contract.Binary}
	}
	return row, previews, nil
}

func (s *Session) chunk(ctx context.Context, id int64, schema, name, field string, ref contract.RowRef, offset int64, limit int) (out *contract.FieldChunk, err error) {
	if offset < 0 || offset > 1<<50 || limit < 1 || limit > 1024 {
		return nil, connections.ErrInvalid
	}
	err = s.withFeature(ctx, func(ctx context.Context, session *agentbridge.Session) error {
		t, err := s.resolve(ctx, session, id, schema, name, ref.Object)
		if err != nil {
			return err
		}
		var c contract.StructureColumn
		for _, col := range t.Columns {
			if col.Name == field {
				c = col
			}
		}
		if c.Name == "" || !readable(c) {
			return contract.ErrUnsupported
		}
		where, err := keyWhere(t, ref)
		if err != nil {
			return err
		}
		query := "SELECT SUBSTRING(" + valueBytes(c) + "," + strconv.FormatInt(offset+1, 10) + "," + strconv.Itoa(limit+4) + ")," + valueLength(c) + " FROM " + qualified(t.Object) + " WHERE " + where + " LIMIT 2"
		q, err := s.query(ctx, session, query, 2)
		if err != nil {
			return err
		}
		if len(q.Sets) != 1 || len(q.Sets[0].Rows) != 1 || len(q.Sets[0].Rows[0]) != 2 || q.Truncated {
			return contract.ErrDataConflict
		}
		v, length := q.Sets[0].Rows[0][0], q.Sets[0].Rows[0][1]
		out = &contract.FieldChunk{Offset: offset, NextOffset: offset, Binary: c.ValueKind == contract.Binary, Null: v.Kind == contract.Null}
		if out.Null {
			if length.Kind != contract.Null {
				return contract.ErrUnsupported
			}
			return nil
		}
		out.TotalUnits, err = strconv.ParseInt(length.Text, 10, 64)
		if err != nil || out.TotalUnits < 0 || v.Kind != contract.Binary {
			return contract.ErrUnsupported
		}
		data := v.Bytes
		if len(data) > limit {
			data = data[:limit]
		}
		if out.Binary {
			out.Text = "0x" + hex.EncodeToString(data)
		} else {
			for len(data) > 0 && !utf8.Valid(data) {
				data = data[:len(data)-1]
			}
			if len(data) == 0 && offset < out.TotalUnits {
				// A limit smaller than one code point, or an arbitrary offset
				// inside it, must not publish replacement bytes or stall paging.
				return connections.ErrInvalid
			}
			out.Text = string(data)
		}
		out.NextOffset = offset + int64(len(data))
		out.HasMore = out.NextOffset < out.TotalUnits
		return nil
	})
	return
}
