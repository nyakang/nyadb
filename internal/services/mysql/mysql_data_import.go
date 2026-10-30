package mysql

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// MySQLDataImportColumn is a reviewed new-table field definition. Type accepts
// only the supported ordinary types, never expressions, defaults or raw SQL.
type MySQLDataImportColumn struct {
	Name, Type           string
	Nullable, PrimaryKey bool
}

// CREATE TABLE is not transactional in MySQL. A confirmed new table remains
// available for inspection after a rolled-back row batch. An acknowledgement
// failure is reported separately and must not lead to automatic replay/drop.
type MySQLDataNewTableImportResult struct {
	Created, CreationUncertain bool
	Saved                      *MySQLDataEditResult
}

var mysqlDataImportTypePattern = regexp.MustCompile(`^([A-Z]+)\s*(?:\(\s*([0-9]+)\s*(?:,\s*([0-9]+)\s*)?\))?$`)
var mysqlDataImportNumberPattern = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
var mysqlDataImportTimePattern = regexp.MustCompile(`^[+-]?([0-9]{1,3}):([0-9]{2}):([0-9]{2})(?:\.([0-9]{1,6}))?$`)

func mysqlDataImportInvalid(reason string) error {
	return fmt.Errorf("%w: invalid new-table import: %s", ErrMySQLDataPage, reason)
}

func mysqlDataImportType(col MySQLDataImportColumn, position int) (MySQLStructureColumn, error) {
	meta := MySQLStructureColumn{Name: col.Name, Nullable: col.Nullable, Position: int64(position)}
	parts := mysqlDataImportTypePattern.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(col.Type)))
	if parts == nil {
		return meta, mysqlDataImportInvalid("unsupported field type")
	}
	kind := parts[1]
	first, second := -1, -1
	var err error
	if parts[2] != "" {
		first, err = strconv.Atoi(parts[2])
		if err != nil {
			return meta, mysqlDataImportInvalid("field type parameter")
		}
	}
	if parts[3] != "" {
		second, err = strconv.Atoi(parts[3])
		if err != nil {
			return meta, mysqlDataImportInvalid("field type parameter")
		}
	}
	meta.Type, meta.DataType = kind, strings.ToLower(kind)
	length := func(n int) { value := int64(n); meta.CharacterMaxLength = &value }
	precision := func(p, s int) { a, b := int64(p), int64(s); meta.NumericPrecision, meta.NumericScale = &a, &b }
	noParameters := func() bool { return first == -1 && second == -1 }
	switch kind {
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT", "BOOLEAN", "BOOL":
		if !noParameters() {
			return meta, mysqlDataImportInvalid("integer field parameter")
		}
		if kind == "INTEGER" {
			meta.DataType = "int"
		}
		if kind == "BOOLEAN" || kind == "BOOL" {
			meta.DataType = "tinyint"
		}
	case "DECIMAL", "NUMERIC":
		if first < 1 || first > 65 || second < 0 || second > 30 || second > first {
			return meta, mysqlDataImportInvalid("DECIMAL precision or scale")
		}
		meta.Type, meta.DataType = fmt.Sprintf("DECIMAL(%d,%d)", first, second), "decimal"
		precision(first, second)
	case "DOUBLE", "FLOAT":
		if !noParameters() {
			return meta, mysqlDataImportInvalid("floating field parameter")
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
			return meta, mysqlDataImportInvalid("string field length")
		}
		meta.Type = fmt.Sprintf("%s(%d)", kind, first)
		length(first)
	case "TEXT", "MEDIUMTEXT", "LONGTEXT", "BLOB", "MEDIUMBLOB", "LONGBLOB":
		if !noParameters() || col.PrimaryKey {
			return meta, mysqlDataImportInvalid("large field type or primary key")
		}
	case "DATE", "JSON":
		if !noParameters() || kind == "JSON" && col.PrimaryKey {
			return meta, mysqlDataImportInvalid("field type or primary key")
		}
	case "DATETIME", "TIMESTAMP", "TIME":
		if second != -1 || first > 6 {
			return meta, mysqlDataImportInvalid("temporal field precision")
		}
		if first == -1 {
			first = 0
		} else {
			meta.Type = fmt.Sprintf("%s(%d)", kind, first)
		}
		value := int64(first)
		meta.DatetimePrecision = &value
	default:
		return meta, mysqlDataImportInvalid("unsupported field type")
	}
	if col.PrimaryKey {
		if col.Nullable {
			return meta, mysqlDataImportInvalid("primary key allows NULL")
		}
		meta.Key = "PRI"
	}
	if strings.Contains(meta.DataType, "char") || strings.Contains(meta.DataType, "text") {
		charset, collation := "utf8mb4", "utf8mb4_0900_ai_ci"
		meta.CharacterSet, meta.Collation = &charset, &collation
	}
	return meta, nil
}

