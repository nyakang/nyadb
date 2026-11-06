package engines

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// Native catalogs use row_to_json, not PostgreSQL's to_jsonb/to_regclass.
// One statement captures the complete view, permissions and dependency scope.
const openGaussViewCatalogSQL = `SELECT CASE WHEN octet_length(row_to_json(v)::text)<=16777216 THEN row_to_json(v)::text END FROM (
 SELECT d.oid::bigint AS database_oid,d.datname AS database,n.oid::bigint AS namespace_oid,row_to_json(n) AS namespace,
 r.oid::bigint AS role_oid,has_schema_privilege(n.oid,'CREATE') AS can_create,
 COALESCE(pg_has_role(current_user,c.relowner,'USAGE') OR r.rolsuper OR r.rolsystemadmin,false) AS can_manage,
 COALESCE(c.oid::bigint,0) AS relation_oid,row_to_json(c) AS relation,
 CASE WHEN c.relkind='v' THEN pg_get_viewdef(c.oid,false) ELSE '' END AS definition,
 (SELECT json_agg(row_to_json(a) ORDER BY a.attnum) FROM pg_attribute a WHERE a.attrelid=c.oid) AS columns,
 (SELECT json_agg(row_to_json(x) ORDER BY x.oid) FROM (SELECT p.oid::bigint AS oid,row_to_json(p) AS data FROM pg_type p WHERE p.oid=c.reltype OR p.typelem=c.reltype) x) AS types,
 (SELECT json_agg(row_to_json(x) ORDER BY x.oid) FROM (SELECT w.oid::bigint AS oid,row_to_json(w) AS data FROM pg_rewrite w WHERE w.ev_class=c.oid) x) AS rules,
 (SELECT json_agg(row_to_json(x) ORDER BY x.oid) FROM (SELECT tr.oid::bigint AS oid,row_to_json(tr) AS data FROM pg_trigger tr WHERE tr.tgrelid=c.oid) x) AS triggers,
 (SELECT json_agg(row_to_json(x) ORDER BY x.classoid,x.objoid,x.objsubid) FROM pg_description x WHERE x.classoid='pg_class'::regclass AND x.objoid=c.oid OR x.classoid='pg_type'::regclass AND x.objoid IN(SELECT p.oid FROM pg_type p WHERE p.oid=c.reltype OR p.typelem=c.reltype)) AS comments,
 (SELECT json_agg(row_to_json(x) ORDER BY x.classid,x.objid,x.objsubid,x.refclassid,x.refobjid,x.refobjsubid,x.deptype) FROM pg_depend x
  WHERE (x.refclassid='pg_class'::regclass AND x.refobjid=c.oid OR x.refclassid='pg_type'::regclass AND x.refobjid IN(SELECT p.oid FROM pg_type p WHERE p.oid=c.reltype OR p.typelem=c.reltype))
  AND NOT(x.classid='pg_rewrite'::regclass AND x.objid IN(SELECT w.oid FROM pg_rewrite w WHERE w.ev_class=c.oid))
  AND NOT(x.classid='pg_type'::regclass AND x.objid IN(SELECT p.oid FROM pg_type p WHERE p.oid=c.reltype OR p.typelem=c.reltype))) AS incoming,
 (SELECT json_agg(row_to_json(x) ORDER BY x.classid,x.objid,x.objsubid,x.refclassid,x.refobjid,x.refobjsubid,x.deptype) FROM pg_depend x
  WHERE x.classid='pg_class'::regclass AND x.objid=c.oid OR x.classid='pg_rewrite'::regclass AND x.objid IN(SELECT w.oid FROM pg_rewrite w WHERE w.ev_class=c.oid)
  OR x.classid='pg_type'::regclass AND x.objid IN(SELECT p.oid FROM pg_type p WHERE p.oid=c.reltype OR p.typelem=c.reltype)) AS outgoing,
 (SELECT json_agg(row_to_json(x) ORDER BY x.object_type) FROM pg_object x WHERE x.object_oid=c.oid) AS objects,
 EXISTS(SELECT 1 FROM pg_event_trigger WHERE evtenabled<>'D') AS hooks
 FROM pg_database d CROSS JOIN pg_roles r CROSS JOIN pg_namespace n LEFT JOIN pg_class c ON c.relnamespace=n.oid AND c.relname=$2
 WHERE d.datname=current_database() AND r.rolname=current_user AND n.nspname=$1
) v`

