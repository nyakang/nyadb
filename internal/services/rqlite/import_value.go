package rqlite

import (
	"encoding/json"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

var importNumber = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// Apply native SQLite affinity without silently rounding exact source numbers.
// Typed REAL inputs already describe a binary float; untyped decimal sources
// must be exactly representable, or use a TEXT destination instead.
func importValueLiteral(c contract.StructureColumn, v contract.DataValue, strict bool) (string, error) {
	if v.Default || len(v.Text)+len(v.Bytes) > contract.DataEditMaxBytes || !utf8.ValidString(v.Text) || v.Binary && v.Text != "" || !v.Binary && len(v.Bytes) > 0 {
		return "", contract.ErrDataPage
	}
	if v.Null || v.Kind == contract.Null {
		if v.Text != "" || len(v.Bytes) > 0 || v.Binary || !c.Nullable {
			return "", contract.ErrDataPage
		}
		return "NULL", nil
	}
	kind := v.Kind
	if kind == "" {
		kind = contract.Text
		if v.Binary {
			kind = contract.Binary
		}
	}
	if v.Binary && kind != contract.Binary {
		return "", contract.ErrDataPage
	}
	cell := contract.Cell{Kind: kind, Text: v.Text, Bytes: v.Bytes}
	if kind == contract.Binary {
		literal, _, err := dataCellLiteral(cell)
		return literal, err
	}
	if kind == contract.Integer {
		if _, err := strconv.ParseInt(v.Text, 10, 64); err != nil {
			return "", contract.ErrDataPage
		}
	} else if kind == contract.Floating {
		f, err := strconv.ParseFloat(v.Text, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return "", contract.ErrDataPage
		}
	} else if kind == contract.Boolean {
		b, err := strconv.ParseBool(v.Text)
		if err != nil {
			return "", contract.ErrDataPage
		}
		cell.Kind = contract.Integer
		cell.Text = "0"
		if b {
			cell.Text = "1"
		}
	} else if kind != contract.Text && kind != contract.Decimal && kind != contract.DateTime && kind != contract.JSON {
		return "", contract.ErrUnsupported
	}
	if kind == contract.JSON && !json.Valid([]byte(v.Text)) {
		return "", contract.ErrDataPage
	}
	typ := strings.ToUpper(c.Type)
	textAffinity := !strings.Contains(typ, "INT") && (strings.Contains(typ, "TEXT") || strings.Contains(typ, "CHAR") || strings.Contains(typ, "CLOB"))
	if textAffinity {
		return dataLiteral(v.Text), nil
	}
	noAffinity := typ == "" || !strings.Contains(typ, "INT") && strings.Contains(typ, "BLOB") || strict && typ == "ANY"
	if noAffinity {
		if cell.Kind == contract.Decimal || cell.Kind == contract.DateTime || cell.Kind == contract.JSON {
			cell.Kind = contract.Text
		}
		literal, _, err := dataCellLiteral(cell)
		return literal, err
	}
	realAffinity := !strings.Contains(typ, "INT") && (strings.Contains(typ, "REAL") || strings.Contains(typ, "FLOA") || strings.Contains(typ, "DOUB"))
	if cell.Kind == contract.Floating {
		literal, _, err := dataCellLiteral(cell)
		return literal, err
	}
	if cell.Kind == contract.DateTime || cell.Kind == contract.JSON {
		return dataLiteral(v.Text), nil
	}
	numeric := strings.Trim(cell.Text, " \t\n\r\v\f")
	if !importNumber.MatchString(numeric) {
		if cell.Kind == contract.Decimal || cell.Kind == contract.Integer {
			return "", contract.ErrDataPage
		}
		return dataLiteral(cell.Text), nil
	}
	if len(numeric) > 4096 {
		return "", contract.ErrDataEditLimit
	}
	n, ok := new(big.Rat).SetString(numeric)
	if !ok {
		if cell.Kind == contract.Decimal || cell.Kind == contract.Integer {
			return "", contract.ErrDataPage
		}
		return dataLiteral(cell.Text), nil
	}
	if !realAffinity && n.IsInt() && n.Num().IsInt64() {
		return n.Num().String(), nil
	}
	f, exact := n.Float64()
	if !exact || math.IsInf(f, 0) || math.IsNaN(f) {
		return "", contract.ErrDataPage
	}
	cell.Kind, cell.Text = contract.Floating, strconv.FormatFloat(f, 'g', -1, 64)
	literal, _, err := dataCellLiteral(cell)
	return literal, err
}
