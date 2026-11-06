// Package importspec contains pure import rules shared by the wizard and adapters.
package importspec

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/mysqlspec"
)

func Identifier(engine, name string) error {
	if engine == "" || engine == "mysql" {
		if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 64 || strings.HasSuffix(name, " ") {
			return contract.ErrDataPage
		}
		_, err := mysqlspec.Identifier(name)
		return err
	}
	if name == "" || !utf8.ValidString(name) || strings.ContainsAny(name, "\x00\r\n") || len(name) > 255 || engine == "postgresql" && len(name) > 63 {
		return contract.ErrDataPage
	}
	return nil
}
func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

var pgType = regexp.MustCompile(`^(?:SMALLINT|INTEGER|BIGINT|REAL|DOUBLE PRECISION|BOOLEAN|TEXT|BYTEA|JSONB?|UUID|DATE|TIMESTAMP(?:\([0-6]\))?(?: WITH(?:OUT)? TIME ZONE)?|TIME(?:\([0-6]\))?(?: WITH(?:OUT)? TIME ZONE)?|NUMERIC(?:\([1-9][0-9]{0,3}(?:,[0-9]{1,4})?\))?|VARCHAR\([1-9][0-9]{0,5}\))$`)

func Columns(engine string, cols []contract.DataImportColumn) ([]contract.StructureColumn, error) {
	if engine == "" || engine == "mysql" {
		return mysqlspec.ImportColumnMetadata(cols)
	}
	if engine != "postgresql" && engine != "sqlite" {
		return nil, contract.ErrUnsupported
	}
	if len(cols) == 0 || len(cols) > 128 {
		return nil, contract.ErrDataPage
	}
	seen := map[string]bool{}
	out := make([]contract.StructureColumn, len(cols))
	for i, c := range cols {
		if err := Identifier(engine, c.Name); err != nil {
			return nil, err
		}
		name := c.Name
		if engine == "sqlite" {
			name = strings.ToLower(name)
		}
		if seen[name] {
			return nil, contract.ErrDataPage
		}
		seen[name] = true
		typ := strings.ToUpper(strings.TrimSpace(c.Type))
		if engine == "sqlite" && !slices.Contains([]string{"INTEGER", "REAL", "TEXT", "BLOB", "ANY"}, typ) || engine == "postgresql" && !pgType.MatchString(typ) {
			return nil, fmt.Errorf("%w: unsupported target type %s", contract.ErrDataPage, typ)
		}
		dataType := strings.ToLower(typ)
		if start := strings.IndexByte(dataType, '('); start >= 0 {
			dataType = dataType[:start]
		}
		out[i] = contract.StructureColumn{Name: c.Name, Type: typ, DataType: dataType, Nullable: c.Nullable && !c.PrimaryKey, Position: int64(i + 1)}
		if typ == "BYTEA" || typ == "BLOB" {
			out[i].ValueKind = contract.Binary
		}
		if c.PrimaryKey {
			out[i].Key = "PRI"
		}
	}
	return out, nil
}
func DDL(engine, schema, table string, cols []contract.DataImportColumn) (string, error) {
	if err := Identifier(engine, schema); err != nil {
		return "", err
	}
	if err := Identifier(engine, table); err != nil {
		return "", err
	}
	metadata, err := Columns(engine, cols)
	if err != nil {
		return "", err
	}
	var fields, primary []string
	for _, c := range metadata {
		field := quote(c.Name) + " " + c.Type
		if !c.Nullable {
			field += " NOT NULL"
		}
		fields = append(fields, field)
		if c.Key == "PRI" {
			primary = append(primary, quote(c.Name))
		}
	}
	if len(primary) > 0 {
		fields = append(fields, "PRIMARY KEY ("+strings.Join(primary, ",")+")")
	}
	return "CREATE TABLE " + quote(schema) + "." + quote(table) + " (" + strings.Join(fields, ",") + ")", nil
}

