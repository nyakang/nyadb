package engines

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// All catalog records are captured by one native statement. Automatic/internal
// relation dependencies include serial sequences, indexes and storage relations.
// Native pg_partition and pg_rlspolicy must not be replaced by PG catalogs.
const openGaussTableCatalogSQL = `WITH RECURSIVE
 target AS(SELECT c.oid,c.* FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2),
 relations(oid) AS(SELECT oid FROM target UNION SELECT d.objid FROM pg_depend d JOIN relations r ON r.oid=d.refobjid
  WHERE d.refclassid='pg_class'::regclass AND d.classid='pg_class'::regclass AND d.deptype IN('a','i') AND d.objsubid=0),
 partitions(oid) AS(SELECT p.oid FROM pg_partition p WHERE p.parentid IN(SELECT oid FROM relations)
  UNION SELECT p.oid FROM pg_partition p JOIN partitions x ON p.parentid=x.oid),
 types AS(SELECT p.oid FROM pg_type p WHERE p.typrelid IN(SELECT oid FROM relations)
  OR p.typelem<>0 AND p.typelem IN(SELECT c.reltype FROM pg_class c WHERE c.oid IN(SELECT oid FROM relations))),
 entries(kind,oid,subid,data) AS(
  SELECT 'namespace',n.oid,0,row_to_json(n) FROM pg_namespace n WHERE n.nspname=$1
  UNION ALL SELECT 'relation',c.oid,0,row_to_json(c) FROM pg_class c WHERE c.oid IN(SELECT oid FROM relations)
  UNION ALL SELECT 'column',a.attrelid,a.attnum,row_to_json(a) FROM pg_attribute a WHERE a.attrelid IN(SELECT oid FROM relations) AND a.attnum>0
  UNION ALL SELECT 'default',a.oid,0,row_to_json(a) FROM pg_attrdef a WHERE a.adrelid IN(SELECT oid FROM relations)
  UNION ALL SELECT 'type',p.oid,0,row_to_json(p) FROM pg_type p WHERE p.oid IN(SELECT oid FROM types)
  UNION ALL SELECT 'index',i.indexrelid,0,row_to_json(i) FROM pg_index i WHERE i.indrelid IN(SELECT oid FROM relations)
  UNION ALL SELECT CASE WHEN c.conrelid IN(SELECT oid FROM relations) THEN 'constraint' ELSE 'incoming_constraint' END,c.oid,0,row_to_json(c)
   FROM pg_constraint c WHERE c.conrelid IN(SELECT oid FROM relations) OR c.confrelid IN(SELECT oid FROM relations)
  UNION ALL SELECT 'trigger',t.oid,0,row_to_json(t) FROM pg_trigger t WHERE t.tgrelid IN(SELECT oid FROM relations)
  UNION ALL SELECT 'rule',r.oid,0,row_to_json(r) FROM pg_rewrite r WHERE r.ev_class IN(SELECT oid FROM relations)
  UNION ALL SELECT 'partition',p.oid,0,row_to_json(p) FROM pg_partition p WHERE p.oid IN(SELECT oid FROM partitions)
  UNION ALL SELECT 'policy',p.oid,0,row_to_json(p) FROM pg_rlspolicy p WHERE p.polrelid IN(SELECT oid FROM relations)
  UNION ALL SELECT 'object',p.object_oid,p.object_type::int,row_to_json(p) FROM pg_object p WHERE p.object_oid IN(SELECT oid FROM relations)
  UNION ALL SELECT 'dependencies',t.oid,0,json_build_object('rows',(
   SELECT json_agg(row_to_json(d) ORDER BY d.classid,d.objid,d.objsubid,d.refclassid,d.refobjid,d.refobjsubid,d.deptype) AS rows FROM pg_depend d
   WHERE d.classid='pg_class'::regclass AND d.objid IN(SELECT oid FROM relations) OR d.refclassid='pg_class'::regclass AND d.refobjid IN(SELECT oid FROM relations)
   OR d.classid='pg_type'::regclass AND d.objid IN(SELECT oid FROM types) OR d.refclassid='pg_type'::regclass AND d.refobjid IN(SELECT oid FROM types)
   OR d.classid='pg_partition'::regclass AND d.objid IN(SELECT oid FROM partitions) OR d.refclassid='pg_partition'::regclass AND d.refobjid IN(SELECT oid FROM partitions)
   OR d.classid='pg_attrdef'::regclass AND d.objid IN(SELECT a.oid FROM pg_attrdef a WHERE a.adrelid IN(SELECT oid FROM relations))
   OR d.classid='pg_constraint'::regclass AND d.objid IN(SELECT a.oid FROM pg_constraint a WHERE a.conrelid IN(SELECT oid FROM relations))
   OR d.classid='pg_trigger'::regclass AND d.objid IN(SELECT a.oid FROM pg_trigger a WHERE a.tgrelid IN(SELECT oid FROM relations))
   OR d.classid='pg_rewrite'::regclass AND d.objid IN(SELECT a.oid FROM pg_rewrite a WHERE a.ev_class IN(SELECT oid FROM relations))
   OR d.classid='pg_rlspolicy'::regclass AND d.objid IN(SELECT a.oid FROM pg_rlspolicy a WHERE a.polrelid IN(SELECT oid FROM relations)))) FROM target t
  UNION ALL SELECT 'shared_dependencies',t.oid,0,json_build_object('rows',(
   SELECT json_agg(row_to_json(d) ORDER BY d.dbid,d.classid,d.objid,d.objsubid,d.refclassid,d.refobjid,d.deptype) FROM pg_shdepend d
   WHERE d.dbid=(SELECT oid FROM pg_database WHERE datname=current_database()) AND (
    d.classid='pg_class'::regclass AND d.objid IN(SELECT oid FROM relations) OR d.classid='pg_type'::regclass AND d.objid IN(SELECT oid FROM types)))) FROM target t
  UNION ALL SELECT 'comments',t.oid,0,json_build_object('rows',(
   SELECT json_agg(row_to_json(d) ORDER BY d.classoid,d.objoid,d.objsubid) AS rows FROM pg_description d
   WHERE d.classoid='pg_class'::regclass AND d.objoid IN(SELECT oid FROM relations) OR d.classoid='pg_type'::regclass AND d.objoid IN(SELECT oid FROM types)
   OR d.classoid='pg_constraint'::regclass AND d.objoid IN(SELECT c.oid FROM pg_constraint c WHERE c.conrelid IN(SELECT oid FROM relations))
   OR d.classoid='pg_trigger'::regclass AND d.objoid IN(SELECT tr.oid FROM pg_trigger tr WHERE tr.tgrelid IN(SELECT oid FROM relations))
   OR d.classoid='pg_rewrite'::regclass AND d.objoid IN(SELECT r.oid FROM pg_rewrite r WHERE r.ev_class IN(SELECT oid FROM relations))
   OR d.classoid='pg_partition'::regclass AND d.objoid IN(SELECT oid FROM partitions))) FROM target t
  UNION ALL SELECT 'security_labels',t.oid,0,json_build_object('rows',(
   SELECT json_agg(row_to_json(d) ORDER BY d.classoid,d.objoid,d.objsubid,d.provider) AS rows FROM pg_seclabel d
   WHERE d.classoid='pg_class'::regclass AND d.objoid IN(SELECT oid FROM relations) OR d.classoid='pg_type'::regclass AND d.objoid IN(SELECT oid FROM types))) FROM target t
  UNION ALL SELECT 'inheritance',t.oid,0,json_build_object('rows',(
   SELECT json_agg(row_to_json(d) ORDER BY d.inhrelid,d.inhparent,d.inhseqno) AS rows FROM pg_inherits d WHERE d.inhrelid=t.oid OR d.inhparent=t.oid)) FROM target t
  UNION ALL SELECT 'publications',t.oid,0,json_build_object('rows',(
   SELECT json_agg(row_to_json(d) ORDER BY d.prpubid,d.prrelid) AS rows FROM pg_publication_rel d WHERE d.prrelid IN(SELECT oid FROM relations))) FROM target t
  UNION ALL SELECT 'copy_definition',t.oid,0,json_build_object(
   'owner',pg_get_userbyid(t.relowner),'creator',current_user,'can_create',has_schema_privilege(t.relnamespace,'CREATE'),
   'tablespace',(SELECT spcname FROM pg_tablespace WHERE oid=t.reltablespace),'comment',obj_description(t.oid,'pg_class'),
   'columns',(SELECT json_agg(json_build_object('number',a.attnum,'dimensions',a.attndims,'name',a.attname,'type',format_type(a.atttypid,a.atttypmod),
    'default',COALESCE(pg_get_expr(d.adbin,d.adrelid,false),''),'generated',COALESCE(d.adgencol::text,''),'comment',col_description(a.attrelid,a.attnum)) ORDER BY a.attnum)
    FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE a.attrelid=t.oid AND a.attnum>0 AND NOT a.attisdropped),
   'indexes',(SELECT json_agg(json_build_object('oid',i.indexrelid::bigint,'name',ic.relname,'kind','index','sql',pg_get_indexdef(i.indexrelid),
    'comment',obj_description(ic.oid,'pg_class')) ORDER BY i.indexrelid) FROM pg_index i JOIN pg_class ic ON ic.oid=i.indexrelid WHERE i.indrelid=t.oid),
   'constraints',(SELECT json_agg(json_build_object('oid',con.oid::bigint,'name',con.conname,'kind',con.contype::text,'sql',pg_get_constraintdef(con.oid,false),
    'indexoid',con.conindid::bigint,'referenceoid',con.confrelid::bigint,'referenceschema',rn.nspname,'referencename',rc.relname,
    'comment',obj_description(con.oid,'pg_constraint')) ORDER BY con.oid) FROM pg_constraint con LEFT JOIN pg_class rc ON rc.oid=con.confrelid LEFT JOIN pg_namespace rn ON rn.oid=rc.relnamespace WHERE con.conrelid=t.oid),
   'triggers',(SELECT json_agg(json_build_object('oid',tr.oid::bigint,'name',tr.tgname,'kind','trigger','sql',pg_get_triggerdef(tr.oid,false),
    'internal',tr.tgisinternal,'enabled',tr.tgenabled::text,'comment',obj_description(tr.oid,'pg_trigger')) ORDER BY tr.oid) FROM pg_trigger tr WHERE tr.tgrelid=t.oid),
   'rules',(SELECT json_agg(json_build_object('oid',w.oid::bigint,'name',w.rulename,'kind','rule','sql',pg_get_ruledef(w.oid,false),
    'enabled',w.ev_enabled::text,'comment',obj_description(w.oid,'pg_rewrite')) ORDER BY w.oid) FROM pg_rewrite w WHERE w.ev_class=t.oid),
   'policies',(SELECT json_agg(json_build_object('oid',pol.oid::bigint,'name',pol.polname,'command',pol.polcmd::text,'permissive',pol.polpermissive,
    'expression',COALESCE(pg_get_expr(pol.polqual,pol.polrelid,false),''),
    'roles',(SELECT json_agg(json_build_object('oid',roleid::bigint,'name',CASE WHEN roleid=0 THEN 'PUBLIC' ELSE pg_get_userbyid(roleid) END) ORDER BY roleid) FROM unnest(pol.polroles) roleid)) ORDER BY pol.oid)
    FROM pg_rlspolicy pol WHERE pol.polrelid=t.oid),
   'sequences',(SELECT json_agg(json_build_object('oid',seq.oid::bigint,'column',dep.refobjsubid,'schema',ns.nspname,'name',seq.relname,'owner',pg_get_userbyid(seq.relowner),'large',seq.relkind='L',
    'start',(SELECT start_value::text FROM pg_sequence_parameters(seq.oid)),
    'increment',(SELECT increment::text FROM pg_sequence_parameters(seq.oid)),
    'min',(SELECT minimum_value::text FROM pg_sequence_parameters(seq.oid)),
    'max',(SELECT maximum_value::text FROM pg_sequence_parameters(seq.oid)),
    'cache',(SELECT cache_value::text FROM pg_sequence_last_value(seq.oid)),
    'cycle',(SELECT cycle_option FROM pg_sequence_parameters(seq.oid)),
    'comment',obj_description(seq.oid,'pg_class')) ORDER BY seq.oid)
    FROM pg_class seq JOIN pg_namespace ns ON ns.oid=seq.relnamespace JOIN pg_depend dep ON dep.classid='pg_class'::regclass AND dep.objid=seq.oid
    WHERE seq.relkind IN('S','L') AND dep.refclassid='pg_class'::regclass AND dep.refobjid=t.oid AND dep.refobjsubid>0 AND dep.deptype IN('a','i'))
  ) FROM target t
 )
 SELECT CASE WHEN octet_length(row_to_json(v)::text)<=16777216 THEN row_to_json(v)::text END FROM(
  SELECT current_database() AS database,$1::text AS namespace,$2::text AS name,d.oid::bigint AS databaseoid,n.oid::bigint AS namespaceoid,
   COALESCE(t.oid::bigint,0) AS relationoid,r.oid::bigint AS roleoid,
   COALESCE(pg_has_role(current_user,t.relowner,'USAGE') OR r.rolsuper OR r.rolsystemadmin,false) AS canmanage,
   EXISTS(SELECT 1 FROM pg_event_trigger WHERE evtenabled<>'D') AS hooks,
   -- row_to_json of this UNION CTE loses its aliases on native 6.0.0.
   (SELECT json_agg(json_build_object('kind',e.kind,'oid',e.oid::bigint,'subid',e.subid,'data',e.data) ORDER BY e.kind,e.oid,e.subid) FROM entries e) AS entries
  FROM pg_database d CROSS JOIN pg_roles r CROSS JOIN pg_namespace n LEFT JOIN target t ON true
  WHERE d.datname=current_database() AND r.rolname=current_user AND n.nspname=$1
 ) v`

