package engines

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestPostgreSQL17DesignCreateChosenNameFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_create_chosen`)
	r := pgDDLRequest(t, s, "nyadb_create_chosen", "initial_name")
	r.Desired = schemaddl.Table{Name: "chosen_name", Postgres: &schemaddl.PostgreSQLTableProperties{Kind: "r", Persistence: "p"}, Clauses: []schemaddl.Clause{{ID: "new:value", Kind: "column", Name: "value", SQL: "numeric(40,20) DEFAULT 12345678901234567890.12345678901234567890"}}}
	before := schemaddl.Signature(r)
	out := s.postgresDesignDDLApply(context.Background(), r)
	if out.State != "committed" || out.Error != nil || !out.Verified || out.Actual == nil || out.Actual.Target.Object.Name != r.Desired.Name {
		t.Fatal("new table form name was not used for the verified receipt", out.State, out.Error)
	}
	var placeholder bool
	if err := s.db.QueryRow(`SELECT to_regclass('nyadb_create_chosen.initial_name') IS NOT NULL`).Scan(&placeholder); err != nil || placeholder || schemaddl.Signature(r) != before {
		t.Fatal("CREATE changed original scope/request or created its old placeholder", err)
	}
	run(t, s, `INSERT INTO nyadb_create_chosen.chosen_name DEFAULT VALUES`)
	var value string
	if err := s.db.QueryRow(`SELECT value::text FROM nyadb_create_chosen.chosen_name`).Scan(&value); err != nil || value != "12345678901234567890.12345678901234567890" {
		t.Fatal("chosen table lost its exact default", value, err)
	}
}
