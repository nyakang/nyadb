package mysql

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"io"
	"strings"
)

// Hex text literals are independent of NO_BACKSLASH_ESCAPES and quote mode.
func dumpText(s string) string {
	return "CONVERT(X'" + hex.EncodeToString([]byte(s)) + "' USING utf8mb4)"
}
func dumpLiteral(c contract.Cell) (string, error) {
	if c.Truncated {
		return "", errors.New("cannot dump truncated cell")
	}
	switch c.Kind {
	case contract.Null:
		return "NULL", nil
	case contract.Binary:
		return "X'" + hex.EncodeToString(c.Bytes) + "'", nil
	case contract.Integer, contract.Decimal, contract.Floating:
		if !mysqlDataImportNumberPattern.MatchString(c.Text) {
			return "", errors.New("invalid numeric backup value")
		}
		return c.Text, nil
	case contract.Boolean:
		switch strings.ToLower(c.Text) {
		case "true", "1":
			return "1", nil
		case "false", "0":
			return "0", nil
		}
		return "", errors.New("invalid boolean backup value")
	case contract.Text, contract.JSON, contract.DateTime:
		return dumpText(c.Text), nil
	}
	return "", errors.New("unknown backup cell kind")
}

func dumpColumnLiteral(column contract.Column, cell contract.Cell) (string, error) {
	if mysqlGeometryType(column.DatabaseType) && cell.Kind != contract.Null {
		if cell.Kind != contract.Binary || len(cell.Bytes) < 9 {
			return "", ErrMySQLDataPage
		}
		return fmt.Sprintf("ST_GeomFromWKB(X'%s', %d, 'axis-order=long-lat')", hex.EncodeToString(cell.Bytes[4:]), binary.LittleEndian.Uint32(cell.Bytes[:4])), nil
	}
	return dumpLiteral(cell)
}

// Large literals are assembled in bounded session-variable statements, keeping
// SQL dumps replayable through the 8 MiB single-statement lexer envelope.
func dumpStreamCell(ctx context.Context, w io.Writer, column contract.Column, cell contract.Cell, variable string) (string, error) {
	if len(cell.Text)+len(cell.Bytes) <= 16<<10 {
		return dumpColumnLiteral(column, cell)
	}
	var raw []byte
	if cell.Kind == contract.Binary {
		raw = cell.Bytes
	} else {
		raw = []byte(cell.Text)
	}
	if _, e := fmt.Fprintf(w, "SET %s=X'';\n", variable); e != nil {
		return "", e
	}
	for start := 0; start < len(raw); start += 1 << 20 {
		if e := ctx.Err(); e != nil {
			return "", e
		}
		end := min(len(raw), start+(1<<20))
		if _, e := fmt.Fprintf(w, "SET %s=CONCAT(%s,X'%s');\n", variable, variable, hex.EncodeToString(raw[start:end])); e != nil {
			return "", e
		}
	}
	if mysqlGeometryType(column.DatabaseType) {
		if len(raw) < 9 {
			return "", ErrMySQLDataPage
		}
		return fmt.Sprintf("ST_GeomFromWKB(SUBSTRING(%s,5),%d,'axis-order=long-lat')", variable, binary.LittleEndian.Uint32(raw[:4])), nil
	}
	if cell.Kind == contract.Binary {
		return variable, nil
	}
	return "CONVERT(" + variable + " USING utf8mb4)", nil
}
