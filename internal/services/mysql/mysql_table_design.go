package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/db/mysqlddl"
)

type DDLStep struct {
	SQL, State string
	Error      error
}

func (m *MySQLSessionManager) SessionGeneration(id int64) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[id] == nil {
		return 0
	}
	return m.versions[id]
}

type DDLOutcome struct {
	State    string // not_executed, executed, partial, uncertain
	Steps    []DDLStep
	Actual   *mysqlddl.Snapshot
	Verified bool
	Error    error
}

// TableDesign reads the complete public DDL and triggers on one physical
// connection, independent of the existing read-only metadata entry point.
func (m *MySQLSessionManager) TableDesign(ctx context.Context, id int64, database, table string) (mysqlddl.Snapshot, error) {
	s, qctx, cancel, e := m.requestContext(ctx, id, 20*time.Second)
	if e != nil {
		return mysqlddl.Snapshot{}, e
	}
	defer cancel()
	m.mu.Lock()
	generation := m.versions[id]
	valid := m.sessions[id] == s
	m.mu.Unlock()
	if !valid {
		return mysqlddl.Snapshot{}, ErrMySQLNotConnected
	}
	conn, e := s.db.Conn(qctx)
	if e != nil {
		return mysqlddl.Snapshot{}, e
	}
	defer conn.Close()
	result, e := readTableDesign(qctx, conn, database, table)
	result.RuntimeGeneration = generation
	return result, e
}
func readCreate(ctx context.Context, conn *sql.Conn, kind, database, name string) (string, error) {
	return readCreateMode(ctx, conn, kind, database, name, nil)
}
func readCreateMode(ctx context.Context, conn *sql.Conn, kind, database, name string, mode *string) (string, error) {
	db, e := quotedMySQLIdentifier(database)
	if e != nil {
		return "", e
	}
	object, e := quotedMySQLIdentifier(name)
	if e != nil {
		return "", e
	}
	rows, e := conn.QueryContext(ctx, "SHOW CREATE "+kind+" "+db+"."+object)
	if e != nil {
		return "", e
	}
	defer rows.Close()
	cols, e := rows.Columns()
	if e != nil {
		return "", e
	}
	if !rows.Next() {
		if e = rows.Err(); e != nil {
			return "", e
		}
		return "", ErrMySQLTableNotFound
	}
	values := make([]sql.NullString, len(cols))
	dest := make([]any, len(cols))
	for i := range dest {
		dest[i] = &values[i]
	}
	if e = rows.Scan(dest...); e != nil {
		return "", e
	}
	if mode != nil {
		for i, c := range cols {
			if strings.EqualFold(c, "sql_mode") {
				*mode = values[i].String
			}
		}
	}
	for i, c := range cols {
		if strings.EqualFold(c, "Create Table") || strings.EqualFold(c, "SQL Original Statement") || strings.EqualFold(c, "Create View") {
			if !values[i].Valid || len(values[i].String) > 8<<20 {
				return "", errors.New("invalid or oversized public DDL")
			}
			return values[i].String, nil
		}
	}
	return "", errors.New("SHOW CREATE did not return a complete definition")
}
func readTableDesign(ctx context.Context, conn *sql.Conn, database, table string) (mysqlddl.Snapshot, error) {
	s := mysqlddl.Snapshot{Database: database, Table: table}
	ddl, e := readCreate(ctx, conn, "TABLE", database, table)
	if e != nil {
		return s, e
	}
	s.DDL = ddl
	s.TriggersUnknown = !canReadAllTriggers(ctx, conn, database, table)
	if e = conn.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode").Scan(&s.SQLMode); e != nil {
		return s, e
	}
	if _, e = mysqlddl.Parse(database, table, ddl); e != nil {
		return s, e
	}
	rows, e := conn.QueryContext(ctx, "SELECT TRIGGER_NAME FROM information_schema.TRIGGERS WHERE EVENT_OBJECT_SCHEMA=? AND EVENT_OBJECT_TABLE=? ORDER BY EVENT_MANIPULATION,ACTION_TIMING,ACTION_ORDER,TRIGGER_NAME", database, table)
	if e != nil {
		return s, e
	}
	var names []string
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			rows.Close()
			return s, e
		}
		if len(names) >= 512 {
			rows.Close()
			return s, ErrMySQLMetadataLimit
		}
		names = append(names, name)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return s, e
	}
	size := len(s.DDL)
	for _, name := range names {
		var mode string
		ddl, e := readCreateMode(ctx, conn, "TRIGGER", database, name, &mode)
		if e != nil {
			return s, e
		}
		size += len(ddl)
		if size > 8<<20 {
			return s, errors.New("complete table design exceeds 8 MiB")
		}
		s.Triggers = append(s.Triggers, mysqlddl.Trigger{Name: name, SQL: ddl, SQLMode: mode})
	}
	return s, ctx.Err()
}

