package dbruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/qdrant"
)

func TestQdrantNativePublicProviderMetadataAndPublishedGenerations(t *testing.T) {
	parsed := qdrantPublicFixture(t, "NYADB_QDRANT_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cfg, _ := connections.QdrantProfileConfig(parsed.Profile)
	c, err := qdrant.Open(ctx, cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	name := "owned_runtime_" + time.Now().Format("150405000000000")
	run := func(method, path, body string) {
		t.Helper()
		if _, err := c.Do(ctx, qdrant.Request{Method: method, Path: path, Body: []byte(body)}); err != nil {
			t.Fatal(err)
		}
	}
	run("PUT", "/collections/"+name, `{"vectors":{"size":2,"distance":"Dot"},"sparse_vectors":{"sparse":{}}}`)
	defer run("DELETE", "/collections/"+name, "")
	run("PUT", "/collections/"+name+"/points?wait=true", `{"points":[{"id":18446744073709551615,"vector":[1,0],"payload":{"integer":9007199254740993,"text":"公开原生🐱"}}]}`)
	alias := name + "_alias"
	run("POST", "/collections/aliases", `{"actions":[{"create_alias":{"collection_name":"`+name+`","alias_name":"`+alias+`"}}]}`)
	m := New()
	RegisterBuiltins(m, nil)
	defer m.Close()
	p := parsed.Profile
	if err := m.Test(ctx, p, parsed.Password); err != nil || m.IsConnected(p.ID) {
		t.Fatal("connection test published a session", err)
	}
	first, err := m.ConnectSession(ctx, p, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	caps := first.Capabilities()
	if !caps.Query || !caps.Metadata || caps.Objects || caps.Documents || caps.Count || caps.EditableTables || caps.Backup || caps.Export {
		t.Fatal("unverified data/management tools exposed", caps)
	}
	meta, err := contract.Require[contract.MetadataProvider](first)
	if err != nil {
		t.Fatal(err)
	}
	names, err := meta.Namespaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range names {
		if n.Ref.ProfileID != p.ID || n.Ref.Schema != "" || n.Ref.Catalog != n.Label || n.Label == alias {
			t.Fatal("public namespace mixed identity or alias", n)
		}
		found = found || n.Ref == m.Namespace(p.ID, name)
	}
	if !found {
		t.Fatal("original physical collection omitted")
	}
	objects, err := meta.Browse(ctx, m.Namespace(p.ID, name))
	if err != nil || len(objects) != 3 {
		t.Fatal("public physical objects", objects, err)
	}
	for _, o := range objects {
		structure, err := meta.TableMetadata(ctx, o.Ref)
		if err != nil || structure.Object != o.Ref || structure.Engine != "Qdrant" || len(structure.Columns) != 0 || len(structure.Definitions) != 1 || structure.Definitions[0].SQL != structure.Definition {
			t.Fatal("native definitions fabricated SQL metadata", err)
		}
		if o.Ref.Kind == "aliases" && !strings.Contains(structure.Definition, alias) {
			t.Fatal("original alias relationship missing")
		}
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
	result, err := lease.Execute(ctx, name, "POST /collections/"+name+`/points/query
{"query":[1,0],"with_payload":true,"with_vector":true,"limit":1}`)
	if err != nil || len(result.Sets) != 2 || result.Sets[0].Rows[0][0].Kind != contract.Integer || result.Sets[0].Rows[0][0].Text != "18446744073709551615" || !strings.Contains(result.Sets[1].Rows[0][1].Text, "9007199254740993") {
		t.Fatal("public point query lost precision or original response", result, err)
	}
	second, err := m.ConnectSession(ctx, p, parsed.Password)
	if err != nil || second.Generation() <= first.Generation() {
		t.Fatal("public generations not monotonic", err)
	}
	if _, err := lease.Execute(ctx, name, "GET /"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old query rebound", err)
	}
	if _, err := meta.Browse(ctx, m.Namespace(p.ID, name)); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old metadata rebound", err)
	}
	if _, err := lease.Replace(); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old lease replacement rebound", err)
	}
	first.Close()
	lease.Release()
	if !m.IsConnected(p.ID) {
		t.Fatal("captured old close killed replacement")
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
	if _, err := q.Execute(ctx, name, "GET /"); err != nil {
		t.Fatal("replacement query damaged", err)
	}
}

func TestQdrantNativePublicReadOnlyProviderDenialAndOriginalRecovery(t *testing.T) {
	parsed := qdrantPublicFixture(t, "NYADB_QDRANT_CONFIG")
	// Anonymous mode verifies its original uncredentialed session. Authenticated
	// modes additionally exercise the server's independent read-only key.
	if parsed.Profile.QdrantOptions.Auth == "api_key" {
		parsed = qdrantPublicFixture(t, "NYADB_QDRANT_READONLY_CONFIG")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	m := New()
	RegisterBuiltins(m, nil)
	defer m.Close()
	session, err := m.ConnectSession(ctx, parsed.Profile, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := contract.Require[contract.MetadataProvider](session)
	if err != nil {
		t.Fatal(err)
	}
	names, err := meta.Namespaces(ctx)
	if err != nil || len(names) == 0 {
		t.Fatal("original key cannot enumerate fixture", err)
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_QDRANT_CONFIG"))
	var fixture struct {
		Collection string `json:"fixture_collection"`
	}
	if err != nil || json.Unmarshal(raw, &fixture) != nil || fixture.Collection == "" {
		t.Fatal("owned seeded collection unavailable")
	}
	name := fixture.Collection
	qp, err := contract.Require[contract.QueryProvider](session)
	if err != nil {
		t.Fatal(err)
	}
	l, err := qp.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	query := "POST /collections/" + name + "/points/query\n{\"query\":[1,0],\"with_payload\":true,\"limit\":1}"
	result, err := l.Execute(ctx, name, query)
	if err != nil || len(result.Sets) != 2 || len(result.Sets[0].Rows) != 1 {
		t.Fatal("original key lost typed native query", err)
	}
	if parsed.Profile.QdrantOptions.Auth == "none" {
		if parsed.Password != "" {
			t.Fatal("anonymous provider captured a key")
		}
		return
	}
	result, err = l.Execute(ctx, name, "PUT /collections/"+name+"/points?wait=true\n{\"points\":[{\"id\":999,\"vector\":[1,0]}]}")
	if !errors.Is(err, contract.ErrSQLSessionLost) || result.Steps[0].State != "uncertain" || result.Sets[0].Rows[0][0].Text != "403" || result.Truncated {
		t.Fatal("native forbidden write lost original evidence", result, err)
	}
	other, err := qp.ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Release()
	if _, err := other.Execute(ctx, name, query); err != nil {
		t.Fatal("write denial retired independent allowed read", err)
	}
	if _, err := l.Execute(ctx, name, query); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("forbidden lease silently recovered", err)
	}
	replacement, err := l.Replace()
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Release()
	if _, err := replacement.Execute(ctx, name, query); err != nil {
		t.Fatal("explicit original read recovery failed", err)
	}
	result, err = replacement.Execute(ctx, name, "GET /collections/"+name+"/points/999")
	if err == nil || result.Sets[0].Rows[0][0].Text != "404" || errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("forbidden write was replayed", err)
	}
}
