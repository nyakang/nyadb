package dameng

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func variableField(c contract.StructureColumn) bool {
	switch c.DataType {
	case "CLOB", "TEXT", "BLOB", "IMAGE", "CHAR", "VARCHAR", "BINARY", "VARBINARY":
		return true
	}
	return false
}

func fieldLength(c contract.StructureColumn) string {
	name := quote(c.Name)
	switch c.DataType {
	case "CLOB", "TEXT", "BLOB", "IMAGE":
		return "SYS.DBMS_LOB.GETLENGTH(" + name + ")"
	case "BINARY", "VARBINARY":
		return "SYS.UTL_RAW.LENGTH(" + name + ")"
	}
	return "LENGTH(" + name + ")"
}

func fieldSlice(c contract.StructureColumn, offset int64, units int) string {
	name := quote(c.Name)
	switch c.DataType {
	case "CLOB", "TEXT", "BLOB", "IMAGE":
		return fmt.Sprintf("SYS.DBMS_LOB.SUBSTR(%s,%d,%d)", name, units, offset+1)
	case "BINARY", "VARBINARY":
		return fmt.Sprintf("SYS.UTL_RAW.SUBSTR(%s,%d,%d)", name, offset+1, units)
	}
	return fmt.Sprintf("SUBSTR(%s,%d,%d)", name, offset+1, units)
}

// Complete fields are explicit. Each wire fragment is small, and both the
// complete byte bound and the server's character/byte length are checked.
func fullFieldAt(ctx context.Context, q metadataQueryer, o contract.ObjectRef, c contract.StructureColumn, where string, args []any) (contract.Cell, error) {
	var total sql.NullInt64
	if err := q.QueryRowContext(ctx, "SELECT "+fieldLength(c)+" FROM "+qualified(o)+" WHERE "+where, args...).Scan(&total); err != nil {
		return contract.Cell{}, fmt.Errorf("DM8 complete field length: %w", err)
	}
	if !total.Valid {
		return contract.Cell{Kind: contract.Null}, nil
	}
	if total.Int64 < 0 || total.Int64 > contract.DataEditMaxBytes {
		return contract.Cell{}, contract.ErrDataEditLimit
	}
	out := contract.Cell{Kind: c.ValueKind}
	if out.Kind == contract.Binary {
		out.Bytes = []byte{}
	}
	var text strings.Builder
	for offset := int64(0); offset < total.Int64; {
		if err := ctx.Err(); err != nil {
			return contract.Cell{}, err
		}
		units := int(min(int64(2048), total.Int64-offset))
		var raw any
		if err := q.QueryRowContext(ctx, "SELECT "+fieldSlice(c, offset, units)+" FROM "+qualified(o)+" WHERE "+where, args...).Scan(&raw); err != nil {
			return contract.Cell{}, fmt.Errorf("DM8 complete field fragment at %d: %w", offset, err)
		}
		fragment, err := cell(raw, c.DataType)
		if err != nil {
			return contract.Cell{}, err
		}
		if fragment.Kind != c.ValueKind || fragment.Truncated {
			return contract.Cell{}, contract.ErrDataConflict
		}
		read := utf8.RuneCountInString(fragment.Text)
		if out.Kind == contract.Binary {
			read = len(fragment.Bytes)
			if len(out.Bytes)+read > contract.DataEditMaxBytes {
				return contract.Cell{}, contract.ErrDataEditLimit
			}
			out.Bytes = append(out.Bytes, fragment.Bytes...)
		} else {
			if text.Len()+len(fragment.Text) > contract.DataEditMaxBytes {
				return contract.Cell{}, contract.ErrDataEditLimit
			}
			text.WriteString(fragment.Text)
		}
		if read <= 0 || read > units {
			return contract.Cell{}, contract.ErrDataConflict
		}
		offset += int64(read)
	}
	if out.Kind != contract.Binary {
		out.Text = text.String()
	}
	return out, nil
}

