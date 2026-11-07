// Package sqlexport serializes complete typed values as explicit SQL text.
package sqlexport

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// DamengTable owns its qualified target and column metadata. Its SQL has no
// COMMIT, reconnect or replay commands. Large exports retain complete values;
// a consumer's separate SQL statement/file limits still apply to execution.
type DamengTable struct {
	object  contract.ObjectRef
	columns []contract.StructureColumn
}

func NewDamengTable(object contract.ObjectRef, columns []contract.StructureColumn) (*DamengTable, error) {
	if object.ProfileID <= 0 || object.Kind != "table" || !damengName(object.Catalog) || !damengName(object.Schema) || !damengName(object.Name) || len(columns) == 0 || len(columns) > 128 {
		return nil, contract.ErrDataPage
	}
	t := &DamengTable{object: object, columns: make([]contract.StructureColumn, len(columns))}
	seen := map[string]bool{}
	for i, c := range columns {
		if !damengName(c.Name) || seen[c.Name] {
			return nil, contract.ErrDataPage
		}
		seen[c.Name] = true
		c.CharacterMaxLength = ownInt(c.CharacterMaxLength)
		c.NumericPrecision, c.NumericScale = ownInt(c.NumericPrecision), ownInt(c.NumericScale)
		c.DatetimePrecision = ownInt(c.DatetimePrecision)
		c.Default, c.CharacterSet, c.Collation = ownString(c.Default), ownString(c.CharacterSet), ownString(c.Collation)
		// Computed columns are omitted by INSERT/copy. IDENTITY is an explicit
		// stored key, whose original value must survive a data export.
		if !contract.DataColumnGenerated(c) || c.Extra == "IDENTITY" {
			if _, err := damengKind(c); err != nil {
				return nil, err
			}
		}
		t.columns[i] = c
	}
	return t, nil
}