type openGaussViewRecord struct {
	DatabaseOID  uint64          `json:"database_oid"`
	Database     string          `json:"database"`
	NamespaceOID uint64          `json:"namespace_oid"`
	Namespace    json.RawMessage `json:"namespace"`
	RoleOID      uint64          `json:"role_oid"`
	CanCreate    bool            `json:"can_create"`
	CanManage    bool            `json:"can_manage"`
	RelationOID  uint64          `json:"relation_oid"`
	Relation     json.RawMessage `json:"relation"`
	Definition   string          `json:"definition"`
	Columns      []json.RawMessage
	Types        []json.RawMessage
	Rules        []json.RawMessage
	Triggers     []json.RawMessage
	Comments     []json.RawMessage
	Incoming     []json.RawMessage
	Outgoing     []json.RawMessage
	Objects      []json.RawMessage
	Hooks        bool
}

func (s *Session) checkOpenGaussView(t contract.SchemaDesignTarget) error {
	if s.kind != connections.OpenGauss || t.Object.Kind != "view" {
		return contract.ErrUnsupported
	}
	if s.isClosed() || t.Generation == 0 || t.Generation != s.generation {
		return contract.ErrRevoked
	}
	if err := s.check(t.Object); err != nil {
		return err
	}
	if err := schemaddl.PostgreSQLIdentifier(t.Object.Schema); err != nil {
		return err
	}
	if err := schemaddl.PostgreSQLIdentifier(t.Object.Name); err != nil {
		return err
	}
	n := t.Object.Schema
	if strings.HasPrefix(n, "pg_") || strings.HasPrefix(n, "dbe_") {
		return contract.ErrUnsupported
	}
	switch n {
	case "information_schema", "blockchain", "coverage", "cstore", "db4ai", "pkg_service", "snapshot", "sqladvisor":
		return contract.ErrUnsupported
	}
	return nil
}

func openGaussReadView(ctx context.Context, c *sql.Conn, t contract.SchemaDesignTarget) (out contract.ViewDesignSnapshot, record openGaussViewRecord, err error) {
	defer func() {
		if err != nil {
			out = contract.ViewDesignSnapshot{}
		}
	}()
	var raw sql.NullString
	if err = c.QueryRowContext(ctx, openGaussViewCatalogSQL, t.Object.Schema, t.Object.Name).Scan(&raw); err != nil {
		return
	}
	if !raw.Valid || json.Unmarshal([]byte(raw.String), &record) != nil || record.Database != t.Object.Catalog || record.DatabaseOID == 0 || record.NamespaceOID == 0 || record.RoleOID == 0 || len(record.Columns) > 10000 || len(record.Definition) > 1<<20 || openGaussViewFingerprint(record) == "" {
		err = errors.New("invalid or excessive openGauss view catalog")
		return
	}
	out = contract.ViewDesignSnapshot{Target: t, Schema: schemaddl.ViewSnapshot{Engine: "opengauss", Revision: openGaussViewFingerprint(record), Exists: record.RelationOID != 0, View: schemaddl.View{Schema: t.Object.Schema, Name: t.Object.Name}}}
	if !out.Schema.Exists {
		return
	}
	var relation struct {
		Kind    string   `json:"relkind"`
		Name    string   `json:"relname"`
		Options []string `json:"reloptions"`
	}
	if err = json.Unmarshal(record.Relation, &relation); err != nil {
		return
	}
	if relation.Kind != "v" || relation.Name != t.Object.Name {
		err = contract.ErrUnsupported
		return
	}
	if !record.CanManage {
		err = errors.New("only the owner or an administrator can manage this view")
		return
	}
	for _, raw := range record.Columns {
		var a struct {
			Number  int    `json:"attnum"`
			Name    string `json:"attname"`
			Dropped bool   `json:"attisdropped"`
		}
		if err = json.Unmarshal(raw, &a); err != nil {
			return
		}
		if a.Number > 0 && !a.Dropped {
			out.Schema.View.Columns = append(out.Schema.View.Columns, a.Name)
		}
	}
	out.Schema.View.SelectSQL = strings.TrimSuffix(strings.TrimSpace(record.Definition), ";")
	out.Schema.View.Options = slices.Clone(relation.Options)
	slices.Sort(out.Schema.View.Options)
	out.Schema.DDL, err = schemaddl.CreateView("opengauss", out.Schema.View, t.Object.Schema)
	return
}

func (s *Session) readOpenGaussView(ctx context.Context, t contract.SchemaDesignTarget) (out contract.ViewDesignSnapshot, err error) {
	if err = s.checkOpenGaussView(t); err != nil {
		return
	}
	ctx, cancel := s.requestContext(ctx)
	defer cancel()
	c, err := s.db.Conn(ctx)
	if err != nil {
		return out, err
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY"); err != nil {
		discardDesign(c)
		return
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, ctx.Err())
			out = contract.ViewDesignSnapshot{}
			if rb := rollbackDesign(c); rb != nil {
				discardDesign(c)
				err = errors.Join(err, rb)
			}
		}
	}()
	if _, err = c.ExecContext(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on"); err != nil {
		return
	}
	var record openGaussViewRecord
	out, record, err = openGaussReadView(ctx, c, t)
	if err == nil && !out.Schema.Exists && !record.CanCreate {
		err = errors.New("insufficient privilege to create a view in the selected schema")
	}
	if err == nil {
		err = s.checkOpenGaussView(t)
	}
	if err == nil {
		_, err = c.ExecContext(ctx, "COMMIT")
	}
	return
}