// MySQLDataImportColumnMetadata presents the exact validated schema to mapping
// and value validation without modifying the caller's reviewed definitions.
func MySQLDataImportColumnMetadata(columns []MySQLDataImportColumn) ([]MySQLStructureColumn, error) {
	if len(columns) == 0 || len(columns) > 128 {
		return nil, mysqlDataImportInvalid("field count")
	}
	metadata := make([]MySQLStructureColumn, len(columns))
	seen := make(map[string]bool, len(columns))
	keyBytes := int64(0)
	for i, col := range columns {
		if _, err := quotedMySQLIdentifier(col.Name); err != nil || !utf8.ValidString(col.Name) || strings.HasSuffix(col.Name, " ") || utf8.RuneCountInString(col.Name) > 64 {
			return nil, mysqlDataImportInvalid("field name")
		}
		fold := strings.ToLower(col.Name)
		if seen[fold] {
			return nil, mysqlDataImportInvalid("duplicate field name")
		}
		seen[fold] = true
		meta, err := mysqlDataImportType(col, i+1)
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
		return nil, mysqlDataImportInvalid("primary key exceeds InnoDB byte limit")
	}
	return metadata, nil
}

func ValidateMySQLDataImportColumns(columns []MySQLDataImportColumn) error {
	_, err := MySQLDataImportColumnMetadata(columns)
	return err
}

// ValidateMySQLDataNewTableImport shares the executor's whole-batch preflight
// with the import review. It does not connect or create the destination table.
func ValidateMySQLDataNewTableImport(columns []MySQLDataImportColumn, edits []MySQLDataEdit) error {
	metadata, err := MySQLDataImportColumnMetadata(columns)
	if err != nil {
		return err
	}
	_, err = mysqlDataImportValidatedEdits(metadata, edits)
	return err
}

func mysqlDataImportCreateSQL(database, table string, columns []MySQLDataImportColumn, metadata []MySQLStructureColumn) (string, error) {
	target, err := mysqlEditTable(database, table)
	if err != nil {
		return "", err
	}
	if utf8.RuneCountInString(database) > 64 || utf8.RuneCountInString(table) > 64 || strings.HasSuffix(database, " ") || strings.HasSuffix(table, " ") || !utf8.ValidString(database) || !utf8.ValidString(table) {
		return "", mysqlDataImportInvalid("database or table name")
	}
	definitions := make([]string, 0, len(columns)+1)
	var primary []string
	for i, col := range columns {
		name, _ := quotedMySQLIdentifier(col.Name)
		definition := name + " " + metadata[i].Type
		if col.Nullable {
			definition += " NULL"
		} else {
			definition += " NOT NULL"
		}
		definitions = append(definitions, definition)
		if col.PrimaryKey {
			primary = append(primary, name)
		}
	}
	if len(primary) > 0 {
		definitions = append(definitions, "PRIMARY KEY ("+strings.Join(primary, ",")+")")
	}
	return "CREATE TABLE " + target + " (" + strings.Join(definitions, ",") + ") ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci", nil
}

