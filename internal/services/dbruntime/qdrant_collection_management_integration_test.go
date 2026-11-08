package dbruntime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/qdrant"
)

func TestQdrantNativePublicCollectionManagementOriginalGenerationClaimsReceiptAndNoReplay(t *testing.T) {
	parsed := qdrantPublicFixture(t, "NYADB_QDRANT_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	name := "owned_bound_runtime_" + time.Now().Format("150405000000000")
	cfg, _ := connections.QdrantProfileConfig(parsed.Profile)
	admin, err := qdrant.Open(ctx, cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	defer func() {
		_, _ = admin.Do(context.Background(), qdrant.Request{Method: "DELETE", Path: "/collections/" + name})
	}()
	m := New()
	RegisterBuiltins(m, nil)
	defer m.Close()
	if err = m.Connect(ctx, parsed.Profile, parsed.Password); err != nil {
		t.Fatal(err)
	}
	first, err := m.ConnectSession(ctx, parsed.Profile, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	o := contract.ObjectRef{ProfileID: parsed.Profile.ID, Kind: "collection_management", Name: "collection_management"}
	p, err := m.CollectionManagement(o.ProfileID)
	if err != nil || !p.SupportsCollectionManagement(o, "collection_create") {
		t.Fatal("native optional management unavailable", err)
	}
	l, err := p.ReserveCollectionManagementDocument(o, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if l.Generation() != first.Generation() || l.Generation() == first.(*session).raw.Generation() {
		t.Fatal("native/raw generation leaked into publication")
	}
	ref := m.Namespace(o.ProfileID, name)
	before, err := l.Read(ctx, []contract.NamespaceRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := l.Prepare(before, contract.CollectionManagementDraft{Action: "collection_create", Collection: ref, Configuration: `{"vectors":{"size":2,"distance":"Dot"},"on_disk_payload":false,"metadata":{"n":9007199254740993}}`, TimeoutSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	r, err := l.Submit(ctx, plan)
	if err != nil || r.State != "response" || !r.Receipt.HasResult || !r.Receipt.Result {
		t.Fatal("native create prematurely settled", err, r.State)
	}
	qp, err := contract.Require[contract.QueryProvider](first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = qp.ReserveQuery(8); !errors.Is(err, contract.ErrNamespaceBusy) {
		t.Fatal("published REST reservation crossed pending management", err)
	}
	r, err = l.Verify(ctx)
	if err != nil || r.State != "applied" || !r.Matched || len(r.Snapshot.Configurations) != 1 || !strings.Contains(r.Snapshot.Configurations[0].JSON, "9007199254740993") {
		t.Fatal("native create exact readback lost", err, r.State)
	}
	before, err = l.Read(ctx, []contract.NamespaceRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	plan, err = l.Prepare(before, contract.CollectionManagementDraft{Action: "collection_update", Collection: ref, Configuration: `{"params":{"on_disk_payload":true},"metadata":{"n":null,"nested":{"null":null}}}`, TimeoutSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	r, err = l.Submit(ctx, plan)
	if err != nil || !r.Receipt.HasResult || !r.Receipt.Result {
		t.Fatal("native update failed", err)
	}
	originalBody := string(r.Receipt.Response.Body)
	r.Receipt.Response.Body[0] = 'x'
	second, err := m.ConnectSession(ctx, parsed.Profile, parsed.Password)
	if err != nil || second.Generation() <= first.Generation() {
		t.Fatal(err)
	}
	if p.SupportsCollectionManagement(o, "collection_update") {
		t.Fatal("original native provider followed replacement")
	}
	if _, err = p.ReserveCollectionManagementDocument(o, 9); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("original native reservation followed replacement", err)
	}
	if _, err = l.Read(ctx, []contract.NamespaceRef{ref}); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("original native read followed replacement", err)
	}
	if _, err = l.Prepare(before, contract.CollectionManagementDraft{Action: "collection_delete", Collection: ref, TimeoutSeconds: 30}); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("original native preparation followed replacement", err)
	}
	for _, operation := range []func() (contract.CollectionManagementResult, error){func() (contract.CollectionManagementResult, error) { return l.Verify(ctx) }, func() (contract.CollectionManagementResult, error) { return l.Submit(ctx, plan) }} {
		r, err = operation()
		if !errors.Is(err, contract.ErrRevoked) || r.State != "indeterminate" || !r.Receipt.HasResult || !r.Receipt.Result || string(r.Receipt.Response.Body) != originalBody {
			t.Fatal("original receipt lost/replayed after native replacement", err, r.State)
		}
		r.Receipt.Response.Body[0] = 'x'
	}
	info, _, err := admin.Collection(ctx, name)
	if err != nil || !strings.Contains(string(info.Result), `"on_disk_payload":true`) || !strings.Contains(string(info.Result), `"nested":{"null":null}`) {
		t.Fatal("original native update effect not observed", err)
	}
	newQP, err := contract.Require[contract.QueryProvider](second)
	if err != nil {
		t.Fatal(err)
	}
	query, err := newQP.ReserveQuery(8)
	if err != nil {
		t.Fatal("replacement session inherited original claim", err)
	}
	defer query.Release()
	l.Close()
	if _, err = query.Execute(ctx, name, "GET /"); err != nil || !m.IsConnected(o.ProfileID) {
		t.Fatal("old original close damaged replacement", err)
	}
	query.Release()
	current, err := m.CollectionManagement(o.ProfileID)
	if err != nil {
		t.Fatal(err)
	}
	newLease, err := current.ReserveCollectionManagementDocument(o, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer newLease.Close()
	if _, err = newLease.Prepare(before, contract.CollectionManagementDraft{Action: "collection_delete", Collection: ref, TimeoutSeconds: 30}); !errors.Is(err, contract.ErrNamespaceChanged) {
		t.Fatal("replacement accepted old observation handle", err)
	}
	before, err = newLease.Read(ctx, []contract.NamespaceRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	alias := m.Namespace(o.ProfileID, name+"_alias")
	plan, err = newLease.Prepare(before, contract.CollectionManagementDraft{Action: "aliases", Aliases: []contract.CollectionAliasChange{{Action: "create_alias", Alias: alias, Collection: ref}}, TimeoutSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = newLease.Submit(ctx, plan); err != nil {
		t.Fatal(err)
	}
	r, err = newLease.Verify(ctx)
	if err != nil || r.State != "applied" || !r.Matched {
		t.Fatal("native qualified alias readback failed", err, r.State)
	}
	before, err = newLease.Read(ctx, []contract.NamespaceRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	plan, err = newLease.Prepare(before, contract.CollectionManagementDraft{Action: "collection_delete", Collection: ref, TimeoutSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = newLease.Submit(ctx, plan); err != nil {
		t.Fatal(err)
	}
	r, err = newLease.Verify(ctx)
	if err != nil || r.State != "applied" || !r.Matched {
		t.Fatal("native original deletion/alias removal failed", err, r.State)
	}
	t.Log("Original public-generation management leases, immutable observations/previews, actual boolean receipts/readback, namespace exclusion, replacement revocation, retained unresolved evidence and independent original close verified")
}
