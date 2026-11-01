package mysql

import (
	"context"
	"encoding/hex"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/mysqlspec"
	"strings"
	"unicode/utf8"
)

const (
	MySQLDataDefaultPageSize = 100
	MySQLDataMaxPageSize     = 200
	mysqlDataCellMaxBytes    = 2048
)

var ErrMySQLDataPage = contract.ErrDataPage

// MySQLDataCell preserves SQL NULL separately from text and bounds cell text.
// Truncated applies to the displayed preview only, never modifies the database.
type MySQLDataCell = contract.DataCell

// MySQLDataPage is a bounded, read-only result from an authenticated session.
// No total COUNT is run; HasMore is determined by fetching one extra row.
type MySQLDataPage = contract.DataPage

func quotedMySQLIdentifier(name string) (string, error) { return mysqlspec.Identifier(name) }

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
