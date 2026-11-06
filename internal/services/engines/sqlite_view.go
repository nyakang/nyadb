package engines

import (
	"context"
	"database/sql"
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) ViewDesign() contract.ViewDesignOperations {
	if s.kind == connections.OpenGauss {
		return contract.ViewDesignOperations{CanApply: func(t contract.SchemaDesignTarget) bool { return s.checkOpenGaussView(t) == nil }, Snapshot: s.readOpenGaussView, Apply: s.applyOpenGaussView}
	}
	if s.kind == connections.PostgreSQL {
		return contract.ViewDesignOperations{CanApply: func(t contract.SchemaDesignTarget) bool { return s.checkPostgresView(t) == nil }, Snapshot: s.readPostgresView, Apply: s.applyPostgresView}
	}
	if s.kind != connections.SQLite {
		return contract.ViewDesignOperations{}
	}
	return contract.ViewDesignOperations{CanApply: func(t contract.SchemaDesignTarget) bool { return s.checkSQLiteView(t, true) == nil }, Snapshot: s.sqliteViewSnapshot, Apply: s.sqliteViewApply}
}
func (s *Session) checkSQLiteView(t contract.SchemaDesignTarget, write bool) error {
	if t.Object.Kind != "view" || s.kind != connections.SQLite || len(t.Object.Name) >= 7 && sqliteCopyIdentifierKey(t.Object.Name[:7]) == "sqlite_" {
		return schemaddl.ErrUnsupported
	}
	// Use the established namespace, read-only and original-generation checks.
	copy := t
	copy.Object.Kind = "table"
	return s.checkDesign(copy, write)
}

func sqliteReadView(ctx context.Context, c *sql.Conn, t contract.SchemaDesignTarget) (contract.ViewDesignSnapshot, error) {
	out := contract.ViewDesignSnapshot{Target: t, Schema: schemaddl.ViewSnapshot{Engine: "sqlite", View: schemaddl.View{Schema: t.Object.Schema, Name: t.Object.Name}}}
	// An empty name is used only by the private catalog reader: no catalog
	// object can have that name. It captures the same bounded complete revision
	// as table operations without interpreting a view as a table.
	probe := t
	probe.Object.Name = ""
	revision, err := sqliteSnapshot(ctx, c, probe)
	if err != nil {
		return out, err
	}
	out.Schema.Revision = revision.Schema.Revision
	rows, err := c.QueryContext(ctx, "SELECT type,name,coalesce(sql,'') FROM "+quote(t.Object.Schema)+".sqlite_schema WHERE name=? COLLATE NOCASE", t.Object.Name)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var kind, name, ddl string
		if err = rows.Scan(&kind, &name, &ddl); err != nil {
			break
		}
		if kind != "view" || name != t.Object.Name || out.Schema.Exists {
			err = errors.New("view identity is occupied by another object")
			break
		}
		view, e := schemaddl.ParseSQLiteView(ddl)
		if e != nil {
			err = e
			break
		}
		if view.Name != name || view.Schema != "" && view.Schema != t.Object.Schema {
			err = errors.New("stored view definition differs from its original identity")
			break
		}
		view.Schema = t.Object.Schema
		out.Schema.Exists, out.Schema.DDL, out.Schema.View = true, ddl, view
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil || !out.Schema.Exists {
		return out, err
	}
	rows, err = c.QueryContext(ctx, "SELECT name,sql FROM "+quote(t.Object.Schema)+".sqlite_schema WHERE type='trigger' AND tbl_name=? ORDER BY name", t.Object.Name)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var d schemaddl.Definition
		d.Kind = "trigger"
		if err = rows.Scan(&d.Name, &d.SQL); err != nil {
			break
		}
		if _, err = schemaddl.DefinitionSQLite(d, t.Object.Schema, t.Object.Name); err != nil {
			break
		}
		out.Schema.View.Triggers = append(out.Schema.View.Triggers, d)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	return out, err
}

func (s *Session) sqliteViewNoTemporary(ctx context.Context) error {
	s.mu.Lock()
	var leases []*Lease
	for l := range s.leases {
		leases = append(leases, l)
	}
	s.mu.Unlock()
	for _, l := range leases {
		l.mu.Lock()
		closed, c := l.closed, l.c
		l.mu.Unlock()
		if closed {
			continue
		}
		if err := sqliteDropNoTemporaryPrograms(ctx, c); err != nil {
			l.mu.Lock()
			closed = l.closed
			l.mu.Unlock()
			if closed && ctx.Err() == nil {
				continue
			}
			return err
		}
	}
	return nil
}

