package schemaddl

import (
	"strings"
	"testing"
)

func pgRetainedPlanFixture(t *testing.T) Snapshot {
	t.Helper()
	base := pgMetadataFixture(t)
	base.Postgres.Entries = append(base.Postgres.Entries,
		pgRecord("constraint", 20, 0, `{"name":"original_check","relation_oid":16,"contype":"c","definition":"CHECK (length(\"text\"\"column\")>0)","comment":"exact ' ; retained"}`),
		pgRecord("index", 30, 0, `{"name":"original_index","relation_oid":16,"constraint_oid":0,"definition":"CREATE UNIQUE INDEX original_index ON demo.\"quoted\"\"table\"(\"text\"\"column\")","comment":"index retained","indisclustered":true,"indisreplident":false}`),
	)
	pgRenameRefresh(t, &base)
	return base
}

func TestPostgreSQLNativePlanRestoresOriginalConstraintCommentsAndIndexDesignation(t *testing.T) {
	base := pgRetainedPlanFixture(t)
	desired := base.Table.Clone()
	for i := range desired.Clauses {
		if desired.Clauses[i].Kind == "constraint" {
			desired.Clauses[i].SQL = `CONSTRAINT original_check CHECK(length("text""column")>=0)`
		}
	}
	desired.Definitions[0].SQL = `CREATE UNIQUE INDEX original_index ON demo."quoted""table" ("text""column") WHERE "text""column" IS NOT NULL`
	plan, err := PlanPostgreSQL(base, desired, "demo", false)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.SQL, "\n")
	for _, fragment := range []string{
		`COMMENT ON CONSTRAINT "original_check" ON "demo"."quoted""table" IS 'exact '' ; retained'`,
		`ALTER TABLE ONLY "demo"."quoted""table" CLUSTER ON "original_index"`,
	} {
		if !strings.Contains(joined, fragment) || strings.Index(joined, fragment) < strings.Index(joined, `CREATE UNIQUE INDEX`) {
			t.Fatal("retained metadata missing or executed before replacement", fragment, joined)
		}
	}
	// Deleting an asset cannot transfer its metadata to a same-name new ID.
	for i := range desired.Clauses {
		if desired.Clauses[i].Kind == "constraint" {
			desired.Clauses[i].ID = "new:replacement"
		}
	}
	desired.Definitions = nil
	plan, err = PlanPostgreSQL(base, desired, "demo", false)
	if err != nil || strings.Contains(strings.Join(plan.SQL, "\n"), "COMMENT ON CONSTRAINT") || strings.Contains(strings.Join(plan.SQL, "\n"), "CLUSTER ON") {
		t.Fatal("deleted metadata attached to a new or missing asset", err)
	}
}

func TestPostgreSQLRetainedReplicaDesignationHonorsExplicitTableChange(t *testing.T) {
	base := pgRetainedPlanFixture(t)
	pgRenamePatch(t, &base, "relation", 16, 0, func(r map[string]any) { r["relreplident"] = "i" })
	pgRenamePatch(t, &base, "index", 30, 0, func(r map[string]any) { r["indisreplident"] = true })
	pgRenameRefresh(t, &base)
	desired := base.Table.Clone()
	desired.Postgres.Comment = "another change"
	plan, err := PlanPostgreSQL(base, desired, "demo", false)
	if err != nil || !strings.Contains(strings.Join(plan.SQL, "\n"), `REPLICA IDENTITY USING INDEX "original_index"`) {
		t.Fatal("original replication index was not restored", err)
	}
	desired.Postgres.ReplicaIdentity = "f"
	plan, err = PlanPostgreSQL(base, desired, "demo", false)
	if err != nil || strings.Contains(strings.Join(plan.SQL, "\n"), "REPLICA IDENTITY USING INDEX") {
		t.Fatal("original designation overrode requested replica identity", err)
	}
}
