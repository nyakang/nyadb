package dbruntime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/meilisearch"
)

func TestMeilisearchNativePublicProviderMetadataAndPublishedGenerations(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cfg, _ := connections.MeilisearchProfileConfig(parsed.Profile)
	admin, err := meilisearch.Open(ctx, cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	uid := "owned_runtime_" + time.Now().Format("150405000000000")
	submit := func(r meilisearch.Request) {
		t.Helper()
		receipt, _, err := admin.Submit(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = admin.WaitTask(ctx, receipt, 30*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	submit(meilisearch.Request{Method: "POST", Path: "/indexes", Body: []byte(`{"uid":"` + uid + `","primaryKey":"id"}`)})
	defer submit(meilisearch.Request{Method: "DELETE", Path: "/indexes/" + uid})
	submit(meilisearch.Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"one","integer":9007199254740993}]`)})
	m := New()
	RegisterBuiltins(m, nil)
	defer m.Close()
	p := parsed.Profile
	if err = m.Test(ctx, p, parsed.Password); err != nil || m.IsConnected(p.ID) {
		t.Fatal("test published session", err)
	}
	first, err := m.ConnectSession(ctx, p, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	features := first.Capabilities()
	data := m.Data(p.ID)
	if !features.Query || features.Objects || !features.Metadata || !features.Documents || !features.Count || !features.FieldChunks || !features.Export || !features.JSONL || features.Keyset || features.EditableTables || data.Saver != nil {
		t.Fatal("public runtime document or management capabilities")
	}
	if data.ReserveDocument == nil {
		t.Fatal("public document provider not composed")
	}
	document, err := data.ReserveDocument(m.Namespace(p.ID, uid).Object("documents", "documents"))
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	originalData := document.Runtime()
	if count, err := originalData.Count(ctx, p.ID, uid, "documents"); err != nil || count != 1 {
		t.Fatal("original public document count", count, err)
	}
	meta, err := contract.Require[contract.MetadataProvider](first)
	if err != nil {
		t.Fatal(err)
	}
	namespaces, err := meta.Namespaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range namespaces {
		if n.Ref == m.Namespace(p.ID, uid) && n.Label == uid && n.Ref.Schema == "" {
			found = true
		}
	}
	if !found {
		t.Fatal("qualified index missing")
	}
	objects, err := meta.Browse(ctx, m.Namespace(p.ID, uid))
	if err != nil || len(objects) != 4 {
		t.Fatal(objects, err)
	}
	for i, kind := range []string{"index", "documents", "settings", "statistics"} {
		if objects[i].Ref.Kind != kind || objects[i].Ref.Namespace() != m.Namespace(p.ID, uid) {
			t.Fatal("qualified native object missing", objects[i])
		}
	}
	structure, err := meta.TableMetadata(ctx, objects[2].Ref)
	if err != nil || structure.Object != objects[2].Ref || !strings.Contains(structure.Definition, "rankingRules") {
		t.Fatal(structure, err)
	}
	qp, err := contract.Require[contract.QueryProvider](first)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := qp.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if err = lease.Preconnect(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := lease.Execute(ctx, uid, "GET /indexes/"+uid+"/documents/one")
	if err != nil || result.Sets[0].Rows[0][0].Text != "200" || !strings.Contains(result.Sets[0].Rows[0][1].Text, "9007199254740993") {
		t.Fatal(result, err)
	}
	second, err := m.ConnectSession(ctx, p, parsed.Password)
	if err != nil || second.Generation() <= first.Generation() {
		t.Fatal("public generations", err)
	}
	if _, err = lease.Execute(ctx, uid, "GET /indexes/"+uid); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old lease rebound", err)
	}
	if _, err = meta.Browse(ctx, m.Namespace(p.ID, uid)); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old metadata rebound", err)
	}
	if _, err = lease.Replace(); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old replacement rebound", err)
	}
	if _, err = originalData.Count(ctx, p.ID, uid, "documents"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old document runtime rebound", err)
	}
	if _, err = data.ReserveDocument(m.Namespace(p.ID, uid).Object("documents", "documents")); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old document reservation rebound", err)
	}
	_ = document.Close()
	_ = first.Close()
	lease.Release()
	if !m.IsConnected(p.ID) {
		t.Fatal("old close killed new session")
	}
	freshDocument, err := m.Data(p.ID).ReserveDocument(m.Namespace(p.ID, uid).Object("documents", "documents"))
	if err != nil {
		t.Fatal(err)
	}
	defer freshDocument.Close()
	if count, err := freshDocument.Runtime().Count(ctx, p.ID, uid, "documents"); err != nil || count != 1 {
		t.Fatal("old close killed new original document", count, err)
	}
	fresh, err := contract.Require[contract.QueryProvider](second)
	if err != nil {
		t.Fatal(err)
	}
	q, err := fresh.ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Release()
	if _, err = q.Execute(ctx, uid, "GET /indexes/"+uid); err != nil {
		t.Fatal("replacement damaged", err)
	}
}
