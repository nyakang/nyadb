package dbruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

func TestNeo4jNativePublicSchemaPreparationOriginalGeneration(t *testing.T) {
	if os.Getenv("NYADB_NEO4J_SCHEMA_PREPARATION_IT") != "1" {
		t.Skip("requires owned Neo4j schema preparation fixture")
	}
	raw, e := os.ReadFile(os.Getenv("NYADB_NEO4J_CONFIG_FILE"))
	if e != nil {
		t.Fatal("cannot read owned private fixture")
	}
	var f struct {
		Config                 connections.Neo4jConfig
		Secret, Nonce, Version string
	}
	if json.Unmarshal(raw, &f) != nil || len(f.Nonce) != 32 || f.Version != "5.26.21" {
		t.Fatal("invalid owned fixture")
	}
	u, e := url.Parse(f.Config.URI)
	if e != nil || u.Hostname() != "localhost" {
		t.Fatal("invalid owned endpoint")
	}
	u.Path = "/" + f.Config.Database
	q := url.Values{"auth": {f.Config.Auth}}
	for key, value := range map[string]string{"ca_file": f.Config.CAFile, "client_cert_file": f.Config.ClientCertFile, "client_key_file": f.Config.ClientKeyFile} {
		if value != "" {
			q.Set(key, value)
		}
	}
	u.RawQuery = q.Encode()
	parsed, e := connections.ParseURI(u.String())
	if e != nil {
		t.Fatal("invalid owned profile")
	}
	p := parsed.Profile
	p.ID, p.Name = 84, "Owned schema preparation"
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	first, e := m.ConnectSession(ctx, p, f.Secret)
	if e != nil {
		t.Fatal(e)
	}
	provider, e := m.GraphSchemaPreparation(p.ID)
	if e != nil {
		t.Fatal(e)
	}
	o := m.Namespace(p.ID, "").Object("graph_schema", "graph_schema")
	if !provider.SupportsGraphSchemaPreparation(o) {
		t.Fatal("native preparatory feature unavailable")
	}
	l, e := provider.ReserveGraphSchemaPreparation(o, 1)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	if l.Generation() != first.Generation() || l.Object() != o {
		t.Fatal("original published identity lost")
	}
	before, e := l.Read(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if len(before.Snapshot().Indexes.Columns) < 16 || len(before.Snapshot().Constraints.Columns) < 10 {
		t.Fatal("public native catalog became concise SHOW")
	}
	draft := contract.GraphSchemaDraft{Action: "create_index", Name: "nya_" + f.Nonce + "_public_preview", Type: "RANGE", EntityType: "NODE", LabelsOrTypes: []string{"NyaSchema"}, Properties: []string{"name"}}
	plan, e := l.Prepare(before, draft)
	if e != nil {
		t.Fatal(e)
	}
	preview := plan.Preview()
	preview.Draft.Properties[0] = "changed"
	if plan.Preview().Draft.Properties[0] != "name" {
		t.Fatal("public preview aliases UI-owned copy")
	}
	foreign, e := provider.ReserveGraphSchemaPreparation(o, 2)
	if e != nil {
		t.Fatal(e)
	}
	defer foreign.Close()
	if _, e := foreign.Prepare(before, draft); !errors.Is(e, contract.ErrGraphSchemaChanged) {
		t.Fatal("different public original lease accepted observation", e)
	}
	second, e := m.ConnectSession(ctx, p, f.Secret)
	if e != nil || second.Generation() <= first.Generation() {
		t.Fatal(e)
	}
	if provider.SupportsGraphSchemaPreparation(o) {
		t.Fatal("old schema provider followed replacement")
	}
	if _, e := l.Read(ctx); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("old native read reached replacement", e)
	}
	if _, e := l.Prepare(before, draft); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("old native preparation reached replacement", e)
	}
	if _, e := provider.ReserveGraphSchemaPreparation(o, 3); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("old native reservation reached replacement", e)
	}
	first.Close()
	l.Close()
	foreign.Close()
	fresh, e := m.GraphSchemaPreparation(p.ID)
	if e != nil {
		t.Fatal(e)
	}
	freshLease, e := fresh.ReserveGraphSchemaPreparation(o, 1)
	if e != nil {
		t.Fatal(e)
	}
	defer freshLease.Close()
	next, e := freshLease.Read(ctx)
	if e != nil || next.Snapshot().Object != o || m.Generation(p.ID) != second.Generation() {
		t.Fatal("old cleanup closed replacement", e)
	}
	for i, key := range next.Snapshot().Indexes.Columns {
		if key == "name" {
			for _, row := range next.Snapshot().Indexes.Rows {
				if row[i].Text == draft.Name {
					t.Fatal("pure public preview executed DDL")
				}
			}
		}
	}
	if plan.Preview().Draft.Name != draft.Name || before.Snapshot().Object != o {
		t.Fatal("original evidence lost at replacement")
	}
}