// AdaptInference keeps the existing format readers' whole-file inference while
// mapping its suggestions to types with lossless storage in the target engine.
func AdaptInference(engine string, cols []contract.DataImportColumn) []contract.DataImportColumn {
	out := slices.Clone(cols)
	for i, c := range out {
		t := strings.ToUpper(c.Type)
		switch engine {
		case "sqlite":
			switch {
			case strings.Contains(t, "BLOB") || strings.Contains(t, "BINARY"):
				t = "BLOB"
			case t == "BIGINT" || t == "INT":
				t = "INTEGER"
			default:
				t = "TEXT"
			}
		case "postgresql":
			switch {
			case strings.Contains(t, "BLOB") || strings.Contains(t, "BINARY"):
				t = "BYTEA"
			case strings.HasPrefix(t, "DECIMAL"):
				t = "NUMERIC"
			case t == "DATETIME":
				t = "TIMESTAMP"
			case strings.Contains(t, "TEXT"):
				t = "TEXT"
			}
		}
		out[i].Type = t
	}
	return out
}
func NeedsKey(mode string) bool {
	return slices.Contains([]string{"update", "upsert", "insert_missing", "delete"}, mode)
}
func Own(r contract.DataImportRequest) (contract.DataImportRequest, error) {
	if r.Mode == "" {
		r.Mode = "append"
	}
	if !slices.Contains([]string{"append", "replace", "update", "upsert", "insert_missing", "delete"}, r.Mode) || r.NewTable && r.Mode != "append" || !r.NewTable && len(r.Columns) > 0 || len(r.MatchColumns) > 128 || !NeedsKey(r.Mode) && len(r.MatchColumns) > 0 {
		return r, contract.ErrDataPage
	}
	r.Columns = slices.Clone(r.Columns)
	r.MatchColumns = slices.Clone(r.MatchColumns)
	if len(r.Edits) > contract.MaxDataImportRows {
		return r, contract.ErrDataEditLimit
	}
	edits := make([]contract.DataEdit, len(r.Edits))
	total := 0
	for i, e := range r.Edits {
		if e.Kind != "insert" || e.Original != nil || len(e.Values) == 0 || len(e.Values) > 128 {
			return r, contract.ErrDataPage
		}
		edits[i] = contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{}}
		for k, v := range e.Values {
			v.Null = v.Null || v.Kind == contract.Null
			v.Binary = v.Binary || v.Kind == contract.Binary
			total += len(v.Text) + len(v.Bytes)
			if total > contract.MaxDataImportBytes {
				return r, contract.ErrDataEditLimit
			}
			v.Bytes = bytes.Clone(v.Bytes)
			edits[i].Values[k] = v
		}
	}
	r.Edits = edits
	return r, nil
}
func Validate(engine string, metadata []contract.StructureColumn, r contract.DataImportRequest) error {
	if engine == "sqlserver" && (r.NewTable || r.ContinueOnError || r.Mode != "" && r.Mode != "append") {
		return contract.ErrUnsupported
	}
	if engine == "" || engine == "mysql" {
		return mysqlspec.ValidateImportRequest(metadata, r)
	}
	r, err := Own(r)
	if err != nil {
		return err
	}
	if r.NewTable {
		metadata, err = Columns(engine, r.Columns)
		if err != nil {
			return err
		}
	}
	allowed := map[string]contract.StructureColumn{}
	for _, c := range metadata {
		allowed[c.Name] = c
	}
	if len(allowed) == 0 {
		return contract.ErrDataPage
	}
	if NeedsKey(r.Mode) {
		seen := map[string]bool{}
		if len(r.MatchColumns) == 0 {
			return contract.ErrDataReadOnly
		}
		for _, k := range r.MatchColumns {
			if _, ok := allowed[k]; !ok || seen[k] {
				return contract.ErrDataReadOnly
			}
			seen[k] = true
		}
	}
	for _, e := range r.Edits {
		for k, v := range e.Values {
			c, ok := allowed[k]
			if !ok || contract.DataColumnGenerated(c) || v.Default || v.Null && v.Binary || v.Binary && v.Text != "" || !v.Binary && len(v.Bytes) > 0 || v.Null && (v.Text != "" || len(v.Bytes) > 0) || !v.Binary && !utf8.ValidString(v.Text) {
				return contract.ErrDataPage
			}
			if v.Null && !c.Nullable && !r.ContinueOnError {
				return contract.ErrDataPage
			}
		}
		for _, k := range r.MatchColumns {
			v, ok := e.Values[k]
			if !ok || v.Null {
				return contract.ErrDataReadOnly
			}
		}
	}
	return nil
}
