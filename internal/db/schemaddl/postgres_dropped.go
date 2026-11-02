package schemaddl

import (
	"encoding/json"
	"errors"
	"fmt"
)

// pgExpectedDroppedAttributes derives native residuals exclusively from the
// original live attribute and desired stable IDs. Actual catalog records and
// fresh CREATE defaults cannot grant permission for an original property loss.
// Callers first validate PlanPostgreSQL (including explicit removal consent),
// independent target declaration and retained target properties.
func pgExpectedDroppedAttributes(original Snapshot, desired Table) ([]PostgreSQLCatalogEntry, error) {
	if !original.Exists || original.Engine != "postgresql" || original.Postgres == nil {
		return nil, ErrUnsupported
	}
	bindings, err := BuildPostgreSQLGraphBindings(*original.Postgres, pgPreservationNames(original.Table))
	if err != nil {
		return nil, err
	}
	kept := map[string]bool{}
	for _, column := range desired.Columns() {
		kept["@column:"+column.ID] = true
	}
	var out []PostgreSQLCatalogEntry
	for _, entry := range original.Postgres.Entries {
		if entry.Kind != "column" || entry.OID != original.Postgres.RelationOID {
			continue
		}
		identity := bindings.Attributes[entry.OID][entry.SubID]
		if identity == "" || kept[identity] {
			continue // Pre-existing dropped slots remain strictly unchanged.
		}
		residual, err := pgNativeDroppedAttribute(entry, original.Postgres.ServerVersion)
		if err != nil {
			return nil, err
		}
		out = append(out, residual)
	}
	return out, nil
}

// Native public pg_attribute semantics are versioned: PG 14-16 keep nullable
// ACL/options and set statistics to 0; PG 17+ clear those fields to NULL.
// All other captured fields (including unknown fields and attlen/attalign)
// must match the ORIGINAL. This is an independent expected transformation,
// not a list of fields to ignore. See docs/postgres-dropped-attribute-2026-11-02.md.
func pgNativeDroppedAttribute(entry PostgreSQLCatalogEntry, version int) (PostgreSQLCatalogEntry, error) {
	if version < 140000 || entry.Kind != "column" || entry.OID <= 0 || entry.OID > 1<<32-1 || entry.SubID <= 0 || entry.SubID > 1<<15-1 {
		return PostgreSQLCatalogEntry{}, ErrUnsupported
	}
	fields, err := pgPreservationFields(entry.Data)
	if err != nil {
		return PostgreSQLCatalogEntry{}, err
	}
	for _, field := range []string{"attrelid", "attname", "attnum", "atttypid", "atttypmod", "attlen", "attbyval", "attalign", "attndims", "attcollation", "attnotnull", "atthasdef", "attidentity", "attgenerated", "attisdropped", "attislocal", "attinhcount", "attstorage", "attcompression", "attstattarget", "attoptions", "attfdwoptions", "attacl", "atthasmissing", "attmissingval", "type_sql", "default_sql", "collation_sql", "comment", "identity_sequence"} {
		if _, exists := fields[field]; !exists {
			return PostgreSQLCatalogEntry{}, errors.New("incomplete PostgreSQL original attribute evidence")
		}
	}
	// PG 18 moved the cache offset out of the public pg_attribute catalog.
	// Do not invent it when absent; any captured value still stays unchanged.
	if _, exists := fields["attcacheoff"]; !exists && version < 180000 {
		return PostgreSQLCatalogEntry{}, errors.New("incomplete PostgreSQL original attribute evidence")
	}
	r := pgGraphRecord{entry: entry, data: fields}
	for _, field := range []string{"attnotnull", "atthasdef", "attbyval", "attislocal"} {
		if _, err := r.boolean(field); err != nil {
			return PostgreSQLCatalogEntry{}, errors.New("invalid PostgreSQL original attribute flags")
		}
	}
	length, le := r.integer("attlen")
	align, ae := r.text("attalign")
	if le != nil || ae != nil || length < -1<<15 || length > 1<<15-1 || align != "c" && align != "s" && align != "i" && align != "d" {
		return PostgreSQLCatalogEntry{}, errors.New("invalid PostgreSQL original attribute layout")
	}
	owner, oe := r.integer("attrelid")
	number, ne := r.integer("attnum")
	typeOID, te := r.integer("atttypid")
	dropped, de := r.boolean("attisdropped")
	missing, me := r.boolean("atthasmissing")
	if oe != nil || ne != nil || te != nil || de != nil || me != nil || owner != entry.OID || number != int64(entry.SubID) || typeOID <= 0 || typeOID > 1<<32-1 || dropped {
		return PostgreSQLCatalogEntry{}, errors.New("invalid PostgreSQL original live attribute identity")
	}
	// Preserve the native JSON OID representation (to_jsonb normally emits
	// decimal strings), without coercing arbitrary numbers or future fields.
	zero := json.RawMessage(`0`)
	if fields["atttypid"][0] == '"' {
		zero = json.RawMessage(`"0"`)
	}
	fields["atttypid"] = zero
	fields["attisdropped"] = json.RawMessage(`true`)
	fields["attnotnull"], fields["atthasdef"] = json.RawMessage(`false`), json.RawMessage(`false`)
	fields["attgenerated"] = json.RawMessage(`""`)
	fields["attname"], err = json.Marshal(fmt.Sprintf("........pg.dropped.%d........", entry.SubID))
	if err != nil {
		return PostgreSQLCatalogEntry{}, err
	}
	if missing || version >= 170000 {
		fields["atthasmissing"], fields["attmissingval"] = json.RawMessage(`false`), json.RawMessage(`null`)
	}
	fields["attstattarget"] = json.RawMessage(`0`)
	if version >= 170000 {
		for _, field := range []string{"attstattarget", "attacl", "attoptions", "attfdwoptions"} {
			fields[field] = json.RawMessage(`null`)
		}
	}
	// These are named loader-derived presentations of removed assets. Native
	// attidentity/storage/alignment/typmod and all other raw fields stay intact.
	fields["type_sql"] = json.RawMessage(`"-"`)
	for _, field := range []string{"default_sql", "collation_sql", "comment"} {
		fields[field] = json.RawMessage(`""`)
	}
	fields["identity_sequence"] = json.RawMessage(`null`)
	entry.Data, err = pgGraphCanonicalJSON(fields)
	if err != nil {
		return PostgreSQLCatalogEntry{}, err
	}
	return entry, nil
}
