package engines

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func (s *Session) checkPostgresView(t contract.SchemaDesignTarget) error {
	if t.Object.Kind != "view" {
		return contract.ErrUnsupported
	}
	t.Object.Kind = "table"
	return s.checkPostgresDesign(t)
}
func postgresViewSnapshot(ctx context.Context, c *sql.Conn, t contract.SchemaDesignTarget) (contract.ViewDesignSnapshot, error) {
	out := contract.ViewDesignSnapshot{Target: t, Schema: schemaddl.ViewSnapshot{Engine: "postgresql", View: schemaddl.View{Schema: t.Object.Schema, Name: t.Object.Name}}}
	var version int
	if e := c.QueryRowContext(ctx, "SHOW server_version_num").Scan(&version); e != nil {
		return out, e
	}
	if version < 140000 || version >= 190000 {
		return out, contract.ErrUnsupported
	}
	// Full relation, attributes, rewrite rules and triggers bind the original
	// revision. CREATE OR REPLACE preserves the existing relation and its users.
	var raw string
	e := c.QueryRowContext(ctx, `SELECT jsonb_build_object('relation',to_jsonb(c),'definition',CASE WHEN c.relkind='v' THEN pg_get_viewdef(c.oid,false) ELSE '' END,'columns',(SELECT jsonb_agg(to_jsonb(a) ORDER BY attnum) FROM pg_attribute a WHERE a.attrelid=c.oid),'rules',(SELECT jsonb_agg(to_jsonb(r) ORDER BY oid) FROM pg_rewrite r WHERE r.ev_class=c.oid),'triggers',(SELECT jsonb_agg(to_jsonb(tr) ORDER BY oid) FROM pg_trigger tr WHERE tr.tgrelid=c.oid))::text FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2`, t.Object.Schema, t.Object.Name).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		out.Schema.Revision = schemaddl.Signature(t.Object)
		return out, nil
	}
	if e != nil {
		return out, e
	}
	if len(raw) > 16<<20 {
		return out, contract.ErrUnsupported
	}
	var data struct {
		Relation struct {
			Kind string `json:"relkind"`
		}
		Definition string
		Columns    []struct {
			Name    string `json:"attname"`
			Number  int    `json:"attnum"`
			Dropped bool   `json:"attisdropped"`
		}
	}
	if e = json.Unmarshal([]byte(raw), &data); e != nil {
		return out, e
	}
	if data.Relation.Kind != "v" {
		return out, errors.New("only ordinary PostgreSQL views can be changed")
	}
	out.Schema.Exists = true
	out.Schema.Revision = schemaddl.Signature(raw)
	out.Schema.View.SelectSQL = strings.TrimSuffix(strings.TrimSpace(data.Definition), ";")
	for _, a := range data.Columns {
		if a.Number > 0 && !a.Dropped {
			out.Schema.View.Columns = append(out.Schema.View.Columns, a.Name)
		}
	}
	out.Schema.DDL, e = schemaddl.CreatePostgreSQLView(out.Schema.View, t.Object.Schema)
	return out, e
}
func (s *Session) readPostgresView(ctx context.Context, t contract.SchemaDesignTarget) (contract.ViewDesignSnapshot, error) {
	if e := s.checkPostgresView(t); e != nil {
		return contract.ViewDesignSnapshot{}, e
	}
	ctx, cancel := s.requestContext(ctx)
	defer cancel()
	c, e := s.db.Conn(ctx)
	if e != nil {
		return contract.ViewDesignSnapshot{}, e
	}
	defer c.Close()
	return postgresViewSnapshot(ctx, c, t)
}
func (s *Session) applyPostgresView(ctx context.Context, r contract.ViewDesignRequest) (out contract.ViewDesignOutcome) {
	out.State = "not_executed"
	r = r.Clone()
	t := r.Original.Target
	if e := s.checkPostgresView(t); e != nil {
		out.Error = e
		return
	}
	p, e := schemaddl.PlanPostgreSQLView(r.Original.Schema, r.Action, r.Desired, t.Object.Schema)
	if e != nil {
		out.Error = e
		return
	}
	ctx, cancel := s.requestContext(ctx)
	defer cancel()
	c, e := s.db.Conn(ctx)
	if e != nil {
		out.Error = e
		return
	}
	defer c.Close()
	if _, e = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on"); e != nil {
		out.Error = e
		return
	}
	settled, commit := false, false
	defer func() {
		if !settled {
			rb := rollbackDesign(c)
			if commit || rb != nil {
				discardDesign(c)
				out.State = "uncertain"
				out.Error = errors.Join(out.Error, rb)
			} else {
				out.State = "rolled_back"
			}
		}
	}()
	var hooks bool
	if e = c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_event_trigger WHERE evtenabled<>'D')").Scan(&hooks); e != nil || hooks {
		out.Error = errors.Join(e, contract.ErrUnsupported)
		return
	}
	if r.Original.Schema.Exists {
		if _, e = c.ExecContext(ctx, "LOCK TABLE "+qualified(t.Object)+" IN ACCESS EXCLUSIVE MODE"); e != nil {
			out.Error = e
			return
		}
	}
	actual, e := postgresViewSnapshot(ctx, c, t)
	if e != nil {
		out.Error = e
		return
	}
	if schemaddl.Signature(actual.Schema) != schemaddl.Signature(r.Original.Schema) {
		out.Error = schemaddl.ErrConflict
		return
	}
	for _, statement := range p.SQL {
		if _, e = c.ExecContext(ctx, statement); e != nil {
			out.Error = e
			return
		}
	}
	if r.Action != "drop" {
		t.Object.Name = r.Desired.Name
	}
	verified, e := postgresViewSnapshot(ctx, c, t)
	if e != nil || verified.Schema.Exists != (r.Action != "drop") {
		out.Error = errors.Join(e, schemaddl.ErrConflict)
		return
	}
	commit = true
	if _, e = c.ExecContext(ctx, "COMMIT"); e != nil {
		out.Error = e
		return
	}
	settled = true
	out.State, out.Verified, out.Actual = "committed", true, &verified
	return
}
