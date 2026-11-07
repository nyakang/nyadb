package engines

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) SchemaDesign() contract.SchemaDesignOperations {
	if s.kind == connections.CockroachDB {
		return contract.SchemaDesignOperations{}
	}
	if s.kind == connections.OpenGauss {
		return contract.SchemaDesignOperations{Snapshot: s.openGaussTableSnapshot,
			CanRename: func(t contract.SchemaDesignTarget) bool { return s.checkOpenGaussTable(t) == nil }, Rename: s.openGaussRenameTable,
			CanCopy: func(t contract.SchemaDesignTarget) bool { return s.checkOpenGaussTable(t) == nil }, Copy: s.openGaussCopyTable,
			CanDrop: func(t contract.SchemaDesignTarget) bool { return s.checkOpenGaussTable(t) == nil }, Drop: s.openGaussDropTable}
	}
	if s.kind == connections.PostgreSQL {
		return contract.SchemaDesignOperations{
			CanApply: func(t contract.SchemaDesignTarget) bool { return s.checkPostgresDesign(t) == nil },
			Snapshot: s.postgresDesignSnapshot, Apply: s.postgresDesignApply,
			CanDrop: func(t contract.SchemaDesignTarget) bool { return s.checkPostgresDesign(t) == nil }, Drop: s.postgresDrop,
			CanCopy: func(t contract.SchemaDesignTarget) bool { return s.checkPostgresDesign(t) == nil }, Copy: s.postgresCopy,
		}
	}
	if s.kind != connections.SQLite {
		return contract.SchemaDesignOperations{}
	}
	return contract.SchemaDesignOperations{CanApply: func(t contract.SchemaDesignTarget) bool { return s.checkDesign(t, true) == nil }, Snapshot: s.sqliteDesignSnapshot, Apply: s.sqliteDesignApply,
		CanDrop: func(t contract.SchemaDesignTarget) bool { return s.checkSQLiteDrop(t) == nil }, Drop: s.sqliteDesignDrop,
		CanCopy: func(t contract.SchemaDesignTarget) bool { return s.checkSQLiteDrop(t) == nil }, Copy: s.sqliteDesignCopy}
}
func (s *Session) checkDesign(t contract.SchemaDesignTarget, write bool) error {
	if t.Generation == 0 || t.Generation != s.generation || s.isClosed() {
		return contract.ErrRevoked
	}
	if t.Object.Kind != "table" {
		return schemaddl.ErrUnsupported
	}
	if err := s.check(t.Object); err != nil {
		return err
	}
	if err := schemaddl.Identifier(t.Object.Name); err != nil {
		return err
	}
	if write {
		d, ok := s.strategy.(sqliteDialect)
		if !ok {
			return schemaddl.ErrUnsupported
		}
		if d.options.ReadOnly {
			return errors.New("SQLite session is read-only")
		}
		for _, a := range d.options.Attachments {
			if a.Name == t.Object.Schema && a.ReadOnly {
				return errors.New("SQLite namespace is read-only")
			}
		}
	}
	return nil
}

func (s *Session) sqliteDesignSnapshot(ctx context.Context, t contract.SchemaDesignTarget) (snapshot contract.SchemaDesignSnapshot, err error) {
	if err := s.checkDesign(t, false); err != nil {
		return contract.SchemaDesignSnapshot{}, err
	}
	ctx, done := s.requestContext(ctx)
	defer done()
	release, err := s.schema.acquire(ctx, true)
	if err != nil {
		return snapshot, err
	}
	defer release()
	if err = s.sqliteLeaseTemporaryTriggers(ctx, t.Object.Name); err != nil {
		return snapshot, err
	}
	c, err := s.db.Conn(ctx)
	if err != nil {
		return contract.SchemaDesignSnapshot{}, err
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN"); err != nil {
		return contract.SchemaDesignSnapshot{}, err
	}
	settled := false
	defer func() {
		if !settled {
			if rollbackErr := rollbackDesign(c); rollbackErr != nil {
				discardDesign(c)
				err = errors.Join(err, rollbackErr)
			}
		}
	}()
	snapshot, err = sqliteSnapshot(ctx, c, t)
	if err != nil {
		return snapshot, err
	}
	_, err = c.ExecContext(ctx, "COMMIT")
	settled = err == nil
	return snapshot, err
}

