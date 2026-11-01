// Package mysqlspec shares pure MySQL import review rules with its adapter.
package mysqlspec

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"math"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var mysqlDataImportTypePattern = regexp.MustCompile(`^([A-Z]+)\s*(?:\(\s*([0-9]+)\s*(?:,\s*([0-9]+)\s*)?\))?$`)
var mysqlDataImportNumberPattern = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
var mysqlDataImportTimePattern = regexp.MustCompile(`^[+-]?([0-9]{1,3}):([0-9]{2}):([0-9]{2})(?:\.([0-9]{1,6}))?$`)

func ImportInvalid(reason string) error {
	return fmt.Errorf("%w: invalid new-table import: %s", contract.ErrDataPage, reason)
}

func ImportColumnType(col contract.DataImportColumn, position int) (contract.StructureColumn, error) {
	meta := contract.StructureColumn{Name: col.Name, Nullable: col.Nullable, Position: int64(position)}
	parts := mysqlDataImportTypePattern.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(col.Type)))
	if parts == nil {
		return meta, ImportInvalid("unsupported field type")
	}
	kind := parts[1]
	first, second := -1, -1
	var err error
	if parts[2] != "" {
		first, err = strconv.Atoi(parts[2])
		if err != nil {
			return meta, ImportInvalid("field type parameter")
		}
	}
	if parts[3] != "" {
		second, err = strconv.Atoi(parts[3])
		if err != nil {
			return meta, ImportInvalid("field type parameter")
		}
	}
	meta.Type, meta.DataType = kind, strings.ToLower(kind)
	length := func(n int) { value := int64(n); meta.CharacterMaxLength = &value }
	precision := func(p, s int) { a, b := int64(p), int64(s); meta.NumericPrecision, meta.NumericScale = &a, &b }
	noParameters := func() bool { return first == -1 && second == -1 }
	switch kind {
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT", "BOOLEAN", "BOOL":
		if !noParameters() {
			return meta, ImportInvalid("integer field parameter")
		}
		if kind == "INTEGER" {
			meta.DataType = "int"
		}
		if kind == "BOOLEAN" || kind == "BOOL" {
			meta.DataType = "tinyint"
		}
	case "DECIMAL", "NUMERIC":
		if first < 1 || first > 65 || second < 0 || second > 30 || second > first {
			return meta, ImportInvalid("DECIMAL precision or scale")
		}
		meta.Type, meta.DataType = fmt.Sprintf("DECIMAL(%d,%d)", first, second), "decimal"
		precision(first, second)
	case "DOUBLE", "FLOAT":
		if !noParameters() {
			return meta, ImportInvalid("floating field parameter")
		}
	case "VARCHAR", "CHAR", "VARBINARY", "BINARY":
		maximum := 255
		if kind == "VARCHAR" {
			maximum = 16383
		}
		if kind == "VARBINARY" {
			maximum = 65535
		}
		if first < 1 || first > maximum || second != -1 {
			return meta, ImportInvalid("string field length")
		}
		meta.Type = fmt.Sprintf("%s(%d)", kind, first)
		length(first)
	case "TEXT", "MEDIUMTEXT", "LONGTEXT", "BLOB", "MEDIUMBLOB", "LONGBLOB":
		if !noParameters() || col.PrimaryKey {
			return meta, ImportInvalid("large field type or primary key")
		}
	case "DATE", "JSON":
		if !noParameters() || kind == "JSON" && col.PrimaryKey {
			return meta, ImportInvalid("field type or primary key")
		}
	case "DATETIME", "TIMESTAMP", "TIME":
		if second != -1 || first > 6 {
			return meta, ImportInvalid("temporal field precision")
		}
		if first == -1 {
			first = 0
		} else {
			meta.Type = fmt.Sprintf("%s(%d)", kind, first)
		}
		value := int64(first)
		meta.DatetimePrecision = &value
	default:
		return meta, ImportInvalid("unsupported field type")
	}
	if col.PrimaryKey {
		if col.Nullable {
			return meta, ImportInvalid("primary key allows NULL")
		}
		meta.Key = "PRI"
	}
	if strings.Contains(meta.DataType, "char") || strings.Contains(meta.DataType, "text") {
		charset, collation := "utf8mb4", "utf8mb4_0900_ai_ci"
		meta.CharacterSet, meta.Collation = &charset, &collation
	}
	return meta, nil
}