func (s *Session) applyOpenGaussView(ctx context.Context, request contract.ViewDesignRequest) (out contract.ViewDesignOutcome) {
	out.State = "not_executed"
	r := request.Clone()
	t := r.Original.Target
	if err := s.checkOpenGaussView(t); err != nil {
		out.Error = err
		return
	}
	if r.Original.Schema.Engine != "opengauss" || r.Original.Schema.View.Name != t.Object.Name || r.Original.Schema.View.Schema != t.Object.Schema {
		out.Error = schemaddl.ErrConflict
		return
	}
	plan, err := schemaddl.PlanView(r.Original.Schema, r.Action, r.Desired, t.Object.Schema)
	if err != nil {
		out.Error = err
		return
	}
	ctx, cancel := s.requestContext(ctx)
	defer cancel()
	c, err := s.db.Conn(ctx)
	if err != nil {
		out.Error = err
		return
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN"); err != nil {
		discardDesign(c)
		out.Error = errors.Join(err, ctx.Err())
		return
	}
	settled, commit := false, false
	defer func() {
		if !settled {
			out.Error = errors.Join(out.Error, ctx.Err())
			rb := rollbackDesign(c)
			if commit || rb != nil {
				discardDesign(c)
				out.State = "uncertain"
				out.Error = errors.Join(out.Error, rb, s.Close())
			} else {
				out.State = "rolled_back"
			}
		}
	}()
	if _, err = c.ExecContext(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on"); err != nil {
		out.Error = err
		return
	}
	actual, before, err := openGaussReadView(ctx, c, t)
	if err != nil || schemaddl.Signature(actual.Schema) != schemaddl.Signature(r.Original.Schema) {
		out.Error = errors.Join(err, schemaddl.ErrConflict)
		return
	}
	if before.Hooks {
		out.Error = errors.New("enabled DDL event triggers are unsupported for view management")
		return
	}
	if r.Original.Schema.Exists {
		// Native LOCK TABLE rejects views. An ALTER VIEW to its already
		// captured owner holds AccessExclusiveLock through transaction end.
		// Read again after that lock: external DDL committed while waiting
		// must be compared with the original, never used as the before-image.
		var relation struct {
			Owner uint64 `json:"relowner"`
		}
		var owner string
		if err = json.Unmarshal(before.Relation, &relation); err == nil {
			err = c.QueryRowContext(ctx, "SELECT pg_get_userbyid($1::oid)", relation.Owner).Scan(&owner)
		}
		if err != nil || owner == "" {
			out.Error = errors.Join(err, schemaddl.ErrConflict)
			return
		}
		if _, err = c.ExecContext(ctx, "ALTER VIEW "+qualified(t.Object)+" OWNER TO "+quote(owner)); err != nil {
			out.Error = err
			return
		}
		actual, before, err = openGaussReadView(ctx, c, t)
		if err != nil || schemaddl.Signature(actual.Schema) != schemaddl.Signature(r.Original.Schema) || before.Hooks {
			out.Error = errors.Join(err, schemaddl.ErrConflict)
			return
		}
	}
	if r.Action == "create" && !before.CanCreate {
		out.Error = errors.New("insufficient privilege to create a view in the selected schema")
		return
	}
	if _, err = c.ExecContext(ctx, "SET LOCAL search_path=pg_catalog,"+quote(t.Object.Schema)); err != nil {
		out.Error = err
		return
	}
	for _, statement := range plan.SQL {
		if _, err = c.ExecContext(ctx, statement); err != nil {
			out.Error = err
			return
		}
	}
	if _, err = c.ExecContext(ctx, "SET LOCAL search_path=pg_catalog"); err != nil {
		out.Error = err
		return
	}
	if r.Action != "drop" {
		t.Object.Name = r.Desired.Name
	}
	verified, after, err := openGaussReadView(ctx, c, t)
	if err != nil || verified.Schema.Exists != (r.Action != "drop") || !openGaussViewPreserved(before, after, r.Action) {
		out.Error = errors.Join(err, schemaddl.ErrConflict)
		return
	}
	if r.Action != "drop" && schemaddl.Signature(verified.Schema.View.Options) != schemaddl.Signature(plan.Desired.Options) {
		out.Error = schemaddl.ErrConflict
		return
	}
	if r.Action == "rename" {
		old, _, err := openGaussReadView(ctx, c, r.Original.Target)
		if err != nil || old.Schema.Exists {
			out.Error = errors.Join(err, schemaddl.ErrConflict)
			return
		}
	}
	if err = s.checkOpenGaussView(t); err != nil {
		out.Error = err
		return
	}
	commit = true
	if _, err = c.ExecContext(ctx, "COMMIT"); err != nil {
		out.Error = err
		return
	}
	settled = true
	out.State, out.Verified, out.Actual = "committed", true, &verified
	return
}

