package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

type programMetadataConnector struct{ seen chan string }

func (c programMetadataConnector) Connect(context.Context) (driver.Conn, error) {
	return programMetadataConn{seen: c.seen}, nil
}
func (programMetadataConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }

type programMetadataConn struct{ seen chan string }

func (programMetadataConn) Close() error { return nil }
func (programMetadataConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not prepared")
}
func (programMetadataConn) Begin() (driver.Tx, error)  { return nil, errors.New("no transactions") }
func (programMetadataConn) Ping(context.Context) error { return nil }
func (c programMetadataConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(args) != 1 || args[0].Value != "app" {
		return nil, errors.New("metadata schema must be bound to app")
	}
	c.seen <- query
	switch {
	case strings.Contains(query, "information_schema.TABLES"):
		return &programMetadataRows{cols: []string{"name", "type", "engine", "comment", "auto", "updated", "length", "estimate"},
			data: [][]driver.Value{{"orders", "BASE TABLE", "InnoDB", "", nil, nil, nil, nil}}}, nil
	case strings.Contains(query, "information_schema.ROUTINES"):
		return &programMetadataRows{cols: []string{"name", "type", "definer", "security", "return", "created", "altered", "definition", "comment"},
			data: [][]driver.Value{
				{"get_price", "FUNCTION", "reader@localhost", "INVOKER", "decimal(10,2)", "2026-10-26 11:01:02", nil, "RETURN 5.50", "price"},
				{"record_sale", "PROCEDURE", "reader@localhost", "DEFINER", "", nil, nil, "BEGIN SELECT 1; END", ""},
			}}, nil
	case strings.Contains(query, "information_schema.PARAMETERS"):
		return &programMetadataRows{cols: []string{"name", "type", "mode", "param", "dtype"},
			data: [][]driver.Value{
				{"get_price", "FUNCTION", "IN", "product_id", "int"},
				{"record_sale", "PROCEDURE", "IN", "sale_id", "bigint"},
				{"record_sale", "PROCEDURE", "OUT", "result_id", "bigint"},
			}}, nil
	case strings.Contains(query, "information_schema.EVENTS"):
		return &programMetadataRows{cols: []string{"name", "definer", "status", "type", "execute", "starts", "ends", "interval", "field", "completion", "created", "altered", "definition", "comment"},
			data: [][]driver.Value{
				{"rotate_logs", "reader@localhost", "ENABLED", "RECURRING", "", "2026-10-26", "", "1", "DAY", "PRESERVE", "2026-10-26", "", "DELETE FROM old_log", "rotate"},
			}}, nil
	default:
		return nil, errors.New("unexpected query")
	}
}

type programMetadataRows struct {
	cols []string
	data [][]driver.Value
	at   int
}

func (r *programMetadataRows) Columns() []string { return r.cols }
func (*programMetadataRows) Close() error        { return nil }
func (r *programMetadataRows) Next(dest []driver.Value) error {
	if r.at >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.at])
	r.at++
	return nil
}

func TestMySQLObjectsWithProgramsBindsSchemaAndPreservesDetails(t *testing.T) {
	manager := NewMySQLSessionManager()
	seen := make(chan string, 4)
	manager.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(programMetadataConnector{seen: seen}), nil
	}
	if err := manager.Connect(context.Background(), connections.ActiveSettings{ProfileID: 101}, ""); err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	objects, err := manager.ObjectsWithPrograms(context.Background(), 101, "app")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 4 {
		t.Fatalf("wrong metadata count: %+v", objects)
	}
	got := map[string]MySQLDBObject{}
	for _, o := range objects {
		got[o.Name] = o
	}
	if got["orders"].Kind != "表" || got["get_price"].Kind != "函数" ||
		got["get_price"].Parameters != "IN product_id int" ||
		got["get_price"].ReturnType != "decimal(10,2)" ||
		got["get_price"].Definition != "RETURN 5.50" ||
		got["record_sale"].Kind != "存储过程" ||
		got["record_sale"].Parameters != "IN sale_id bigint, OUT result_id bigint" ||
		got["rotate_logs"].Kind != "事件" ||
		got["rotate_logs"].Schedule != "EVERY 1 DAY" ||
		got["rotate_logs"].OnCompletion != "PRESERVE" ||
		got["rotate_logs"].Status != "ENABLED" {
		t.Fatalf("program metadata missing: %+v", objects)
	}
	for i := 0; i < 4; i++ {
		query := <-seen
		if !strings.Contains(query, "= ?") || strings.Contains(query, "'app'") {
			t.Fatalf("metadata query not parameterized: %s", query)
		}
	}
}