func ImportColumnMetadata(columns []contract.DataImportColumn) ([]contract.StructureColumn, error) {
	if len(columns) == 0 || len(columns) > 128 {
		return nil, ImportInvalid("field count")
	}
	metadata := make([]contract.StructureColumn, len(columns))
	seen := make(map[string]bool, len(columns))
	keyBytes := int64(0)
	for i, col := range columns {
		if _, err := Identifier(col.Name); err != nil || !utf8.ValidString(col.Name) || strings.HasSuffix(col.Name, " ") || utf8.RuneCountInString(col.Name) > 64 {
			return nil, ImportInvalid("field name")
		}
		fold := strings.ToLower(col.Name)
		if seen[fold] {
			return nil, ImportInvalid("duplicate field name")
		}
		seen[fold] = true
		meta, err := ImportColumnType(col, i+1)
		if err != nil {
			return nil, err
		}
		metadata[i] = meta
		if col.PrimaryKey {
			size := int64(16)
			if meta.CharacterMaxLength != nil {
				size = *meta.CharacterMaxLength
				if meta.CharacterSet != nil {
					size *= 4
				}
			}
			if meta.NumericPrecision != nil {
				size = *meta.NumericPrecision/2 + 1
			}
			keyBytes += size
		}
	}
	if keyBytes > 3072 {
		return nil, ImportInvalid("primary key exceeds InnoDB byte limit")
	}
	return metadata, nil
}

func ValidateImportColumns(columns []contract.DataImportColumn) error {
	_, err := ImportColumnMetadata(columns)
	return err
}

func ValidateNewTableImport(columns []contract.DataImportColumn, edits []contract.DataEdit) error {
	metadata, err := ImportColumnMetadata(columns)
	if err != nil {
		return err
	}
	_, err = ValidatedImportEdits(metadata, edits)
	return err
}

func DecimalFits(text string, precision, scale int64) bool {
	// Bound lexical work before big.Rat handles exponent notation.
	if len(text) > 256 || !mysqlDataImportNumberPattern.MatchString(text) {
		return false
	}
	if i := strings.IndexAny(text, "eE"); i >= 0 {
		exponent, err := strconv.Atoi(text[i+1:])
		if err != nil || exponent < -100 || exponent > 100 {
			return false
		}
	}
	value, ok := new(big.Rat).SetString(text)
	if !ok {
		return false
	}
	unit := new(big.Int).Exp(big.NewInt(10), big.NewInt(scale), nil)
	scaled := new(big.Rat).Mul(value, new(big.Rat).SetInt(unit))
	if !scaled.IsInt() {
		return false
	}
	maximum := new(big.Int).Exp(big.NewInt(10), big.NewInt(precision), nil)
	return new(big.Int).Abs(scaled.Num()).Cmp(maximum) < 0
}

func ImportValueValid(col contract.StructureColumn, value contract.DataValue) bool {
	if value.Null {
		return true
	}
	binary := BinaryType(strings.ToLower(col.DataType))
	if binary != value.Binary {
		return false
	}
	if binary {
		if col.CharacterMaxLength != nil && int64(len(value.Bytes)) > *col.CharacterMaxLength {
			return false
		}
		switch col.DataType {
		case "tinyblob":
			return len(value.Bytes) <= 255
		case "blob":
			return len(value.Bytes) <= 65535
		}
		return true
	}
	if col.CharacterMaxLength != nil && int64(utf8.RuneCountInString(value.Text)) > *col.CharacterMaxLength {
		return false
	}
	switch col.DataType {
	case "tinyint", "smallint", "mediumint", "int", "bigint":
		bits := map[string]int{"tinyint": 8, "smallint": 16, "mediumint": 24, "int": 32, "bigint": 64}[col.DataType]
		if strings.Contains(strings.ToLower(col.Type), "unsigned") {
			_, err := strconv.ParseUint(value.Text, 10, bits)
			return err == nil
		}
		_, err := strconv.ParseInt(value.Text, 10, bits)
		return err == nil
	case "decimal":
		if col.NumericPrecision == nil || col.NumericScale == nil || !DecimalFits(value.Text, *col.NumericPrecision, *col.NumericScale) {
			return false
		}
		if strings.Contains(strings.ToLower(col.Type), "unsigned") {
			number, ok := new(big.Rat).SetString(value.Text)
			return ok && number.Sign() >= 0
		}
		return true
	case "double", "float":
		if len(value.Text) > 256 || !mysqlDataImportNumberPattern.MatchString(value.Text) {
			return false
		}
		bits := 64
		if col.DataType == "float" {
			bits = 32
		}
		n, err := strconv.ParseFloat(value.Text, bits)
		return err == nil && !math.IsInf(n, 0) && !math.IsNaN(n)
	case "json":
		return json.Valid([]byte(value.Text))
	case "text":
		return len(value.Text) <= 65535
	case "tinytext":
		return len(value.Text) <= 255
	case "date":
		parsed, err := time.Parse("2006-01-02", value.Text)
		return err == nil && parsed.Year() >= 1000
	case "datetime", "timestamp":
		fraction := 0
		if dot := strings.IndexByte(value.Text, '.'); dot >= 0 {
			fraction = len(value.Text) - dot - 1
		}
		parsed, err := time.Parse("2006-01-02 15:04:05.999999999", value.Text)
		precision := int64(0)
		if col.DatetimePrecision != nil {
			precision = *col.DatetimePrecision
		}
		return err == nil && parsed.Year() >= 1000 && int64(fraction) <= precision
	case "time":
		parts := mysqlDataImportTimePattern.FindStringSubmatch(value.Text)
		if parts == nil {
			return false
		}
		h, _ := strconv.Atoi(parts[1])
		m, _ := strconv.Atoi(parts[2])
		s, _ := strconv.Atoi(parts[3])
		precision := int64(0)
		if col.DatetimePrecision != nil {
			precision = *col.DatetimePrecision
		}
		return h <= 838 && m < 60 && s < 60 && int64(len(parts[4])) <= precision
	}
	return true
}

