package engines

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"sort"
)

// Utility LOCK statements precede the first catalog SELECT, so a change
// committed while waiting remains visible to the original revision check.
func postgresLockDesignReferences(ctx context.Context, c *sql.Conn, r contract.SchemaDesignRequest) error {
	refs, err := schemaddl.PostgreSQLReferencedTables(r.Desired, r.Original.Target.Object.Schema)
	if err != nil {
		return err
	}
	prior, err := schemaddl.PostgreSQLReferencedTables(r.Original.Schema.Table, r.Original.Target.Object.Schema)
	if err != nil {
		return err
	}
	seen := map[schemaddl.PostgreSQLTableReference]bool{}
	for _, ref := range append(refs, prior...) {
		seen[ref] = true
	}
	refs = nil
	for ref := range seen {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Schema != refs[j].Schema {
			return refs[i].Schema < refs[j].Schema
		}
		return refs[i].Name < refs[j].Name
	})
	for _, ref := range refs {
		original := r.Original.Target.Object
		if r.Original.Schema.Exists && ref.Schema == original.Schema && ref.Name == original.Name {
			continue
		}
		if _, err := c.ExecContext(ctx, "LOCK TABLE ONLY "+schemaddl.Quote(ref.Schema)+"."+schemaddl.Quote(ref.Name)+" IN SHARE MODE"); err != nil {
			return err
		}
	}
	return nil
}

// Capture newly referenced relations before an empty FK probe installs its
// temporary internal triggers. Those probe effects must never become the
// original baseline for the referenced table.
func postgresCaptureDesignReferences(ctx context.Context, c *sql.Conn, original contract.SchemaDesignSnapshot, desired schemaddl.Table) (contract.SchemaDesignSnapshot, error) {
	refs, err := schemaddl.PostgreSQLReferencedTables(desired, original.Target.Object.Schema)
	if err != nil {
		return original, err
	}
	seed := original.Schema.Postgres.Clone()
	known := map[int64]bool{}
	for _, e := range seed.Entries {
		if e.Kind == "relation" {
			known[e.OID] = true
		}
	}
	added := false
	for _, ref := range refs {
		var oid int64
		if err := c.QueryRowContext(ctx, `SELECT c.oid::bigint FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND c.relkind='r' AND NOT c.relispartition`, ref.Schema, ref.Name).Scan(&oid); err != nil {
			return original, err
		}
		if !known[oid] {
			seed.Entries = append(seed.Entries, schemaddl.PostgreSQLCatalogEntry{Kind: "relation", OID: oid, Data: json.RawMessage(`{"reference":true}`)})
			known[oid], added = true, true
		}
	}
	if !added {
		return original, nil
	}
	return postgresCatalogSnapshotPreserving(ctx, c, original.Target, seed)
}
