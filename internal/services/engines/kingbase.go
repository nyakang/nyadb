package engines

import (
	"context"
	"database/sql"
	"net"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	kingbasesvc "github.com/nyakang/nyadb/internal/services/kingbase"
)

// This product uses the audited V8R6 PG-mode catalog rules with a separate
// native connector and server gate. It never inherits PG management features.
type kingbaseDialect struct{ postgresDialect }

func (kingbaseDialect) open(p connections.Profile, password string, dial func(context.Context, string, string) (net.Conn, error)) (*sql.DB, error) {
	p, err := connections.NormalizeProfile(p)
	if err != nil {
		return nil, err
	}
	cfg, err := connections.KingbaseProfileConfig(p)
	if err != nil {
		return nil, err
	}
	connector, err := kingbasesvc.NewSessionConnector(cfg, password, dial)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(connector), nil
}
func (kingbaseDialect) namespace(schema string) error {
	if schema == "" || len(schema) > 63 || strings.ContainsAny(schema, "\x00\r\n") || strings.HasPrefix(schema, "pg_") {
		return contract.ErrUnsupported
	}
	switch schema {
	case "information_schema", "sys_catalog", "sysaudit", "sysmac", "sys_hm", "perf", "anon", "src_restrict", "xlog_record_read":
		return contract.ErrUnsupported
	}
	return nil
}
func (d kingbaseDialect) schemas(ctx context.Context, q queryer) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT nspname FROM pg_catalog.pg_namespace WHERE pg_catalog.has_schema_privilege(oid,'USAGE') ORDER BY nspname LIMIT 50001`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		if d.namespace(name) == nil {
			out = append(out, name)
		}
		if len(out) > 50000 {
			return nil, contract.ErrUnsupported
		}
	}
	return out, rows.Err()
}
func (d kingbaseDialect) structure(ctx context.Context, s *Session, q queryer, o contract.ObjectRef) (contract.Table, error) {
	var allowed bool
	if err := queryOne(ctx, q, `SELECT pg_catalog.has_schema_privilege(n.oid,'USAGE') AND pg_catalog.has_table_privilege(c.oid,'SELECT') FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2`, []any{o.Schema, o.Name}, &allowed); err != nil {
		return contract.Table{Object: o}, err
	}
	if !allowed {
		return contract.Table{Object: o}, contract.ErrUnsupported
	}
	t, err := s.postgresStructure(ctx, q, o)
	if err != nil {
		return t, err
	}
	// Only immediate, ready, live ordinary btree keys with built-in default
	// operator classes prove identity for the shared equality/order semantics.
	t.Key = nil
	for i := range t.Columns {
		t.Columns[i].Primary = false
	}
	keys, err := kingbaseKeys(ctx, q, t)
	if err != nil {
		return t, err
	}
	if len(keys) > 0 {
		t.Key = keys[0]
	}
	var ordinary bool
	if err = queryOne(ctx, q, `SELECT c.relkind='r' AND NOT c.relispartition AND NOT c.relhassubclass AND am.amname='heap' AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_inherits inh WHERE inh.inhrelid=c.oid OR inh.inhparent=c.oid) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_catalog.pg_am am ON am.oid=c.relam WHERE n.nspname=$1 AND c.relname=$2`, []any{o.Schema, o.Name}, &ordinary); err != nil {
		return t, err
	}
	if !ordinary {
		t.Key = nil
	}
	t.Editable = o.Kind == "table" && ordinary && len(t.Key) > 0
	return t, nil
}

func kingbaseKeys(ctx context.Context, q queryer, t contract.Table) ([][]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT i.indexrelid,a.attname,i.indisprimary FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid=i.indrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace JOIN pg_catalog.pg_class ic ON ic.oid=i.indexrelid JOIN pg_catalog.pg_am am ON am.oid=ic.relam CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY k(num,ord) JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum=k.num WHERE n.nspname=$1 AND c.relname=$2 AND i.indisunique AND i.indisvalid AND i.indisready AND i.indislive AND i.indimmediate AND i.indpred IS NULL AND i.indexprs IS NULL AND am.amname='btree' AND k.ord<=i.indnkeyatts AND NOT EXISTS(SELECT 1 FROM unnest(i.indkey) WITH ORDINALITY kk(num,ord) JOIN pg_catalog.pg_attribute aa ON aa.attrelid=c.oid AND aa.attnum=kk.num WHERE kk.ord<=i.indnkeyatts AND (NOT aa.attnotnull OR aa.attgenerated<>'')) AND NOT EXISTS(SELECT 1 FROM unnest(i.indclass) ocid JOIN pg_catalog.pg_opclass oc ON oc.oid=ocid JOIN pg_catalog.pg_namespace ns ON ns.oid=oc.opcnamespace WHERE NOT oc.opcdefault OR ns.nspname<>'pg_catalog') ORDER BY i.indisprimary DESC,i.indexrelid,k.ord`, t.Object.Schema, t.Object.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys [][]string
	var previous int64
	for rows.Next() {
		var id int64
		var name string
		var primary bool
		if err = rows.Scan(&id, &name, &primary); err != nil {
			return nil, err
		}
		if id != previous {
			keys = append(keys, nil)
			previous = id
		}
		keys[len(keys)-1] = append(keys[len(keys)-1], name)
		if len(keys) == 1 && primary {
			for i := range t.Columns {
				if t.Columns[i].Name == name {
					t.Columns[i].Primary = true
				}
			}
		}
	}
	return keys, rows.Err()
}

func (d kingbaseDialect) setSchema(ctx context.Context, c *sql.Conn, schema string) error {
	if err := d.namespace(schema); err != nil {
		return err
	}
	return d.postgresDialect.setSchema(ctx, c, schema)
}