func ValidatedImportEdits(metadata []contract.StructureColumn, edits []contract.DataEdit) ([]contract.DataEdit, error) {
	if len(edits) == 0 || len(edits) > contract.DataMaxPageSize {
		return nil, contract.ErrDataPage
	}
	byName := make(map[string]contract.StructureColumn, len(metadata))
	for _, col := range metadata {
		byName[col.Name] = col
	}
	copyEdits := make([]contract.DataEdit, len(edits))
	total := 0
	for i, edit := range edits {
		if edit.Kind != "insert" || edit.Original != nil || len(edit.Values) == 0 {
			return nil, contract.ErrDataPage
		}
		copyEdits[i] = contract.DataEdit{Kind: "insert", Values: make(map[string]contract.DataValue, len(edit.Values))}
		for name, value := range edit.Values {
			col, ok := byName[name]
			if !ok || value.Default || value.Null && value.Binary || !ImportValueValid(col, value) {
				return nil, fmt.Errorf("%w: row %d field %s", contract.ErrDataPage, i+1, name)
			}
			total += len(value.Text) + len(value.Bytes)
			if total > contract.DataEditMaxBytes {
				return nil, contract.ErrDataEditLimit
			}
			value.Bytes = bytes.Clone(value.Bytes)
			// The MySQL driver treats a nil []byte as SQL NULL. Binary=true
			// denotes an explicit binary value, including zero bytes.
			if value.Binary && value.Bytes == nil {
				value.Bytes = []byte{}
			}
			copyEdits[i].Values[name] = value
		}
		for _, col := range metadata {
			if !col.Nullable {
				if _, ok := edit.Values[col.Name]; !ok {
					return nil, fmt.Errorf("%w: missing field %s", contract.ErrDataPage, col.Name)
				}
			}
		}
		if _, _, _, err := AssignmentsBounded(metadata, copyEdits[i].Values, true, contract.DataEditMaxBytes); err != nil {
			return nil, err
		}
	}
	return copyEdits, nil
}

func OwnedImportRequest(request contract.DataImportRequest) (contract.DataImportRequest, error) {
	if request.Mode == "" {
		request.Mode = "append"
	}
	if request.Mode != "append" && request.Mode != "update" && request.Mode != "upsert" && request.Mode != "insert_missing" && request.Mode != "delete" && request.Mode != "replace" || request.NewTable && request.Mode != "append" || !request.NewTable && len(request.Columns) != 0 {
		return request, contract.ErrDataPage
	}
	if len(request.Edits) == 0 || len(request.Edits) > contract.MaxDataImportRows || len(request.MatchColumns) > 16 || (request.Mode == "append" || request.Mode == "replace") && len(request.MatchColumns) != 0 {
		return request, contract.ErrDataPage
	}
	request.Columns = append([]contract.DataImportColumn(nil), request.Columns...)
	request.MatchColumns = append([]string(nil), request.MatchColumns...)
	owned := make([]contract.DataEdit, len(request.Edits))
	total := 0
	for i, edit := range request.Edits {
		if edit.Kind != "insert" || edit.Original != nil || len(edit.Values) == 0 || len(edit.Values) > 128 {
			return request, contract.ErrDataPage
		}
		owned[i] = contract.DataEdit{Kind: "insert", Values: make(map[string]contract.DataValue, len(edit.Values))}
		for name, value := range edit.Values {
			if _, err := Identifier(name); err != nil || value.Default {
				return request, contract.ErrDataPage
			}
			total += len(value.Text) + len(value.Bytes)
			if total > contract.MaxDataImportBytes {
				return request, contract.ErrDataEditLimit
			}
			value.Bytes = bytes.Clone(value.Bytes)
			if value.Binary && value.Bytes == nil {
				value.Bytes = []byte{}
			}
			owned[i].Values[name] = value
		}
	}
	request.Edits = owned
	return request, nil
}

