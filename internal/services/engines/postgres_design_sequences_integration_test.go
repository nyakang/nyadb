package engines

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestPostgreSQL17DesignSequenceStateFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_sequence_state; CREATE SCHEMA nyadb_sequence_external;
CREATE TABLE nyadb_sequence_state.original(id bigint GENERATED ALWAYS AS IDENTITY, external bigint, label text);
CREATE SEQUENCE nyadb_sequence_external."seq""quoted;" AS bigint INCREMENT -7 MINVALUE -9223372036854775807 MAXVALUE 9223372036854775807 CACHE 13 CYCLE;
ALTER TABLE nyadb_sequence_state.original ALTER COLUMN external SET DEFAULT nextval('nyadb_sequence_external."seq""quoted;"'::regclass);
INSERT INTO nyadb_sequence_state.original(label) VALUES('preserved');
SELECT setval('nyadb_sequence_state.original_id_seq',9007199254740993,false);
SELECT setval('nyadb_sequence_external."seq""quoted;"',-9007199254740993,true)`)
	base := pgDesignRead(t, s, "nyadb_sequence_state", "original")
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ExecContext(context.Background(), "BEGIN ISOLATION LEVEL REPEATABLE READ"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	before, err := postgresSequenceStates(context.Background(), c, base.Schema.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	var identityOID, externalOID int64
	for oid, state := range before {
		if state.Namespace == "nyadb_sequence_state" && state.Name == "original_id_seq" {
			identityOID = oid
			if state.LastValue != 9007199254740993 || state.Called {
				t.Fatal("identity sequence precision or uncalled state changed")
			}
		}
		if state.Namespace == "nyadb_sequence_external" && state.Name == `seq"quoted;` {
			externalOID = oid
			if state.LastValue != -9007199254740993 || !state.Called {
				t.Fatal("external descending sequence precision or called state changed")
			}
		}
	}
	if identityOID == 0 || externalOID == 0 {
		t.Fatal("captured sequence closure omitted identity or outside default")
	}
	wrong := base.Schema.Postgres.Clone()
	wrong.Database = "another_database"
	if got, err := postgresSequenceStates(context.Background(), c, wrong); !errors.Is(err, schemaddl.ErrConflict) || got != nil {
		t.Fatal("original OID was read from another physical database", err)
	}
	// A second backend's setval remains visible inside repeatable read. It is
	// an actual sequence mutation, and ROLLBACK cannot undo it.
	for name, sql := range map[string]string{
		"exact value": `SELECT setval('nyadb_sequence_external."seq""quoted;"',-9007199254740992,true)`,
		"called flag": `SELECT setval('nyadb_sequence_external."seq""quoted;"',-9007199254740993,false)`,
	} {
		t.Run(name, func(t *testing.T) {
			run(t, s, sql)
			current, err := postgresSequenceStates(context.Background(), c, base.Schema.Postgres)
			if err != nil || !errors.Is(postgresCompareSequenceStates(before, current, nil), schemaddl.ErrDataChanged) {
				t.Fatal("live non-MVCC sequence change was not detected", err)
			}
			run(t, s, `SELECT setval('nyadb_sequence_external."seq""quoted;"',-9007199254740993,true)`)
		})
	}
	if err := rollbackDesign(c); err != nil {
		t.Fatal(err)
	}
	current, err := postgresSequenceStates(context.Background(), c, base.Schema.Postgres)
	if err != nil || postgresCompareSequenceStates(before, current, nil) != nil {
		t.Fatal("fixture restore did not retain exact sequence state", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if current, err := postgresSequenceStates(ctx, c, base.Schema.Postgres); !errors.Is(err, context.Canceled) || current != nil {
		t.Fatal("cancelled sequence read produced partial proof", err)
	}
	_ = c.Close()
	r := contract.SchemaDesignRequest{Original: base, Desired: base.Schema.Table.Clone()}
	r.Desired.Postgres.Comment = "verified sequence state"
	out := s.postgresDesignMetadataApply(context.Background(), r)
	if out.Error != nil || !out.Verified || out.State != "committed" || out.Actual == nil {
		t.Fatal("private metadata executor failed", out)
	}
	r.Original, r.Desired = *out.Actual, out.Actual.Schema.Table.Clone()
	pgNativeField(t, &r.Desired, "label").Name = "renamed_label"
	out = s.postgresDesignRenameApply(context.Background(), r)
	if out.Error != nil || !out.Verified || out.State != "committed" || out.Actual == nil {
		t.Fatal("private rename executor failed", out)
	}
	c, err = s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	current, err = postgresSequenceStates(context.Background(), c, out.Actual.Schema.Postgres)
	if err != nil || postgresCompareSequenceStates(before, current, nil) != nil {
		t.Fatal("metadata or rename consumed sequence values", err)
	}
	if _, err := c.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExecContext(context.Background(), `DROP SEQUENCE nyadb_sequence_external."seq""quoted;" CASCADE`); err != nil {
		t.Fatal(err)
	}
	if current, err := postgresSequenceStates(context.Background(), c, out.Actual.Schema.Postgres); !errors.Is(err, schemaddl.ErrConflict) || current != nil {
		t.Fatal("missing later sequence retained a partial original state map", err)
	}
	if err := rollbackDesign(c); err != nil {
		t.Fatal(err)
	}
}

func TestPostgreSQL17DesignSequenceRemovalFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_sequence_removal;
CREATE TABLE nyadb_sequence_removal.original(id bigint GENERATED ALWAYS AS IDENTITY, serial_value bigserial, ordinary bigint);
CREATE SEQUENCE nyadb_sequence_removal.external;
ALTER TABLE nyadb_sequence_removal.original ALTER COLUMN ordinary SET DEFAULT nextval('nyadb_sequence_removal.external');
INSERT INTO nyadb_sequence_removal.original(ordinary) VALUES(7)`)
	base := pgDesignRead(t, s, "nyadb_sequence_removal", "original")
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	before, err := postgresSequenceStates(context.Background(), c, base.Schema.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	var identityOID, serialOID, externalOID int64
	for oid, state := range before {
		switch state.Name {
		case "original_id_seq":
			identityOID = oid
		case "original_serial_value_seq":
			serialOID = oid
		case "external":
			externalOID = oid
		}
	}
	if identityOID == 0 || serialOID == 0 || externalOID == 0 {
		t.Fatal("sequence ownership fixture incomplete")
	}
	draft := base.Schema.Table.Clone()
	pgNativeEditField(t, &draft, "id", func(f *schemaddl.PostgreSQLField) { f.Identity, f.IdentityOptions = "", "" })
	removed, err := postgresSequenceRemovals(base.Schema, draft, "nyadb_sequence_removal", false)
	if err != nil || len(removed) != 1 || !removed[identityOID] {
		t.Fatal("identity removal did not select its original owned sequence", err)
	}
	draft = base.Schema.Table.Clone()
	pgNativeEditField(t, &draft, "serial_value", func(f *schemaddl.PostgreSQLField) { f.Default, f.HasDefault = "", false })
	pgNativeEditField(t, &draft, "ordinary", func(f *schemaddl.PostgreSQLField) { f.Default, f.HasDefault = "", false })
	removed, err = postgresSequenceRemovals(base.Schema, draft, "nyadb_sequence_removal", false)
	if err != nil || len(removed) != 0 {
		t.Fatal("removing a default granted sequence deletion permission", err)
	}
	draft = base.Schema.Table.Clone()
	for i, field := range draft.Clauses {
		if field.Name == "serial_value" {
			draft.Clauses = append(draft.Clauses[:i], draft.Clauses[i+1:]...)
			break
		}
	}
	if removed, err := postgresSequenceRemovals(base.Schema, draft, "nyadb_sequence_removal", false); err == nil || removed != nil {
		t.Fatal("sequence removal bypassed field deletion confirmation")
	}
	removed, err = postgresSequenceRemovals(base.Schema, draft, "nyadb_sequence_removal", true)
	if err != nil || len(removed) != 1 || !removed[serialOID] {
		t.Fatal("confirmed serial column drop lost original sequence ownership", err)
	}
	// Execute the actual plan in the existing native harness. Its newly bound
	// state proof checks all retained sequences and the requested disappearance.
	_ = c.Close()
	actual := pgNativeFixtureApplyWithOriginalContext(t, s, base, draft, true)
	c, err = s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	current, err := postgresSequenceStates(context.Background(), c, actual.Schema.Postgres)
	if err != nil || postgresCompareSequenceStates(before, current, removed) != nil {
		t.Fatal("native column deletion altered retained sequence state", err)
	}
	// Unknown IDs cannot substitute for an original owned identity.
	bad := maps.Clone(removed)
	bad[externalOID] = true
	if err := postgresCompareSequenceStates(before, current, bad); err == nil {
		t.Fatal("outside sequence was treated as requested deletion")
	}
}

func TestPostgreSQL17DesignSequenceReadPermissionFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE ROLE nyadb_sequence_owner;
CREATE SCHEMA nyadb_sequence_permission; GRANT USAGE ON SCHEMA nyadb_sequence_permission TO nyadb_sequence_owner;
CREATE TABLE nyadb_sequence_permission.original(id bigint GENERATED ALWAYS AS IDENTITY, external bigint);
CREATE SEQUENCE nyadb_sequence_permission.outside;
ALTER TABLE nyadb_sequence_permission.original ALTER COLUMN external SET DEFAULT nextval('nyadb_sequence_permission.outside');
ALTER TABLE nyadb_sequence_permission.original OWNER TO nyadb_sequence_owner`)
	base := pgDesignRead(t, s, "nyadb_sequence_permission", "original")
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExecContext(context.Background(), "SET ROLE nyadb_sequence_owner"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		reset, err := s.db.Conn(context.Background())
		if err == nil {
			_, _ = reset.ExecContext(context.Background(), "RESET ROLE")
			_ = reset.Close()
		}
	})
	if states, err := postgresSequenceStates(context.Background(), c, base.Schema.Postgres); !errors.Is(err, schemaddl.ErrUnsupported) || states != nil || !strings.Contains(err.Error(), "sequence state unavailable") {
		t.Fatal("sequence permission failure returned a partial state map", err)
	}
	_ = c.Close()
	r := contract.SchemaDesignRequest{Original: base, Desired: base.Schema.Table.Clone()}
	// Native metadata SQL and rename do not evaluate defaults. They retain
	// their existing permissions instead of requiring SELECT on an unrelated
	// default sequence just to comment on a table the caller already owns.
	r.Desired.Postgres.Comment = "metadata keeps original permission requirements"
	out := s.postgresDesignMetadataApply(context.Background(), r)
	if out.State != "committed" || out.Error != nil || !out.Verified || out.Actual == nil {
		t.Fatal("metadata unnecessarily required external sequence read permission", out)
	}
	c, err = s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ExecContext(context.Background(), "RESET ROLE"); err != nil {
		t.Fatal(err)
	}
	var comment string
	if err := c.QueryRowContext(context.Background(), `SELECT COALESCE(obj_description('nyadb_sequence_permission.original'::regclass,'pg_class'),'')`).Scan(&comment); err != nil || comment != r.Desired.Postgres.Comment {
		t.Fatal("permitted metadata edit was not committed", err)
	}
}
