package engines

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestPostgreSQL17DesignPublicBindingFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_public_design; CREATE TYPE nyadb_public_design.mood AS ENUM('saved'); CREATE TABLE nyadb_public_design.readonly(value nyadb_public_design.mood)`)
	ops := s.SchemaDesign()
	target := contract.SchemaDesignTarget{Object: s.Namespace("nyadb_public_design").Object("table", "created"), Generation: s.Generation()}
	s.db.SetMaxOpenConns(1)
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	if !ops.CanApply(target) || time.Since(start) > time.Second {
		t.Fatal("CanApply blocks on the server")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	s.db.SetMaxOpenConns(8)
	other := target
	other.Generation++
	if ops.CanApply(other) {
		t.Fatal("revoked generation accepted")
	}
	base, err := ops.Snapshot(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	desired := schemaddl.Table{Name: "created", Postgres: &schemaddl.PostgreSQLTableProperties{Kind: "r", Persistence: "p"}, Clauses: []schemaddl.Clause{
		{ID: "new:id", Kind: "column", Name: "id", SQL: "bigint GENERATED ALWAYS AS IDENTITY NOT NULL"},
		{ID: "new:value", Kind: "column", Name: "value", SQL: "numeric(40,20)"},
		{ID: "new:payload", Kind: "column", Name: "payload", SQL: "bytea"},
		{ID: "new:optional", Kind: "column", Name: "optional", SQL: "text"},
		{ID: "new:array", Kind: "column", Name: "items", SQL: "integer[]"},
	}}
	out := ops.Apply(context.Background(), contract.SchemaDesignRequest{Original: base, Desired: desired})
	if out.State != "committed" || !out.Verified || out.Error != nil || out.Actual == nil {
		t.Fatal("public CREATE", out.State, out.Error)
	}
	run(t, s, `INSERT INTO nyadb_public_design.created(value,payload,optional,items) VALUES(12345678901234567890.12345678901234567890,decode('00ff','hex'),NULL,ARRAY[1,2])`)
	r := contract.SchemaDesignRequest{Original: out.Actual.Clone(), Desired: out.Actual.Schema.Table.Clone()}
	r.Desired.Clauses = append(r.Desired.Clauses, schemaddl.Clause{ID: "new:added", Kind: "column", Name: "added", SQL: "text DEFAULT 'new'"})
	out = ops.Apply(context.Background(), r)
	if out.State != "committed" || !out.Verified || out.Error != nil {
		t.Fatal("public ALTER", out.State, out.Error)
	}
	var value, payload, items, added string
	var null bool
	if err = s.db.QueryRow(`SELECT value::text,encode(payload,'hex'),optional IS NULL,items::text,added FROM nyadb_public_design.created`).Scan(&value, &payload, &null, &items, &added); err != nil || value != "12345678901234567890.12345678901234567890" || payload != "00ff" || !null || items != "{1,2}" || added != "new" {
		t.Fatal("independent committed values", value, payload, null, items, added, err)
	}
	r = contract.SchemaDesignRequest{Original: out.Actual.Clone(), Desired: out.Actual.Schema.Table.Clone()}
	r.Desired.Name = "renamed"
	out = ops.Apply(context.Background(), r)
	if out.State != "committed" || !out.Verified || out.Error != nil || out.Actual.Target.Object.Name != "renamed" {
		t.Fatal("public rename", out.State, out.Error)
	}
	readonly := pgDesignRead(t, s, "nyadb_public_design", "readonly")
	draft := readonly.Schema.Table.Clone()
	draft.Name = "forbidden"
	out = ops.Apply(context.Background(), contract.SchemaDesignRequest{Original: readonly, Desired: draft})
	if out.State != "not_executed" || !errors.Is(out.Error, schemaddl.ErrPostgreSQLCoreTypes) {
		t.Fatal("public user-type rename escaped scope", out.State, out.Error)
	}
}
