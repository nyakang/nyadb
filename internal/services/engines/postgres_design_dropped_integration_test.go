package engines

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestPostgreSQL17DesignDroppedAttributeFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_dropped_attribute;
CREATE ROLE nyadb_dropped_reader;
CREATE TABLE nyadb_dropped_attribute.original(keep_id uuid, discarded text DEFAULT 'original', keep_payload bytea);
INSERT INTO nyadb_dropped_attribute.original(keep_id,keep_payload) VALUES('00000000-0000-0000-0000-000000000001',decode('00ff','hex'));
ALTER TABLE nyadb_dropped_attribute.original ADD COLUMN fast bigint DEFAULT 9007199254740993;
ALTER TABLE nyadb_dropped_attribute.original ADD COLUMN legacy integer;
ALTER TABLE nyadb_dropped_attribute.original DROP COLUMN legacy;
ALTER TABLE nyadb_dropped_attribute.original ALTER COLUMN discarded SET STORAGE EXTERNAL;
ALTER TABLE nyadb_dropped_attribute.original ALTER COLUMN discarded SET COMPRESSION pglz;
ALTER TABLE nyadb_dropped_attribute.original ALTER COLUMN discarded SET STATISTICS 73;
ALTER TABLE nyadb_dropped_attribute.original ALTER COLUMN discarded SET(n_distinct=0.5);
GRANT SELECT(discarded) ON nyadb_dropped_attribute.original TO nyadb_dropped_reader;
COMMENT ON COLUMN nyadb_dropped_attribute.original.discarded IS 'original field comment';`)
	base := pgDesignRead(t, s, "nyadb_dropped_attribute", "original")
	desired := base.Schema.Table.Clone()
	desired.Clauses = slices.DeleteFunc(desired.Clauses, func(c schemaddl.Clause) bool {
		return c.Kind == "column" && (c.Name == "discarded" || c.Name == "fast")
	})
	if _, err := schemaddl.PlanPostgreSQL(base.Schema, desired, "nyadb_dropped_attribute", false); err == nil {
		t.Fatal("column removal did not require explicit loss confirmation")
	}
	actual := pgNativeFixtureApplyWithOriginalContext(t, s, base, desired, true)
	if len(actual.Schema.Table.Columns()) != 2 {
		t.Fatal("removed column remained in native declaration")
	}
	var attributes, dropped int
	if err := s.db.QueryRow(`SELECT count(*),count(*) FILTER(WHERE attisdropped) FROM pg_attribute WHERE attrelid='nyadb_dropped_attribute.original'::regclass AND attnum>0`).Scan(&attributes, &dropped); err != nil || attributes != 5 || dropped != 3 {
		t.Fatal("native removal reused or deleted original attribute slots", err)
	}
	graph, err := schemaddl.ProjectPostgreSQLDeclarationGraph(*actual.Schema.Postgres, pgDesiredGraphNames(actual.Schema.Table))
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		name, field string
		position    int
		value       json.RawMessage
	}{
		{"removed alignment", "attalign", 2, json.RawMessage(`"d"`)},
		{"removed layout length", "attlen", 2, json.RawMessage(`8`)},
		{"removed identity", "attnum", 2, json.RawMessage(`6`)},
		{"missing value not cleared", "attmissingval", 4, json.RawMessage(`[9007199254740993]`)},
		{"old dropped slot changed", "attlen", 5, json.RawMessage(`8`)},
		{"unrequested unknown state", "future", 2, json.RawMessage(`"private mutation"`)},
	} {
		t.Run(sample.name, func(t *testing.T) {
			changed := actual.Schema.Clone()
			for i, e := range changed.Postgres.Entries {
				if e.Kind != "column" || e.OID != changed.Postgres.RelationOID || e.SubID != sample.position {
					continue
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(e.Data, &fields); err != nil {
					t.Fatal(err)
				}
				fields[sample.field] = sample.value
				changed.Postgres.Entries[i].Data, err = json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
			}
			changed.Revision, err = changed.Postgres.Fingerprint()
			if err != nil {
				t.Fatal(err)
			}
			if err := schemaddl.VerifyPostgreSQLUnselectedCatalog(base.Schema, desired, changed, graph, "nyadb_dropped_attribute", true); err == nil || strings.Contains(err.Error(), "private mutation") {
				t.Fatal("requested deletion granted wrong residual state", err)
			}
		})
	}
}

func TestPostgreSQL17DesignDroppedIdentityFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_dropped_identity;
CREATE TABLE nyadb_dropped_identity.original(keep_value bigint, removed_id bigint GENERATED ALWAYS AS IDENTITY, removed_generated bigint GENERATED ALWAYS AS (keep_value*2) STORED);
INSERT INTO nyadb_dropped_identity.original(keep_value) VALUES(9007199254740993);`)
	base := pgDesignRead(t, s, "nyadb_dropped_identity", "original")
	desired := base.Schema.Table.Clone()
	desired.Clauses = slices.DeleteFunc(desired.Clauses, func(c schemaddl.Clause) bool { return c.Kind == "column" && c.Name != "keep_value" })
	actual := pgNativeFixtureApplyWithOriginalContext(t, s, base, desired, true)
	var sequenceExists bool
	var value string
	if err := s.db.QueryRow(`SELECT to_regclass('nyadb_dropped_identity.original_removed_id_seq') IS NOT NULL,keep_value::text FROM nyadb_dropped_identity.original`).Scan(&sequenceExists, &value); err != nil || sequenceExists || value != "9007199254740993" || len(actual.Schema.Table.Columns()) != 1 {
		t.Fatal("native identity/generation removal lost retained state", err)
	}
}
