package trino

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const previewRows = 2000
const previewCellBytes = 4096
const previewBytes = 16 << 20

var decimalText = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?$`)

func valueKind(decl string) contract.CellKind {
	d := strings.ToLower(decl)
	switch {
	case d == "bigint" || d == "integer" || d == "smallint" || d == "tinyint":
		return contract.Integer
	case strings.HasPrefix(d, "decimal("):
		return contract.Decimal
	case d == "real" || d == "double":
		return contract.Floating
	case d == "boolean":
		return contract.Boolean
	case d == "varbinary":
		return contract.Binary
	case d == "date" || strings.HasPrefix(d, "time(") || strings.HasPrefix(d, "timestamp(") || d == "time" || d == "timestamp":
		return contract.DateTime
	case d == "json" || strings.HasPrefix(d, "array(") || strings.HasPrefix(d, "map(") || strings.HasPrefix(d, "row("):
		return contract.JSON
	default:
		return contract.Text
	}
}
func nativeCell(raw json.RawMessage, decl string) (contract.Cell, error) {
	if bytes.Equal(raw, []byte("null")) {
		return contract.Cell{Kind: contract.Null}, nil
	}
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return contract.Cell{}, ErrProtocol
	}
	k := valueKind(decl)
	cell := contract.Cell{Kind: k}
	if k == contract.Integer {
		if _, err := strconv.ParseInt(string(raw), 10, 64); err != nil {
			return cell, ErrProtocol
		}
		cell.Text = string(raw)
		return cell, nil
	}
	if k == contract.Boolean {
		if string(raw) != "true" && string(raw) != "false" {
			return cell, ErrProtocol
		}
		cell.Text = string(raw)
		return cell, nil
	}
	if k == contract.Floating && len(raw) > 0 && raw[0] != '"' {
		var n json.Number
		if json.Unmarshal(raw, &n) != nil {
			return cell, ErrProtocol
		}
		cell.Text = n.String()
		return cell, nil
	}
	if k == contract.JSON && strings.ToLower(decl) != "json" {
		cell.Text = string(raw)
		return cell, nil
	}
	if json.Unmarshal(raw, &cell.Text) != nil {
		return cell, contract.ErrUnsupported
	}
	if k == contract.Decimal && !decimalText.MatchString(cell.Text) {
		return cell, ErrProtocol
	}
	if k == contract.JSON && !json.Valid([]byte(cell.Text)) {
		return cell, ErrProtocol
	}
	if k == contract.Binary {
		v, err := base64.StdEncoding.Strict().DecodeString(cell.Text)
		if err != nil {
			return cell, ErrProtocol
		}
		cell.Bytes = make([]byte, len(v))
		copy(cell.Bytes, v)
		cell.Text = ""
	}
	return cell, nil
}
func nativeColumns(cols []Column) []contract.Column {
	out := make([]contract.Column, len(cols))
	for i, col := range cols {
		out[i] = contract.Column{Name: col.Name, DatabaseType: col.Type, Nullable: true}
	}
	return out
}
func preview(cell contract.Cell) contract.Cell {
	if len(cell.Text) > previewCellBytes {
		n := previewCellBytes
		for n > 0 && !utf8.ValidString(cell.Text[:n]) {
			n--
		}
		cell.Text = strings.Clone(cell.Text[:n])
		cell.Truncated = true
	}
	if len(cell.Bytes) > previewCellBytes {
		cell.Bytes = bytes.Clone(cell.Bytes[:previewCellBytes])
		cell.Truncated = true
	}
	return cell
}
