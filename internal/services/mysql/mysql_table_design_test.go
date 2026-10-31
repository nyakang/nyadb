package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/db/mysqlddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type designFixture struct {
	mu                   sync.Mutex
	ddl, target, trigger string
	statements           []string
	fail                 func(context.Context, string) error
}
type designConnector struct{ fixture *designFixture }

func (c designConnector) Connect(context.Context) (driver.Conn, error) {
	return &designConn{fixture: c.fixture}, nil
}
func (c designConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }

type designConn struct{ fixture *designFixture }

func (*designConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (*designConn) Close() error                        { return nil }
func (*designConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (*designConn) Ping(context.Context) error          { return nil }
func (c *designConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	f := c.fixture
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasPrefix(q, "SHOW GRANTS"):
		return &programMetadataRows{cols: []string{"grants"}, data: [][]driver.Value{{"GRANT ALL PRIVILEGES ON `db`.* TO `test`@`%`"}}}, nil
	case strings.HasPrefix(q, "SHOW CREATE TABLE"):
		return &programMetadataRows{cols: []string{"Table", "Create Table"}, data: [][]driver.Value{{"t", f.ddl}}}, nil
	case strings.HasPrefix(q, "SHOW CREATE TRIGGER"):
		return &programMetadataRows{cols: []string{"Trigger", "sql_mode", "SQL Original Statement"}, data: [][]driver.Value{{"tr", "", f.trigger}}}, nil
	case strings.Contains(q, "@@SESSION.sql_mode"):
		return &programMetadataRows{cols: []string{"mode"}, data: [][]driver.Value{{""}}}, nil
	case strings.Contains(q, "TRIGGER_NAME"):
		var data [][]driver.Value
		if f.trigger != "" {
			data = [][]driver.Value{{"tr"}}
		}
		return &programMetadataRows{cols: []string{"TRIGGER_NAME"}, data: data}, nil
	case strings.Contains(q, "COUNT(*)"):
		return &programMetadataRows{cols: []string{"count"}, data: [][]driver.Value{{int64(0)}}}, nil
	}
	return nil, errors.New("unexpected fixture query: " + q)
}
func (c *designConn) ExecContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	f := c.fixture
	f.mu.Lock()
	f.statements = append(f.statements, q)
	fail := f.fail
	f.mu.Unlock()
	if fail != nil {
		if e := fail(ctx, q); e != nil {
			return nil, e
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasPrefix(q, "ALTER TABLE") || strings.HasPrefix(q, "CREATE TABLE") {
		f.ddl = f.target
	}
	return driver.RowsAffected(0), nil
}
func designManager(t *testing.T) (*MySQLSessionManager, *designFixture, mysqlddl.Snapshot, mysqlddl.Draft) {
	t.Helper()
	f := &designFixture{ddl: "CREATE TABLE `t` (`id` bigint NOT NULL) ENGINE=InnoDB"}
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(designConnector{fixture: f}), nil
	}
	if e := m.Connect(context.Background(), connections.ActiveSettings{ProfileID: 71}, ""); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { m.Close() })
	snapshot, e := m.TableDesign(context.Background(), 71, "db", "t")
	if e != nil {
		t.Fatal(e)
	}
	table, e := mysqlddl.Parse("db", "t", snapshot.DDL)
	if e != nil {
		t.Fatal(e)
	}
	table.Clauses[0].SQL = "`id` bigint NOT NULL COMMENT 'changed'"
	draft := mysqlddl.Draft{Table: table}
	f.target = table.DDL()
	return m, f, snapshot, draft
}
func TestTableDesignFingerprintAndSessionGenerationBlockExecution(t *testing.T) {
	m, f, snapshot, draft := designManager(t)
	plan, e := mysqlddl.Build(snapshot, draft)
	if e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	f.ddl = strings.Replace(f.ddl, "bigint", "int", 1)
	f.mu.Unlock()
	out := m.ApplyTableDesign(context.Background(), 71, plan)
	if out.State != "not_executed" || out.Error == nil || len(f.statements) != 0 {
		t.Fatal("external DDL conflict was executed")
	}
	f.mu.Lock()
	f.ddl = snapshot.DDL
	f.mu.Unlock()
	plan.Original.RuntimeGeneration++
	out = m.ApplyTableDesign(context.Background(), 71, plan)
	if out.Error == nil || len(f.statements) != 0 {
		t.Fatal("stale original session executed")
	}
}
func TestTableDesignExecutesOnceAndVerifiesReadback(t *testing.T) {
	m, f, snapshot, draft := designManager(t)
	plan, e := mysqlddl.Build(snapshot, draft)
	if e != nil {
		t.Fatal(e)
	}
	out := m.ApplyTableDesign(context.Background(), 71, plan)
	if out.Error != nil || !out.Verified || out.State != "executed" || out.Actual == nil || len(f.statements) != 1 {
		t.Fatalf("failed verified design: %+v", out)
	}
}
func TestTableDesignPartialFailureAndBadConnectionNeverReplay(t *testing.T) {
	for _, network := range []bool{false, true} {
		t.Run(map[bool]string{false: "permission", true: "network"}[network], func(t *testing.T) {
			m, f, snapshot, draft := designManager(t)
			f.trigger = "CREATE TRIGGER `tr` BEFORE INSERT ON `t` FOR EACH ROW SET NEW.`id`=1"
			snapshot.Triggers = []mysqlddl.Trigger{{Name: "tr", SQL: f.trigger}}
			draft.Triggers = []mysqlddl.Trigger{{Name: "tr", SQL: strings.Replace(f.trigger, "=1", "=2", 1)}}
			plan, e := mysqlddl.Build(snapshot, draft)
			if e != nil {
				t.Fatal(e)
			}
			f.fail = func(_ context.Context, q string) error {
				if network {
					return driver.ErrBadConn
				}
				if strings.HasPrefix(q, "CREATE TRIGGER") {
					return &mysqldriver.MySQLError{Number: 1142, Message: "permission denied"}
				}
				return nil
			}
			out := m.ApplyTableDesign(context.Background(), 71, plan)
			if network {
				if out.State != "uncertain" || len(f.statements) != 1 {
					t.Fatalf("bad connection retried: %+v %v", out, f.statements)
				}
			} else {
				if out.State != "partial" || len(f.statements) != 3 || out.Steps[0].State != "executed" || out.Steps[2].State != "not_executed" {
					t.Fatalf("partial DDL misreported: %+v", out)
				}
			}
		})
	}
}
func TestTableDesignCancelWaitsForDriverAcknowledgement(t *testing.T) {
	m, f, snapshot, draft := designManager(t)
	plan, e := mysqlddl.Build(snapshot, draft)
	if e != nil {
		t.Fatal(e)
	}
	started, release := make(chan struct{}), make(chan struct{})
	f.fail = func(ctx context.Context, _ string) error { close(started); <-ctx.Done(); <-release; return ctx.Err() }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan DDLOutcome, 1)
	go func() { done <- m.ApplyTableDesign(ctx, 71, plan) }()
	<-started
	cancel()
	select {
	case <-done:
		t.Fatal("cancel did not await executor")
	default:
	}
	close(release)
	out := <-done
	if out.State != "uncertain" || len(f.statements) != 1 {
		t.Fatalf("cancelled DDL was replayed/misreported: %+v", out)
	}
}