func ownInt(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func ownString(p *string) *string {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func damengName(s string) bool {
	return s != "" && len(s) <= 128 && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}

func damengQuote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func (t *DamengTable) target() string {
	return damengQuote(t.object.Schema) + "." + damengQuote(t.object.Name)
}

func damengKind(c contract.StructureColumn) (contract.CellKind, error) {
	var kind contract.CellKind
	switch c.DataType {
	case "TINYINT", "SMALLINT", "INT", "INTEGER", "BIGINT":
		kind = contract.Integer
	case "DECIMAL", "NUMERIC", "NUMBER", "DEC":
		kind = contract.Decimal
	case "REAL", "FLOAT", "DOUBLE":
		kind = contract.Floating
	case "BIT", "BOOLEAN":
		kind = contract.Boolean
	case "CHAR", "VARCHAR", "CLOB", "TEXT":
		kind = contract.Text
	case "BINARY", "VARBINARY", "BLOB", "IMAGE":
		kind = contract.Binary
	case "DATE", "TIME", "TIME WITH TIME ZONE", "TIMESTAMP", "TIMESTAMP WITH TIME ZONE":
		kind = contract.DateTime
	default:
		return "", contract.ErrUnsupported
	}
	if c.ValueKind != kind {
		return "", contract.ErrDataPage
	}
	return kind, nil
}

var damengNumber = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?$`)
var damengBinaryType = regexp.MustCompile(`^(BINARY|VARBINARY)\(([0-9]+)\)$`)

func damengLiteral(c contract.StructureColumn, v contract.DataValue) (string, error) {
	if len(v.Text)+len(v.Bytes) > contract.DataExportFieldMaxBytes {
		return "", contract.ErrDataEditLimit
	}
	if v.Default {
		if v.Null || v.Binary || v.Kind != "" || v.Text != "" || len(v.Bytes) != 0 {
			return "", contract.ErrDataPage
		}
		return "DEFAULT", nil
	}
	kind, err := damengKind(c)
	if err != nil {
		return "", err
	}
	if v.Null || v.Kind == contract.Null {
		if v.Binary || v.Text != "" || len(v.Bytes) != 0 || v.Kind != "" && v.Kind != contract.Null && v.Kind != kind {
			return "", contract.ErrDataPage
		}
		return "NULL", nil
	}
	if v.Kind != "" && v.Kind != kind || v.Binary && kind != contract.Binary || kind != contract.Binary && len(v.Bytes) != 0 || kind == contract.Binary && v.Text != "" || !utf8.ValidString(v.Text) {
		return "", contract.ErrDataPage
	}
	switch kind {
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
		if _, err := strconv.ParseInt(v.Text, 10, bits); err != nil {
			return "", contract.ErrDataPage
		}
		return v.Text, nil
	case contract.Decimal:
		if !damengNumber.MatchString(v.Text) || c.NumericPrecision == nil || c.NumericScale == nil || *c.NumericPrecision < 1 || *c.NumericPrecision > 38 || *c.NumericScale < 0 || *c.NumericScale > *c.NumericPrecision {
			return "", contract.ErrDataPage
		}
		parts := strings.Split(strings.TrimLeft(v.Text, "+-"), ".")
		if len(strings.TrimLeft(parts[0], "0")) > int(*c.NumericPrecision-*c.NumericScale) || len(parts) == 2 && len(parts[1]) > int(*c.NumericScale) {
			return "", contract.ErrDataPage
		}
		return fmt.Sprintf("CAST('%s' AS DECIMAL(%d,%d))", v.Text, *c.NumericPrecision, *c.NumericScale), nil
	case contract.Floating:
		n, err := strconv.ParseFloat(v.Text, 64)
		if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
			return "", contract.ErrDataPage
		}
		return "CAST('" + strconv.FormatFloat(n, 'g', -1, 64) + "' AS " + c.DataType + ")", nil
	case contract.Boolean:
		switch v.Text {
		case "true":
			return "1", nil
		case "false":
			return "0", nil
		}
		return "", contract.ErrDataPage
	case contract.Binary:
		if len(v.Bytes) > 0 {
			// A native 0x constant can be assigned directly to a full BLOB.
			// HEXTORAW has a scalar input limit and maps empty input to NULL.
			return "0x" + hex.EncodeToString(v.Bytes), nil
		}
		if c.DataType == "BLOB" || c.DataType == "IMAGE" {
			return "EMPTY_BLOB()", nil
		}
		parts := damengBinaryType.FindStringSubmatch(c.Type)
		if parts == nil || parts[1] != c.DataType {
			return "", contract.ErrDataPage
		}
		n, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || n < 1 || n > 8000 {
			return "", contract.ErrDataPage
		}
		return fmt.Sprintf("CAST('' AS %s(%d))", c.DataType, n), nil
	case contract.Text:
		lob := c.DataType == "CLOB" || c.DataType == "TEXT"
		if v.Text == "" && lob {
			return "EMPTY_CLOB()", nil
		}
		return damengTextLiteral(v.Text, lob), nil
	case contract.DateTime:
		layout, target := "2006-01-02", "DATE"
		if c.DataType != "DATE" {
			if c.DatetimePrecision == nil || *c.DatetimePrecision < 0 || *c.DatetimePrecision > 6 {
				return "", contract.ErrDataPage
			}
			layout, target = "15:04:05.999999", fmt.Sprintf("TIME(%d)", *c.DatetimePrecision)
			if strings.HasPrefix(c.DataType, "TIMESTAMP") {
				layout, target = "2006-01-02 "+layout, fmt.Sprintf("TIMESTAMP(%d)", *c.DatetimePrecision)
			}
			if strings.Contains(c.DataType, "TIME ZONE") {
				layout, target = layout+" -07:00", target+" WITH TIME ZONE"
			}
		}
		parsed, err := time.Parse(layout, v.Text)
		if err != nil || strings.ContainsAny(v.Text, "',") || (c.DataType == "DATE" || strings.HasPrefix(c.DataType, "TIMESTAMP")) && parsed.Year() < 1 {
			return "", contract.ErrDataPage
		}
		if at := strings.IndexByte(v.Text, '.'); at >= 0 && c.DatetimePrecision != nil {
			end := at + 1
			for end < len(v.Text) && v.Text[end] >= '0' && v.Text[end] <= '9' {
				end++
			}
			if end-at-1 > int(*c.DatetimePrecision) {
				return "", contract.ErrDataPage
			}
		}
		// Native typed literals parse ISO values independently of the target
		// session's NLS formats; a string CAST alone does not.
		keyword := "TIME"
		if c.DataType == "DATE" {
			keyword = "DATE"
		} else if strings.HasPrefix(c.DataType, "TIMESTAMP") {
			keyword = "TIMESTAMP"
		}
		return "CAST(" + keyword + " '" + v.Text + "' AS " + target + ")", nil
	}
	return "", contract.ErrUnsupported
}

// Keep data CR/LF/NUL out of script lines. disql normalizes literal line breaks;
// a distinct absent Unicode marker permits bounded REPLACE expressions even
// for a field made entirely of control characters, without a million joins.
func damengTextLiteral(text string, lob bool) string {
	type replacement struct {
		marker string
		code   int
	}
	var replacements []replacement
	if strings.ContainsAny(text, "\x00\r\n") {
		used := map[rune]bool{}
		for _, r := range text {
			used[r] = true
		}
		next := rune(0xe000)
		for _, code := range []int{0, 13, 10} {
			if !strings.ContainsRune(text, rune(code)) {
				continue
			}
			for used[next] {
				next++
			}
			marker := string(next)
			next++
			text = strings.ReplaceAll(text, string(rune(code)), marker)
			replacements = append(replacements, replacement{marker, code})
		}
	}
	literal := "'" + strings.ReplaceAll(text, "'", "''") + "'"
	if lob {
		literal = "CAST(" + literal + " AS CLOB)"
	}
	for _, r := range replacements {
		literal = fmt.Sprintf("REPLACE(%s,'%s',CHR(%d))", literal, r.marker, r.code)
	}
	return "(" + literal + ")"
}

func (t *DamengTable) rowSize(values []contract.DataValue) error {
	if len(values) != len(t.columns) {
		return contract.ErrDataPage
	}
	total := 0
	for _, v := range values {
		total += len(v.Text) + len(v.Bytes)
		if len(v.Text)+len(v.Bytes) > contract.DataExportFieldMaxBytes || total > contract.DataExportRowMaxBytes {
			return contract.ErrDataEditLimit
		}
	}
	return nil
}

func (t *DamengTable) Insert(values []contract.DataValue) (string, error) {
	if err := t.rowSize(values); err != nil {
		return "", err
	}
	var names, literals []string
	identity := false
	for i, c := range t.columns {
		if contract.DataColumnGenerated(c) && c.Extra != "IDENTITY" {
			continue
		}
		literal, err := damengLiteral(c, values[i])
		if err != nil {
			return "", err
		}
		if c.Extra == "IDENTITY" && literal == "DEFAULT" {
			// Native DM8 rejects an explicit DEFAULT for IDENTITY; omit it.
			continue
		}
		names, literals = append(names, damengQuote(c.Name)), append(literals, literal)
		identity = identity || c.Extra == "IDENTITY"
	}
	if len(names) == 0 {
		return "", contract.ErrUnsupported
	}
	statement := "INSERT INTO " + t.target() + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(literals, ", ") + ");"
	if identity {
		// This is explicit exported SQL. Restore OFF on success and failure;
		// no implicit COMMIT is added to the caller's transaction.
		setting := "SET IDENTITY_INSERT " + t.target()
		statement = "BEGIN\n  " + setting + " ON;\n  " + statement + "\n  " + setting + " OFF;\nEXCEPTION WHEN OTHERS THEN\n  " + setting + " OFF;\n  RAISE;\nEND;\n/"
	}
	return statement + "\n", nil
}

func (t *DamengTable) Update(values []contract.DataValue, ref contract.RowRef) (string, error) {
	if err := t.rowSize(values); err != nil {
		return "", err
	}
	if ref.Object != t.object || ref.Database != t.object.Schema || ref.Table != t.object.Name || len(ref.Columns) == 0 || len(ref.Columns) != len(ref.TypedValues) || len(ref.Columns) != len(ref.Values) {
		return "", contract.ErrDataPage
	}
	keys := map[string]contract.StructureColumn{}
	var assignments []string
	for i, c := range t.columns {
		if c.Key == "PRI" {
			if c.Nullable || c.ValueKind != contract.Integer && c.ValueKind != contract.Decimal || contract.DataColumnGenerated(c) && c.Extra != "IDENTITY" {
				return "", contract.ErrUnsupported
			}
			keys[c.Name] = c
			continue
		}
		if contract.DataColumnGenerated(c) || c.Extra == "IDENTITY" {
			continue
		}
		literal, err := damengLiteral(c, values[i])
		if err != nil {
			return "", err
		}
		assignments = append(assignments, damengQuote(c.Name)+" = "+literal)
	}
	if len(keys) != len(ref.Columns) || len(assignments) == 0 {
		return "", contract.ErrDataReadOnly
	}
	var predicates []string
	for i, name := range ref.Columns {
		c, ok := keys[name]
		cell := ref.TypedValues[i]
		raw, _ := json.Marshal(cell)
		if !ok || cell.Kind != c.ValueKind || cell.Truncated || len(cell.Bytes) != 0 || !bytes.Equal(raw, ref.Values[i]) {
			return "", contract.ErrDataPage
		}
		delete(keys, name)
		literal, err := damengLiteral(c, contract.DataValue{Kind: cell.Kind, Text: cell.Text})
		if err != nil {
			return "", err
		}
		predicates = append(predicates, damengQuote(name)+" = "+literal)
	}
	return "UPDATE " + t.target() + " SET " + strings.Join(assignments, ", ") + " WHERE " + strings.Join(predicates, " AND ") + ";\n", nil
}
