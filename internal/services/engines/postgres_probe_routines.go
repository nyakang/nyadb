package engines

import (
	"context"
	"database/sql"
	"errors"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// This protects explicit index routine calls before CREATE's expression
// validation. It is not yet a general probe safety proof: custom types/casts,
// operators/opclasses, implicit callbacks and other expression scopes remain
// separate prerequisites. Never use this result alone to enable Apply.
func postgresCheckIndexProbeRoutines(ctx context.Context, c *sql.Conn, desired schemaddl.Table, schema string) error {
	approved := map[string]bool{
		"abs": true, "round": true, "trunc": true, "ceil": true, "ceiling": true, "floor": true,
		"sign": true, "sqrt": true, "power": true, "mod": true,
		"lower": true, "upper": true, "length": true, "char_length": true, "character_length": true,
		"octet_length": true, "bit_length": true, "substring": true, "substr": true,
		"left": true, "right": true, "btrim": true, "ltrim": true, "rtrim": true,
		"replace": true, "translate": true,
	}
	checked := map[string]bool{}
	for _, definition := range desired.Definitions {
		if definition.Kind != "index" {
			continue
		}
		calls, err := schemaddl.PostgreSQLIndexProbeCalls(definition, schema, desired.Name)
		if err != nil {
			return err
		}
		for _, call := range calls {
			if call.Schema != "" && call.Schema != "pg_catalog" || !approved[call.Name] {
				return errors.New("PostgreSQL definition probe requires a supported core index routine")
			}
			if checked[call.Name] {
				continue
			}
			// The probe installs search_path=pg_catalog. Check EVERY overload,
			// so a user/extension overload cannot acquire trust by sharing a
			// supported builtin name. IMMUTABLE alone is never sufficient.
			rows, err := c.QueryContext(ctx, `SELECT p.oid::bigint,p.prokind::text,p.provolatile::text,l.lanname::text
FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
JOIN pg_catalog.pg_language l ON l.oid=p.prolang WHERE n.nspname='pg_catalog' AND p.proname=$1`, call.Name)
			if err != nil {
				return err
			}
			count := 0
			for rows.Next() {
				var oid int64
				var kind, volatility, language string
				if err := rows.Scan(&oid, &kind, &volatility, &language); err != nil {
					rows.Close()
					return err
				}
				count++
				if count > 64 || oid <= 0 || oid >= 16384 || kind != "f" || volatility != "i" || language != "internal" && language != "c" && language != "sql" {
					rows.Close()
					return errors.New("PostgreSQL index probe routine is not an immutable supported core overload")
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if count == 0 {
				return errors.New("PostgreSQL core index probe routine is unavailable")
			}
			checked[call.Name] = true
		}
	}
	return ctx.Err()
}