func (s *Session) checkOpenGaussTable(t contract.SchemaDesignTarget) error {
	if s.kind != connections.OpenGauss || t.Object.Kind != "table" {
		return contract.ErrUnsupported
	}
	if s.isClosed() || t.Generation == 0 || t.Generation != s.generation {
		return contract.ErrRevoked
	}
	if err := s.check(t.Object); err != nil {
		return err
	}
	if schemaddl.PostgreSQLIdentifier(t.Object.Schema) != nil || schemaddl.PostgreSQLIdentifier(t.Object.Name) != nil {
		return contract.ErrUnsupported
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

func openGaussReadTable(ctx context.Context, c *sql.Conn, t contract.SchemaDesignTarget) (out contract.SchemaDesignSnapshot, err error) {
	var raw sql.NullString
	if err = c.QueryRowContext(ctx, openGaussTableCatalogSQL, t.Object.Schema, t.Object.Name).Scan(&raw); err != nil {
		return
	}
	p := &schemaddl.OpenGaussCatalog{}
	if !raw.Valid || json.Unmarshal([]byte(raw.String), p) != nil || p.Database != t.Object.Catalog || p.Namespace != t.Object.Schema || p.Name != t.Object.Name {
		return out, errors.New("invalid or excessive native table catalog")
	}
	revision, err := p.Fingerprint()
	if err != nil {
		return out, err
	}
	if p.RelationOID != 0 {
		if !p.CanManage {
			return out, errors.New("only the owner or an administrator can manage this table")
		}
		for _, e := range p.Entries {
			if e.Kind == "relation" && e.OID == p.RelationOID {
				var r struct {
					Name        string `json:"relname"`
					Kind        string `json:"relkind"`
					Persistence string `json:"relpersistence"`
					OfType      uint64 `json:"reloftype"`
				}
				if json.Unmarshal(e.Data, &r) != nil || r.Name != t.Object.Name || r.Kind != "r" || r.OfType != 0 || r.Persistence != "p" && r.Persistence != "u" {
					return out, contract.ErrUnsupported
				}
			}
			if e.Kind == "inheritance" {
				var r struct{ Rows []json.RawMessage }
				if json.Unmarshal(e.Data, &r) != nil || len(r.Rows) != 0 {
					return out, contract.ErrUnsupported
				}
			}
		}
	}
	out = contract.SchemaDesignSnapshot{Target: t, Schema: schemaddl.Snapshot{Engine: "opengauss", Revision: revision, Exists: p.RelationOID != 0, Table: schemaddl.Table{Name: t.Object.Name}, OpenGauss: p}}
	return
}

func (s *Session) openGaussTableSnapshot(ctx context.Context, t contract.SchemaDesignTarget) (out contract.SchemaDesignSnapshot, err error) {
	if err = s.checkOpenGaussTable(t); err != nil {
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
			out = contract.SchemaDesignSnapshot{}
			err = errors.Join(err, ctx.Err())
			if rb := rollbackDesign(c); rb != nil {
				discardDesign(c)
				err = errors.Join(err, rb)
			}
		}
	}()
	if _, err = c.ExecContext(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on"); err == nil {
		out, err = openGaussReadTable(ctx, c, t)
	}
	if err == nil {
		err = s.checkOpenGaussTable(t)
	}
	if err == nil {
		_, err = c.ExecContext(ctx, "COMMIT")
	}
	return
}

func (s *Session) openGaussDropTable(ctx context.Context, original contract.SchemaDesignSnapshot) (out contract.SchemaDesignOutcome) {
	out.State = "not_executed"
	r := original.Clone()
	t := r.Target
	if err := s.checkOpenGaussTable(t); err != nil {
		out.Error = err
		return
	}
	if !r.Schema.Exists || r.Schema.Engine != "opengauss" || r.Schema.Table.Name != t.Object.Name || r.Schema.OpenGauss == nil || r.Schema.Postgres != nil {
		out.Error = schemaddl.ErrConflict
		return
	}
	p := r.Schema.OpenGauss
	revision, err := p.Fingerprint()
	if err != nil || revision != r.Schema.Revision || p.RelationOID == 0 || p.Database != t.Object.Catalog || p.Namespace != t.Object.Schema || p.Name != t.Object.Name {
		out.Error = errors.Join(err, schemaddl.ErrConflict)
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
	if _, err = c.ExecContext(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on"); err == nil {
		_, err = c.ExecContext(ctx, "LOCK TABLE "+qualified(t.Object)+" IN ACCESS EXCLUSIVE MODE")
	}
	if err != nil {
		out.Error = err
		return
	}
	// READ COMMITTED and a new statement after acquiring the lock are crucial:
	// never accept an object replacement or DDL committed while waiting.
	before, err := openGaussReadTable(ctx, c, t)
	if err != nil || !before.Schema.Exists || before.Schema.Revision != r.Schema.Revision {
		out.Error = errors.Join(err, schemaddl.ErrConflict)
		return
	}
	if before.Schema.OpenGauss.Hooks {
		out.Error = errors.New("enabled DDL event triggers are unsupported for table management")
		return
	}
	if _, err = c.ExecContext(ctx, "DROP TABLE "+qualified(t.Object)+" RESTRICT"); err != nil {
		out.Error = err
		return
	}
	actual, err := openGaussReadTable(ctx, c, t)
	if err != nil || actual.Schema.Exists || !openGaussDropScopePreserved(before.Schema.OpenGauss, actual.Schema.OpenGauss) {
		out.Error = errors.Join(err, schemaddl.ErrConflict)
		return
	}
	// Verify the captured row type, serial sequences, indexes and storage
	// relations are actually gone, independently of the original table name.
	owned := map[string][]string{}
	for _, e := range before.Schema.OpenGauss.Entries {
		catalog := ""
		switch e.Kind {
		case "relation":
			catalog = "pg_class"
		case "type":
			catalog = "pg_type"
		case "partition":
			catalog = "pg_partition"
		}
		if catalog != "" {
			owned[catalog] = append(owned[catalog], strconv.FormatUint(e.OID, 10))
		}
	}
	for _, catalog := range []string{"pg_class", "pg_type", "pg_partition"} {
		ids := owned[catalog]
		if len(ids) == 0 {
			continue
		}
		var remains bool
		if err = c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+catalog+" WHERE oid IN("+strings.Join(ids, ",")+"))").Scan(&remains); err != nil || remains {
			out.Error = errors.Join(err, schemaddl.ErrConflict)
			return
		}
	}
	if err = s.checkOpenGaussTable(t); err != nil {
		out.Error = err
		return
	}
	commit = true
	if _, err = c.ExecContext(ctx, "COMMIT"); err != nil {
		out.Error = err
		return
	}
	settled = true
	out.State, out.Verified, out.Actual = "committed", true, &actual
	return
}

func openGaussDropScopePreserved(before, after *schemaddl.OpenGaussCatalog) bool {
	if before == nil || after == nil || after.RelationOID != 0 || after.Hooks || before.DatabaseOID != after.DatabaseOID || before.NamespaceOID != after.NamespaceOID || before.RoleOID != after.RoleOID {
		return false
	}
	var a, b json.RawMessage
	for _, e := range before.Entries {
		if e.Kind == "namespace" && e.OID == before.NamespaceOID {
			a = e.Data
		}
	}
	for _, e := range after.Entries {
		if e.Kind == "namespace" && e.OID == after.NamespaceOID {
			b = e.Data
		}
	}
	return openGaussWithout(a) != "" && openGaussWithout(a) == openGaussWithout(b)
}
