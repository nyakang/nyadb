package engines

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) countData(ctx context.Context, id int64, schema, name string) (int64, error) {
	if id != s.id {
		return 0, connections.ErrInvalid
	}
	o := s.Namespace(schema).Object("table", name)
	var count int64
	err := s.dataQuery(ctx, o, nil, func(ctx context.Context, q queryer) error {
		t, err := s.structure(ctx, q, o)
		if err != nil {
			return err
		}
		return queryOne(ctx, q, "SELECT COUNT(*) FROM "+qualified(t.Object), nil, &count)
	})
	return count, err
}
func (s *Session) chunkData(ctx context.Context, id int64, schema, name, field string, ref contract.RowRef, offset int64, units int, owner contract.Transaction) (*contract.FieldChunk, error) {
	if id != s.id || offset < 0 || offset > math.MaxInt64-1025 || units <= 0 || units > 1024 {
		return nil, connections.ErrInvalid
	}
	o := s.Namespace(schema).Object("table", name)
	var out *contract.FieldChunk
	err := s.dataQuery(ctx, o, owner, func(ctx context.Context, q queryer) error {
		t, err := s.structure(ctx, q, o)
		if err != nil {
			return err
		}
		if err = validateDataRef(t, ref); err != nil {
			return err
		}
		f, ok := validColumns(t)[field]
		if !ok {
			return connections.ErrInvalid
		}
		c := quote(field)
		var projection, length, storage string
		if s.kind == connections.SQLite {
			projection = "CASE WHEN typeof(" + c + ")='text' AND instr(" + c + ",char(0))>0 THEN _nyadb_text_slice_v1(" + c + "," + s.arg(1) + "," + s.arg(2) + ") WHEN length(" + c + ")=0 THEN " + c + " ELSE substr(" + c + "," + s.arg(1) + "," + s.arg(2) + ") END"
			length = "CASE WHEN typeof(" + c + ")='text' AND instr(" + c + ",char(0))>0 THEN _nyadb_text_units_v1(" + c + ") ELSE length(" + c + ") END"
			storage = "typeof(" + c + ")"
		} else {
			typ := canonicalDataType(f.DatabaseType)
			if typ != "bytea" && !pgDataText(f.DatabaseType) && typ != "json" && typ != "jsonb" {
				return contract.ErrUnsupported
			}
			storage = "'text'"
			length = "char_length(" + c + "::text)"
			if typ == "bytea" {
				storage = "'blob'"
				length = "octet_length(" + c + ")"
			} else {
				c += "::text"
			}
			projection = "substring(" + c + " from " + s.arg(1) + "::integer for " + s.arg(2) + "::integer)"
		}
		args := []any{offset + 1, units}
		var conditions []string
		cols := validColumns(t)
		for i, k := range ref.Columns {
			if err = validateTypedValue(ref.TypedValues[i], s.kind); err != nil {
				return err
			}
			args = append(args, parameter(ref.TypedValues[i]))
			conditions = append(conditions, s.strategy.comparison(cols[k], quote(k), s.arg(len(args))))
		}
		rows, err := q.QueryContext(ctx, "SELECT "+projection+","+length+","+storage+" FROM "+qualified(t.Object)+" WHERE "+strings.Join(conditions, " AND ")+" LIMIT 2", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			if err = rows.Err(); err != nil {
				return err
			}
			return sql.ErrNoRows
		}
		var fragment any
		var total sql.NullInt64
		var kind string
		if err = rows.Scan(&fragment, &total, &kind); err != nil {
			return err
		}
		if rows.Next() {
			return ErrConflict
		}
		if err = rows.Err(); err != nil {
			return err
		}
		out = &contract.FieldChunk{Offset: offset, Binary: kind == "blob"}
		if !total.Valid {
			out.Null = true
			return nil
		}
		if kind != "text" && kind != "blob" {
			return contract.ErrUnsupported
		}
		out.TotalUnits = total.Int64
		if fragment == nil && offset >= out.TotalUnits {
			if out.Binary {
				fragment = []byte{}
			} else {
				fragment = ""
			}
		}
		if out.Binary {
			v, ok := fragment.([]byte)
			if !ok {
				return errors.New("invalid binary fragment")
			}
			out.Text = "0x" + hex.EncodeToString(v)
			out.NextOffset = offset + int64(len(v))
		} else {
			v := cell(fragment, "text", s.kind).Text
			if !utf8.ValidString(v) {
				return errors.New("invalid UTF-8 fragment")
			}
			out.Text = v
			out.NextOffset = offset + int64(utf8.RuneCountInString(v))
		}
		out.HasMore = out.NextOffset < out.TotalUnits
		return nil
	})
	return out, err
}
