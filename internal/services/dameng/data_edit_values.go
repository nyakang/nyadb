package dameng

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

const dataBatchBytes = 64 << 20

func chargeDataRow(budget *int, row []contract.Cell) error {
	for _, c := range row {
		*budget -= 192 + len(c.Text) + 3*len(c.Bytes)
	}
	if *budget < 0 {
		return contract.ErrDataEditLimit
	}
	return nil
}

var binaryDeclaration = regexp.MustCompile(`(?i)^\s*(?:VARBINARY|BINARY)\s*\(\s*([0-9]+)\s*\)\s*$`)

func cloneDataSnapshot(v *contract.RowSnapshot) *contract.RowSnapshot {
	if v == nil {
		return nil
	}
	n := *v
	n.Columns = append([]string(nil), v.Columns...)
	n.Ref.Columns = append([]string(nil), v.Ref.Columns...)
	n.Ref.Values = make([][]byte, len(v.Ref.Values))
	for i, b := range v.Ref.Values {
		n.Ref.Values[i] = bytes.Clone(b)
	}
	n.Ref.TypedValues = make([]contract.Cell, len(v.Ref.TypedValues))
	for i, c := range v.Ref.TypedValues {
		n.Ref.TypedValues[i] = c.Clone()
	}
	n.Original = make([]contract.Cell, len(v.Original))
	for i, c := range v.Original {
		n.Original[i] = c.Clone()
	}
	if v.Fingerprints != nil {
		n.Fingerprints = make([]*string, len(v.Fingerprints))
	}
	for i, p := range v.Fingerprints {
		if p != nil {
			text := *p
			n.Fingerprints[i] = &text
		}
	}
	return &n
}

func cloneDataEdits(edits []contract.DataEdit) ([]contract.DataEdit, error) {
	if len(edits) == 0 || len(edits) > 1000 {
		return nil, connections.ErrInvalid
	}
	budget := dataBatchBytes
	charge := func(c contract.Cell) bool { budget -= 192 + len(c.Text) + 3*len(c.Bytes); return budget >= 0 }
	for _, e := range edits {
		if len(e.Values) > 512 {
			return nil, contract.ErrDataEditLimit
		}
		for n, v := range e.Values {
			if len(n) > 1024 || len(v.Text) > contract.DataEditMaxBytes || len(v.Bytes) > contract.DataEditMaxBytes || !charge(contract.Cell{Text: v.Text, Bytes: v.Bytes}) {
				return nil, contract.ErrDataEditLimit
			}
		}
		if v := e.Original; v != nil {
			if len(v.Columns) > 512 || len(v.Original) > 512 || len(v.Ref.Columns) > 16 || len(v.Ref.TypedValues) > 16 || len(v.Ref.Values) > 16 || len(v.Fingerprints) > 512 || len(v.Metadata) > 128 {
				return nil, connections.ErrInvalid
			}
			for _, name := range append(append([]string{v.Ref.Database, v.Ref.Table, v.Ref.Object.Catalog, v.Ref.Object.Schema, v.Ref.Object.Kind, v.Ref.Object.Name}, v.Columns...), v.Ref.Columns...) {
				if len(name) > 1024 {
					return nil, connections.ErrInvalid
				}
			}
			for _, c := range v.Original {
				if len(c.Text) > contract.DataEditMaxBytes || len(c.Bytes) > contract.DataEditMaxBytes || !charge(c) {
					return nil, contract.ErrDataEditLimit
				}
			}
			for _, c := range v.Ref.TypedValues {
				if !charge(c) || len(c.Text) > 128 || len(c.Bytes) != 0 {
					return nil, connections.ErrInvalid
				}
			}
			for _, b := range v.Ref.Values {
				if len(b) > 8192 {
					return nil, connections.ErrInvalid
				}
				budget -= len(b)
			}
			for _, n := range v.Columns {
				if len(n) > 1024 {
					return nil, connections.ErrInvalid
				}
				budget -= len(n)
			}
			for _, p := range v.Fingerprints {
				if p != nil && len(*p) > 128 {
					return nil, connections.ErrInvalid
				}
			}
		}
		if budget < 0 {
			return nil, contract.ErrDataEditLimit
		}
	}
	out := make([]contract.DataEdit, len(edits))
	for i, e := range edits {
		out[i] = contract.DataEdit{Kind: e.Kind, Original: cloneDataSnapshot(e.Original), Values: make(map[string]contract.DataValue, len(e.Values))}
		for n, v := range e.Values {
			v.Bytes = bytes.Clone(v.Bytes)
			out[i].Values[n] = v
		}
	}
	return out, nil
}

