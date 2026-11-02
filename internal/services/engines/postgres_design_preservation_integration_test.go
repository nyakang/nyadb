package engines

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestPostgreSQL17DesignRetainedCatalogFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_retained_catalog;
CREATE ROLE nyadb_retained_reader;
CREATE TABLE nyadb_retained_catalog.original(id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, value text DEFAULT 'retained', CONSTRAINT retained_check CHECK(id>0));
CREATE UNIQUE INDEX retained_index ON nyadb_retained_catalog.original(id);
COMMENT ON CONSTRAINT retained_check ON nyadb_retained_catalog.original IS 'constraint retained';
COMMENT ON INDEX nyadb_retained_catalog.retained_index IS 'index retained';
COMMENT ON TYPE nyadb_retained_catalog.original IS 'row type retained';
GRANT SELECT ON nyadb_retained_catalog.original TO nyadb_retained_reader;
GRANT UPDATE(value) ON nyadb_retained_catalog.original TO nyadb_retained_reader;
ALTER TABLE nyadb_retained_catalog.original ALTER COLUMN value SET(n_distinct=0.5);
ALTER TABLE nyadb_retained_catalog.original CLUSTER ON retained_index;
ALTER TABLE nyadb_retained_catalog.original ENABLE ROW LEVEL SECURITY;
CREATE POLICY retained_policy ON nyadb_retained_catalog.original FOR SELECT USING(id>0);
INSERT INTO nyadb_retained_catalog.original(value) VALUES('existing')`)
	base := pgDesignRead(t, s, "nyadb_retained_catalog", "original")
	draft := base.Schema.Table.Clone()
	for i := range draft.Clauses {
		if draft.Clauses[i].Kind == "constraint" && draft.Clauses[i].Name == "retained_check" {
			draft.Clauses[i].SQL = `CONSTRAINT retained_check CHECK(id>=0)`
		}
	}
	base = pgNativeFixtureApply(t, s, base, draft, false)
	var comment string
	if err := s.db.QueryRow(`SELECT obj_description(oid,'pg_constraint') FROM pg_constraint WHERE conrelid='nyadb_retained_catalog.original'::regclass AND conname='retained_check'`).Scan(&comment); err != nil || comment != "constraint retained" {
		t.Fatal("explicit constraint replacement discarded original comment", err)
	}
	draft = base.Schema.Table.Clone()
	draft.Definitions[0].SQL = `CREATE UNIQUE INDEX retained_index ON nyadb_retained_catalog.original(id) INCLUDE(value)`
	base = pgNativeFixtureApply(t, s, base, draft, false)
	var clustered bool
	if err := s.db.QueryRow(`SELECT indisclustered FROM pg_index WHERE indexrelid='nyadb_retained_catalog.retained_index'::regclass`).Scan(&clustered); err != nil || !clustered {
		t.Fatal("explicit index replacement discarded original cluster designation", err)
	}
	run(t, s, `ALTER TABLE nyadb_retained_catalog.original REPLICA IDENTITY USING INDEX retained_index`)
	base = pgDesignRead(t, s, "nyadb_retained_catalog", "original")
	draft = base.Schema.Table.Clone()
	draft.Definitions[0].SQL = `CREATE UNIQUE INDEX retained_index ON nyadb_retained_catalog.original(id DESC) INCLUDE(value)`
	base = pgNativeFixtureApply(t, s, base, draft, false)
	var replication bool
	if err := s.db.QueryRow(`SELECT indisreplident FROM pg_index WHERE indexrelid='nyadb_retained_catalog.retained_index'::regclass`).Scan(&replication); err != nil || !replication {
		t.Fatal("explicit index replacement discarded original replication designation", err)
	}
	// A native type rewrite keeps ACLs, field options, row-type properties and
	// policy identity; these do not acquire a fresh CREATE's empty defaults.
	draft = base.Schema.Table.Clone()
	pgNativeEditField(t, &draft, "value", func(f *schemaddl.PostgreSQLField) { f.Type = "character varying(100)" })
	base = pgNativeFixtureApply(t, s, base, draft, false)
	draft = base.Schema.Table.Clone()
	draft.Postgres.ReplicaIdentity = "f"
	pgNativeFixtureApply(t, s, base, draft, false)
	if err := s.db.QueryRow(`SELECT indisreplident FROM pg_index WHERE indexrelid='nyadb_retained_catalog.retained_index'::regclass`).Scan(&replication); err != nil || replication {
		t.Fatal("original index designation overrode explicit replica identity change", err)
	}
}

// These mutations are injected into captured real-server evidence. They test
// the original preservation guard even when a fresh target graph agrees with
// the changed property; they do not claim the mutation was executed by SQL.
func TestPostgreSQL17DesignRetainedCatalogRejectsFreshDefaultsFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_retained_rejection;
CREATE TABLE nyadb_retained_rejection.original(id bigint PRIMARY KEY, value text);
CREATE INDEX retained_index ON nyadb_retained_rejection.original(value);
ALTER TABLE nyadb_retained_rejection.original CLUSTER ON retained_index;
COMMENT ON CONSTRAINT original_pkey ON nyadb_retained_rejection.original IS 'retain original';`)
	base := pgDesignRead(t, s, "nyadb_retained_rejection", "original").Schema
	root := base.Postgres.RelationOID
	var index, constraint, rowType int64
	bindings, err := schemaddl.BuildPostgreSQLGraphBindings(*base.Postgres, pgDesiredGraphNames(base.Table))
	if err != nil {
		t.Fatal(err)
	}
	for object, identity := range bindings.Objects {
		if identity == "@index:retained_index" {
			index = object.ObjectOID
		} else if strings.HasPrefix(identity, "@constraint:") {
			constraint = object.ObjectOID
		} else if identity == "@row-type" {
			rowType = object.ObjectOID
		}
	}
	if index == 0 || constraint == 0 || rowType == 0 {
		t.Fatal("real retained fixture assets missing")
	}
	for _, sample := range []struct {
		name, kind, field string
		oid               int64
		sub               int
		before, after     json.RawMessage
	}{
		{"unknown table property", "relation", "fixture_future", root, 0, json.RawMessage(`{"exact":9007199254740993}`), json.RawMessage(`{"exact":9007199254740992}`)},
		{"unknown field property", "column", "fixture_future", root, 2, json.RawMessage(`9007199254740993`), json.RawMessage(`9007199254740992`)},
		{"unknown row type property", "type", "fixture_future", rowType, 0, json.RawMessage(`true`), json.RawMessage(`false`)},
		{"table permission loss", "relation", "relacl", root, 0, json.RawMessage(`["fixture=r/owner"]`), json.RawMessage(`null`)},
		{"field permission loss", "column", "attacl", root, 2, json.RawMessage(`["fixture=r/owner"]`), json.RawMessage(`null`)},
		{"constraint comment loss", "constraint", "comment", constraint, 0, json.RawMessage(`"retain original"`), json.RawMessage(`""`)},
		{"cluster designation loss", "index", "indisclustered", index, 0, json.RawMessage(`true`), json.RawMessage(`false`)},
	} {
		t.Run(sample.name, func(t *testing.T) {
			original := base.Clone()
			patch := func(snapshot *schemaddl.Snapshot, value json.RawMessage) {
				found := false
				for i, entry := range snapshot.Postgres.Entries {
					if entry.Kind != sample.kind || entry.OID != sample.oid || entry.SubID != sample.sub {
						continue
					}
					found = true
					var fields map[string]json.RawMessage
					if err := json.Unmarshal(entry.Data, &fields); err != nil {
						t.Fatal(err)
					}
					fields[sample.field] = value
					var err error
					snapshot.Postgres.Entries[i].Data, err = json.Marshal(fields)
					if err != nil {
						t.Fatal(err)
					}
				}
				if !found {
					t.Fatal("injected retained record absent")
				}
				var err error
				snapshot.Revision, err = snapshot.Postgres.Fingerprint()
				if err != nil {
					t.Fatal(err)
				}
				snapshot.Table, err = snapshot.Postgres.Table()
				if err != nil {
					t.Fatal(err)
				}
			}
			patch(&original, sample.before)
			actual := original.Clone()
			patch(&actual, sample.after)
			beforeSignature := schemaddl.Signature(original)
			fresh, err := schemaddl.ProjectPostgreSQLDeclarationGraph(*actual.Postgres, pgDesiredGraphNames(actual.Table))
			if err != nil {
				t.Fatal(err)
			}
			if err := schemaddl.VerifyPostgreSQLRetainedCatalog(original, original.Table.Clone(), actual, fresh, "nyadb_retained_rejection", false); err == nil || !strings.Contains(err.Error(), sample.field) || strings.Contains(err.Error(), "900719925474099") {
				t.Fatal("fresh declaration authorized original property loss or logged values", err)
			}
			if schemaddl.Signature(original) != beforeSignature {
				t.Fatal("retained proof mutated original catalog evidence")
			}
		})
	}
	// Native security-label providers are optional PostgreSQL extensions. Use
	// typed injected records here, without installing a provider or claiming a
	// real label write. The label has to follow the original retained type.
	labelled := base.Clone()
	labelled.Postgres.Entries = append(labelled.Postgres.Entries, schemaddl.PostgreSQLCatalogEntry{Kind: fmt.Sprintf("security_label:%d", bindings.Classes["pg_type"]), OID: rowType, Data: json.RawMessage(`{"labels":[{"provider":"fixture","label":"original"}]}`)})
	labelled.Revision, err = labelled.Postgres.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := schemaddl.ProjectPostgreSQLDeclarationGraph(*base.Postgres, pgDesiredGraphNames(base.Table))
	if err != nil {
		t.Fatal(err)
	}
	if err := schemaddl.VerifyPostgreSQLRetainedCatalog(labelled, labelled.Table, base, fresh, "nyadb_retained_rejection", false); err == nil || !strings.Contains(err.Error(), "security_label") {
		t.Fatal("fresh declaration erased original security label", err)
	}
}
