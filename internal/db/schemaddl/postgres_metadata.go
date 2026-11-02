package schemaddl

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// PlanPostgreSQLMetadata is the first, non-converting PostgreSQL plan. It only
// changes table/column comments and column storage, compression and statistics.
// Names, definitions, permissions, dependencies and unknown properties stay
// untouched. It is not a create/rebuild or general ALTER planner.
func PlanPostgreSQLMetadata(original Snapshot, desired Table, schema string) (Plan, error) {
	p := Plan{Mode: "noop"}
	if original.Engine != "postgresql" || !original.Exists || original.Postgres == nil || original.Postgres.RelationOID <= 0 || original.Postgres.Namespace != schema {
		return Plan{}, ErrUnsupported
	}
	if PostgreSQLIdentifier(schema) != nil || PostgreSQLIdentifier(desired.Name) != nil {
		return Plan{}, ErrUnsupported
	}
	revision, err := original.Postgres.Fingerprint()
	if err != nil {
		return Plan{}, err
	}
	canonical, err := original.Postgres.Table()
	if err != nil || revision != original.Revision || Signature(canonical) != Signature(original.Table) {
		return Plan{}, errors.Join(ErrConflict, err)
	}
	if desired.Name != canonical.Name || desired.Postgres == nil || len(desired.Clauses) != len(canonical.Clauses) {
		return Plan{}, ErrUnsupported
	}
	if canonical.Postgres.Kind != "r" || canonical.Postgres.IsPartition || len(canonical.Postgres.Parents) != 0 {
		return Plan{}, ErrUnsupported
	}
	unchanged := desired.Clone()
	unchanged.Postgres.Comment = canonical.Postgres.Comment
	target := Quote(schema) + "." + Quote(canonical.Name)
	if desired.Postgres.Comment != canonical.Postgres.Comment {
		literal, err := postgresComment(desired.Postgres.Comment)
		if err != nil {
			return Plan{}, err
		}
		p.SQL = append(p.SQL, "COMMENT ON TABLE "+target+" IS "+literal)
	}
	for i, before := range canonical.Clauses {
		after := desired.Clauses[i]
		if before.Kind != "column" {
			continue
		}
		if before.Postgres == nil || after.Postgres == nil || PostgreSQLIdentifier(before.Name) != nil {
			return Plan{}, ErrUnsupported
		}
		a, b := before.Postgres, after.Postgres
		// Inherited attributes can affect descendants; this first planner does
		// not acquire/verify a partition or inheritance lock set.
		if !a.Local || a.InheritanceCount != 0 {
			return Plan{}, ErrUnsupported
		}
		unchanged.Clauses[i].Postgres.Comment = a.Comment
		unchanged.Clauses[i].Postgres.Storage = a.Storage
		unchanged.Clauses[i].Postgres.Compression = a.Compression
		unchanged.Clauses[i].Postgres.Statistics = a.Statistics
		column := "ALTER TABLE ONLY " + target + " ALTER COLUMN " + Quote(before.Name)
		if a.Comment != b.Comment {
			literal, err := postgresComment(b.Comment)
			if err != nil {
				return Plan{}, err
			}
			p.SQL = append(p.SQL, "COMMENT ON COLUMN "+target+"."+Quote(before.Name)+" IS "+literal)
		}
		if a.Storage != b.Storage {
			storage := map[string]string{"p": "PLAIN", "e": "EXTERNAL", "m": "MAIN", "x": "EXTENDED"}[b.Storage]
			if storage == "" {
				return Plan{}, ErrUnsupported
			}
			p.SQL = append(p.SQL, column+" SET STORAGE "+storage)
		}
		if a.Compression != b.Compression {
			compression, ok := map[string]string{"": "default", "p": "pglz", "l": "lz4"}[b.Compression]
			if !ok {
				return Plan{}, ErrUnsupported
			}
			p.SQL = append(p.SQL, column+" SET COMPRESSION "+compression)
		}
		if a.Statistics != b.Statistics {
			if b.Statistics < -1 || b.Statistics > 10000 {
				return Plan{}, ErrUnsupported
			}
			p.SQL = append(p.SQL, fmt.Sprintf("%s SET STATISTICS %d", column, b.Statistics))
		}
	}
	if Signature(unchanged) != Signature(canonical) {
		return Plan{}, ErrUnsupported
	}
	if len(p.SQL) > 0 {
		p.Mode = "metadata"
	}
	return p, nil
}

func postgresComment(value string) (string, error) {
	if len(value) > 1<<20 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return "", errors.New("invalid or oversized PostgreSQL comment")
	}
	if value == "" {
		return "NULL", nil
	}
	// The executor always uses standard_conforming_strings=on.
	return "'" + strings.ReplaceAll(value, "'", "''") + "'", nil
}

// VerifyPostgreSQLMetadata checks the complete catalog, allowing only the
// explicitly requested attributes to differ. Unknown catalog keys and exact
// numeric values remain in this comparison, including external dependencies.
func VerifyPostgreSQLMetadata(original Snapshot, desired Table, actual Snapshot, schema string) error {
	if _, err := PlanPostgreSQLMetadata(original, desired, schema); err != nil {
		return err
	}
	if !actual.Exists || actual.Engine != original.Engine || actual.Postgres == nil || Signature(actual.Table) != Signature(desired) {
		return errors.New("PostgreSQL metadata differs from the requested table")
	}
	expected := original.Postgres.Clone()
	type change struct{ before, after *PostgreSQLColumnProperties }
	columns := map[int]change{}
	for i, c := range desired.Clauses {
		if c.Kind == "column" {
			var number int
			if _, err := fmt.Sscanf(c.ID, "column:%d", &number); err != nil {
				return err
			}
			columns[number] = change{original.Table.Clauses[i].Postgres, c.Postgres}
		}
	}
	for i, entry := range expected.Entries {
		if entry.OID != expected.RelationOID || entry.Kind != "relation" && entry.Kind != "column" {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(string(entry.Data)))
		decoder.UseNumber()
		var raw map[string]any
		if err := decoder.Decode(&raw); err != nil {
			return err
		}
		if entry.Kind == "relation" {
			if original.Table.Postgres.Comment != desired.Postgres.Comment {
				raw["comment"] = desired.Postgres.Comment
			}
		} else if column, ok := columns[entry.SubID]; ok {
			a, b := column.before, column.after
			if a.Comment != b.Comment {
				raw["comment"] = b.Comment
			}
			if a.Storage != b.Storage {
				raw["attstorage"] = b.Storage
			}
			if a.Compression != b.Compression {
				raw["attcompression"] = b.Compression
			}
			if a.Statistics != b.Statistics {
				if b.Statistics == -1 && expected.ServerVersion >= 170000 {
					// PG 17 moved the default marker to a nullable catalog
					// field. Zero explicitly disables statistics collection.
					raw["attstattarget"] = nil
				} else {
					raw["attstattarget"] = b.Statistics
				}
			}
		}
		var err error
		expected.Entries[i].Data, err = json.Marshal(raw)
		if err != nil {
			return err
		}
	}
	want, err := expected.Fingerprint()
	if err != nil {
		return err
	}
	got, err := actual.Postgres.Fingerprint()
	if err != nil {
		return err
	}
	if got != want || actual.Revision != got {
		return errors.New("PostgreSQL catalog changed beyond the requested metadata")
	}
	return nil
}
