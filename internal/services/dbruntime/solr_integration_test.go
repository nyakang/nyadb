package dbruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/solr"
)

func solrPublicFixture(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_SOLR_WORKBENCH_IT") != "1" {
		t.Skip("requires owned native Solr workbench fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_SOLR_CONFIG"))
	if err != nil {
		t.Fatal("private probe unavailable")
	}
	var probe struct {
		Endpoint, Auth, Username string
		PasswordFile             string `json:"password_file"`
		CAFile                   string `json:"ca_file"`
		ServerName               string `json:"server_name"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		t.Fatal("invalid private probe")
	}
	secret, err := os.ReadFile(probe.PasswordFile)
	if err != nil {
		t.Fatal("private credential unavailable")
	}
	u, err := url.Parse(probe.Endpoint)
	if err != nil {
		t.Fatal("invalid native endpoint")
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil {
		t.Fatal("invalid native port")
	}
	p, _ := connections.Defaults(connections.Solr)
	p.ID, p.Name, p.Host, p.Port, p.Username = 96, "Native Solr runtime", u.Hostname(), uint16(port), probe.Username
	p.SolrOptions = connections.SolrOptions{Version: 1, Auth: probe.Auth, ContextPath: u.Path, CAFile: probe.CAFile, ServerName: probe.ServerName}
	if u.Scheme == "http" {
		p.TLSMode = "disable"
	}
	return p, string(secret)
}

func TestSolrNativePublicDocumentFeaturesAndPublishedGenerations(t *testing.T) {
	p, secret := solrPublicFixture(t)
	ctx, stop := context.WithTimeout(context.Background(), 360*time.Second)
	defer stop()
	cfg, err := connections.SolrProfileConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := solr.Open(ctx, cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := "nyadb_public_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	cloud := admin.Identity().Mode == "solrcloud"
	kind, create, remove := "core", "/admin/cores?action=CREATE&configSet=nyadb_edit&name=", "/admin/cores?action=UNLOAD&deleteInstanceDir=true&core="
	if cloud {
		kind, create, remove = "collection", "/admin/collections?action=CREATE&numShards=2&replicationFactor=1&collection.configName=nyadb_edit&name=", "/admin/collections?action=DELETE&name="
	}
	if _, err = admin.Do(ctx, solr.Request{Method: "GET", Path: create + name + "&wt=json"}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, e := admin.Do(cleanup, solr.Request{Method: "GET", Path: remove + name + "&wt=json"}); e != nil {
			t.Error("owned public target cleanup", e)
		}
	}()
	m := New()
	RegisterBuiltins(m, nil)
	defer m.Close()
	if err = m.Test(ctx, p, secret); err != nil || m.IsConnected(p.ID) {
		t.Fatal("native check published a session", err)
	}
	first, err := m.ConnectSession(ctx, p, secret)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := contract.Require[contract.MetadataProvider](first)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := meta.Namespaces(ctx)
	if err != nil || len(ns) != 1 || ns[0].Ref.Schema != "" || !strings.HasPrefix(ns[0].Ref.Catalog, "solr-observation:") {
		t.Fatal("native qualified namespace", err)
	}
	o := ns[0].Ref.Object(kind, name)
	objects, err := meta.Browse(ctx, ns[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, obj := range objects {
		if obj.Ref == o && (obj.Kind == "Core" || obj.Kind == "集合") {
			found = true
		}
	}
	if !found {
		t.Fatal("public native target missing")
	}
	r := m.Data(p.ID)
	if r.ReserveDocument == nil || r.AdvancedLoader != nil || r.Saver != nil {
		t.Fatal("unowned data executor")
	}
	a, err := r.ReserveDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := r.ReserveDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ar, br := a.Runtime(), b.Runtime()
	if !ar.Capabilities.EditableTables || ar.Saver == nil || ar.Verify == nil || ar.Count == nil || ar.Chunk == nil || ar.JSONL == nil || ar.Importer != nil {
		t.Fatal("public bound capabilities")
	}
	info, err := ar.TableInfo(ctx, p.ID, ns[0].Label, name)
	if err != nil || info == nil || info.Database != ns[0].Label || info.Name != name || info.Kind != kind || info.Engine != "Solr" {
		t.Fatal("public original-label table information", err)
	}
	structure, err := ar.TableMetadata(ctx, p.ID, ns[0].Label, name)
	if err != nil || structure == nil || structure.Database != ns[0].Label || structure.Object != o || !structure.Editable {
		t.Fatal("public original-label data metadata", err)
	}
	created, err := ar.Saver(ctx, p.ID, ns[0].Label, name, []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: `{"id":"public-key","exact_l":9007199254740993,"text_s":"原生;公共工作台🐱"}`}}}})
	if err != nil || created == nil || !created.CommitConfirmed {
		t.Fatal("public native create", err)
	}
	options := contract.DataOptions{Object: o, Limit: 1, FilterColumn: "_key", FilterMode: "equal", FilterValue: "public-key"}
	page, err := ar.AdvancedLoader(ctx, p.ID, ns[0].Label, name, options)
	if err != nil || page == nil || len(page.Rows) != 1 || page.Editable == nil || !*page.Editable {
		t.Fatal("public native page", err)
	}
	foreign := options
	foreign.After = page.CurrentCursor
	if _, e := br.AdvancedLoader(ctx, p.ID, ns[0].Label, name, foreign); e == nil {
		t.Fatal("another document accepted original cursor")
	}
	row, err := ar.RowReader(ctx, p.ID, ns[0].Label, name, page.RowSnapshots[0].Ref)
	if err != nil || !strings.Contains(row.Values["document"].Text, "9007199254740993") {
		t.Fatal("public full row", err)
	}
	text := strings.Replace(row.Values["document"].Text, "9007199254740993", "9007199254740995", 1)
	saved, err := ar.Saver(ctx, p.ID, ns[0].Label, name, []contract.DataEdit{{Kind: "update", Original: row.Snapshot, Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: text}}}})
	if err != nil || saved == nil || !saved.CommitConfirmed {
		t.Fatal("public versioned update", err)
	}
	var output bytes.Buffer
	if err = br.JSONL(ctx, o, &output); err != nil || !strings.Contains(output.String(), "9007199254740995") {
		t.Fatal("public exact export", err)
	}
	page, err = ar.AdvancedLoader(ctx, p.ID, ns[0].Label, name, options)
	if err != nil || len(page.Rows) != 1 {
		t.Fatal("saved original page", err)
	}
	deleted, err := ar.Saver(ctx, p.ID, ns[0].Label, name, []contract.DataEdit{{Kind: "delete", Original: page.RowSnapshots[0]}})
	if err != nil || deleted == nil || !deleted.CommitConfirmed {
		t.Fatal("public versioned delete", err)
	}
	qp, err := contract.Require[contract.QueryProvider](first)
	if err != nil {
		t.Fatal(err)
	}
	q, err := qp.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Release()
	result, err := q.Execute(ctx, ns[0].Label, "GET /admin/info/system?wt=json")
	if err != nil || len(result.Rows) != 1 || result.Rows[0][0] != "200" || !strings.Contains(result.Rows[0][1], "9.9.0") || result.AffectedRows != 0 {
		t.Fatal("public native REST", err)
	}
	second, err := m.ConnectSession(ctx, p, secret)
	if err != nil || second.Generation() <= first.Generation() {
		t.Fatal("public generation replacement", err)
	}
	if _, e := ar.AdvancedLoader(ctx, p.ID, ns[0].Label, name, options); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("old data lease rebound", e)
	}
	if _, e := q.Execute(ctx, ns[0].Label, "GET /admin/info/system?wt=json"); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("old REST lease rebound", e)
	}
	if err = a.Close(); err != nil || !m.IsConnected(p.ID) {
		t.Fatal("old close affected replacement", err)
	}
	fresh, err := m.Data(p.ID).ReserveDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if _, err = fresh.Runtime().AdvancedLoader(ctx, p.ID, ns[0].Label, name, options); err != nil {
		t.Fatal("replacement refused original qualified target", err)
	}
}
