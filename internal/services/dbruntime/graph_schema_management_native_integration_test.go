package dbruntime_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

func TestNeo4jNativePublicSchemaManagementOriginalGenerationAndReceipts(t *testing.T) {
	if os.Getenv("NYADB_NEO4J_SCHEMA_MANAGEMENT_IT") != "1" {
		t.Skip("requires owned Neo4j schema management fixture")
	}
	p, secret, nonce := graphSchemaNativeProfileTest(t)
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dummy := p
	dummy.ID++
	if _, e := m.ConnectSession(ctx, dummy, secret); e != nil {
		t.Fatal(e)
	}
	first, e := m.ConnectSession(ctx, p, secret)
	if e != nil {
		t.Fatal(e)
	}
	provider, e := m.GraphSchemaManagement(p.ID)
	if e != nil {
		t.Fatal(e)
	}
	o := m.Namespace(p.ID, "").Object("graph_schema", "graph_schema")
	if !provider.SupportsGraphSchemaManagement(o) {
		t.Fatal("native original management unsupported")
	}
	l, e := provider.ReserveGraphSchemaManagement(o, 1)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	if l.Generation() != first.Generation() || l.Generation() == 1 {
		t.Fatal("public original generation not translated")
	}
	before, e := l.Read(ctx)
	if e != nil {
		t.Fatal(e)
	}
	draft := contract.GraphSchemaDraft{Action: "create_index", Name: "nya_" + nonce + "_runtime_schema", Type: "RANGE", EntityType: "NODE", LabelsOrTypes: []string{"SchemaRuntime_" + nonce}, Properties: []string{"name"}}
	plan, e := l.Prepare(before, draft)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := l.Submit(ctx, &graphSchemaTestPlan{preview: plan.Preview()}, contract.GraphSchemaConfirmation{}); !errors.Is(e, contract.ErrGraphSchemaChanged) {
		t.Fatal("forged public preview dispatched", e)
	}
	r, e := l.Submit(ctx, plan, contract.GraphSchemaConfirmation{})
	if e != nil || r.State != "completed" || !r.ReceiptValid || r.HasVerification {
		t.Fatal("public original native receipt lost", e)
	}
	r, e = l.Verify(ctx)
	if e != nil || r.State != "verified" || !r.Matched {
		t.Fatal(e)
	}
	r.Receipt.Summary.Counters["indexes_added"] = 9
	r.Verification.Indexes.Columns[0] = "mutated"
	next, e := m.ConnectSession(ctx, p, secret)
	if e != nil {
		t.Fatal(e)
	}
	if next.Generation() <= first.Generation() {
		t.Fatal("public generation did not increase")
	}
	if _, e := provider.ReserveGraphSchemaManagement(o, 2); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("old public management reservation followed replacement", e)
	}
	retained, e := l.Verify(ctx)
	if !errors.Is(e, contract.ErrRevoked) || !retained.ReceiptValid || retained.Receipt.Summary.Counters["indexes_added"] != 1 || retained.Verification.Indexes.Columns[0] != "id" {
		t.Fatal("revocation lost independent original receipt/verification", e)
	}
	if _, e := l.Submit(ctx, plan, contract.GraphSchemaConfirmation{}); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("replaced original native write replayable", e)
	}
	first.Close()
	l.Close()
	fresh, e := m.GraphSchemaManagement(p.ID)
	if e != nil {
		t.Fatal(e)
	}
	current, e := fresh.ReserveGraphSchemaManagement(o, 1)
	if e != nil {
		t.Fatal(e)
	}
	defer current.Close()
	actual, e := current.Read(ctx)
	if e != nil || actual.Snapshot().Object != o || m.Generation(p.ID) != next.Generation() {
		t.Fatal("old cleanup closed replacement", e)
	}
	deletePlan, e := current.Prepare(actual, contract.GraphSchemaDraft{Action: "drop_index", Name: draft.Name})
	if e != nil {
		t.Fatal(e)
	}
	removed, e := current.Submit(ctx, deletePlan, contract.GraphSchemaConfirmation{Name: draft.Name, AcceptNonAtomicDrop: true})
	if e != nil || !removed.ReceiptValid {
		t.Fatal("explicit replacement-native cleanup incomplete", e)
	}
	verified, e := current.Verify(ctx)
	if e != nil || !verified.Matched || verified.State != "verified" {
		t.Fatal(e)
	}
}
