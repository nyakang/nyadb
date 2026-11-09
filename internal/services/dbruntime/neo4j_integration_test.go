package dbruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

func TestNeo4jNativePublicRuntimeQualifiedGraphAndGeneration(t *testing.T) {
	if os.Getenv("NYADB_NEO4J_PUBLIC_IT") != "1" {
		t.Skip("requires owned Neo4j public fixture")
	}
	raw, e := os.ReadFile(os.Getenv("NYADB_NEO4J_CONFIG_FILE"))
	if e != nil {
		t.Fatal("cannot read private fixture")
	}
	var f struct {
		Config                 connections.Neo4jConfig
		Secret, Nonce, Version string
	}
	if json.Unmarshal(raw, &f) != nil || len(f.Nonce) != 32 || f.Version != "5.26.21" {
		t.Fatal("invalid fixture")
	}
	u, _ := url.Parse(f.Config.URI)
	u.Path = "/" + f.Config.Database
	q := url.Values{"auth": {f.Config.Auth}}
	for k, v := range map[string]string{"ca_file": f.Config.CAFile, "client_cert_file": f.Config.ClientCertFile, "client_key_file": f.Config.ClientKeyFile} {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	parsed, e := connections.ParseURI(u.String())
	if e != nil {
		t.Fatal("invalid fixture profile")
	}
	p := parsed.Profile
	p.ID = 84
	p.Name = "Owned Neo4j runtime"
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	original, e := m.ConnectSession(ctx, p, f.Secret)
	if e != nil {
		t.Fatal(e)
	}
	c := original.Capabilities()
	if !c.Query || !c.Metadata || !c.Documents || !c.Keyset || !c.Count || !c.FieldChunks || !c.Export || !c.JSONL || c.Objects || !c.EditableTables || c.Transactions || c.Schemas || c.Import {
		t.Fatal("fabricated graph capabilities")
	}
	metadata, e := contract.Require[contract.GraphMetadataProvider](original)
	if e != nil {
		t.Fatal(e)
	}
	n := m.Namespace(p.ID, "")
	if n != (contract.NamespaceRef{ProfileID: p.ID, Catalog: "neo4j"}) {
		t.Fatal("home database guessed or became schema")
	}
	schema, e := metadata.GraphSchema(ctx, n)
	if e != nil || schema.Namespace != n || !schema.Functions.Completed || len(schema.Functions.Rows) == 0 {
		t.Fatal("public graph metadata lost", e)
	}
	provider, e := contract.Require[contract.QueryProvider](original)
	if e != nil {
		t.Fatal(e)
	}
	reserved, e := provider.ReserveQuery(1)
	if e != nil {
		t.Fatal(e)
	}
	defer reserved.Release()
	graph, ok := reserved.(contract.GraphQueryLease)
	if !ok {
		t.Fatal("public wrapper hid typed graph feature")
	}
	r, e := graph.ExecuteGraph(ctx, contract.GraphQueryRequest{Database: n.Catalog, Query: "RETURN $exact AS exact, point({x:1,y:2}) AS point", Parameters: map[string]any{"exact": int64(9223372036854775807)}})
	if e != nil || !r.Completed || r.Rows[0][0].Text != strconv.FormatInt(9223372036854775807, 10) || r.Rows[0][1].Kind != "point2d" || r.Summary.Database != n.Catalog {
		t.Fatal("public native result narrowed", e)
	}
	replacement, e := m.ConnectSession(ctx, p, f.Secret)
	if e != nil {
		t.Fatal(e)
	}
	if replacement.Generation() <= original.Generation() {
		t.Fatal("generation did not increase")
	}
	if _, e = graph.ExecuteGraph(ctx, contract.GraphQueryRequest{Query: "RETURN 1"}); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("old lease reached replacement", e)
	}
	if _, e = metadata.GraphSchema(ctx, n); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("old metadata reached replacement", e)
	}
	original.Close()
	reserved.Release()
	if !m.IsConnected(p.ID) || m.Generation(p.ID) != replacement.Generation() {
		t.Fatal("old cleanup closed replacement")
	}
}
