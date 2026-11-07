package starrocks

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestStarRocksScalarBytesAndPrecision(t *testing.T) {
	for _, tc := range []struct {
		v    any
		decl string
		kind contract.CellKind
		text string
	}{{[]byte("170141183460469231731687303715884105727"), "largeint", contract.Integer, "170141183460469231731687303715884105727"}, {[]byte("9007199254740993.12345678901234567890"), "decimal(38,20)", contract.Decimal, "9007199254740993.12345678901234567890"}, {1.2345678901234567, "DOUBLE", contract.Floating, "1.2345678901234567"}, {[]byte(""), "STRING", contract.Text, ""}, {nil, "STRING", contract.Null, ""}} {
		c, err := nativeCell(tc.v, tc.decl)
		if err != nil || c.Kind != tc.kind || c.Text != tc.text {
			t.Fatal("native type or exact value lost")
		}
	}
	raw := []byte{0xff, 0, 1}
	c, err := nativeCell(raw, "bitmap")
	raw[0] = 1
	if err != nil || c.Kind != contract.Binary || c.Bytes[0] != 0xff {
		t.Fatal("binary source bytes aliased or lost")
	}
	if _, err = nativeCell([]byte(strings.Repeat("x", completeCellBytes+1)), "STRING"); err == nil {
		t.Fatal("over-limit export silently truncated")
	}
	if cell, err := nativeCell(float32(1.234567), "FLOAT"); err != nil || cell.Kind != contract.Floating || cell.Text != "1.234567" {
		t.Fatal("native float32 precision lost")
	}
	if cell, err := nativeCell([]byte("ABC"), "BLOB"); err != nil || cell.Kind != contract.Binary || !bytes.Equal(cell.Bytes, []byte("ABC")) {
		t.Fatal("printable wire BLOB became text")
	}
}
func TestStarRocksSQLPreflightAndLiteralBoundaries(t *testing.T) {
	parts, err := statements(`-- comment; INSERT bad
SELECT 'quoted;value', 'escaped\';value'; #comment;
SELECT 2`)
	if err != nil || len(parts) != 2 {
		t.Fatal("native quoted/commented boundary lost")
	}
	for _, source := range []string{"SELECT 1; BEGIN", "SELECT 1; START TRANSACTION", "SELECT 1; USE hive.db", "USE `hive`.`db`", "USE 'hive.db'", "USE", "SET sql_mode='NO_BACKSLASH_ESCAPES'", "SET binary_encoding_format=hex", "SET CATALOG hive", "SET binary_encoding_level=nested", "SET enable_prepare_stmt=false", "SWITCH other", "/*! SELECT 1 */", "SELECT 'unterminated"} {
		if _, err = statements(source); err == nil {
			t.Fatal("unsupported or malformed script admitted")
		}
	}
	for _, source := range []string{"USE db", "USE `a``b`", "USE default_catalog.db", "USE `default_catalog`.`db`"} {
		if _, err := statements(source); err != nil {
			t.Fatal("native database selection rejected")
		}
	}
	table := &contract.TableStructure{Object: contract.ObjectRef{ProfileID: 1, Catalog: "default_catalog", Schema: "a`b", Name: "table", Kind: "TABLE"}, Columns: []contract.StructureColumn{{Name: "id", Type: "BIGINT", ValueKind: contract.Integer}, {Name: "state", Type: "BITMAP", ValueKind: contract.Binary}}}
	q, args, err := dataQuery(table, contract.DataOptions{Limit: 2, FilterColumn: "id", FilterValue: "1' OR 1=1"}, nil, true)
	if err != nil || strings.Contains(q, "OR 1=1") || len(args) != 1 || !strings.Contains(q, "`default_catalog`.`a``b`.`table`") {
		t.Fatal("filter value or namespace interpolated unsafely")
	}
	if _, _, err = dataQuery(table, contract.DataOptions{Limit: 2, SortColumn: "state"}, nil, true); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("opaque-state sorting accepted")
	}
	table.Columns = append(table.Columns, contract.StructureColumn{Name: "unverified", Type: "PERCENTILE", ValueKind: contract.Binary})
	if _, _, err := dataQuery(table, contract.DataOptions{Limit: 2}, nil, true); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("unverified aggregate state could silently become NULL")
	}
}

type countingConnector struct{ calls int }

func (c *countingConnector) Connect(context.Context) (driver.Conn, error) {
	c.calls++
	return nil, driver.ErrBadConn
}
func (c *countingConnector) Driver() driver.Driver { return nil }
func TestStarRocksLostConnectorNeverRedials(t *testing.T) {
	base := &countingConnector{}
	c := &onceConnector{Connector: base}
	_, _ = c.Connect(context.Background())
	_, err := c.Connect(context.Background())
	if base.calls != 1 || !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("database/sql retry could redial or replay")
	}
	err = safeError(&mysqldriver.MySQLError{Number: 1105, Message: "secret SQL password"})
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("server diagnostic exposed SQL values")
	}
}