func openGaussViewFingerprint(record openGaussViewRecord) string {
	// Same-owner locking and ordinary native DDL update these operational
	// timestamps. Definition/ACL/comment/dependency changes remain fingerprinted.
	objects := make([]json.RawMessage, len(record.Objects))
	for i, raw := range record.Objects {
		var data map[string]json.RawMessage
		if json.Unmarshal(raw, &data) != nil || data == nil {
			return ""
		}
		delete(data, "mtime")
		delete(data, "changecsn")
		objects[i], _ = json.Marshal(data)
	}
	record.Objects = objects
	return schemaddl.Signature(record)
}

func openGaussViewPreserved(before, after openGaussViewRecord, action string) bool {
	if action != "create" && action != "alter" && action != "rename" && action != "drop" {
		return false
	}
	if before.DatabaseOID != after.DatabaseOID || before.NamespaceOID != after.NamespaceOID || before.RoleOID != after.RoleOID || schemaddl.Signature(before.Namespace) != schemaddl.Signature(after.Namespace) || after.Hooks {
		return false
	}
	if action == "drop" {
		return after.RelationOID == 0
	}
	if action == "create" {
		var relation struct {
			Owner uint64 `json:"relowner"`
		}
		return json.Unmarshal(after.Relation, &relation) == nil && after.RelationOID != 0 && relation.Owner == after.RoleOID
	}
	if before.RelationOID != after.RelationOID || len(after.Columns) < len(before.Columns) || schemaddl.Signature(before.Incoming) != schemaddl.Signature(after.Incoming) || schemaddl.Signature(before.Triggers) != schemaddl.Signature(after.Triggers) || schemaddl.Signature(before.Comments) != schemaddl.Signature(after.Comments) {
		return false
	}
	relationFields := []string{}
	typeFields := []string{}
	if action == "rename" {
		relationFields, typeFields = []string{"relname"}, []string{"typname"}
		if len(before.Columns) != len(after.Columns) || before.Definition != after.Definition || schemaddl.Signature(before.Outgoing) != schemaddl.Signature(after.Outgoing) {
			return false
		}
	} else {
		relationFields = []string{"relnatts", "reloptions"}
	}
	relation := openGaussWithout(before.Relation, relationFields...)
	if relation == "" || relation != openGaussWithout(after.Relation, relationFields...) || !openGaussCatalogArrayMatches(before.Types, after.Types, typeFields...) || !openGaussCatalogArrayMatches(before.Objects, after.Objects, "mtime", "changecsn") {
		return false
	}
	for i := range before.Columns {
		if schemaddl.Signature(before.Columns[i]) != schemaddl.Signature(after.Columns[i]) {
			return false
		}
	}
	if len(before.Rules) != len(after.Rules) {
		return false
	}
	for i := range before.Rules {
		fields := []string{}
		var rule struct {
			Data struct {
				Name string `json:"rulename"`
			}
		}
		if json.Unmarshal(before.Rules[i], &rule) != nil {
			return false
		}
		if action == "alter" && rule.Data.Name == "_RETURN" {
			fields = []string{"ev_action"}
		}
		if !openGaussCatalogArrayMatches(before.Rules[i:i+1], after.Rules[i:i+1], fields...) {
			return false
		}
	}
	return true
}

func openGaussWithout(raw json.RawMessage, fields ...string) string {
	var data map[string]json.RawMessage
	if json.Unmarshal(raw, &data) != nil || data == nil {
		return ""
	}
	for _, field := range fields {
		delete(data, field)
	}
	return schemaddl.Signature(data)
}

func openGaussCatalogArrayMatches(a, b []json.RawMessage, fields ...string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		var x, y map[string]json.RawMessage
		if json.Unmarshal(a[i], &x) != nil || json.Unmarshal(b[i], &y) != nil || x == nil || y == nil {
			return false
		}
		if raw, ok := x["data"]; ok {
			data := openGaussWithout(raw, fields...)
			if data == "" || data != openGaussWithout(y["data"], fields...) || openGaussWithout(a[i], "data") != openGaussWithout(b[i], "data") {
				return false
			}
		} else {
			data := openGaussWithout(a[i], fields...)
			if data == "" || data != openGaussWithout(b[i], fields...) {
				return false
			}
		}
	}
	return true
}
