package engines

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func pgOriginalCapture(t *testing.T, s *Session, original contract.SchemaDesignSnapshot) contract.SchemaDesignSnapshot {
	t.Helper()
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ExecContext(context.Background(), "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	for _, statement := range []string{"SET LOCAL search_path=pg_catalog", "SET LOCAL standard_conforming_strings=on"} {
		if _, err = c.ExecContext(context.Background(), statement); err != nil {
			t.Fatal(err)
		}
	}
	captured, err := postgresCatalogSnapshotPreserving(context.Background(), c, original.Target, original.Schema.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ExecContext(context.Background(), "COMMIT"); err != nil {
		t.Fatal(err)
	}
	return captured
}

func pgOriginalEntry(t *testing.T, p *schemaddl.PostgreSQLCatalog, kind, field, value string) schemaddl.PostgreSQLCatalogEntry {
	t.Helper()
	for _, e := range p.Entries {
		if e.Kind != kind {
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(e.Data, &fields) != nil {
			t.Fatal("invalid real original catalog record")
		}
		var text string
		if json.Unmarshal(fields[field], &text) == nil && text == value {
			return e
		}
	}
	t.Fatal("real original catalog entry absent", kind, field, value)
	return schemaddl.PostgreSQLCatalogEntry{}
}

func pgOriginalFind(p *schemaddl.PostgreSQLCatalog, kind string, oid int64, sub int) (schemaddl.PostgreSQLCatalogEntry, bool) {
	for _, e := range p.Entries {
		if e.Kind == kind && e.OID == oid && e.SubID == sub {
			return e, true
		}
	}
	return schemaddl.PostgreSQLCatalogEntry{}, false
}

func TestPostgreSQL17DesignOriginalContextFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_original_context; CREATE SCHEMA nyadb_original_external;
CREATE TYPE nyadb_original_external.detached_mood AS ENUM('one','two');
CREATE FUNCTION nyadb_original_external.detached_cast(nyadb_original_external.detached_mood) RETURNS integer LANGUAGE SQL IMMUTABLE AS $$SELECT 1$$;
CREATE CAST(nyadb_original_external.detached_mood AS integer) WITH FUNCTION nyadb_original_external.detached_cast(nyadb_original_external.detached_mood) AS ASSIGNMENT;
CREATE TABLE nyadb_original_external.detached_parent(id uuid PRIMARY KEY, quota bigint GENERATED ALWAYS AS IDENTITY);
COMMENT ON TABLE nyadb_original_external.detached_parent IS 'original external relation';
CREATE PUBLICATION nyadb_original_publication FOR TABLE nyadb_original_external.detached_parent;
CREATE TABLE nyadb_original_context.original(id uuid, parent_id uuid, mood nyadb_original_external.detached_mood,
 CONSTRAINT detached_fk FOREIGN KEY(parent_id) REFERENCES nyadb_original_external.detached_parent(id));`)
	base := pgDesignRead(t, s, "nyadb_original_context", "original")
	originalSignature := schemaddl.Signature(base)
	parent := pgOriginalEntry(t, base.Schema.Postgres, "relation", "relname", "detached_parent")
	mood := pgOriginalEntry(t, base.Schema.Postgres, "type", "typname", "detached_mood")
	fn := pgOriginalEntry(t, base.Schema.Postgres, "routine", "proname", "detached_cast")
	publication := pgOriginalEntry(t, base.Schema.Postgres, "publication", "pubname", "nyadb_original_publication")
	sequence := pgOriginalEntry(t, base.Schema.Postgres, "relation", "relname", "detached_parent_quota_seq")
	run(t, s, `ALTER TABLE nyadb_original_context.original DROP CONSTRAINT detached_fk; ALTER TABLE nyadb_original_context.original DROP COLUMN mood;`)
	current := pgDesignRead(t, s, "nyadb_original_context", "original")
	for _, expected := range []schemaddl.PostgreSQLCatalogEntry{parent, mood, fn, publication, sequence} {
		if _, found := pgOriginalFind(current.Schema.Postgres, expected.Kind, expected.OID, expected.SubID); found {
			t.Fatal("fixture did not detach the original external context", expected.Kind)
		}
	}
	preserved := pgOriginalCapture(t, s, base)
	for _, expected := range []schemaddl.PostgreSQLCatalogEntry{parent, mood, fn, publication, sequence} {
		entry, found := pgOriginalFind(preserved.Schema.Postgres, expected.Kind, expected.OID, expected.SubID)
		if !found {
			t.Fatal("original identity was not reread from the real service", expected.Kind)
		}
		if expected.Kind == parent.Kind && expected.OID == parent.OID {
			// DROP FK can alter the external parent's trigger/cache state. The
			// capture reads that current state; it must not replay the old row.
			var before, after map[string]json.RawMessage
			if json.Unmarshal(expected.Data, &before) != nil || json.Unmarshal(entry.Data, &after) != nil {
				t.Fatal("invalid captured parent catalog record")
			}
			for _, field := range []string{"relowner", "relacl", "comment", "relname", "namespace"} {
				if string(before[field]) != string(after[field]) {
					t.Fatal("unmodified external parent property was not reread", field)
				}
			}
		} else if string(entry.Data) != string(expected.Data) {
			t.Fatal("unmodified external catalog record was not reread", expected.Kind)
		}
	}
	if len(preserved.Schema.Table.Columns()) != 2 || pgCatalogHas(preserved.Schema.Postgres, "column", "attname", "mood") {
		t.Fatal("old catalog data was replayed instead of reading current records")
	}
	run(t, s, `CREATE OR REPLACE FUNCTION nyadb_original_external.detached_cast(nyadb_original_external.detached_mood) RETURNS integer LANGUAGE SQL IMMUTABLE AS $$SELECT 2$$;
COMMENT ON TABLE nyadb_original_external.detached_parent IS 'current external relation';
ALTER TYPE nyadb_original_external.detached_mood ADD VALUE 'three';`)
	changed := pgOriginalCapture(t, s, base)
	updatedFn := pgOriginalEntry(t, changed.Schema.Postgres, "routine", "proname", "detached_cast")
	updatedParent := pgOriginalEntry(t, changed.Schema.Postgres, "relation", "relname", "detached_parent")
	if updatedFn.OID != fn.OID || updatedParent.OID != parent.OID || string(updatedFn.Data) == string(fn.Data) || string(updatedParent.Data) == string(parent.Data) || !pgCatalogHas(changed.Schema.Postgres, "enum", "enumlabel", "three") {
		t.Fatal("current detached context changes were invisible or borrowed new identities")
	}
	run(t, s, `DROP CAST(nyadb_original_external.detached_mood AS integer); DROP FUNCTION nyadb_original_external.detached_cast(nyadb_original_external.detached_mood);`)
	deleted := pgOriginalCapture(t, s, base)
	if _, found := pgOriginalFind(deleted.Schema.Postgres, "routine", fn.OID, 0); found {
		t.Fatal("original seed recreated or published a deleted routine")
	}
	if schemaddl.Signature(base) != originalSignature {
		t.Fatal("original context capture mutated its input")
	}
}

func TestPostgreSQL17DesignOriginalContextScopeFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_original_scope; CREATE TABLE nyadb_original_scope.original(id uuid);`)
	base := pgDesignRead(t, s, "nyadb_original_scope", "original")
	for name, mutate := range map[string]func(*schemaddl.PostgreSQLCatalog){
		"database oid":  func(p *schemaddl.PostgreSQLCatalog) { p.DatabaseOID++ },
		"namespace oid": func(p *schemaddl.PostgreSQLCatalog) { p.NamespaceOID++ },
		"version":       func(p *schemaddl.PostgreSQLCatalog) { p.ServerVersion++ },
		"byte order":    func(p *schemaddl.PostgreSQLCatalog) { p.NativeByteOrder = "big" },
	} {
		t.Run(name, func(t *testing.T) {
			original := base.Schema.Postgres.Clone()
			mutate(original)
			if partial, err := postgresCatalogSnapshotPreserving(context.Background(), s.db, base.Target, original); !errors.Is(err, schemaddl.ErrConflict) || partial.Schema.Postgres != nil {
				t.Fatal("different original catalog scope published evidence", err)
			}
		})
	}
	target := base.Target
	target.Object.Catalog = "same_oids_wrong_database_label"
	if partial, err := postgresCatalogSnapshot(context.Background(), s.db, target); !errors.Is(err, schemaddl.ErrConflict) || partial.Schema.Postgres != nil {
		t.Fatal("header borrowed the caller's database label", err)
	}
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ExecContext(context.Background(), "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if partial, err := postgresCatalogSnapshotPreserving(ctx, c, base.Target, base.Schema.Postgres); !errors.Is(err, context.Canceled) || partial.Schema.Postgres != nil {
		t.Fatal("cancelled original context read published data", err)
	}
	// Invalid classes fail before SQL. A retry on this original connection
	// remains read-only and does not carry a partial evidence buffer forward.
	invalid := base.Schema.Postgres.Clone()
	invalid.Entries = append(invalid.Entries, schemaddl.PostgreSQLCatalogEntry{Kind: "unsupported_catalog", OID: 900000, Data: json.RawMessage(`{"value":"private"}`)})
	if _, err := postgresCatalogSnapshotPreserving(context.Background(), c, base.Target, invalid); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("unsupported seed was ignored or logged", err)
	}
	if reread, err := postgresCatalogSnapshotPreserving(context.Background(), c, base.Target, base.Schema.Postgres); err != nil || !reread.Schema.Exists {
		t.Fatal("original connection unusable after rejected capture", err)
	}
}
