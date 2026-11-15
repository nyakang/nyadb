package tdengine

import (
	"encoding/hex"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

var decimalType = regexp.MustCompile(`^DECIMAL\(\s*(\d+)\s*,\s*(\d+)\s*\)$`)
var decimalValue = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?$`)

func nativeDataLiteral(column contract.StructureColumn, v contract.DataValue, precision string) (string, error) {
	if v.Default || v.Binary && v.Kind != contract.Binary || v.Kind != contract.Binary && len(v.Bytes) != 0 {
		return "", connections.ErrInvalid
	}
	if len(v.Text)+len(v.Bytes) > contract.DataEditMaxBytes {
		return "", contract.ErrDataEditLimit
	}
	if v.Null || v.Kind == contract.Null {
		if v.Text != "" || len(v.Bytes) != 0 || v.Binary || !column.Nullable {
			return "", connections.ErrInvalid
		}
		return "NULL", nil
	}
	if v.Kind != column.ValueKind {
		return "", connections.ErrInvalid
	}
	switch v.Kind {
	case contract.DateTime:
		return timestampLiteral(contract.Cell{Kind: v.Kind, Text: v.Text}, precision)
	case contract.Integer:
		typ := strings.TrimSuffix(column.DataType, " UNSIGNED")
		bits := map[string]int{"TINYINT": 8, "SMALLINT": 16, "INT": 32, "BIGINT": 64}[typ]
		if bits == 0 {
			return "", contract.ErrUnsupported
		}
		if strings.HasSuffix(column.DataType, " UNSIGNED") {
			n, err := strconv.ParseUint(v.Text, 10, bits)
			if err != nil {
				return "", connections.ErrInvalid
			}
			return strconv.FormatUint(n, 10), nil
		}
		n, err := strconv.ParseInt(v.Text, 10, bits)
		if err != nil {
			return "", connections.ErrInvalid
		}
		return strconv.FormatInt(n, 10), nil
	case contract.Floating:
		bits := 64
		if column.DataType == "FLOAT" {
			bits = 32
		}
		n, err := strconv.ParseFloat(v.Text, bits)
		if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
			return "", connections.ErrInvalid
		}
		return strconv.FormatFloat(n, 'g', -1, bits), nil
	case contract.Boolean:
		if v.Text != "true" && v.Text != "false" {
			return "", connections.ErrInvalid
		}
		return v.Text, nil
	case contract.Decimal:
		typ := decimalType.FindStringSubmatch(column.DataType)
		if len(typ) != 3 || !decimalValue.MatchString(v.Text) {
			return "", connections.ErrInvalid
		}
		p, _ := strconv.Atoi(typ[1])
		scale, _ := strconv.Atoi(typ[2])
		integer, fraction, _ := strings.Cut(strings.TrimLeft(v.Text, "+-"), ".")
		if p < 1 || p > 38 || scale < 0 || scale > p || len(strings.TrimLeft(integer, "0")) > p-scale || len(fraction) > scale {
			return "", connections.ErrInvalid
		}
		return literal(v.Text), nil // Never round an exact decimal through float64.
	case contract.Binary:
		if column.DataType != "VARBINARY" || v.Text != "" {
			return "", contract.ErrUnsupported
		}
		if column.CharacterMaxLength != nil && int64(len(v.Bytes)) > *column.CharacterMaxLength {
			return "", connections.ErrInvalid
		}
		return "'\\x" + hex.EncodeToString(v.Bytes) + "'", nil
	case contract.Text:
		if !utf8.ValidString(v.Text) || strings.ContainsRune(v.Text, 0) {
			return "", contract.ErrUnsupported
		}
		units := int64(len(v.Text))
		if column.DataType == "NCHAR" {
			units = int64(utf8.RuneCountInString(v.Text))
		}
		if column.CharacterMaxLength != nil && units > *column.CharacterMaxLength {
			return "", connections.ErrInvalid
		}
		return literal(v.Text), nil
	default:
		return "", contract.ErrUnsupported
	}
}