func validateDataSnapshot(s *Session, t *contract.TableStructure, key []string, identity string, v *contract.RowSnapshot) error {
	if v == nil {
		return connections.ErrInvalid
	}
	if v.Mode != "values" || len(v.Fingerprints) != 0 {
		return contract.ErrUnsupported
	}
	if v.Metadata != s.dataSignature(t, contract.DataOptions{}, nil, identity) {
		return contract.ErrDataConflict
	}
	if len(v.Columns) != len(t.Columns) || len(v.Original) != len(t.Columns) {
		return connections.ErrInvalid
	}
	if _, _, err := rowWhere(t, key, v.Ref); err != nil {
		return err
	}
	for i, c := range t.Columns {
		if v.Columns[i] != c.Name {
			return connections.ErrInvalid
		}
		value := v.Original[i]
		if value.Truncated || len(value.Text) > contract.DataEditMaxBytes || len(value.Bytes) > contract.DataEditMaxBytes || !utf8.ValidString(value.Text) {
			return contract.ErrDataEditLimit
		}
		if value.Kind == contract.Null {
			if !c.Nullable || value.Text != "" || len(value.Bytes) != 0 {
				return connections.ErrInvalid
			}
		} else if value.Kind != c.ValueKind || value.Kind == contract.Binary && value.Text != "" || value.Kind != contract.Binary && len(value.Bytes) != 0 {
			return connections.ErrInvalid
		}
		for j, k := range key {
			if k == c.Name && !sameDataCell(value, v.Ref.TypedValues[j]) {
				return connections.ErrInvalid
			}
		}
	}
	return nil
}

func sameDataCell(a, b contract.Cell) bool {
	return a.Kind == b.Kind && a.Text == b.Text && a.Truncated == b.Truncated && bytes.Equal(a.Bytes, b.Bytes)
}

func sameDataRow(a, b []contract.Cell) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameDataCell(a[i], b[i]) {
			return false
		}
	}
	return true
}

func sameDataRef(a, b contract.RowRef) bool {
	return a.Object == b.Object && a.Database == b.Database && a.Table == b.Table && reflect.DeepEqual(a.Columns, b.Columns) && sameDataRow(a.TypedValues, b.TypedValues) && reflect.DeepEqual(a.Values, b.Values)
}

func decimalBounds(text string, c contract.StructureColumn) error {
	if _, err := dataParameter(contract.Cell{Kind: contract.Decimal, Text: text}, contract.Decimal); err != nil {
		return err
	}
	if c.NumericPrecision == nil || c.NumericScale == nil || *c.NumericPrecision < 1 || *c.NumericPrecision > 38 || *c.NumericScale < 0 || *c.NumericScale > *c.NumericPrecision {
		return contract.ErrUnsupported
	}
	text = strings.TrimPrefix(strings.TrimPrefix(text, "-"), "+")
	parts := strings.Split(text, ".")
	fractional := 0
	if len(parts) == 2 {
		fractional = len(parts[1])
	}
	if fractional > int(*c.NumericScale) || len(strings.TrimLeft(parts[0], "0")) > int(*c.NumericPrecision-*c.NumericScale) {
		return connections.ErrInvalid
	}
	return nil
}

func temporalBounds(text string, c contract.StructureColumn) error {
	if strings.ContainsRune(text, ',') {
		return connections.ErrInvalid
	}
	layout := "2006-01-02"
	if c.DataType != "DATE" {
		if c.DatetimePrecision == nil || *c.DatetimePrecision < 0 || *c.DatetimePrecision > 6 {
			return contract.ErrUnsupported
		}
		if at := strings.IndexByte(text, '.'); at >= 0 {
			end := at + 1
			for end < len(text) && text[end] >= '0' && text[end] <= '9' {
				end++
			}
			if end == at+1 || end-at-1 > int(*c.DatetimePrecision) {
				return connections.ErrInvalid
			}
		}
		layout = "15:04:05.999999999"
		if strings.HasPrefix(c.DataType, "TIMESTAMP") {
			layout = "2006-01-02 " + layout
		}
		if strings.Contains(c.DataType, "TIME ZONE") {
			layout += " -07:00"
		}
	}
	v, err := time.Parse(layout, text)
	if err != nil || (c.DataType == "DATE" || strings.HasPrefix(c.DataType, "TIMESTAMP")) && v.Year() < 1 {
		return connections.ErrInvalid
	}
	if strings.Contains(c.DataType, "TIME ZONE") {
		_, offset := v.Zone()
		if offset < -12*3600 || offset > 14*3600 {
			return connections.ErrInvalid
		}
	}
	return nil
}

