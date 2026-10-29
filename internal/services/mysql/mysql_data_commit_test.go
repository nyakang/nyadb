package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

type commitFixture struct {
	applied         bool
	value           string
	writes, commits int
}
type commitConnector struct{ f *commitFixture }

func (c commitConnector) Connect(context.Context) (driver.Conn, error) {
	return &commitConn{f: c.f}, nil
}
func (commitConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }

type commitConn struct {
	f       *commitFixture
	pending string
}

func (c *commitConn) Close() error { return nil }
func (c *commitConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (c *commitConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *commitConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.pending = c.f.value
	return commitTx{c}, nil
}

type commitTx struct{ c *commitConn }

func (tx commitTx) Commit() error {
	tx.c.f.commits++
	if tx.c.f.applied {
		tx.c.f.value = tx.c.pending
	}
	tx.c.pending = ""
	return errors.New("simulated lost COMMIT acknowledgement")
}
func (tx commitTx) Rollback() error { tx.c.pending = ""; return nil }
func (c *commitConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, "SET SESSION") {
		return driver.RowsAffected(0), nil
	}
	if strings.HasPrefix(q, "UPDATE ") {
		c.f.writes++
		c.pending = "afterhash"
		return driver.RowsAffected(1), nil
	}
	return nil, errors.New("unexpected exec")
}
func (c *commitConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.HasPrefix(q, "SELECT 1 FROM "):
		return &structureRows{cols: []string{"one"}}, nil
	case strings.Contains(q, "@@SESSION.sql_mode"):
		return &structureRows{cols: []string{"mode"}, values: [][]driver.Value{{"STRICT_TRANS_TABLES"}}}, nil
	case strings.Contains(q, "information_schema.TABLES"):
		return &structureRows{cols: []string{"engine", "kind"}, values: [][]driver.Value{{"InnoDB", "BASE TABLE"}}}, nil
	case strings.Contains(q, "information_schema.COLUMNS") && !strings.Contains(q, "STATISTICS"):
		return &structureRows{cols: []string{"name", "type", "kind", "pos", "null", "default", "key", "extra", "comment", "length", "precision", "scale", "datetime", "charset", "collation"}, values: [][]driver.Value{
			{"id", "int", "int", int64(1), "NO", nil, "PRI", "", "", nil, int64(10), int64(0), nil, nil, nil},
			{"note", "varchar(80)", "varchar", int64(2), "YES", nil, "", "", "", int64(80), nil, nil, nil, "utf8mb4", "utf8mb4_0900_ai_ci"},
		}}, nil
	case strings.Contains(q, "information_schema.STATISTICS"):
		return &structureRows{cols: []string{"index", "name", "prefix", "nullable"}, values: [][]driver.Value{{"PRIMARY", "id", nil, "NO"}}}, nil
	case strings.HasPrefix(q, "SHOW WARNINGS"):
		return &structureRows{cols: []string{"level", "code", "message"}}, nil
	case strings.HasPrefix(q, "SELECT `id`,SUBSTRING("):
		return &structureRows{cols: []string{"id", "note"}, values: [][]driver.Value{{int64(1), "after"}}}, nil
	case strings.Contains(q, "SHA2(CAST("):
		value := c.f.value
		if c.pending != "" {
			value = c.pending
		}
		return &structureRows{cols: []string{"id", "note"}, values: [][]driver.Value{{"idhash", value}}}, nil
	}
	return nil, errors.New("unexpected query")
}
func TestMySQLDataUnknownCommitIsReadVerifiedWithoutReplay(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(map[bool]string{false: "rolled_back", true: "committed"}[applied], func(t *testing.T) {
			f := &commitFixture{applied: applied, value: "beforehash"}
			m := NewMySQLSessionManager()
			m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
				return sql.OpenDB(commitConnector{f}), nil
			}
			if e := m.Connect(context.Background(), connections.ActiveSettings{ProfileID: 1}, ""); e != nil {
				t.Fatal(e)
			}
			defer m.Close()
			db := m.sessions[1].db
			columns, e := readMySQLColumnMetadata(context.Background(), db, "db", "records", 128)
			if e != nil {
				t.Fatal(e)
			}
			ref := MySQLRowRef{Database: "db", Table: "records", Columns: []string{"id"}, Values: [][]byte{[]byte("1")}}
			original := mysqlSnapshotFromRaw(ref, columns, []any{"idhash", "beforehash"})
			result, e := m.ApplyMySQLDataEdits(context.Background(), 1, "db", "records", []MySQLDataEdit{{Kind: "update", Original: original, Values: map[string]MySQLDataValue{"note": {Text: "after"}}}})
			if !errors.Is(e, ErrMySQLDataCommitUncertain) || result == nil || !result.CommitUncertain {
				t.Fatalf("uncertain commit concealed %v %v", result, e)
			}
			verification, e := m.VerifyMySQLDataEdits(context.Background(), 1, "db", "records", result)
			if e != nil {
				t.Fatal(e)
			}
			expected := "not_applied"
			if applied {
				expected = "applied"
			}
			if verification.Status != expected || f.writes != 1 || f.commits != 1 {
				t.Fatalf("verification/replay %v writes %d commits %d", verification, f.writes, f.commits)
			}
		})
	}
}