func readFullRowAt(ctx context.Context, q metadataQueryer, t *contract.TableStructure, key []string, ref contract.RowRef) ([]contract.Cell, error) {
	where, args, err := rowWhere(t, key, ref)
	if err != nil {
		return nil, err
	}
	projection, err := dataProjection(t, 1024)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, "SELECT "+projection+" FROM "+qualified(t.Object)+" WHERE "+where+" LIMIT 2", args...)
	if err != nil {
		return nil, fmt.Errorf("DM8 complete row preview: %w", err)
	}
	values, err := scanData(rows, t, 2, 1024)
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if len(values) != 1 {
		return nil, contract.ErrDataConflict
	}
	row := values[0]
	budget := 16 << 20
	for i, c := range t.Columns {
		if row[i].Truncated {
			if !variableField(c) {
				return nil, contract.ErrUnsupported
			}
			row[i], err = fullFieldAt(ctx, q, t.Object, c, where, args)
			if err != nil {
				return nil, err
			}
		}
		if len(row[i].Text) > contract.DataEditMaxBytes || len(row[i].Bytes) > contract.DataEditMaxBytes || row[i].Truncated {
			return nil, contract.ErrDataEditLimit
		}
		// Values, snapshots and returned byte maps are independent copies.
		budget -= 192 + len(row[i].Text) + len(row[i].Bytes)*3
		if budget < 0 {
			return nil, contract.ErrDataEditLimit
		}
	}
	return row, nil
}

func fullRowRef(t *contract.TableStructure, key []string, row []contract.Cell) contract.RowRef {
	ref := contract.RowRef{Object: t.Object, Database: t.Object.Schema, Table: t.Object.Name, Columns: append([]string(nil), key...)}
	cols := map[string]int{}
	for i, c := range t.Columns {
		cols[c.Name] = i
	}
	for _, k := range key {
		v := row[cols[k]].Clone()
		b, _ := json.Marshal(v)
		ref.TypedValues = append(ref.TypedValues, v)
		ref.Values = append(ref.Values, b)
	}
	return ref
}

func (s *Session) fullSnapshot(t *contract.TableStructure, key []string, row []contract.Cell, identity string) *contract.RowSnapshot {
	snapshot := &contract.RowSnapshot{Mode: "values", Metadata: s.dataSignature(t, contract.DataOptions{}, nil, identity), Ref: fullRowRef(t, key, row)}
	for i, c := range t.Columns {
		snapshot.Columns = append(snapshot.Columns, c.Name)
		snapshot.Original = append(snapshot.Original, row[i].Clone())
	}
	return snapshot
}

// Native LOB locators do not retain the scalar SERIALIZABLE snapshot after a
// concurrent replacement (-2201). Keep writes blocked for the entire explicit
// complete read, including every fragment; all locks end on original rollback.
func (s *Session) completeDataRead(ctx context.Context, o contract.ObjectRef, read func(context.Context, *sql.Conn, *contract.TableStructure) error) error {
	return s.dataReadLocked(ctx, o, true, read)
}

func (s *Session) readDataRow(ctx context.Context, id int64, schema, name string, ref contract.RowRef) (*contract.DataRow, error) {
	return s.readDataRowWith(ctx, id, schema, name, ref, s.completeDataRead)
}

func (s *Session) readDataRowWith(ctx context.Context, id int64, schema, name string, ref contract.RowRef, read dataReader) (out *contract.DataRow, err error) {
	o, err := s.dataObject(id, schema, name, ref.Object)
	if err != nil {
		return nil, err
	}
	err = read(ctx, o, func(ctx context.Context, q *sql.Conn, t *contract.TableStructure) error {
		identity, e := tableIdentityAt(ctx, q, o)
		if e != nil {
			return e
		}
		key, e := primaryKeyAt(ctx, q, t)
		if e != nil {
			return e
		}
		if len(key) == 0 {
			return contract.ErrDataReadOnly
		}
		row, e := readFullRowAt(ctx, q, t, key, ref)
		if e != nil {
			return e
		}
		out = &contract.DataRow{Columns: t.Columns, Snapshot: s.fullSnapshot(t, key, row, identity), Values: make(map[string]contract.DataValue, len(row))}
		for i, c := range t.Columns {
			v := row[i]
			out.Values[c.Name] = contract.DataValue{Kind: v.Kind, Text: v.Text, Bytes: bytes.Clone(v.Bytes), Null: v.Kind == contract.Null, Binary: v.Kind == contract.Binary}
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
