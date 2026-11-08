package dbruntime

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/couchdb"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func couchdbPublicFixture(t *testing.T, configName string) connections.URIImport {
	t.Helper()
	if os.Getenv("NYADB_COUCHDB_WORKBENCH_IT") != "1" {
		t.Skip("requires owned native CouchDB workbench fixture")
	}
	raw, err := os.ReadFile(os.Getenv(configName))
	if err != nil {
		t.Fatal("private CouchDB fixture config unavailable")
	}
	var cfg struct {
		Endpoint, Auth, Username string
		CAFile                   string `json:"ca_file"`
		ServerName               string `json:"server_name"`
		PasswordFile             string `json:"password_file"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		t.Fatal("invalid private CouchDB config")
	}
	secret := ""
	if cfg.PasswordFile != "" {
		raw, err = os.ReadFile(cfg.PasswordFile)
		if err != nil {
			t.Fatal("private credential unavailable")
		}
		secret = string(raw)
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil {
		t.Fatal("invalid private endpoint")
	}
	uri := url.URL{Scheme: "couchdb", Host: endpoint.Host, Path: endpoint.Path}
	options := url.Values{"auth": {cfg.Auth}, "sslmode": {"disable"}}
	if endpoint.Scheme == "https" {
		options.Set("sslmode", "verify-full")
	}
	if cfg.CAFile != "" {
		options.Set("ca_file", cfg.CAFile)
	}
	if cfg.ServerName != "" {
		options.Set("server_name", cfg.ServerName)
	}
	if cfg.Auth == "basic" {
		uri.User = url.UserPassword(cfg.Username, secret)
	}
	uri.RawQuery = options.Encode()
	parsed, err := connections.ParseURI(uri.String())
	if err != nil {
		t.Fatal("public CouchDB URI rejected", err)
	}
	parsed.Profile.ID, parsed.Profile.Name = 96, "Native CouchDB workbench"
	return parsed
}

func TestCouchDBNativePublicProviderMetadataAndPublishedGenerations(t *testing.T) {
	parsed := couchdbPublicFixture(t, "NYADB_COUCHDB_ADMIN_CONFIG")
	p, secret := parsed.Profile, parsed.Password
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	m := New()
	RegisterBuiltins(m, nil)
	defer m.Close()
	if err := m.Test(ctx, p, secret); err != nil || m.IsConnected(p.ID) {
		t.Fatal("connection test published session", err)
	}
	first, err := m.ConnectSession(ctx, p, secret)
	if err != nil {
		t.Fatal(err)
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
	name := "owned_runtime_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	cfg, _ := connections.CouchDBProfileConfig(p)
	admin, err := couchdb.Open(ctx, cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err = admin.Do(ctx, couchdb.Request{Method: "PUT", Path: "/" + name}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, err := admin.Do(context.Background(), couchdb.Request{Method: "DELETE", Path: "/" + name})
		if err != nil {
			t.Error("owned database cleanup", err)
		}
	}()
	if _, err = admin.Do(ctx, couchdb.Request{Method: "PUT", Path: "/" + name + "/_design/shared", Body: []byte(`{"_id":"_design/shared","views":{"exact":{"map":"function(doc) { emit(doc._id, doc.n); }"}},"n":9007199254740993}`)}); err != nil {
		t.Fatal(err)
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
		if n.Ref == m.Namespace(p.ID, name) && n.Label == name && n.Ref.Schema == "" {
			found = true
		}
	}
	if !found {
		t.Fatal("qualified public database missing")
	}
	objects, err := meta.Browse(ctx, m.Namespace(p.ID, name))
	if err != nil || len(objects) != 3 || objects[0].Kind != "数据库信息" || objects[1].Kind != "设计文档" || objects[1].Ref.Kind != "design_document" {
		t.Fatal(objects, err)
	}
	structure, err := meta.TableMetadata(ctx, objects[1].Ref)
	if err != nil || len(structure.Definitions) != 2 || !strings.Contains(structure.Definition, "9007199254740993") {
		t.Fatal(structure, err)
	}
	features := first.Capabilities()
	data := m.Data(p.ID)
	if !features.Query || !features.Metadata || !features.Documents || features.EditableTables || data.ReserveDocument == nil || data.Saver != nil || data.Importer != nil {
		t.Fatal("readonly public data capabilities")
	}
	object := m.Namespace(p.ID, name).Object("documents", "_all_docs")
	document, err := data.ReserveDocument(object)
	if err != nil {
		t.Fatal("public document reservation", err)
	}
	defer document.Close()
	bound := document.Runtime()
	page, err := bound.AdvancedLoader(ctx, p.ID, name, "_all_docs", contract.DataOptions{Object: object, Limit: 1})
	if err != nil || len(page.Rows) != 1 || page.Rows[0][0].Text != "_design/shared" || !strings.Contains(page.Rows[0][2].Text, "9007199254740993") || bound.Selector.Dialect != "mango" || bound.ReadDescription == nil {
		t.Fatal("public original document page", err)
	}
	if bound.Capabilities.EditableTables || bound.Saver != nil || bound.Importer != nil || bound.Transactions.Begin != nil {
		t.Fatal("public document inferred write semantics")
	}
	if _, err = data.ReserveDocument((contract.NamespaceRef{ProfileID: p.ID + 1, Catalog: name}).Object("documents", "_all_docs")); err == nil {
		t.Fatal("foreign profile document accepted")
	}
	if err = lease.Preconnect(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := lease.Execute(ctx, name, "GET /"+name+"/_design/shared")
	if err != nil || len(result.Sets) != 1 || result.Sets[0].Rows[0][0].Text != "200" || !strings.Contains(result.Sets[0].Rows[0][1].Text, "9007199254740993") {
		t.Fatal(result, err)
	}
	second, err := m.ConnectSession(ctx, p, secret)
	if err != nil || second.Generation() <= first.Generation() {
		t.Fatal("published generation not monotonic", err)
	}
	if _, err = lease.Execute(ctx, name, "GET /"+name); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old query lease rebound", err)
	}
	if _, err = meta.Browse(ctx, m.Namespace(p.ID, name)); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old metadata rebound", err)
	}
	if _, err = bound.Count(ctx, p.ID, name, "_all_docs"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old document rebound after published generation replacement", err)
	}
	if _, err = data.ReserveDocument(object); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old provider reopened data on replacement", err)
	}
	_ = first.Close()
	_ = document.Close()
	lease.Release()
	if !m.IsConnected(p.ID) {
		t.Fatal("original close killed replacement")
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
	if _, err = q.Execute(ctx, name, "GET /"+name); err != nil {
		t.Fatal("replacement damaged", err)
	}
	freshDocument, err := m.Data(p.ID).ReserveDocument(object)
	if err != nil {
		t.Fatal("replacement document unavailable", err)
	}
	defer freshDocument.Close()
	count, err := freshDocument.Runtime().Count(ctx, p.ID, name, "_all_docs")
	if err != nil || count != 1 {
		t.Fatal("original close damaged replacement document", count, err)
	}
}
