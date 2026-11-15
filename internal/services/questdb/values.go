package questdb

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nyakang/nyadb/internal/db/contract"
)

const previewBytes = 16 << 20
const previewRows = 10000
const previewCellBytes = 4096

func typeName(oid uint32) string {
	switch oid {
	case pgtype.Int2OID:
		return "SHORT"
	case pgtype.Int4OID:
		return "INT"
	case pgtype.Int8OID:
		return "LONG"
	case pgtype.Float4OID:
		return "FLOAT"
	case pgtype.Float8OID:
		return "DOUBLE"
	case pgtype.BoolOID:
		return "BOOLEAN"
	case pgtype.DateOID:
		return "DATE"
	case pgtype.TimestampOID, pgtype.TimestamptzOID:
		return "TIMESTAMP"
	case pgtype.ByteaOID:
		return "BINARY"
	case pgtype.UUIDOID:
		return "UUID"
	default:
		return "STRING"
	}
}

func valueKind(decl string) contract.CellKind {
	switch strings.ToUpper(decl) {
	case "BYTE", "SHORT", "INT", "LONG":
		return contract.Integer
	case "FLOAT", "DOUBLE":
		return contract.Floating
	case "BOOLEAN":
		return contract.Boolean
	case "DATE", "TIMESTAMP":
		return contract.DateTime
	case "BINARY":
		return contract.Binary
	default:
		return contract.Text
	}
}

func nativeCell(raw []byte, oid uint32) (contract.Cell, error) {
	if raw == nil {
		return contract.Cell{Kind: contract.Null}, nil
	}
	kind := valueKind(typeName(oid))
	if kind == contract.Binary {
		if !bytes.HasPrefix(raw, []byte(`\x`)) {
			return contract.Cell{}, contract.ErrUnsupported
		}
		v, err := hex.DecodeString(string(raw[2:]))
		if err != nil {
			return contract.Cell{}, err
		}
		return contract.Cell{Kind: kind, Bytes: v}, nil
	}
	if !utf8.Valid(raw) {
		return contract.Cell{}, contract.ErrUnsupported
	}
	text := string(raw)
	if kind == contract.Boolean {
		if text == "t" {
			text = "true"
		} else if text == "f" {
			text = "false"
		} else {
			return contract.Cell{}, contract.ErrUnsupported
		}
	}
	return contract.Cell{Kind: kind, Text: text}, nil
}

// Results use original PG text frames. Arguments retain their explicit native
// OIDs through Exec mode, avoiding QuestDB's unspecified-numeric DOUBLE bind.
func execute(ctx context.Context, c *pgx.Conn, sql string, args []any, index int, sink func(int, []contract.Column, []contract.Cell) error, budget *int) (set contract.ResultSet, err error) {
	rows, err := openRows(ctx, c, sql, args)
	if err != nil {
		return set, err
	}
	defer rows.Close()
	desc := rows.FieldDescriptions()
	if len(desc) > 512 {
		return set, contract.ErrUnsupported
	}
	for _, d := range desc {
		if d.Format != 0 {
			return set, contract.ErrUnsupported
		}
		set.Columns = append(set.Columns, contract.Column{Name: d.Name, DatabaseType: typeName(d.DataTypeOID), Nullable: true})
	}
	flags := map[string]bool{}
	for rows.Next() {
		raw := rows.RawValues()
		if len(raw) != len(desc) {
			return set, errors.New("incomplete QuestDB row")
		}
		cells := make([]contract.Cell, len(raw))
		for i, v := range raw {
			cells[i], err = nativeCell(v, desc[i].DataTypeOID)
			if err != nil {
				return set, err
			}
		}
		if sink != nil {
			if err = sink(index, set.Columns, cells); err != nil {
				return set, err
			}
		}
		if budget == nil {
			set.Rows = append(set.Rows, cells)
			continue
		}
		if len(set.Rows) >= previewRows {
			flags["rows"] = true
			continue
		}
		size := 64 * len(cells)
		for i := range cells {
			if len(cells[i].Text) > previewCellBytes {
				n := previewCellBytes
				for n > 0 && !utf8.ValidString(cells[i].Text[:n]) {
					n--
				}
				cells[i].Text = strings.Clone(cells[i].Text[:n])
				cells[i].Truncated = true
			}
			if len(cells[i].Bytes) > previewCellBytes {
				cells[i].Bytes = bytes.Clone(cells[i].Bytes[:previewCellBytes])
				cells[i].Truncated = true
			}
			if cells[i].Truncated {
				flags["cell"] = true
			}
			size += len(cells[i].Text) + len(cells[i].Bytes)
		}
		if size > *budget {
			flags["bytes"] = true
			continue
		}
		*budget -= size
		set.Rows = append(set.Rows, cells)
	}
	rows.Close()
	if !rows.CommandTag().Select() {
		set.AffectedRows = rows.CommandTag().RowsAffected()
	}
	for _, reason := range []string{"rows", "bytes", "cell"} {
		if flags[reason] {
			set.Truncation = append(set.Truncation, reason)
		}
	}
	return set, rows.Err()
}
