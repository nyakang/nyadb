package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

type tableInfoQuery func(context.Context, string, []driver.NamedValue) (driver.Rows, error)
type tableInfoConnector struct{ query tableInfoQuery }
type tableInfoConn struct{ query tableInfoQuery }

func (c tableInfoConnector) Connect(context.Context) (driver.Conn, error) {
	return tableInfoConn{c.query}, nil
}
func (tableInfoConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }
func (tableInfoConn) Close() error               { return nil }
func (tableInfoConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (tableInfoConn) Begin() (driver.Tx, error)  { return nil, errors.New("unexpected transaction") }
func (tableInfoConn) Ping(context.Context) error { return nil }
func (c tableInfoConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.query(ctx, q, args)
}

func tableInfoTestManager(t *testing.T, query tableInfoQuery) *MySQLSessionManager {
	t.Helper()
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(tableInfoConnector{query}), nil
	}
	if err := m.Connect(context.Background(), connections.ActiveSettings{ProfileID: 31}, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func tableInfoTestRows(kind string) driver.Rows {
	return &structureRows{
		cols:   []string{"kind", "engine", "format", "collation", "comment", "options", "created", "modified", "checked", "rows", "auto", "data", "index", "max", "free"},
		values: [][]driver.Value{{kind, "InnoDB", "Dynamic", "utf8mb4_0900_ai_ci", "table comment", "", "2026-10-20 13:13:16", nil, nil, int64(21), nil, int64(4096), int64(0), nil, int64(0)}},
	}
}

func TestMySQLTableInfoReadsOnlyBoundObjectAndPreservesNulls(t *testing.T) {
	queries := 0
	database, table := "db' OR 1=1 --", "table` ; DROP TABLE x"
	m := tableInfoTestManager(t, func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
		queries++
		if len(args) != 2 || args[0].Value != database || args[1].Value != table || strings.Contains(q, database) ||
			strings.Contains(q, table) || strings.Contains(q, "COUNT(") || !strings.Contains(q, "TABLE_NAME = ?") {
			t.Errorf("unexpected query: %s %+v", q, args)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 20*time.Second {
			t.Error("missing bounded deadline")
		}
		return tableInfoTestRows("BASE TABLE"), nil
	})
	info, err := m.TableInfo(context.Background(), 31, database, table)
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 || info.Database != database || info.Name != table || info.Engine != "InnoDB" || info.RowFormat != "Dynamic" ||
		info.CreatedAt != "2026-10-20 13:13:16" || info.ModifiedAt != "" || info.CheckedAt != "" || info.AutoIncrement != nil ||
		info.MaxDataLength != nil || info.IndexLength == nil || *info.IndexLength != 0 || info.EstimatedRows == nil || *info.EstimatedRows != 21 {
		t.Fatalf("incorrect metadata: %+v", info)
	}
}

func TestMySQLTableInfoViewDoesNotInventStorageStatistics(t *testing.T) {
	m := tableInfoTestManager(t, func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
		return tableInfoTestRows("VIEW"), nil
	})
	info, err := m.TableInfo(context.Background(), 31, "db", "view")
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != "VIEW" || info.Comment != "table comment" || info.Engine != "" || info.EstimatedRows != nil || info.DataLength != nil || info.CreatedAt != "" {
		t.Fatalf("view displayed table statistics: %+v", info)
	}
}

func TestMySQLTableInfoMissingOrInvalid(t *testing.T) {
	m := tableInfoTestManager(t, func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
		return &structureRows{cols: []string{"kind"}}, nil
	})
	if _, err := m.TableInfo(context.Background(), 31, "db", "gone"); !errors.Is(err, ErrMySQLTableNotFound) {
		t.Fatalf("missing table: %v", err)
	}
	for _, name := range []string{"", "bad\x00name", strings.Repeat("a", 257)} {
		if _, err := m.TableInfo(context.Background(), 31, name, "t"); !errors.Is(err, connections.ErrInvalid) {
			t.Fatalf("invalid info name: %v", err)
		}
		if _, err := m.CountTableRows(context.Background(), 31, "db", name); !errors.Is(err, connections.ErrInvalid) {
			t.Fatalf("invalid count name: %v", err)
		}
	}
	if _, err := m.TableInfo(context.Background(), 0, "db", "t"); !errors.Is(err, ErrMySQLNotConnected) {
		t.Fatal(err)
	}
}

func TestMySQLTableCountQuotesIdentifiersAndCountsWholeObject(t *testing.T) {
	m := tableInfoTestManager(t, func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
		if q != "SELECT COUNT(*) FROM `d``b`.`t``x`" || len(args) != 0 {
			t.Errorf("count query: %q %+v", q, args)
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 20*time.Second {
			t.Error("count must have 20-second deadline")
		}
		return &structureRows{cols: []string{"COUNT(*)"}, values: [][]driver.Value{{int64(0)}}}, nil
	})
	count, err := m.CountTableRows(context.Background(), 31, "d`b", "t`x")
	if err != nil || count != 0 {
		t.Fatalf("exact zero count: %d %v", count, err)
	}
}

func TestMySQLTableInfoAndCountStopOnDisconnect(t *testing.T) {
	for _, count := range []bool{false, true} {
		t.Run(map[bool]string{false: "info", true: "count"}[count], func(t *testing.T) {
			started := make(chan struct{})
			m := tableInfoTestManager(t, func(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			})
			done := make(chan error, 1)
			go func() {
				var err error
				if count {
					_, err = m.CountTableRows(context.Background(), 31, "db", "t")
				} else {
					_, err = m.TableInfo(context.Background(), 31, "db", "t")
				}
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("query did not start")
			}
			if err := m.Disconnect(31); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("disconnect: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("disconnect did not stop query")
			}
		})
	}
}