func ImportNeedsKey(mode string) bool {
	return mode == "update" || mode == "upsert" || mode == "insert_missing" || mode == "delete"
}

func ValidateImportRequest(metadata []contract.StructureColumn, request contract.DataImportRequest) error {
	request, err := OwnedImportRequest(request)
	if err != nil {
		return err
	}
	if request.NewTable {
		metadata, err = ImportColumnMetadata(request.Columns)
		if err != nil {
			return err
		}
	}
	if len(metadata) == 0 || len(metadata) > 128 {
		return contract.ErrDataPage
	}
	allowed := make(map[string]contract.StructureColumn, len(metadata))
	for _, col := range metadata {
		allowed[col.Name] = col
	}
	if ImportNeedsKey(request.Mode) {
		if _, err = ImportMatchKey(request.MatchColumns, [][]string{request.MatchColumns}); err != nil {
			return err
		}
	}
	for _, edit := range request.Edits {
		for name := range edit.Values {
			col, ok := allowed[name]
			if !ok || contract.DataColumnGenerated(col) {
				return contract.ErrDataPage
			}
		}
		if ImportNeedsKey(request.Mode) {
			if _, err = ImportRowRef("db", "table", request.MatchColumns, edit); err != nil {
				return err
			}
			for _, name := range request.MatchColumns {
				if !ImportValueValid(allowed[name], edit.Values[name]) {
					return contract.ErrDataPage
				}
			}
		}
		if request.Mode == "delete" {
			continue
		}
		if err = ValidateImportRow(metadata, edit, request.Mode == "append" || request.Mode == "replace"); err != nil && !request.ContinueOnError {
			return err
		}
	}
	return nil
}

func ValidateImportRow(columns []contract.StructureColumn, edit contract.DataEdit, insert bool) error {
	byName := make(map[string]contract.StructureColumn, len(columns))
	for _, col := range columns {
		byName[col.Name] = col
		if insert && !col.Nullable && col.Default == nil && !contract.DataColumnGenerated(col) && !strings.Contains(strings.ToLower(col.Extra), "auto_increment") {
			if _, ok := edit.Values[col.Name]; !ok {
				return contract.ErrDataPage
			}
		}
	}
	for name, value := range edit.Values {
		col, ok := byName[name]
		if !ok || value.Default || value.Null && value.Binary || !ImportValueValid(col, value) {
			return contract.ErrDataPage
		}
	}
	_, _, _, err := AssignmentsBounded(columns, edit.Values, insert, contract.MaxDataImportBytes)
	return err
}

func ImportMatchKey(chosen []string, keys [][]string) ([]string, error) {
	if len(chosen) == 0 {
		return nil, contract.ErrDataReadOnly
	}
	seen := make(map[string]bool, len(chosen))
	for _, name := range chosen {
		if _, err := Identifier(name); err != nil || seen[name] {
			return nil, contract.ErrDataReadOnly
		}
		seen[name] = true
	}
	for _, key := range keys {
		if len(key) != len(chosen) {
			continue
		}
		matches := true
		for _, name := range key {
			matches = matches && seen[name]
		}
		if matches {
			return append([]string(nil), key...), nil
		}
	}
	return nil, contract.ErrDataReadOnly
}

func ImportRowRef(database, table string, key []string, edit contract.DataEdit) (contract.RowRef, error) {
	ref := contract.RowRef{Database: database, Table: table, Columns: append([]string(nil), key...)}
	for _, name := range key {
		value, ok := edit.Values[name]
		if !ok || value.Null || value.Default {
			return ref, contract.ErrDataReadOnly
		}
		if value.Binary {
			ref.Values = append(ref.Values, append([]byte{}, value.Bytes...))
		} else {
			ref.Values = append(ref.Values, []byte(value.Text))
		}
	}
	_, _, err := RowPredicate(ref, database, table)
	return ref, err
}

