package mysql

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/services/connections"
)

const (
	MySQLDataDefaultPageSize = 100
	MySQLDataMaxPageSize     = 200
	mysqlDataCellMaxBytes    = 2048
)

var ErrMySQLDataPage = errors.New("invalid MySQL data page")

// MySQLDataCell preserves SQL NULL separately from text and bounds cell text.
// Truncated applies to the displayed preview only, never modifies the database.
type MySQLDataCell struct {
	Text      string
	Null      bool
	Truncated bool
}

// MySQLDataPage is a bounded, read-only result from an authenticated session.
// No total COUNT is run; HasMore is determined by fetching one extra row.
type MySQLDataPage struct {
	Database, Table string
	Columns         []string
	ColumnKinds     []string               // original server DATA_TYPE per displayed column
	ColumnMetadata  []MySQLStructureColumn // full server field attributes, in Columns order
	Rows            [][]MySQLDataCell
	Offset          int64
	Limit           int
	HasMore         bool
	StableOrder     bool // true when a complete non-null unique index orders rows.
	KeysetAvailable bool
	NextCursor      *MySQLDataCursor
	RowRefs         []*MySQLRowRef
	RowSnapshots    []*MySQLRowSnapshot
	// QuerySQL is the executed parameterized statement, without bound values.
	QuerySQL    string
	CompletedAt time.Time
	Duration    time.Duration
}

func quotedMySQLIdentifier(name string) (string, error) {
	if !validMySQLMetadataName(name) || !utf8.ValidString(name) || strings.ContainsAny(name, "\r\n") {
		return "", fmt.Errorf("%w: invalid database or table name", connections.ErrInvalid)
	}
	// Backtick is the MySQL identifier delimiter. Escaping it by doubling
	// prevents even user-specified names from changing the SQL structure.
	return "`" + strings.ReplaceAll(name, "`", "``") + "`", nil
}

// TableDataPage keeps the original API while using the bounded, metadata-validated
// query implementation. No caller can accidentally fall back to SELECT *.
func (m *MySQLSessionManager) TableDataPage(ctx context.Context, id int64, database, table string, offset int64, limit int) (*MySQLDataPage, error) {
	return m.TableDataPageWithOptions(ctx, id, database, table, MySQLDataOptions{Offset: offset, Limit: limit})
}

func mysqlDataPreviewCell(v any, dbType string) MySQLDataCell {
	if v == nil {
		return MySQLDataCell{Null: true}
	}
	var text string
	switch value := v.(type) {
	case []byte:
		upper := strings.ToUpper(dbType)
		if mysqlDataBinaryType(strings.ToLower(dbType)) || strings.Contains(upper, "BLOB") || !utf8.Valid(value) {
			// Raw binary must never be interpreted as terminal control sequences.
			text = "0x" + hex.EncodeToString(value)
		} else {
			text = string(value)
		}
	case string:
		text = value
	default:
		text = fmt.Sprint(value)
	}
	cell := MySQLDataCell{}
	if len(text) > mysqlDataCellMaxBytes {
		text = text[:mysqlDataCellMaxBytes]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		cell.Truncated = true
	}
	cell.Text = text
	return cell
}
