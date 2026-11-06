package engines

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// Wire configuration and value comparison are shared; catalogs and product
// verification belong to CockroachDB rather than PostgreSQL's version contract.
type cockroachDialect struct{ postgresDialect }

var cockroachVersionPattern = regexp.MustCompile(`^CockroachDB (?:CCL|OSS) v([0-9]+)\.([0-9]+)\.([0-9]+)(?: |-|$)`)

func validateCockroachVersion(version string) error {
	m := cockroachVersionPattern.FindStringSubmatch(version)
	if m == nil {
		return fmt.Errorf("%w: CockroachDB connection requires a CockroachDB server", contract.ErrUnsupported)
	}
	major, e1 := strconv.Atoi(m[1])
	minor, e2 := strconv.Atoi(m[2])
	_, e3 := strconv.Atoi(m[3])
	if e1 != nil || e2 != nil || e3 != nil || major < 25 || major == 25 && minor < 2 {
		return fmt.Errorf("%w: CockroachDB 25.2 or later is required", contract.ErrUnsupported)
	}
	return nil
}
func validateCockroachServer(ctx context.Context, db *sql.DB) error {
	var version string
	if err := db.QueryRowContext(ctx, "SELECT version()").Scan(&version); err != nil {
		return err
	}
	return validateCockroachVersion(version)
}
func (cockroachDialect) schemas(ctx context.Context, q queryer) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT schema_name FROM information_schema.schemata WHERE catalog_name=current_database() AND schema_name NOT IN ('pg_catalog','information_schema','crdb_internal','pg_extension') ORDER BY schema_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err = rows.Scan(&n); err != nil {
			return nil, err
		}
		if len(names) >= 50000 {
			return nil, errors.New("schema metadata limit exceeded")
		}
		names = append(names, n)
	}
	return names, rows.Err()
}
func (cockroachDialect) objects(ctx context.Context, q queryer, schema string) (*sql.Rows, error) {
	return q.QueryContext(ctx, `SELECT table_name,CASE WHEN table_type='BASE TABLE' THEN 'table' ELSE 'view' END FROM information_schema.tables WHERE table_catalog=current_database() AND table_schema=$1 ORDER BY table_name`, schema)
}
func (cockroachDialect) structure(ctx context.Context, s *Session, q queryer, o contract.ObjectRef) (contract.Table, error) {
	return s.cockroachStructure(ctx, q, o)
}
func cockroachCreate(ctx context.Context, q queryer, o contract.ObjectRef) (string, error) {
	rows, err := q.QueryContext(ctx, "SHOW CREATE "+qualified(o))
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", errors.Join(sql.ErrNoRows, rows.Err())
	}
	var name, ddl string
	if err = rows.Scan(&name, &ddl); err != nil {
		return "", err
	}
	if ddl == "" || len(ddl) > 1<<20 || rows.Next() {
		return "", errors.New("complete object definition unavailable or exceeds 1 MiB")
	}
	return ddl, rows.Err()
}
func cockroachTableIdentity(ctx context.Context, q queryer, o contract.ObjectRef) (string, error) {
	ddl, err := cockroachCreate(ctx, q, o)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(ddl))
	return hex.EncodeToString(sum[:]), nil
}
func cockroachDataType(decl string) string {
	t := strings.ToUpper(decl)
	switch t {
	case "INT8":
		return "bigint"
	case "INT4":
		return "integer"
	case "INT2":
		return "smallint"
	case "STRING":
		return "text"
	case "BYTES":
		return "bytea"
	case "BOOL":
		return "boolean"
	case "FLOAT8":
		return "double precision"
	case "FLOAT4":
		return "real"
	case "TIMESTAMP":
		return "timestamp without time zone"
	case "TIMESTAMPTZ":
		return "timestamp with time zone"
	case "TIME":
		return "time without time zone"
	case "TIMETZ":
		return "time with time zone"
	}
	return strings.ToLower(decl)
}
func (s *Session) cockroachDataOrder(t contract.Table, r contract.PageRequest, identity string) ([]dataOrder, bool, string, error) {
	cols := validColumns(t)
	stable := len(t.Key) > 0 && len(t.Key) <= 16
	if len(r.Sorts) > 16 {
		return nil, false, "", contract.ErrUnsupported
	}
	var orders []dataOrder
	for _, v := range r.Sorts {
		f, ok := cols[v.Column]
		if !ok {
			return nil, false, "", contract.ErrUnsupported
		}
		if f.Nullable || strings.Contains(strings.ToLower(f.DatabaseType), "collate") || !pgDataScalar(f.DatabaseType) && !pgDataNumeric(f.DatabaseType) && !pgDataText(f.DatabaseType) && canonicalDataType(f.DatabaseType) != "bytea" {
			stable = false
		}
		orders = append(orders, dataOrder{v.Column, quote(v.Column), v.Descending})
	}
	for _, key := range t.Key {
		f := cols[key]
		if f.Nullable || strings.Contains(strings.ToLower(f.DatabaseType), "collate") || !pgDataScalar(f.DatabaseType) && !pgDataNumeric(f.DatabaseType) && !pgDataText(f.DatabaseType) && canonicalDataType(f.DatabaseType) != "bytea" {
			stable = false
		}
		orders = append(orders, dataOrder{key, quote(key), false})
	}
	// Native uncollated strings use binary ordering. Collated columns are not
	// selected as edit keys and cannot prove portable cursor comparisons.
	return orders, stable, identity, nil
}
