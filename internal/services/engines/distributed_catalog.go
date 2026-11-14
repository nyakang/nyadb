package engines

import (
	"context"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/pgdistributed"
)

// Native catalogs are independent of navigation labels. JSON arrays preserve
// identifier newlines and commas which a delimiter-based aggregate would lose.
const cloudberryDistributionSQL = `SELECT dp.policytype::text,
 COALESCE((SELECT json_agg(a.attname ORDER BY k.ord)::text
 FROM unnest(dp.distkey::smallint[]) WITH ORDINALITY k(attnum,ord)
 JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum=k.attnum
 WHERE NOT a.attisdropped),'[]'),COALESCE(cardinality(dp.distkey::smallint[]),0)
 FROM pg_catalog.pg_class c
 JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 JOIN pg_catalog.gp_distribution_policy dp ON dp.localoid=c.oid
 WHERE n.nspname=$1 AND c.relname=$2 LIMIT 2`

const openTenBaseDistributionSQL = `SELECT xc.pclocatortype::text,
 COALESCE((SELECT json_agg(a.attname ORDER BY k.ord)::text
 FROM unnest(xc.discolnums::smallint[]) WITH ORDINALITY k(attnum,ord)
 JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum=k.attnum
 WHERE NOT a.attisdropped),'[]'),COALESCE(cardinality(xc.discolnums::smallint[]),0)
 FROM pg_catalog.pg_class c
 JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 JOIN pg_catalog.pgxc_class xc ON xc.pcrelid=c.oid
 WHERE n.nspname=$1 AND c.relname=$2 LIMIT 2`

// readDistributedPolicy is used by the forthcoming native product strategy.
// It runs on the caller's original metadata snapshot/transaction, never a new
// profile lookup, and treats absent policies separately from unknown semantics.
func (s *Session) readDistributedPolicy(ctx context.Context, q queryer, object contract.ObjectRef) (*pgdistributed.Policy, error) {
	if err := s.check(object); err != nil {
		return nil, err
	}
	product := pgdistributed.Product(s.kind)
	if object.ProfileID <= 0 || object.Catalog == "" || object.Schema == "" || object.Name == "" || object.Kind != "table" {
		return nil, contract.ErrUnsupported
	}
	var query string
	switch product {
	case pgdistributed.Cloudberry:
		query = cloudberryDistributionSQL
	case pgdistributed.OpenTenBase:
		query = openTenBaseDistributionSQL
	default:
		return nil, contract.ErrUnsupported
	}
	rows, err := q.QueryContext(ctx, query, object.Schema, object.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	policy := &pgdistributed.Policy{Product: product}
	var raw string
	var expected int
	if err = rows.Scan(&policy.Code, &raw, &expected); err != nil {
		return nil, err
	}
	if rows.Next() {
		return nil, errors.New("ambiguous native distribution metadata")
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(raw) > 256<<10 || !utf8.ValidString(raw) || json.Unmarshal([]byte(raw), &policy.Columns) != nil || policy.Columns == nil || len(policy.Columns) != expected {
		return nil, pgdistributed.ErrPolicy
	}
	if _, err = policy.Clause(); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return policy, nil
}