// Validate storage precision before binding. Server-side defaults and trigger
// transformations are read back; submitted values never pass through a float
// intermediary for integer/decimal editing.
func editParameter(c contract.StructureColumn, v contract.DataValue) (any, error) {
	if len(v.Text) > contract.DataEditMaxBytes || len(v.Bytes) > contract.DataEditMaxBytes {
		return nil, contract.ErrDataEditLimit
	}
	if !utf8.ValidString(v.Text) || v.Default {
		return nil, connections.ErrInvalid
	}
	if v.Null || v.Kind == contract.Null {
		if !c.Nullable || v.Binary || v.Text != "" || len(v.Bytes) != 0 || v.Kind != "" && v.Kind != contract.Null && v.Kind != c.ValueKind {
			return nil, connections.ErrInvalid
		}
		return nil, nil
	}
	if v.Kind != "" && v.Kind != c.ValueKind || v.Binary && c.ValueKind != contract.Binary || c.ValueKind != contract.Binary && len(v.Bytes) != 0 || c.ValueKind == contract.Binary && v.Text != "" {
		return nil, connections.ErrInvalid
	}
	switch c.ValueKind {
	case contract.Integer:
		bits := 64
		switch c.DataType {
		case "TINYINT":
			bits = 8
		case "SMALLINT":
			bits = 16
		case "INT", "INTEGER":
			bits = 32
		}
		n, err := strconv.ParseInt(v.Text, 10, bits)
		if err != nil {
			return nil, connections.ErrInvalid
		}
		return n, nil
	case contract.Decimal:
		if err := decimalBounds(v.Text, c); err != nil {
			return nil, err
		}
		return dpi.Decimal(v.Text), nil
	case contract.Floating:
		n, err := strconv.ParseFloat(v.Text, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || c.DataType == "REAL" && float64(float32(n)) != n {
			return nil, connections.ErrInvalid
		}
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(c.Type)), "FLOAT") && strings.Contains(c.Type, "(") {
			return nil, contract.ErrUnsupported
		}
		return n, nil
	case contract.Boolean:
		if v.Text != "true" && v.Text != "false" {
			return nil, connections.ErrInvalid
		}
		return v.Text == "true", nil
	case contract.DateTime:
		if err := temporalBounds(v.Text, c); err != nil {
			return nil, err
		}
		return v.Text, nil
	case contract.Text:
		if c.DataType == "CHAR" || c.DataType == "VARCHAR" {
			if c.CharacterMaxLength == nil || *c.CharacterMaxLength < 1 {
				return nil, contract.ErrUnsupported
			}
			length := utf8.RuneCountInString(v.Text)
			// The verified native CHAR binding retains a byte-sized fixed
			// storage limit even with LENGTH_IN_CHAR=1. VARCHAR uses characters.
			if c.DataType == "CHAR" {
				length = len(v.Text)
			}
			if length > int(*c.CharacterMaxLength) {
				return nil, connections.ErrInvalid
			}
		}
		return v.Text, nil
	case contract.Binary:
		if c.DataType == "BINARY" || c.DataType == "VARBINARY" {
			parts := binaryDeclaration.FindStringSubmatch(c.Type)
			if parts == nil {
				return nil, contract.ErrUnsupported
			}
			n, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil || n < 1 {
				return nil, contract.ErrUnsupported
			}
			if int64(len(v.Bytes)) > n {
				return nil, connections.ErrInvalid
			}
		}
		return bytes.Clone(v.Bytes), nil
	}
	return nil, contract.ErrUnsupported
}

func defaultValueValid(v contract.DataValue) bool {
	return v.Default && !v.Null && !v.Binary && v.Kind == "" && v.Text == "" && len(v.Bytes) == 0
}

func snapshotRefKey(v *contract.RowSnapshot) string { b, _ := json.Marshal(v.Ref); return string(b) }