func mysqlDataImportDecimalFits(text string, precision, scale int64) bool {
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

func mysqlDataImportValueValid(col MySQLStructureColumn, value MySQLDataValue) bool {
	if value.Null {
		return true
	}
	binary := strings.Contains(col.DataType, "binary") || strings.Contains(col.DataType, "blob")
	if binary != value.Binary {
		return false
	}
	if binary {
		if col.CharacterMaxLength != nil && int64(len(value.Bytes)) > *col.CharacterMaxLength {
			return false
		}
		return col.DataType != "blob" || len(value.Bytes) <= 65535
	}
	if col.CharacterMaxLength != nil && int64(utf8.RuneCountInString(value.Text)) > *col.CharacterMaxLength {
		return false
	}
	switch col.DataType {
	case "tinyint", "smallint", "mediumint", "int", "bigint":
		bits := map[string]int{"tinyint": 8, "smallint": 16, "mediumint": 24, "int": 32, "bigint": 64}[col.DataType]
		_, err := strconv.ParseInt(value.Text, 10, bits)
		return err == nil
	case "decimal":
		return mysqlDataImportDecimalFits(value.Text, *col.NumericPrecision, *col.NumericScale)
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
	case "date":
		parsed, err := time.Parse("2006-01-02", value.Text)
		return err == nil && parsed.Year() >= 1000
	case "datetime", "timestamp":
		fraction := 0
		if dot := strings.IndexByte(value.Text, '.'); dot >= 0 {
			fraction = len(value.Text) - dot - 1
		}
		parsed, err := time.Parse("2006-01-02 15:04:05.999999999", value.Text)
		return err == nil && parsed.Year() >= 1000 && int64(fraction) <= *col.DatetimePrecision
	case "time":
		parts := mysqlDataImportTimePattern.FindStringSubmatch(value.Text)
		if parts == nil {
			return false
		}
		h, _ := strconv.Atoi(parts[1])
		m, _ := strconv.Atoi(parts[2])
		s, _ := strconv.Atoi(parts[3])
		return h <= 838 && m < 60 && s < 60 && int64(len(parts[4])) <= *col.DatetimePrecision
	}
	return true
}

func mysqlDataImportValidatedEdits(metadata []MySQLStructureColumn, edits []MySQLDataEdit) ([]MySQLDataEdit, error) {
	if len(edits) == 0 || len(edits) > MySQLDataMaxPageSize {
		return nil, ErrMySQLDataPage
	}
	byName := make(map[string]MySQLStructureColumn, len(metadata))
	for _, col := range metadata {
		byName[col.Name] = col
	}
	copyEdits := make([]MySQLDataEdit, len(edits))
	total := 0
	for i, edit := range edits {
		if edit.Kind != "insert" || edit.Original != nil || len(edit.Values) == 0 {
			return nil, ErrMySQLDataPage
		}
		copyEdits[i] = MySQLDataEdit{Kind: "insert", Values: make(map[string]MySQLDataValue, len(edit.Values))}
		for name, value := range edit.Values {
			col, ok := byName[name]
			if !ok || value.Default || value.Null && value.Binary || !mysqlDataImportValueValid(col, value) {
				return nil, fmt.Errorf("%w: row %d field %s", ErrMySQLDataPage, i+1, name)
			}
			total += len(value.Text) + len(value.Bytes)
			if total > MySQLDataEditMaxBytes {
				return nil, ErrMySQLDataEditLimit
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
					return nil, fmt.Errorf("%w: missing field %s", ErrMySQLDataPage, col.Name)
				}
			}
		}
		if _, _, _, err := mysqlEditAssignments(metadata, copyEdits[i].Values, true); err != nil {
			return nil, err
		}
	}
	return copyEdits, nil
}

// ImportMySQLDataIntoNewTable creates only the explicitly reviewed destination.
// It pins the original managed metadata connection across DDL and the row
// transaction. It neither replaces an existing table nor replays an operation
// after reconnection. The caller must await the driver's cancellation reply.
func (m *MySQLSessionManager) ImportMySQLDataIntoNewTable(ctx context.Context, id int64, database, table string, columns []MySQLDataImportColumn, edits []MySQLDataEdit) (*MySQLDataNewTableImportResult, error) {
	result := &MySQLDataNewTableImportResult{}
	metadata, err := MySQLDataImportColumnMetadata(columns)
	if err != nil {
		return result, err
	}
	createSQL, err := mysqlDataImportCreateSQL(database, table, columns, metadata)
	if err != nil {
		return result, err
	}
	ownedEdits, err := mysqlDataImportValidatedEdits(metadata, edits)
	if err != nil {
		return result, err
	}
	session, qctx, cancel, err := m.requestContext(ctx, id, 45*time.Second)
	if err != nil {
		return result, err
	}
	defer cancel()
	if err = qctx.Err(); err != nil {
		return result, err
	}
	conn, err := session.db.Conn(qctx)
	if err != nil {
		return result, err
	}
	defer conn.Close()
	var originalMode string
	if err = conn.QueryRowContext(qctx, "SELECT @@SESSION.sql_mode").Scan(&originalMode); err != nil {
		return result, err
	}
	if _, err = conn.ExecContext(qctx, "SET SESSION sql_mode = ?", originalMode+",STRICT_ALL_TABLES"); err != nil {
		return result, err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if _, restoreErr := conn.ExecContext(cleanup, "SET SESSION sql_mode = ?", originalMode); restoreErr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if err = qctx.Err(); err != nil {
		return result, err
	}
	if _, err = conn.ExecContext(qctx, createSQL); err != nil {
		var serverError *mysqldriver.MySQLError
		result.CreationUncertain = !errors.As(err, &serverError)
		return result, err
	}
	result.Created = true
	tx, err := conn.BeginTx(qctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	target, _ := mysqlEditTable(database, table)
	result.Saved, err = applyMySQLDataEditsInTransaction(qctx, tx, database, table, target, ownedEdits)
	if err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		result.Saved.CommitUncertain = true
		return result, fmt.Errorf("%w: %v", ErrMySQLDataCommitUncertain, err)
	}
	return result, nil
}