func (s *Session) sqliteViewSnapshot(ctx context.Context, t contract.SchemaDesignTarget) (out contract.ViewDesignSnapshot, err error) {
	if err = s.checkSQLiteView(t, false); err != nil {
		return out, err
	}
	ctx, done := s.requestContext(ctx)
	defer done()
	release, err := s.schema.acquire(ctx, true)
	if err != nil {
		return out, err
	}
	defer release()
	if err = s.sqliteLeaseTemporaryTriggers(ctx, t.Object.Name); err != nil {
		return out, err
	}
	if err = s.sqliteViewNoTemporary(ctx); err != nil {
		return out, err
	}
	c, err := s.db.Conn(ctx)
	if err != nil {
		return out, err
	}
	defer c.Close()
	if err = sqliteDropNoTemporaryPrograms(ctx, c); err != nil {
		return out, err
	}
	if _, err = c.ExecContext(ctx, "BEGIN"); err != nil {
		return out, err
	}
	settled := false
	defer func() {
		if !settled {
			if e := rollbackDesign(c); e != nil {
				discardDesign(c)
				err = errors.Join(err, e)
			}
		}
	}()
	out, err = sqliteReadView(ctx, c, t)
	if err != nil {
		return out, err
	}
	_, err = c.ExecContext(ctx, "COMMIT")
	settled = err == nil
	return out, err
}

func (s *Session) sqliteViewApply(ctx context.Context, request contract.ViewDesignRequest) (out contract.ViewDesignOutcome) {
	out.State = "not_executed"
	r := request.Clone()
	t := r.Original.Target
	if err := s.checkSQLiteView(t, true); err != nil {
		out.Error = err
		return
	}
	if r.Original.Schema.View.Name != t.Object.Name || r.Original.Schema.View.Schema != t.Object.Schema {
		out.Error = errors.New("view snapshot differs from captured original scope")
		return
	}
	p, err := schemaddl.PlanSQLiteView(r.Original.Schema, r.Action, r.Desired, t.Object.Schema)
	if err != nil {
		out.Error = err
		return
	}
	if r.Action == "create" && p.Desired.Name != t.Object.Name {
		out.Error = errors.New("create view target differs from its original absence snapshot")
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
	if err = s.sqliteViewNoTemporary(ctx); err != nil {
		out.Error = err
		return
	}
	c, err := s.db.Conn(ctx)
	if err != nil {
		out.Error = err
		return
	}
	defer c.Close()
	if err = sqliteDropNoTemporaryPrograms(ctx, c); err != nil {
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
			if e := rollbackDesign(c); e != nil {
				discardDesign(c)
				out.State = "uncertain"
				out.Error = errors.Join(out.Error, ErrUncertain, e)
			} else {
				out.State = "rolled_back"
			}
		}
	}()
	original, err := sqliteReadView(ctx, c, t)
	if err != nil {
		out.Error = err
		return
	}
	if schemaddl.Signature(original.Schema) != schemaddl.Signature(r.Original.Schema) {
		out.Error = schemaddl.ErrConflict
		return
	}
	if r.Action == "rename" {
		destination := t
		destination.Object.Name = p.Desired.Name
		absence, e := sqliteReadView(ctx, c, destination)
		if e != nil || absence.Schema.Exists {
			out.Error = errors.Join(e, errors.New("view rename destination already exists; no object will be overwritten"))
			return
		}
	}
	// Compile other stored views/table programs before and after mutation. This
	// validates dependencies without running their SELECT expressions or DML.
	if err = sqliteDropDependencies(ctx, c, t.Object.Schema, t.Object.Name); err != nil {
		out.Error = err
		return
	}
	for _, statement := range p.SQL {
		if _, err = c.ExecContext(ctx, statement); err != nil {
			out.Error = err
			return
		}
	}
	if err = sqliteDropDependencies(ctx, c, t.Object.Schema, ""); err != nil {
		out.Error = err
		return
	}
	actualTarget := t
	if r.Action != "drop" {
		actualTarget.Object.Name = p.Desired.Name
	}
	actual, err := sqliteReadView(ctx, c, actualTarget)
	if err != nil {
		out.Error = err
		return
	}
	if r.Action == "drop" {
		if actual.Schema.Exists {
			out.Error = errors.New("dropped view is still present")
			return
		}
	} else if !actual.Schema.Exists || !schemaddl.SQLiteViewMatches(actual.Schema.View, p.Desired, t.Object.Schema) {
		out.Error = errors.New("view definition or preserved triggers could not be verified")
		return
	}
	if r.Action == "rename" {
		old, e := sqliteReadView(ctx, c, t)
		if e != nil || old.Schema.Exists {
			out.Error = errors.Join(e, errors.New("renamed original view is still present"))
			return
		}
	}
	if _, err = c.ExecContext(ctx, "COMMIT"); err != nil {
		out.Error = err
		return
	}
	settled = true
	out.State, out.Verified, out.Actual = "committed", true, &actual
	return
}
