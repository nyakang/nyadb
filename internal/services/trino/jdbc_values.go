package trino

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

var jdbcTimestampPrecision = regexp.MustCompile(`^TIMESTAMP\(([0-9]+)\)`)
var ErrIncompleteJDBC = errors.New("original Trino JDBC value is incomplete; use HTTP mode for a complete export")

// DecodeJDBCValue decodes the original TrinoAgent getObject().toString() wire
// representation. It is separate from nativeCell: the HTTP protocol retains
// binary/composite content that this original JDBC agent has already lost.
// Incomplete projections keep the exact original text and never become a
// complete binary/JSON cell. DecodeQuery keeps the original receipt alongside
// these cells; neither a display projection nor that receipt repairs JDBC loss.
func DecodeJDBCValue(declaration string, raw json.RawMessage) (contract.Cell, error) {
	raw = bytes.TrimSpace(raw)
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return contract.Cell{}, agentbridge.ErrNativeResult
	}
	if bytes.Equal(raw, []byte("null")) {
		return contract.Cell{Kind: contract.Null}, nil
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return contract.Cell{}, agentbridge.ErrNativeResult
	}
	decl := strings.ToUpper(strings.TrimSpace(declaration))
	base := decl
	if index := strings.IndexByte(base, '('); index >= 0 {
		base = strings.TrimSpace(base[:index])
	}
	switch base {
	case "TINYINT", "SMALLINT", "INTEGER", "BIGINT", "DECIMAL", "BOOLEAN", "VARCHAR", "CHAR", "JSON", "DATE", "TIMESTAMP", "TIMESTAMP WITH TIME ZONE":
		cell, err := agentbridge.DecodeStandardValue(declaration, raw)
		if err == nil && strings.HasPrefix(decl, "TIMESTAMP") {
			if precision := jdbcTimestampPrecision.FindStringSubmatch(decl); precision != nil {
				n, parseErr := strconv.Atoi(precision[1])
				cell.Truncated = parseErr != nil || n > 9
			}
		}
		return cell, err
	case "REAL", "DOUBLE":
		// Java uses Infinity/-Infinity; preserve its original spelling.
		if text == "Infinity" || text == "-Infinity" {
			return contract.Cell{Kind: contract.Floating, Text: text}, nil
		}
		return agentbridge.DecodeStandardValue(declaration, raw)
	case "TIME", "TIME WITH TIME ZONE":
		cell, err := agentbridge.DecodeStandardValue(declaration, raw)
		// java.sql.Time does not retain the original fractional precision.
		// Keep even an apparently whole-second display out of complete edits.
		cell.Truncated = true
		return cell, err
	default:
		// VARBINARY is byte[].toString(); ARRAY/MAP/ROW use Java renderings
		// that cannot recover original typed content. Unknown types remain
		// display-only too.
		return contract.Cell{Kind: contract.Text, Text: text, Truncated: true}, nil
	}
}