func AssignmentsBounded(columns []contract.StructureColumn, values map[string]contract.DataValue, insert bool, limit int) ([]string, []string, []any, error) {
	allowed := map[string]contract.StructureColumn{}
	for _, col := range columns {
		allowed[col.Name] = col
	}
	keys := make([]string, 0, len(values))
	for name := range values {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	names := make([]string, 0, len(keys))
	expressions := make([]string, 0, len(keys))
	var args []any
	total := 0
	for _, name := range keys {
		col, ok := allowed[name]
		if !ok || contract.DataColumnGenerated(col) {
			return nil, nil, nil, contract.ErrDataPage
		}
		v := values[name]
		if (v.Null && v.Default) || (v.Binary && v.Text != "") || (!v.Binary && len(v.Bytes) > 0) {
			return nil, nil, nil, contract.ErrDataPage
		}
		if v.Null && !col.Nullable {
			return nil, nil, nil, contract.ErrDataPage
		}
		if (v.Null || v.Default) && (v.Text != "" || len(v.Bytes) > 0) {
			return nil, nil, nil, contract.ErrDataPage
		}
		total += len(v.Text) + len(v.Bytes)
		if total > limit {
			return nil, nil, nil, contract.ErrDataEditLimit
		}
		if !v.Binary && !utf8.ValidString(v.Text) {
			return nil, nil, nil, contract.ErrDataPage
		}
		q, _ := Identifier(name)
		names = append(names, q)
		switch {
		case v.Default:
			expressions = append(expressions, "DEFAULT")
		case v.Null:
			expressions = append(expressions, "?")
			args = append(args, nil)
		case v.Binary:
			if GeometryType(col.DataType) {
				if len(v.Bytes) < 9 {
					return nil, nil, nil, contract.ErrDataPage
				}
				expressions = append(expressions, "ST_GeomFromWKB(?, ?, 'axis-order=long-lat')")
				args = append(args, v.Bytes[4:], binary.LittleEndian.Uint32(v.Bytes[:4]))
				continue
			}
			expressions = append(expressions, "?")
			// A nil []byte is SQL NULL to the driver. Binary without Null
			// means an empty binary value, including hex/base64 import cells.
			if v.Bytes == nil {
				v.Bytes = []byte{}
			}
			args = append(args, v.Bytes)
		default:
			expressions = append(expressions, "?")
			args = append(args, v.Text)
		}
	}
	if !insert && len(keys) == 0 {
		return nil, nil, nil, contract.ErrDataPage
	}
	return names, expressions, args, nil
}

func QualifiedTable(database, table string) (string, error) {
	db, e := Identifier(database)
	if e != nil {
		return "", e
	}
	tbl, e := Identifier(table)
	if e != nil {
		return "", e
	}
	return db + "." + tbl, nil
}

func RowPredicate(ref contract.RowRef, database, table string) (string, []any, error) {
	if ref.Database != database || ref.Table != table || len(ref.Columns) == 0 || len(ref.Columns) > 16 || len(ref.Columns) != len(ref.Values) {
		return "", nil, contract.ErrDataReadOnly
	}
	parts := make([]string, len(ref.Columns))
	args := make([]any, len(parts))
	seen := map[string]bool{}
	for i, name := range ref.Columns {
		q, e := Identifier(name)
		if e != nil {
			return "", nil, e
		}
		if seen[name] || ref.Values[i] == nil || len(ref.Values[i]) > 4096 {
			return "", nil, contract.ErrDataReadOnly
		}
		seen[name] = true
		parts[i] = q + " = ?"
		args[i] = ref.Values[i]
	}
	return strings.Join(parts, " AND "), args, nil
}

func GeometryType(kind string) bool {
	switch strings.ToLower(kind) {
	case "geometry", "point", "linestring", "polygon", "multipoint", "multilinestring", "multipolygon", "geometrycollection":
		return true
	}
	return false
}

func Identifier(name string) (string, error) {
	if !ValidMetadataName(name) || !utf8.ValidString(name) || strings.ContainsAny(name, "\r\n") {
		return "", fmt.Errorf("%w: invalid database or table name", connections.ErrInvalid)
	}
	// Backtick is the MySQL identifier delimiter. Escaping it by doubling
	// prevents even user-specified names from changing the SQL structure.
	return "`" + strings.ReplaceAll(name, "`", "``") + "`", nil
}

func ValidMetadataName(name string) bool {
	return name != "" && len(name) <= 256 && !strings.ContainsRune(name, 0)
}

func BinaryType(kind string) bool {
	switch kind {
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob", "bit", "geometry",
		"point", "linestring", "polygon", "multipoint", "multilinestring", "multipolygon", "geometrycollection":
		return true
	}
	return false
}

func ValidNumber(text string) bool { return mysqlDataImportNumberPattern.MatchString(text) }