type sqliteSchemaEntry struct {
	Type, Name, Table, SQL string
	Root                   int64
}

func sqliteSnapshot(ctx context.Context, q queryer, t contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
	snap := contract.SchemaDesignSnapshot{Target: t, Schema: schemaddl.Snapshot{Engine: "sqlite", Table: schemaddl.Table{Name: t.Object.Name}}}
	var version int64
	rows, err := q.QueryContext(ctx, "PRAGMA "+quote(t.Object.Schema)+".schema_version")
	if err != nil {
		return snap, err
	}
	if !rows.Next() {
		rows.Close()
		return snap, errors.New("schema version absent")
	}
	err = rows.Scan(&version)
	rows.Close()
	if err != nil {
		return snap, err
	}
	rows, err = q.QueryContext(ctx, "SELECT type,name,tbl_name,rootpage,coalesce(sql,'') FROM "+quote(t.Object.Schema)+".sqlite_schema ORDER BY type,name")
	if err != nil {
		return snap, err
	}
	var entries []sqliteSchemaEntry
	bytes := 0
	for rows.Next() {
		var e sqliteSchemaEntry
		if err = rows.Scan(&e.Type, &e.Name, &e.Table, &e.Root, &e.SQL); err != nil {
			rows.Close()
			return snap, err
		}
		bytes += len(e.SQL) + len(e.Name) + len(e.Table)
		if len(entries) >= 10000 || bytes > 16<<20 || len(e.SQL) > 1<<20 {
			rows.Close()
			return snap, errors.New("complete schema snapshot exceeds safety limits")
		}
		entries = append(entries, e)
		if strings.EqualFold(e.Name, t.Object.Name) && (e.Type == "table" || e.Type == "view") {
			if e.Name != t.Object.Name || e.Type != "table" {
				rows.Close()
				return snap, errors.New("table identity is occupied by another object")
			}
			snap.Schema.Exists, snap.Schema.DDL = true, e.SQL
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return snap, err
	}
	snap.Schema.Revision = schemaddl.Signature(struct {
		Version int64
		Entries []sqliteSchemaEntry
	}{version, entries})
	if !snap.Schema.Exists {
		return snap, nil
	}
	snap.Schema.Table, err = schemaddl.ParseSQLite(snap.Schema.DDL)
	if err != nil {
		return snap, err
	}
	for _, e := range entries {
		if e.Table == t.Object.Name && (e.Type == "index" || e.Type == "trigger") && e.SQL != "" {
			snap.Schema.Table.Definitions = append(snap.Schema.Table.Definitions, schemaddl.Definition{Name: e.Name, Kind: e.Type, SQL: e.SQL})
		}
	}
	// Temporary triggers are connection-local and absent from the persistent
	// catalog; silently losing them during DROP/CREATE is never acceptable.
	rows, err = q.QueryContext(ctx, "SELECT name FROM temp.sqlite_schema WHERE type='trigger' AND tbl_name=?", t.Object.Name)
	if err != nil {
		return snap, err
	}
	hasTemp := rows.Next()
	err = rows.Err()
	rows.Close()
	if hasTemp {
		return snap, errors.New("table has temporary triggers; close them before designing")
	}
	return snap, err
}

func rollbackDesign(c *sql.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := c.ExecContext(ctx, "ROLLBACK")
	return err
}
func discardDesign(c *sql.Conn) { _ = c.Raw(func(any) error { return driver.ErrBadConn }) }

func (s *Session) sqliteDesignApply(ctx context.Context, request contract.SchemaDesignRequest) (out contract.SchemaDesignOutcome) {
	out.State = "not_executed"
	r := request.Clone()
	t := r.Original.Target
	if err := s.checkDesign(t, true); err != nil {
		out.Error = err
		return
	}
	if r.Original.Schema.Table.Name != t.Object.Name {
		out.Error = errors.New("draft original identity differs from captured table")
		return
	}
	plan, err := schemaddl.PlanSQLite(r.Original.Schema, r.Desired, t.Object.Schema, r.AllowColumnDrop)
	if err != nil {
		out.Error = err
		return
	}
	ctx, done := s.requestContext(ctx)
	defer done()
	release, err := s.schema.acquire(ctx, true)
	if err != nil {
		out.Error = err
		return
	}
	defer release()
	if err = s.sqliteLeaseTemporaryTriggers(ctx, t.Object.Name); err != nil {
		out.Error = err
		return
	}
	c, err := s.db.Conn(ctx)
	if err != nil {
		out.Error = err
		return
	}
	defer c.Close()
	var foreignKeys, legacy int
	if err = c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		out.Error = err
		return
	}
	if err = c.QueryRowContext(ctx, "PRAGMA legacy_alter_table").Scan(&legacy); err != nil {
		out.Error = err
		return
	}
	// Restoration runs even after cancellation. A connection with unknown
	// transaction/PRAGMA state is discarded rather than returned to the pool.
	defer func() {
		restore, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, a := c.ExecContext(restore, fmt.Sprintf("PRAGMA foreign_keys=%d", foreignKeys))
		_, b := c.ExecContext(restore, fmt.Sprintf("PRAGMA legacy_alter_table=%d", legacy))
		var actualFK, actualLegacy int
		d := c.QueryRowContext(restore, "PRAGMA foreign_keys").Scan(&actualFK)
		e := c.QueryRowContext(restore, "PRAGMA legacy_alter_table").Scan(&actualLegacy)
		if a != nil || b != nil || d != nil || e != nil || actualFK != foreignKeys || actualLegacy != legacy {
			discardDesign(c)
			out.Error = errors.Join(out.Error, errors.New("design connection discarded after state restoration failed"), a, b, d, e)
		}
	}()
	if plan.Mode == "rebuild" {
		if _, err = c.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err == nil {
			_, err = c.ExecContext(ctx, "PRAGMA legacy_alter_table=ON")
		}
	} else {
		// Native rename must rewrite dependent FK/trigger/view SQL regardless of
		// a user's legacy_alter_table preference.
		_, err = c.ExecContext(ctx, "PRAGMA legacy_alter_table=OFF")
	}
	if err != nil {
		out.Error = err
		return
	}
	if _, err = c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		out.Error = err
		return
	}
	settled := false
	defer func() {
		if !settled {
			if rollbackErr := rollbackDesign(c); rollbackErr != nil {
				discardDesign(c)
				out.State, out.Verified = "uncertain", false
				out.Error = errors.Join(out.Error, ErrUncertain, rollbackErr)
			} else {
				out.State = "rolled_back"
			}
		}
	}()
	actual, err := sqliteSnapshot(ctx, c, t)
	if err != nil {
		out.Error = err
		return
	}
	if actual.Schema.Revision != r.Original.Schema.Revision || schemaddl.Signature(actual.Schema) != schemaddl.Signature(r.Original.Schema) {
		out.Error = schemaddl.ErrConflict
		return
	}
	if plan.Mode == "noop" {
		if _, err = c.ExecContext(ctx, "ROLLBACK"); err != nil {
			out.Error = err
			return
		}
		settled = true
		out.Actual, out.Verified = &actual, true
		return
	}
	if err = sqliteForeignKeyCheck(ctx, c, t.Object.Schema); err != nil {
		out.Error = err
		return
	}
	if plan.Mode == "rebuild" {
		out.Rows, err = sqliteRebuild(ctx, c, t.Object, actual.Schema.Table, r.Desired, plan)
	} else {
		for _, statement := range plan.SQL {
			if _, err = c.ExecContext(ctx, statement); err != nil {
				break
			}
		}
	}
	if err != nil {
		out.Error = err
		return
	}
	if err = sqliteValidateDependents(ctx, c, t.Object.Schema, r.Desired.Name); err == nil {
		err = sqliteForeignKeyCheck(ctx, c, t.Object.Schema)
	}
	if err != nil {
		out.Error = err
		return
	}
	newTarget := t
	newTarget.Object.Name = r.Desired.Name
	verified, err := sqliteSnapshot(ctx, c, newTarget)
	if err != nil || !verified.Schema.Exists {
		out.Error = errors.Join(err, errors.New("changed table could not be verified"))
		return
	}
	if plan.Mode == "create" || plan.Mode == "rebuild" || plan.Mode == "definitions" {
		if !sqliteDesiredMatches(verified.Schema.Table, r.Desired, t.Object.Schema) {
			out.Error = errors.New("server schema differs from requested table")
			return
		}
	}
	// Commit is the final database operation. Cancellation waits for this
	// acknowledgement; failures roll back or report an uncertain outcome.
	if _, err = c.ExecContext(ctx, "COMMIT"); err != nil {
		out.Error = err
		return
	}
	settled = true
	out.State, out.Verified, out.Actual = "committed", true, &verified
	return
}

