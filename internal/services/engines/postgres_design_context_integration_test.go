package engines

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestPostgreSQL17DesignUnselectedContextFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_unselected; CREATE SCHEMA nyadb_unselected_external;
CREATE TYPE nyadb_unselected_external.mood AS ENUM('one','two');
CREATE FUNCTION nyadb_unselected_external.mood_cast(nyadb_unselected_external.mood) RETURNS integer LANGUAGE SQL IMMUTABLE AS $$SELECT 1$$;
CREATE CAST(nyadb_unselected_external.mood AS integer) WITH FUNCTION nyadb_unselected_external.mood_cast(nyadb_unselected_external.mood) AS ASSIGNMENT;
CREATE TABLE nyadb_unselected_external.parent(id uuid PRIMARY KEY, quota bigint GENERATED ALWAYS AS IDENTITY);
CREATE TABLE nyadb_unselected.original(id uuid PRIMARY KEY, parent_id uuid, mood nyadb_unselected_external.mood, payload text,
 CONSTRAINT outgoing_fk FOREIGN KEY(parent_id) REFERENCES nyadb_unselected_external.parent(id));
CREATE TABLE nyadb_unselected_external.child(id uuid, CONSTRAINT incoming_fk FOREIGN KEY(id) REFERENCES nyadb_unselected.original(id));
CREATE VIEW nyadb_unselected_external.incoming_view AS SELECT id FROM nyadb_unselected.original;
CREATE POLICY retained_policy ON nyadb_unselected.original FOR SELECT USING(id IS NOT NULL);
CREATE PUBLICATION nyadb_unselected_publication FOR TABLE nyadb_unselected.original;
INSERT INTO nyadb_unselected.original(id,payload) VALUES('00000000-0000-0000-0000-000000000001',repeat('retained',1000));`)
	base := pgDesignRead(t, s, "nyadb_unselected", "original")
	before := schemaddl.Signature(base)
	expected, err := schemaddl.ProjectPostgreSQLDeclarationGraph(*base.Schema.Postgres, pgDesiredGraphNames(base.Schema.Table))
	if err != nil {
		t.Fatal(err)
	}
	// The declaration is independently captured BEFORE the metadata request.
	// A fresh changed external record must never become the expected baseline.
	desired := base.Schema.Table.Clone()
	desired.Postgres.Comment = "requested target comment"
	plan, err := schemaddl.PlanPostgreSQL(base.Schema, desired, "nyadb_unselected", false)
	if err != nil || plan.Mode != "alter" {
		t.Fatal("target metadata plan unavailable", err)
	}
	for _, sample := range []struct {
		name, statement string
		accepted        bool
	}{
		{"unchanged original context", "", true},
		{"external function body", `CREATE OR REPLACE FUNCTION nyadb_unselected_external.mood_cast(nyadb_unselected_external.mood) RETURNS integer LANGUAGE SQL IMMUTABLE AS $$SELECT 2$$`, false},
		{"external enum member", `ALTER TYPE nyadb_unselected_external.mood ADD VALUE 'three'`, false},
		{"incoming FK deleted", `ALTER TABLE nyadb_unselected_external.child DROP CONSTRAINT incoming_fk`, false},
		{"incoming trigger disabled", `ALTER TABLE nyadb_unselected_external.child DISABLE TRIGGER ALL`, false},
		{"target policy changed", `ALTER POLICY retained_policy ON nyadb_unselected.original USING (false)`, false},
		{"external rewrite changed", `CREATE OR REPLACE VIEW nyadb_unselected_external.incoming_view AS SELECT id FROM nyadb_unselected.original WHERE id IS NOT NULL`, false},
		{"publication changed", `ALTER PUBLICATION nyadb_unselected_publication SET (publish='insert')`, false},
		{"external sequence settings", `ALTER SEQUENCE nyadb_unselected_external.parent_quota_seq CACHE 17`, false},
		{"external asset comment", `COMMENT ON TABLE nyadb_unselected_external.parent IS 'private external content'`, false},
		{"original external object deleted", `DROP CAST(nyadb_unselected_external.mood AS integer); DROP FUNCTION nyadb_unselected_external.mood_cast(nyadb_unselected_external.mood)`, false},
	} {
		t.Run(sample.name, func(t *testing.T) {
			c, err := s.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err = c.ExecContext(context.Background(), "BEGIN ISOLATION LEVEL REPEATABLE READ"); err != nil {
				t.Fatal(err)
			}
			defer rollbackDesign(c)
			for _, statement := range []string{"SET LOCAL search_path=pg_catalog", "SET LOCAL standard_conforming_strings=on", "LOCK TABLE ONLY nyadb_unselected.original IN ACCESS EXCLUSIVE MODE"} {
				if _, err := c.ExecContext(context.Background(), statement); err != nil {
					t.Fatal(err)
				}
			}
			for _, statement := range plan.SQL {
				if _, err := c.ExecContext(context.Background(), statement); err != nil {
					t.Fatal("target metadata fixture failed", err)
				}
			}
			if sample.statement != "" {
				if _, err := c.ExecContext(context.Background(), sample.statement); err != nil {
					t.Fatal("external mutation fixture failed", err)
				}
			}
			actual, err := postgresCatalogSnapshotPreserving(context.Background(), c, base.Target, base.Schema.Postgres)
			if err != nil {
				t.Fatal("current original context unavailable", err)
			}
			// Prove that these external changes pass the previous target-only
			// guard, before asserting the new original context guard rejects.
			if err := schemaddl.VerifyPostgreSQLRetainedCatalog(base.Schema, desired, actual.Schema, expected, "nyadb_unselected", false); err != nil {
				t.Fatal("fixture changed selected target semantics", err)
			}
			err = schemaddl.VerifyPostgreSQLUnselectedCatalog(base.Schema, desired, actual.Schema, expected, "nyadb_unselected", false)
			if sample.accepted && err != nil || !sample.accepted && !errors.Is(err, schemaddl.ErrConflict) {
				t.Fatal("unselected context verdict incorrect", err)
			}
			if err != nil && strings.Contains(err.Error(), "private external content") {
				t.Fatal("context error exposed private catalog text")
			}
			if schemaddl.Signature(base) != before {
				t.Fatal("context guard mutated original evidence")
			}
			if _, err := c.ExecContext(context.Background(), "ROLLBACK"); err != nil {
				t.Fatal("fixture rollback not acknowledged", err)
			}
		})
	}
	final := pgOriginalCapture(t, s, base)
	if err := schemaddl.VerifyPostgreSQLUnselectedCatalog(base.Schema, base.Schema.Table, final.Schema, expected, "nyadb_unselected", false); err != nil {
		t.Fatal("rollback did not preserve original catalog context", err)
	}
	var count, length int
	if err := s.db.QueryRow(`SELECT count(*),max(length(payload)) FROM nyadb_unselected.original`).Scan(&count, &length); err != nil || count != 1 || length != 8000 {
		t.Fatal("fixture changed original data", err)
	}
}