// ApplyTableDesign binds preflight, execution and verification to the original
// session and a single physical connection. Conn.ExecContext does not use
// database/sql's automatic bad-connection retry loop. No step is replayed.
func (m *MySQLSessionManager) ApplyTableDesign(ctx context.Context, id int64, plan mysqlddl.Plan) DDLOutcome {
	out := DDLOutcome{State: "not_executed"}
	// Rebuild from immutable inputs: never execute an unvalidated modified preview.
	checked, e := mysqlddl.Build(plan.Original, plan.Desired)
	if e != nil {
		out.Error = e
		return out
	}
	if len(checked.Statements) != len(plan.Statements) {
		out.Error = errors.New("DDL preview no longer matches draft")
		return out
	}
	for i, s := range checked.Statements {
		if s != plan.Statements[i] {
			out.Error = errors.New("DDL preview changed")
			return out
		}
	}
	s, qctx, cancel, e := m.requestContext(ctx, id, 0)
	if e != nil {
		out.Error = e
		return out
	}
	defer cancel()
	valid := func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.sessions[id] == s && m.versions[id] == plan.Original.RuntimeGeneration && s.ctx.Err() == nil
	}
	if !valid() {
		out.Error = errors.New("original DDL session generation expired")
		return out
	}
	conn, e := s.db.Conn(qctx)
	if e != nil {
		out.Error = e
		return out
	}
	defer conn.Close()
	// The DDL connection is discarded after use, including partial trigger SQL
	// mode changes. It never returns altered session state to a metadata pool.
	defer conn.Raw(func(any) error { return driver.ErrBadConn })
	var mode string
	if e = conn.QueryRowContext(qctx, "SELECT @@SESSION.sql_mode").Scan(&mode); e != nil {
		out.Error = e
		return out
	}
	if plan.Original.SQLMode != "" && plan.Original.SQLMode != mode {
		out.Error = errors.New("DDL session SQL mode changed since the original read")
		return out
	}
	if strings.Contains(mode, "NO_BACKSLASH_ESCAPES") || strings.Contains(mode, "ANSI_QUOTES") {
		out.Error = errors.New("table designer requires standard MySQL literal and identifier SQL modes")
		return out
	}
	if plan.Desired.New {
		var n int
		e = conn.QueryRowContext(qctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", plan.Original.Database, plan.Original.Table).Scan(&n)
		if e != nil {
			out.Error = e
			return out
		}
		if n != 0 {
			out.Error = errors.New("new table target already exists")
			return out
		}
	} else {
		actual, e := readTableDesign(qctx, conn, plan.Original.Database, plan.Original.Table)
		if e != nil {
			out.Error = e
			return out
		}
		if actual.Fingerprint() != plan.Fingerprint {
			out.Actual = &actual
			out.Error = errors.New("table structure changed externally; draft retained")
			return out
		}
		if actual.TriggersUnknown && len(plan.Desired.Triggers) > 0 {
			out.Error = errors.New("trigger metadata visibility cannot be established; execution blocked")
			return out
		}
	}
	for _, statement := range plan.Statements {
		out.Steps = append(out.Steps, DDLStep{SQL: statement, State: "not_executed"})
	}
	for i, statement := range plan.Statements {
		if e = qctx.Err(); e != nil || !valid() {
			if e == nil {
				e = ErrMySQLNotConnected
			}
			out.Error = e
			if i > 0 {
				out.State = "partial"
			}
			return out
		}
		_, e = conn.ExecContext(qctx, statement)
		if e != nil {
			out.Steps[i].Error = e
			out.Error = e
			var serverErr *mysqldriver.MySQLError
			if errors.As(e, &serverErr) {
				out.Steps[i].State = "not_executed"
				if i > 0 {
					out.State = "partial"
				}
			} else {
				out.Steps[i].State = "uncertain"
				out.State = "uncertain"
			}
			return out
		}
		out.Steps[i].State = "executed"
		out.State = "executed"
	}
	actual, e := readTableDesign(qctx, conn, plan.Original.Database, plan.Original.Table)
	if e != nil {
		out.Error = fmt.Errorf("DDL completed, verification read failed: %w", e)
		out.State = "uncertain"
		return out
	}
	actual.RuntimeGeneration = plan.Original.RuntimeGeneration
	out.Actual = &actual
	if e = mysqlddl.Verify(plan.Desired, actual); e != nil {
		out.Error = e
		return out
	}
	if !valid() {
		out.State = "uncertain"
		out.Error = ErrMySQLNotConnected
		return out
	}
	out.Verified = true
	return out
}