func sqliteDesiredMatches(actual, desired schemaddl.Table, schema string) bool {
	// SQLite deparses only the CREATE prefix/object qualification. Reparse a
	// canonical render so identifiers and generated clause IDs normalize alike.
	canonical, err := schemaddl.CreateSQLite(desired, schema, desired.Name)
	if err != nil {
		return false
	}
	parsed, err := schemaddl.ParseSQLite(canonical)
	if err != nil {
		return false
	}
	if len(actual.Clauses) != len(parsed.Clauses) {
		return false
	}
	for i := range actual.Clauses {
		actual.Clauses[i].ID = parsed.Clauses[i].ID
	}
	if len(actual.Definitions) != len(desired.Definitions) {
		return false
	}
	definitions := map[string]string{}
	for _, definition := range desired.Definitions {
		statement, err := schemaddl.DefinitionSQLite(definition, schema, desired.Name)
		if err != nil {
			return false
		}
		definitions[definition.Kind+":"+definition.Name] = strings.TrimSuffix(strings.TrimSpace(statement), ";")
	}
	for _, definition := range actual.Definitions {
		statement, err := schemaddl.DefinitionSQLite(definition, schema, actual.Name)
		if err != nil || strings.TrimSuffix(strings.TrimSpace(statement), ";") != definitions[definition.Kind+":"+definition.Name] {
			return false
		}
	}
	actual.Definitions, parsed.Definitions = nil, nil
	return schemaddl.Signature(actual) == schemaddl.Signature(parsed)
}

