package engines

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) checkPostgresDesign(t contract.SchemaDesignTarget) error {
	if s.kind != connections.PostgreSQL || t.Object.Kind != "table" {
		return schemaddl.ErrUnsupported
	}
	if t.Generation == 0 || t.Generation != s.generation {
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
	if strings.HasPrefix(t.Object.Schema, "pg_") || t.Object.Schema == "information_schema" {
		return errors.New("system PostgreSQL schemas are read-only")
	}
	return nil
}

// postgresDesignSnapshot uses a dedicated owned connection and a repeatable
// read transaction, so catalog parts cannot come from different commits. It
// does not execute a draft, reserve an editor lease, or modify a user relation.
func (s *Session) postgresDesignSnapshot(ctx context.Context, target contract.SchemaDesignTarget) (snapshot contract.SchemaDesignSnapshot, err error) {
	defer func() {
		if err != nil {
			snapshot = contract.SchemaDesignSnapshot{}
		}
	}()
	if err = s.checkPostgresDesign(target); err != nil {
		return snapshot, err
	}
	ctx, cancel := s.requestContext(ctx)
	defer cancel()
	c, err := s.db.Conn(ctx)
	if err != nil {
		return snapshot, err
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY"); err != nil {
		return snapshot, err
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
	for _, setting := range []string{"SET LOCAL search_path = pg_catalog", "SET LOCAL standard_conforming_strings = on"} {
		if _, err = c.ExecContext(ctx, setting); err != nil {
			return snapshot, err
		}
	}
	snapshot, err = postgresCatalogSnapshot(ctx, c, target)
	if err != nil {
		return snapshot, err
	}
	if err = s.checkPostgresDesign(target); err != nil {
		return snapshot, err
	}
	_, err = c.ExecContext(ctx, "COMMIT")
	settled = err == nil
	return snapshot, err
}

func postgresCatalogSnapshot(ctx context.Context, q queryer, target contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
	return postgresCatalogSnapshotPreserving(ctx, q, target, nil)
}

// A preserving snapshot is private verification evidence. It supplements the
// current closure with original object identities, without writing stale JSON
// back to PostgreSQL or replacing the ordinary workbench snapshot's scope.
func postgresCatalogSnapshotPreserving(ctx context.Context, q queryer, target contract.SchemaDesignTarget, original *schemaddl.PostgreSQLCatalog) (snap contract.SchemaDesignSnapshot, err error) {
	defer func() {
		if err != nil {
			snap = contract.SchemaDesignSnapshot{}
		}
	}()
	if err := ctx.Err(); err != nil {
		return snap, err
	}
	original = original.Clone()
	seeds, err := postgresOriginalCatalogSeeds(original)
	if err != nil {
		return snap, err
	}
	if original != nil && (original.Database != target.Object.Catalog || original.Namespace != target.Object.Schema) {
		return snap, schemaddl.ErrConflict
	}
	snap = contract.SchemaDesignSnapshot{Target: target, Schema: schemaddl.Snapshot{Engine: "postgresql", Table: schemaddl.Table{Name: target.Object.Name}}}
	catalog := &schemaddl.PostgreSQLCatalog{Database: target.Object.Catalog, Namespace: target.Object.Schema}
	rows, err := q.QueryContext(ctx, `SELECT current_setting('server_version_num')::integer,version(),d.oid::bigint,n.oid::bigint,COALESCE(c.oid::bigint,0),COALESCE(c.relkind::text,''),d.datname::text,n.nspname::text
FROM pg_database d JOIN pg_namespace n ON n.nspname=$1 LEFT JOIN pg_class c ON c.relnamespace=n.oid AND c.relname=$2 WHERE d.datname=current_database()`, target.Object.Schema, target.Object.Name)
	if err != nil {
		return snap, err
	}
	if !rows.Next() {
		err = rows.Err()
		rows.Close()
		if err == nil {
			err = errors.New("original PostgreSQL namespace does not exist")
		}
		return snap, err
	}
	var kind string
	var serverPlatform string
	err = rows.Scan(&catalog.ServerVersion, &serverPlatform, &catalog.DatabaseOID, &catalog.NamespaceOID, &catalog.RelationOID, &kind, &catalog.Database, &catalog.Namespace)
	rows.Close()
	if err != nil {
		return snap, err
	}
	catalog.NativeByteOrder = postgresNativeByteOrder(serverPlatform)
	if catalog.Database != target.Object.Catalog || catalog.Namespace != target.Object.Schema {
		return snap, schemaddl.ErrConflict
	}
	if original != nil {
		before := original.Clone()
		before.RelationOID, before.Entries = 0, nil
		header := catalog.Clone()
		header.RelationOID = 0
		if schemaddl.Signature(before) != schemaddl.Signature(header) {
			return snap, schemaddl.ErrConflict
		}
	}
	if catalog.ServerVersion < 140000 {
		return snap, errors.New("complete PostgreSQL design snapshots require PostgreSQL 14 or later")
	}
	if catalog.RelationOID != 0 && kind != "r" && kind != "p" {
		return snap, errors.New("original table name is occupied by another PostgreSQL object")
	}
	rows, err = q.QueryContext(ctx, postgresDesignCatalogFor(catalog.ServerVersion), catalog.NamespaceOID, seeds)
	if err != nil {
		return snap, err
	}
	bytes := 0
	for rows.Next() {
		var entry schemaddl.PostgreSQLCatalogEntry
		var raw sql.NullString
		if err = rows.Scan(&entry.Kind, &entry.OID, &entry.SubID, &raw); err != nil {
			rows.Close()
			return snap, err
		}
		if !raw.Valid {
			rows.Close()
			return snap, errors.New("complete PostgreSQL catalog record exceeds 1 MiB")
		}
		bytes += len(raw.String) + len(entry.Kind) + 32
		if len(catalog.Entries) >= 10000 || bytes > 16<<20 {
			rows.Close()
			return snap, errors.New("complete PostgreSQL catalog exceeds safety limits")
		}
		entry.Data = json.RawMessage(raw.String)
		catalog.Entries = append(catalog.Entries, entry)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return snap, err
	}
	revision, err := catalog.Fingerprint()
	if err != nil {
		return snap, err
	}
	snap.Schema.Postgres = catalog
	snap.Schema.Revision = revision
	snap.Schema.Exists = catalog.RelationOID != 0
	if snap.Schema.Exists {
		snap.Schema.Table, err = catalog.Table()
		if err != nil {
			return snap, err
		}
		if snap.Schema.Table.Name != target.Object.Name {
			return snap, errors.New("PostgreSQL catalog returned a different table identity")
		}
		snap.Schema.DDL, err = schemaddl.PostgreSQLStructuralDDL(snap.Schema.Table, target.Object.Schema)
		if err != nil {
			return snap, err
		}
	}
	return snap, nil
}

// pg_node_tree prints by-value Datum bytes in the database server's native
// memory order. The client architecture and binary send protocol do not prove
// that order. Unsupported compiler targets stay unknown instead of guessing.
func postgresNativeByteOrder(version string) string {
	_, target, ok := strings.Cut(version, " on ")
	if !ok {
		return ""
	}
	target, _, _ = strings.Cut(target, ",")
	architecture, _, _ := strings.Cut(strings.TrimSpace(target), "-")
	switch architecture {
	case "x86_64", "amd64", "i386", "i486", "i586", "i686", "aarch64", "arm64", "arm", "armv7l", "ppc64le", "powerpc64le", "riscv64", "loongarch64":
		return "little"
	case "s390x", "s390", "sparc", "sparc64", "powerpc", "powerpc64", "ppc", "ppc64", "mips", "mips64":
		return "big"
	}
	return ""
}

func postgresDesignCatalogFor(version int) string {
	filter, records, seed := "", "", ""
	if version >= 150000 {
		filter = " OR oid IN (SELECT pnpubid FROM pg_publication_namespace WHERE pnnspid=$1)"
		records = " UNION ALL SELECT 'publication_namespace',p.oid,0,to_jsonb(p) FROM pg_publication_namespace p WHERE pnnspid=$1 OR p.oid IN (SELECT objid FROM required WHERE classid='pg_publication_namespace'::regclass)"
		seed = " UNION SELECT 'pg_publication_namespace'::regclass::oid,oid FROM pg_publication_namespace WHERE pnnspid=$1"
	}
	sql := strings.ReplaceAll(postgresDesignCatalogSQL, "/* PUB_NAMESPACE_FILTER */", filter)
	sql = strings.ReplaceAll(sql, "/* PUB_NAMESPACE_SEED */", seed)
	return strings.ReplaceAll(sql, "/* PUB_NAMESPACE_RECORDS */", records)
}

// All user namespace relations and their structural neighbours participate in
// the revision. Descendants, referenced/referencing FK relations, outside views,
// owned sequences and indexes are captured by original OID. Outbound catalog
// dependencies additionally include routines, types, collations and operators.
// Definitions and public catalog JSON are complete; no pg_authid, row values,
// sequence current values, optimizer statistics or connection secrets are read.
const postgresDesignCatalogSQL = `WITH RECURSIVE
original_objects AS (
 SELECT COALESCE(NULLIF(o.classid,0),to_regclass('pg_catalog.'||o.catalog)::oid) AS classid,o.objid
 FROM jsonb_to_recordset($2::jsonb) AS o(catalog text,classid oid,objid oid)
),
roots AS (SELECT oid FROM pg_class WHERE relnamespace=$1),
namespace_types(oid) AS (
 SELECT oid FROM pg_type WHERE typnamespace=$1
 UNION SELECT t.oid FROM pg_type t JOIN namespace_types n ON t.typelem=n.oid OR t.typbasetype=n.oid
),
descendants(oid) AS (SELECT oid FROM roots UNION SELECT i.inhrelid FROM pg_inherits i JOIN descendants d ON i.inhparent=d.oid),
related AS (
 SELECT oid FROM descendants
 UNION SELECT attrelid FROM pg_attribute WHERE attnum>0 AND NOT attisdropped AND atttypid IN (SELECT oid FROM namespace_types)
 UNION SELECT conrelid FROM pg_constraint WHERE confrelid IN (SELECT oid FROM roots) AND conrelid<>0
 UNION SELECT confrelid FROM pg_constraint WHERE conrelid IN (SELECT oid FROM roots) AND confrelid<>0
 UNION SELECT inhparent FROM pg_inherits WHERE inhrelid IN (SELECT oid FROM roots)
 UNION SELECT r.ev_class FROM pg_rewrite r JOIN pg_depend d ON d.classid='pg_rewrite'::regclass AND d.objid=r.oid AND d.refclassid='pg_class'::regclass WHERE d.refobjid IN (SELECT oid FROM roots)
),
relation_scope AS (
 SELECT oid FROM related
 UNION SELECT indexrelid FROM pg_index WHERE indrelid IN (SELECT oid FROM related)
 UNION SELECT objid FROM pg_depend WHERE classid='pg_class'::regclass AND refclassid='pg_class'::regclass AND refobjid IN (SELECT oid FROM related) AND deptype IN ('a','i')
),
seed(classid,objid) AS (
 SELECT 'pg_class'::regclass::oid,oid FROM relation_scope
 UNION SELECT 'pg_constraint'::regclass::oid,oid FROM pg_constraint WHERE conrelid IN (SELECT oid FROM relation_scope)
 UNION SELECT 'pg_constraint'::regclass::oid,oid FROM pg_constraint WHERE contypid IN (SELECT oid FROM pg_type WHERE typnamespace=$1 UNION SELECT atttypid FROM pg_attribute WHERE attrelid IN (SELECT oid FROM relation_scope) AND attnum>0 AND NOT attisdropped)
 UNION SELECT 'pg_trigger'::regclass::oid,oid FROM pg_trigger WHERE tgrelid IN (SELECT oid FROM relation_scope)
 UNION SELECT 'pg_attrdef'::regclass::oid,oid FROM pg_attrdef WHERE adrelid IN (SELECT oid FROM relation_scope)
 UNION SELECT 'pg_rewrite'::regclass::oid,oid FROM pg_rewrite WHERE ev_class IN (SELECT oid FROM relation_scope)
 UNION SELECT 'pg_policy'::regclass::oid,oid FROM pg_policy WHERE polrelid IN (SELECT oid FROM relation_scope)
 UNION SELECT 'pg_proc'::regclass::oid,oid FROM pg_proc WHERE pronamespace=$1
 UNION SELECT 'pg_proc'::regclass::oid,objid FROM pg_depend WHERE classid='pg_proc'::regclass AND ((refclassid='pg_class'::regclass AND refobjid IN (SELECT oid FROM roots)) OR (refclassid='pg_type'::regclass AND refobjid IN (SELECT oid FROM namespace_types)))
 UNION SELECT 'pg_event_trigger'::regclass::oid,oid FROM pg_event_trigger
 UNION SELECT 'pg_default_acl'::regclass::oid,oid FROM pg_default_acl WHERE defaclnamespace IN (0,$1)
 UNION SELECT 'pg_publication'::regclass::oid,oid FROM pg_publication WHERE puballtables
 /* PUB_NAMESPACE_SEED */
 UNION SELECT 'pg_type'::regclass::oid,oid FROM pg_type WHERE oid IN (SELECT oid FROM namespace_types) OR oid IN (SELECT atttypid FROM pg_attribute WHERE attrelid IN (SELECT oid FROM relation_scope) AND attnum>0 AND NOT attisdropped)
 UNION SELECT 'pg_collation'::regclass::oid,oid FROM pg_collation WHERE collnamespace=$1
 UNION SELECT 'pg_operator'::regclass::oid,oid FROM pg_operator WHERE oprnamespace=$1
 UNION SELECT 'pg_operator'::regclass::oid,objid FROM pg_depend WHERE classid='pg_operator'::regclass AND refclassid='pg_type'::regclass AND refobjid IN (SELECT oid FROM namespace_types)
 UNION SELECT 'pg_am'::regclass::oid,relam FROM pg_class WHERE oid IN (SELECT oid FROM relation_scope) AND relam<>0
 UNION SELECT 'pg_cast'::regclass::oid,oid FROM pg_cast WHERE castsource IN (SELECT oid FROM pg_type WHERE typnamespace=$1 UNION SELECT atttypid FROM pg_attribute WHERE attrelid IN (SELECT oid FROM relation_scope) AND attnum>0 AND NOT attisdropped) OR casttarget IN (SELECT oid FROM pg_type WHERE typnamespace=$1 UNION SELECT atttypid FROM pg_attribute WHERE attrelid IN (SELECT oid FROM relation_scope) AND attnum>0 AND NOT attisdropped)
 UNION SELECT 'pg_opclass'::regclass::oid,unnest(indclass::oid[]) FROM pg_index WHERE indrelid IN (SELECT oid FROM relation_scope)
 UNION SELECT 'pg_amop'::regclass::oid,oid FROM pg_amop WHERE amopfamily IN (SELECT opcfamily FROM pg_opclass WHERE oid IN (SELECT unnest(indclass::oid[]) FROM pg_index WHERE indrelid IN (SELECT oid FROM relation_scope)))
 UNION SELECT 'pg_amproc'::regclass::oid,oid FROM pg_amproc WHERE amprocfamily IN (SELECT opcfamily FROM pg_opclass WHERE oid IN (SELECT unnest(indclass::oid[]) FROM pg_index WHERE indrelid IN (SELECT oid FROM relation_scope)))
 UNION SELECT classid,objid FROM original_objects
),
type_dependencies(classid,objid,refclassid,refobjid) AS (
 SELECT classid,objid,refclassid,refobjid FROM pg_depend WHERE deptype<>'p'
 UNION SELECT 'pg_type'::regclass::oid,oid,'pg_type'::regclass::oid,typelem FROM pg_type WHERE typelem<>0
 UNION SELECT 'pg_type'::regclass::oid,oid,'pg_type'::regclass::oid,typbasetype FROM pg_type WHERE typbasetype<>0
 -- Records emitted for a required relation/type must themselves participate
 -- in the SAME closure. Otherwise an original seed of a captured TOAST index
 -- or publication member discovers extra records in an unchanged reread.
 UNION SELECT 'pg_class'::regclass::oid,indrelid,'pg_class'::regclass::oid,indexrelid FROM pg_index
 UNION SELECT 'pg_class'::regclass::oid,refobjid,'pg_class'::regclass::oid,objid FROM pg_depend WHERE classid='pg_class'::regclass AND refclassid='pg_class'::regclass AND deptype IN ('a','i')
 UNION SELECT 'pg_class'::regclass::oid,oid,'pg_type'::regclass::oid,reltype FROM pg_class WHERE reltype<>0
 UNION SELECT 'pg_type'::regclass::oid,oid,'pg_type'::regclass::oid,typarray FROM pg_type WHERE typarray<>0
 UNION SELECT 'pg_type'::regclass::oid,oid,'pg_class'::regclass::oid,typrelid FROM pg_type WHERE typrelid<>0
 UNION SELECT 'pg_class'::regclass::oid,adrelid,'pg_attrdef'::regclass::oid,oid FROM pg_attrdef
 UNION SELECT 'pg_class'::regclass::oid,conrelid,'pg_constraint'::regclass::oid,oid FROM pg_constraint WHERE conrelid<>0
 UNION SELECT 'pg_type'::regclass::oid,contypid,'pg_constraint'::regclass::oid,oid FROM pg_constraint WHERE contypid<>0
 UNION SELECT 'pg_class'::regclass::oid,tgrelid,'pg_trigger'::regclass::oid,oid FROM pg_trigger
 UNION SELECT 'pg_class'::regclass::oid,polrelid,'pg_policy'::regclass::oid,oid FROM pg_policy
 UNION SELECT 'pg_class'::regclass::oid,ev_class,'pg_rewrite'::regclass::oid,oid FROM pg_rewrite
 UNION SELECT 'pg_class'::regclass::oid,prrelid,'pg_publication_rel'::regclass::oid,oid FROM pg_publication_rel
 UNION SELECT 'pg_class'::regclass::oid,inhparent,'pg_class'::regclass::oid,inhrelid FROM pg_inherits
 UNION SELECT 'pg_opfamily'::regclass::oid,amopfamily,'pg_amop'::regclass::oid,oid FROM pg_amop
 UNION SELECT 'pg_opfamily'::regclass::oid,amprocfamily,'pg_amproc'::regclass::oid,oid FROM pg_amproc
),
required(classid,objid) AS (
 SELECT classid,objid FROM seed
 UNION SELECT d.refclassid,d.refobjid FROM type_dependencies d JOIN required r ON d.classid=r.classid AND d.objid=r.objid
),
record_relations AS (SELECT oid FROM relation_scope UNION SELECT objid FROM required WHERE classid='pg_class'::regclass UNION SELECT typrelid FROM pg_type WHERE typrelid<>0 AND oid IN (SELECT objid FROM required WHERE classid='pg_type'::regclass)),
records(kind,id,subid,value) AS (
 SELECT 'catalog_class',c.oid,0,jsonb_build_object('name',c.relname) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='pg_catalog' AND c.relname IN ('pg_class','pg_attrdef','pg_constraint','pg_trigger','pg_type')
 UNION ALL
 SELECT 'namespace',n.oid,0,to_jsonb(n)||jsonb_build_object('owner_name',pg_get_userbyid(n.nspowner),'comment',obj_description(n.oid,'pg_namespace')) FROM pg_namespace n WHERE n.oid=$1
 UNION ALL SELECT 'database',d.oid,0,(to_jsonb(d)-ARRAY['datfrozenxid','datminmxid'])||jsonb_build_object('owner_name',pg_get_userbyid(d.datdba),'comment',obj_description(d.oid,'pg_database')) FROM pg_database d WHERE datname=current_database()
 UNION ALL SELECT 'default_acl',d.oid,0,to_jsonb(d) FROM pg_default_acl d WHERE defaclnamespace IN (0,$1) OR d.oid IN (SELECT objid FROM required WHERE classid='pg_default_acl'::regclass)
 UNION ALL SELECT 'event_trigger',e.oid,0,to_jsonb(e) FROM pg_event_trigger e
 UNION ALL SELECT 'relation',c.oid,0,(to_jsonb(c)-ARRAY['relpages','reltuples','relallvisible','relallfrozen','relfrozenxid','relminmxid'])||jsonb_build_object('owner_name',pg_get_userbyid(c.relowner),'access_method',COALESCE(am.amname,''),'tablespace_name',COALESCE(ts.spcname,''),'namespace',n.nspname,'comment',COALESCE(obj_description(c.oid,'pg_class'),''),'parents',COALESCE((SELECT jsonb_agg(quote_ident(pn.nspname)||'.'||quote_ident(pc.relname) ORDER BY inh.inhseqno) FROM pg_inherits inh JOIN pg_class pc ON pc.oid=inh.inhparent JOIN pg_namespace pn ON pn.oid=pc.relnamespace WHERE inh.inhrelid=c.oid),'[]'::jsonb),'partition_key',CASE WHEN c.relkind='p' THEN pg_get_partkeydef(c.oid) ELSE '' END,'partition_bound',COALESCE(pg_get_expr(c.relpartbound,c.oid,false),''),'view_definition',CASE WHEN c.relkind IN ('v','m') THEN pg_get_viewdef(c.oid,false) ELSE '' END)
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_am am ON am.oid=c.relam LEFT JOIN pg_tablespace ts ON ts.oid=c.reltablespace WHERE c.oid IN (SELECT oid FROM record_relations)
 UNION ALL SELECT 'column',a.attrelid,a.attnum,to_jsonb(a)||jsonb_build_object('type_sql',format_type(a.atttypid,a.atttypmod),'default_sql',COALESCE(pg_get_expr(ad.adbin,ad.adrelid,false),''),'collation_sql',CASE WHEN a.attcollation<>0 AND a.attcollation<>t.typcollation THEN quote_ident(cn.nspname)||'.'||quote_ident(co.collname) ELSE '' END,'comment',COALESCE(col_description(a.attrelid,a.attnum),''),'identity_sequence',CASE WHEN a.attidentity<>'' THEN (SELECT to_jsonb(seq)||jsonb_build_object('schema',sn.nspname,'name',sc.relname) FROM pg_depend dep JOIN pg_sequence seq ON seq.seqrelid=dep.objid JOIN pg_class sc ON sc.oid=seq.seqrelid JOIN pg_namespace sn ON sn.oid=sc.relnamespace WHERE dep.classid='pg_class'::regclass AND dep.refclassid='pg_class'::regclass AND dep.refobjid=a.attrelid AND dep.refobjsubid=a.attnum AND dep.deptype='i') ELSE NULL END)
 FROM pg_attribute a LEFT JOIN pg_attrdef ad ON ad.adrelid=a.attrelid AND ad.adnum=a.attnum LEFT JOIN pg_type t ON t.oid=a.atttypid LEFT JOIN pg_collation co ON co.oid=a.attcollation LEFT JOIN pg_namespace cn ON cn.oid=co.collnamespace WHERE a.attrelid IN (SELECT oid FROM record_relations) AND a.attnum>0
 UNION ALL SELECT 'default',d.oid,0,to_jsonb(d)||jsonb_build_object('definition',pg_get_expr(d.adbin,d.adrelid,false)) FROM pg_attrdef d WHERE adrelid IN (SELECT oid FROM record_relations)
 UNION ALL SELECT 'constraint',con.oid,0,to_jsonb(con)||jsonb_build_object('name',con.conname,'definition',pg_get_constraintdef(con.oid,false),'relation_oid',con.conrelid::bigint,'comment',COALESCE(obj_description(con.oid,'pg_constraint'),'')) FROM pg_constraint con WHERE con.conrelid IN (SELECT oid FROM record_relations) OR con.oid IN (SELECT objid FROM required WHERE classid='pg_constraint'::regclass) OR con.contypid IN (SELECT objid FROM required WHERE classid='pg_type'::regclass)
 UNION ALL SELECT 'index',i.indexrelid,0,to_jsonb(i)||jsonb_build_object('name',c.relname,'definition',pg_get_indexdef(i.indexrelid,0,false),'relation_oid',i.indrelid::bigint,'constraint_oid',COALESCE((SELECT oid FROM pg_constraint WHERE conindid=i.indexrelid AND contype IN ('p','u','x') LIMIT 1),0)::bigint,'comment',COALESCE(obj_description(i.indexrelid,'pg_class'),'')) FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid WHERE i.indrelid IN (SELECT oid FROM record_relations)
 UNION ALL SELECT 'trigger',t.oid,0,to_jsonb(t)||jsonb_build_object('name',t.tgname,'definition',pg_get_triggerdef(t.oid,false),'relation_oid',t.tgrelid::bigint,'internal',t.tgisinternal,'constraint_oid',t.tgconstraint::bigint,'enabled',t.tgenabled,'comment',COALESCE(obj_description(t.oid,'pg_trigger'),'')) FROM pg_trigger t WHERE tgrelid IN (SELECT oid FROM record_relations)
 UNION ALL SELECT 'sequence',s.seqrelid,0,to_jsonb(s) FROM pg_sequence s WHERE seqrelid IN (SELECT oid FROM record_relations)
 UNION ALL SELECT 'inheritance',i.inhrelid,i.inhseqno,to_jsonb(i) FROM pg_inherits i WHERE inhrelid IN (SELECT oid FROM record_relations) OR inhparent IN (SELECT oid FROM record_relations)
 UNION ALL SELECT 'partition',p.partrelid,0,to_jsonb(p) FROM pg_partitioned_table p WHERE partrelid IN (SELECT oid FROM record_relations)
 UNION ALL SELECT 'policy',p.oid,0,to_jsonb(p)||jsonb_build_object('using_expression',COALESCE(pg_get_expr(p.polqual,p.polrelid,false),''),'check_expression',COALESCE(pg_get_expr(p.polwithcheck,p.polrelid,false),'')) FROM pg_policy p WHERE polrelid IN (SELECT oid FROM record_relations)
 UNION ALL SELECT 'rewrite',r.oid,0,to_jsonb(r)||jsonb_build_object('definition',pg_get_ruledef(r.oid,false)) FROM pg_rewrite r WHERE ev_class IN (SELECT oid FROM record_relations)
 UNION ALL SELECT 'routine',p.oid,0,to_jsonb(p)||jsonb_build_object('namespace',n.nspname,'definition',CASE WHEN p.prokind<>'a' THEN pg_get_functiondef(p.oid) ELSE NULL END,'identity_arguments',pg_get_function_identity_arguments(p.oid),'comment',COALESCE(obj_description(p.oid,'pg_proc'),'')) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE p.oid IN (SELECT objid FROM required WHERE classid='pg_proc'::regclass)
 UNION ALL SELECT 'aggregate',a.aggfnoid,0,to_jsonb(a) FROM pg_aggregate a WHERE aggfnoid IN (SELECT objid FROM required WHERE classid='pg_proc'::regclass)
 UNION ALL SELECT 'type',t.oid,0,to_jsonb(t)||jsonb_build_object('namespace',n.nspname,'comment',COALESCE(obj_description(t.oid,'pg_type'),'')) FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE t.oid IN (SELECT objid FROM required WHERE classid='pg_type'::regclass)
 UNION ALL SELECT 'enum',e.oid,0,to_jsonb(e) FROM pg_enum e WHERE enumtypid IN (SELECT objid FROM required WHERE classid='pg_type'::regclass)
 UNION ALL SELECT 'range',r.rngtypid,0,to_jsonb(r) FROM pg_range r WHERE rngtypid IN (SELECT objid FROM required WHERE classid='pg_type'::regclass)
 UNION ALL SELECT 'collation',c.oid,0,to_jsonb(c) FROM pg_collation c WHERE c.oid IN (SELECT objid FROM required WHERE classid='pg_collation'::regclass)
 UNION ALL SELECT 'operator',o.oid,0,to_jsonb(o) FROM pg_operator o WHERE o.oid IN (SELECT objid FROM required WHERE classid='pg_operator'::regclass)
 UNION ALL SELECT 'cast',c.oid,0,to_jsonb(c) FROM pg_cast c WHERE c.oid IN (SELECT objid FROM required WHERE classid='pg_cast'::regclass)
 UNION ALL SELECT 'access_method',a.oid,0,to_jsonb(a) FROM pg_am a WHERE a.oid IN (SELECT objid FROM required WHERE classid='pg_am'::regclass)
 UNION ALL SELECT 'operator_class',o.oid,0,to_jsonb(o) FROM pg_opclass o WHERE o.oid IN (SELECT objid FROM required WHERE classid='pg_opclass'::regclass)
 UNION ALL SELECT 'operator_family',o.oid,0,to_jsonb(o) FROM pg_opfamily o WHERE o.oid IN (SELECT objid FROM required WHERE classid='pg_opfamily'::regclass) OR o.oid IN (SELECT opcfamily FROM pg_opclass WHERE oid IN (SELECT objid FROM required WHERE classid='pg_opclass'::regclass))
 UNION ALL SELECT 'access_operator',o.oid,0,to_jsonb(o) FROM pg_amop o WHERE o.oid IN (SELECT objid FROM required WHERE classid='pg_amop'::regclass) OR o.amopfamily IN (SELECT opcfamily FROM pg_opclass WHERE oid IN (SELECT objid FROM required WHERE classid='pg_opclass'::regclass))
 UNION ALL SELECT 'access_procedure',o.oid,0,to_jsonb(o) FROM pg_amproc o WHERE o.oid IN (SELECT objid FROM required WHERE classid='pg_amproc'::regclass) OR o.amprocfamily IN (SELECT opcfamily FROM pg_opclass WHERE oid IN (SELECT objid FROM required WHERE classid='pg_opclass'::regclass))
 UNION ALL SELECT 'extension',e.oid,0,to_jsonb(e) FROM pg_extension e WHERE e.oid IN (SELECT objid FROM required WHERE classid='pg_extension'::regclass)
 UNION ALL SELECT 'publication_relation',p.oid,0,to_jsonb(p) FROM pg_publication_rel p WHERE prrelid IN (SELECT oid FROM record_relations) OR p.oid IN (SELECT objid FROM required WHERE classid='pg_publication_rel'::regclass)
 UNION ALL SELECT 'publication',p.oid,0,to_jsonb(p) FROM pg_publication p WHERE puballtables OR oid IN (SELECT objid FROM required WHERE classid='pg_publication'::regclass) OR oid IN (SELECT prpubid FROM pg_publication_rel WHERE prrelid IN (SELECT oid FROM record_relations)) /* PUB_NAMESPACE_FILTER */
 /* PUB_NAMESPACE_RECORDS */
 UNION ALL SELECT 'security_label:'||l.classoid::text,l.objoid,l.objsubid,jsonb_build_object('labels',jsonb_agg(jsonb_build_object('provider',l.provider,'label',l.label) ORDER BY l.provider)) FROM pg_seclabel l WHERE (l.classoid,l.objoid) IN (SELECT classid,objid FROM required) GROUP BY l.classoid,l.objoid,l.objsubid
 UNION ALL SELECT 'dependency',d.objid,0,jsonb_build_object('edges',jsonb_agg(to_jsonb(d) ORDER BY classid,objsubid,refclassid,refobjid,refobjsubid,deptype)) FROM pg_depend d WHERE (d.classid,d.objid) IN (SELECT classid,objid FROM required) GROUP BY d.objid
)
SELECT kind,id::bigint,subid::integer,CASE WHEN octet_length(value::text)<=1048576 THEN value::text ELSE NULL END FROM records ORDER BY kind,id,subid LIMIT 10001`
