package dbruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type schemaHandle struct {
	handle
	load  func(context.Context, contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error)
	apply func(context.Context, contract.SchemaDesignRequest) contract.SchemaDesignOutcome
}

func (s *schemaHandle) Generation() uint64 { return 973 }
func (s *schemaHandle) SchemaDesign() contract.SchemaDesignOperations {
	return contract.SchemaDesignOperations{Snapshot: s.load, Apply: s.apply}
}

func TestSchemaFeatureTranslatesGenerationAndCannotBypassRevocation(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	h := &schemaHandle{handle: handle{id: 1}}
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
	s, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil {
		t.Fatal(err)
	}
	target := contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: 1, Catalog: "main", Schema: "main", Kind: "table", Name: "t"}, Generation: s.Generation()}
	var calls int
	h.load = func(_ context.Context, t2 contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
		calls++
		if t2.Generation != 973 || t2.Object != target.Object {
			t.Fatalf("untranslated scope: %+v", t2)
		}
		return contract.SchemaDesignSnapshot{Target: t2, Schema: schemaddl.Snapshot{Engine: "sqlite", Revision: "original", Table: schemaddl.Table{Name: "t", Clauses: []schemaddl.Clause{{ID: "1", Kind: "column", Name: "id", SQL: "INT"}}}}}, nil
	}
	ops := m.SchemaDesign(1)
	snap, err := ops.Snapshot(context.Background(), target)
	if err != nil || snap.Target != target {
		t.Fatalf("public snapshot: %+v %v", snap, err)
	}
	r := contract.SchemaDesignRequest{Original: snap, Desired: snap.Schema.Table.Clone()}
	h.apply = func(_ context.Context, raw contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
		calls++
		if raw.Original.Target.Generation != 973 {
			t.Fatal(raw.Original.Target)
		}
		raw.Desired.Clauses[0].Name = "raw mutation"
		raw.Original.Schema.Table.Clauses[0].Name = "other mutation"
		return contract.SchemaDesignOutcome{State: "committed", Verified: true, Actual: &raw.Original}
	}
	// Resolve again to capture the injected apply function on the UI thread.
	ops = m.SchemaDesign(1)
	out := ops.Apply(context.Background(), r)
	if out.Actual == nil || out.Actual.Target.Generation != s.Generation() || r.Desired.Clauses[0].Name != "id" || r.Original.Schema.Table.Clauses[0].Name != "id" {
		t.Fatalf("mutable or untranslated result: %+v", out)
	}
	wrong := target
	wrong.Generation = 973
	if _, err = ops.Snapshot(context.Background(), wrong); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal(err)
	}
	wrong = target
	wrong.Object.ProfileID = 2
	if _, err = ops.Snapshot(context.Background(), wrong); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal(err)
	}
	if err = m.Disconnect(1); err != nil {
		t.Fatal(err)
	}
	p, err := contract.Require[contract.SchemaDesignProvider](s)
	if err != nil {
		t.Fatal(err)
	}
	if p.SchemaDesign().Apply != nil {
		t.Fatal("revoked provider exposes raw feature")
	}
	if out = ops.Apply(context.Background(), r); !errors.Is(out.Error, contract.ErrRevoked) || out.State != "not_executed" {
		t.Fatal(out)
	}
	if _, err = ops.Snapshot(context.Background(), target); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestSchemaLateCommitKeepsItsOriginalReceipt(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	h := &schemaHandle{handle: handle{id: 1}}
	h.apply = func(_ context.Context, r contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
		if err := m.Disconnect(1); err != nil {
			t.Fatal(err)
		}
		return contract.SchemaDesignOutcome{State: "committed", Verified: true, Actual: &r.Original}
	}
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
	s, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil {
		t.Fatal(err)
	}
	r := contract.SchemaDesignRequest{Original: contract.SchemaDesignSnapshot{Target: contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: 1, Kind: "table", Name: "t"}, Generation: s.Generation()}}}
	r.Desired.Name = "t"
	out := m.SchemaDesign(1).Apply(context.Background(), r)
	if out.State != "committed" || !out.Verified || out.Actual == nil || out.Actual.Target.Generation != s.Generation() {
		t.Fatalf("receipt lost: %+v", out)
	}
}

func TestPostgreSQLReadOnlyCatalogBindingCopiesOriginalMetadataAndGeneration(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	h := &schemaHandle{handle: handle{id: 1}}
	var original contract.SchemaDesignSnapshot
	h.load = func(_ context.Context, target contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
		if target.Generation != 973 {
			t.Fatal("raw PostgreSQL generation was not translated")
		}
		original = contract.SchemaDesignSnapshot{Target: target, Schema: schemaddl.Snapshot{Engine: "postgresql", Revision: "fixture", Postgres: &schemaddl.PostgreSQLCatalog{Entries: []schemaddl.PostgreSQLCatalogEntry{{Kind: "relation", OID: 12, Data: json.RawMessage(`{"name":"original"}`)}}}, Table: schemaddl.Table{Name: "t", Postgres: &schemaddl.PostgreSQLTableProperties{ACL: []string{"reader=r/owner"}}, Clauses: []schemaddl.Clause{{ID: "column:1", Name: "id", SQL: "bigint", Postgres: &schemaddl.PostgreSQLColumnProperties{Parameters: []string{"n_distinct=1"}}}}}}}
		return original, nil
	}
	m.Register(connections.PostgreSQL, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
	p := profile(1)
	p.Kind = connections.PostgreSQL
	p.Host, p.Port, p.Database, p.Username, p.TLSMode = "127.0.0.1", 5432, "database", "fixture", "disable"
	s, err := m.ConnectSession(context.Background(), p, "")
	if err != nil {
		t.Fatal(err)
	}
	target := contract.SchemaDesignTarget{Generation: s.Generation(), Object: contract.ObjectRef{ProfileID: 1, Catalog: "database", Schema: "namespace", Kind: "table", Name: "t"}}
	ops := m.SchemaDesign(1)
	if ops.Snapshot == nil || ops.Apply != nil || ops.CanApply != nil {
		t.Fatal("read-only catalog binding acquired mutation methods")
	}
	snapshot, err := ops.Snapshot(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Target != target {
		t.Fatal("public PostgreSQL scope changed")
	}
	snapshot.Schema.Postgres.Entries[0].Data[0] = '!'
	snapshot.Schema.Table.Postgres.ACL[0] = "changed"
	snapshot.Schema.Table.Clauses[0].Postgres.Parameters[0] = "changed"
	if original.Schema.Postgres.Entries[0].Data[0] != '{' || original.Schema.Table.Postgres.ACL[0] != "reader=r/owner" || original.Schema.Table.Clauses[0].Postgres.Parameters[0] != "n_distinct=1" {
		t.Fatal("published catalog shared mutable raw buffers")
	}
	if err = m.Disconnect(1); err != nil {
		t.Fatal(err)
	}
	if _, err = ops.Snapshot(context.Background(), target); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("revoked read-only catalog binding remained usable", err)
	}
}