func sqliteForeignKeyCheck(ctx context.Context, c *sql.Conn, schema string) error {
	rows, err := c.QueryContext(ctx, "PRAGMA "+quote(schema)+".foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("schema change has foreign key violations")
	}
	return rows.Err()
}
func sqliteValidateDependents(ctx context.Context, c *sql.Conn, schema, table string) error {
	rows, err := c.QueryContext(ctx, "SELECT name FROM "+quote(schema)+".sqlite_schema WHERE type='view' ORDER BY name")
	if err != nil {
		return err
	}
	var views []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		views = append(views, name)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, name := range views {
		rows, err = c.QueryContext(ctx, "SELECT * FROM "+quote(schema)+"."+quote(name)+" LIMIT 0")
		if err != nil {
			return fmt.Errorf("dependent view cannot be read: %w", err)
		}
		rows.Close()
	}
	return sqliteValidateTablePrograms(ctx, c, schema, table)
}

func sqliteValidateTablePrograms(ctx context.Context, c *sql.Conn, schema, table string) error {
	info, err := sqliteDesignColumns(ctx, c, schema, table)
	if err != nil {
		return err
	}
	columns := schemaddl.Table{}
	for _, col := range info {
		columns.Clauses = append(columns.Clauses, schemaddl.Clause{Kind: "column", Name: col.Name})
	}
	rows, err := c.QueryContext(ctx, "SELECT name,sql FROM "+quote(schema)+".sqlite_schema WHERE type='trigger' AND tbl_name=?", table)
	if err != nil {
		return err
	}
	for rows.Next() {
		definition := schemaddl.Definition{Kind: "trigger"}
		if err = rows.Scan(&definition.Name, &definition.SQL); err == nil {
			err = schemaddl.ValidateSQLiteTriggerColumns(definition, columns)
		}
		if err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	statements := []string{"EXPLAIN INSERT INTO " + quote(schema) + "." + quote(table) + " DEFAULT VALUES", "EXPLAIN DELETE FROM " + quote(schema) + "." + quote(table)}
	var updates []string
	for _, col := range info {
		if col.Hidden == 0 {
			updates = append(updates, quote(col.Name)+"="+quote(col.Name))
		}
	}
	if len(updates) > 0 {
		statements = append(statements, "EXPLAIN UPDATE "+quote(schema)+"."+quote(table)+" SET "+strings.Join(updates, ","))
	}
	for _, statement := range statements {
		rows, err = c.QueryContext(ctx, statement)
		if err != nil {
			return fmt.Errorf("table trigger cannot be compiled: %w", err)
		}
		rows.Close()
	}
	return nil
}

func sqliteShadowName() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "__nyadb_rebuild_" + hex.EncodeToString(b[:]), nil
}

