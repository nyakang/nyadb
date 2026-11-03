package engines

import (
	"context"
	"database/sql"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestPostgreSQL17DesignGraphOriginalReferencesFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_graph_refs;
CREATE TABLE nyadb_graph_refs.original(id bigint);
ALTER TABLE nyadb_graph_refs.original ADD COLUMN original_relation regclass DEFAULT 'nyadb_graph_refs.original'::regclass;
ALTER TABLE nyadb_graph_refs.original ADD COLUMN original_type regtype DEFAULT 'nyadb_graph_refs.original'::regtype;`)
	original := pgDesignRead(t, s, "nyadb_graph_refs", "original")
	desired := original.Schema.Table.Clone()
	desired.Postgres.Comment = "reference aliases"
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	baseline, err := s.postgresCanonicalDesired(context.Background(), c, contract.SchemaDesignRequest{Original: original, Desired: desired})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := schemaddl.ProjectPostgreSQLDeclarationGraph(*original.Schema.Postgres, pgDesiredGraphNames(desired))
	if err != nil {
		t.Fatal(err)
	}
	without, err := schemaddl.ProjectPostgreSQLDeclarationGraph(*baseline.Probe.Postgres, baseline.GraphNames)
	if err != nil {
		t.Fatal(err)
	}
	if err := schemaddl.ComparePostgreSQLDeclarationGraphs(actual, without); err == nil {
		t.Fatal("original row type/relation references were guessed without bindings")
	}
	reference := schemaddl.PostgreSQLGraphReference{Catalog: *original.Schema.Postgres, Names: pgDesiredGraphNames(original.Schema.Table)}
	wanted, err := schemaddl.ProjectPostgreSQLDeclarationGraph(*baseline.Probe.Postgres, baseline.GraphNames, reference)
	if err != nil {
		t.Fatal(err)
	}
	if err := schemaddl.ComparePostgreSQLDeclarationGraphs(actual, wanted); err != nil {
		t.Fatal("explicit original reference mapping failed", err)
	}
	for _, record := range wanted.Records {
		if record.Kind == "relation" && record.Object == "@table" {
			// Only the selected probe has this record, despite its original
			// references having the same logical key.
			if string(record.Physical) == "" {
				t.Fatal("selected relation physical state missing")
			}
		}
	}
	wrong := reference
	wrong.Catalog.Namespace = "another"
	if partial, err := schemaddl.ProjectPostgreSQLDeclarationGraph(*baseline.Probe.Postgres, baseline.GraphNames, wrong); err == nil || len(partial.Records) > 0 {
		t.Fatal("cross-scope reference returned graph evidence", err)
	}
	pgProbeAbsent(t, c, "nyadb_graph_refs")
	after, err := postgresCatalogSnapshot(context.Background(), c, original.Target)
	if err != nil || after.Schema.Revision != original.Schema.Revision {
		t.Fatal("reference mapping changed original catalog", err)
	}
}

func TestPostgreSQL17DesignStatisticsDefaultFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_statistics; CREATE TABLE nyadb_statistics.stats(value text)`)
	for _, wanted := range []int{0, 73, -1} {
		original := pgDesignRead(t, s, "nyadb_statistics", "stats")
		if wanted == 0 && original.Schema.Table.Columns()[0].Postgres.Statistics != -1 {
			t.Fatal("fresh default decoded as disabled statistics")
		}
		desired := original.Schema.Table.Clone()
		desired.Clauses[0].Postgres.Statistics = wanted
		out := s.postgresDesignMetadataApply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: desired})
		if out.State != "committed" || !out.Verified || out.Error != nil || out.Actual.Schema.Table.Columns()[0].Postgres.Statistics != wanted {
			t.Fatal("versioned statistics update not verified", out.State, out.Error)
		}
		var actual sql.NullInt64
		if err := s.db.QueryRow(`SELECT attstattarget::integer FROM pg_attribute WHERE attrelid='nyadb_statistics.stats'::regclass AND attname='value'`).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if wanted == -1 {
			if original.Schema.Postgres.ServerVersion >= 170000 && actual.Valid || original.Schema.Postgres.ServerVersion < 170000 && (!actual.Valid || actual.Int64 != -1) {
				t.Fatal("default reset retained an explicit zero/value")
			}
		} else if !actual.Valid || actual.Int64 != int64(wanted) {
			t.Fatal("explicit statistics value changed", actual)
		}
	}
}