// Called while the session's schema writer is held. Arbitrary SQL and newly
// reserved leases cannot create temporary triggers until the writer releases.
// Lease.Close may still revoke its own captured physical connection.
func (s *Session) sqliteLeaseTemporaryTriggers(ctx context.Context, table string) error {
	s.mu.Lock()
	leases := make([]*Lease, 0, len(s.leases))
	for l := range s.leases {
		leases = append(leases, l)
	}
	s.mu.Unlock()
	for _, l := range leases {
		l.mu.Lock()
		closed := l.closed
		c := l.c
		l.mu.Unlock()
		if closed {
			continue
		}
		inTransaction, err := sqliteLeaseHasTransaction(ctx, c)
		if err != nil {
			l.mu.Lock()
			closed = l.closed
			l.mu.Unlock()
			if closed && ctx.Err() == nil {
				continue
			}
			return err
		}
		if inTransaction {
			return errors.New("SQL session has an open transaction; finish it before designing")
		}
		var count int
		err = c.QueryRowContext(ctx, "SELECT count(*) FROM temp.sqlite_schema WHERE type='trigger' AND tbl_name=?", table).Scan(&count)
		if err != nil {
			l.mu.Lock()
			closed = l.closed
			l.mu.Unlock()
			if closed && ctx.Err() == nil {
				continue
			}
			return err
		}
		if count > 0 {
			return errors.New("table has temporary triggers in a SQL session; remove them before designing")
		}
	}
	return ctx.Err()
}

// SQLite ignores foreign_keys changes inside a transaction. Under the schema
// gate this reversible probe detects manual BEGIN/SAVEPOINT on a SQL lease,
// without relying on driver internals or committing/rolling back user work.
// Otherwise a writer waiting on that transaction would prevent its COMMIT
// query from acquiring the reader gate.
func sqliteLeaseHasTransaction(ctx context.Context, c *sql.Conn) (active bool, err error) {
	var before, after int
	if err = c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&before); err != nil {
		return
	}
	defer func() {
		restore, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, restoreErr := c.ExecContext(restore, fmt.Sprintf("PRAGMA foreign_keys=%d", before))
		var current int
		readErr := c.QueryRowContext(restore, "PRAGMA foreign_keys").Scan(&current)
		if restoreErr != nil || readErr != nil || current != before {
			discardDesign(c)
			err = errors.Join(err, errors.New("SQL connection discarded after schema probe restoration failed"), restoreErr, readErr)
		}
	}()
	if _, err = c.ExecContext(ctx, fmt.Sprintf("PRAGMA foreign_keys=%d", 1-before)); err != nil {
		return
	}
	if err = c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&after); err != nil {
		return
	}
	active = after == before
	return
}
