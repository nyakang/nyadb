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
	"github.com/nyakang/nyadb/internal/services/elasticsearch"
)

func elasticsearchFixtureProfile(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_ELASTICSEARCH_IT") != "1" {
		t.Skip("requires owned disposable Elasticsearch")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_ELASTICSEARCH_CONFIG"))
	if err != nil {
		t.Fatal("cannot read private probe")
	}
	var c struct {
		Endpoint, Username, Auth string
		PasswordFile             string `json:"password_file"`
		CAFile                   string `json:"ca_file"`
	}
	if json.Unmarshal(raw, &c) != nil {
		t.Fatal("invalid private probe")
	}
	secret, err := os.ReadFile(c.PasswordFile)
	if err != nil {
		t.Fatal("cannot read private secret")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		t.Fatal("invalid probe endpoint")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal("invalid probe port")
	}
	p, _ := connections.Defaults(connections.Elasticsearch)
	p.ID = 92
	p.Name = "Native Elasticsearch runtime"
	p.Host = u.Hostname()
	p.Port = uint16(port)
	p.Username = c.Username
	p.ElasticsearchOptions.Auth = c.Auth
	p.ElasticsearchOptions.CAFile = c.CAFile
	if u.Scheme == "http" {
		p.TLSMode = "disable"
	}
	return p, string(secret)
}
func TestElasticsearchNativePublicDocumentFeaturesAndPublishedGenerations(t *testing.T) {
	p, secret := elasticsearchFixtureProfile(t)
	ctx, stop := context.WithTimeout(context.Background(), 120*time.Second)
	defer stop()
	cfg, err := connections.ElasticsearchProfileConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := elasticsearch.Open(ctx, cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	index := "nyadb_public_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	run := func(method, path, body string) elasticsearch.Response {
		t.Helper()
		r, e := admin.Do(ctx, elasticsearch.Request{Method: method, Path: path, Body: []byte(body)})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	run("PUT", "/"+index, `{"settings":{"number_of_replicas":0},"mappings":{"dynamic":false}}`)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, e := admin.Do(cleanup, elasticsearch.Request{Method: "DELETE", Path: "/" + index}); e != nil {
			t.Error("owned index cleanup", e)
		}
	}()
	type credentials struct{ auth, username, secret string }
	cases := []credentials{{p.ElasticsearchOptions.Auth, p.Username, secret}}
	if cfg.Auth == "basic" {
		body, _ := json.Marshal(map[string]any{"name": "nyadb-public-binding", "expiration": "5m", "role_descriptors": map[string]any{"fixture": map[string]any{"cluster": []string{"monitor"}, "indices": []any{map[string]any{"names": []string{index}, "privileges": []string{"all"}}}}}})
		reply := run("POST", "/_security/api_key", string(body))
		var key struct{ ID, Encoded string }
		if json.Unmarshal(reply.Body, &key) != nil || key.Encoded == "" {
			t.Fatal("missing API key receipt")
		}
		cases = append(cases, credentials{"api_key", "", key.Encoded})
		defer func() {
			body, _ := json.Marshal(map[string][]string{"ids": {key.ID}})
			run("DELETE", "/_security/api_key", string(body))
		}()
	}
	for _, tc := range cases {
		t.Run(tc.auth, func(t *testing.T) {
			current := p
			current.Username = tc.username
			current.ElasticsearchOptions.Auth = tc.auth
			m := New()
			RegisterBuiltins(m, nil)
			defer m.Close()
			first, err := m.ConnectSession(ctx, current, tc.secret)
			if err != nil {
				t.Fatal(err)
			}
			c := first.Capabilities()
			if !c.Documents || !c.Query || !c.EditableTables || !c.Export || !c.Count || !c.FieldChunks || !c.Keyset || c.Transactions || c.Import || c.Explain || c.DesignTables {
				t.Fatal("public capabilities", c)
			}
			meta, err := contract.Require[contract.MetadataProvider](first)
			if err != nil {
				t.Fatal(err)
			}
			ns, err := meta.Namespaces(ctx)
			if err != nil || len(ns) != 1 || ns[0].Ref.Catalog != admin.Identity().ClusterUUID || ns[0].Ref.Schema != "" {
				t.Fatal("original cluster namespace", err)
			}
			o := ns[0].Ref.Object("index", index)
			objs, err := meta.Browse(ctx, ns[0].Ref)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, v := range objs {
				if v.Ref == o && v.Kind == "索引" {
					found = true
				}
			}
			if !found {
				t.Fatal("qualified native index missing")
			}
			r := m.Data(p.ID)
			if r.ReserveDocument == nil || r.AdvancedLoader != nil || r.Saver != nil {
				t.Fatal("unowned parent data executor")
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
			if !ar.Capabilities.EditableTables || ar.Saver == nil || ar.Verify == nil || ar.JSONL == nil || ar.Count == nil || ar.Chunk == nil {
				t.Fatal("bound operations disabled")
			}
			id := "public-" + tc.auth
			created, err := ar.Saver(ctx, p.ID, ns[0].Label, index, []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"_id": {Text: id}, "document": {Kind: contract.JSON, Text: `{"exact":9007199254740993,"name":"公共工作台🐱"}`}}}})
			if err != nil || created == nil || !created.CommitConfirmed || !created.RefreshRequired {
				t.Fatal("public bound create", err)
			}
			options := contract.DataOptions{Object: o, Limit: 1, FilterColumn: "_id", FilterMode: "equal", FilterValue: id}
			page, err := ar.AdvancedLoader(ctx, p.ID, ns[0].Label, index, options)
			if err != nil || page == nil || len(page.Rows) != 1 || page.CurrentCursor == nil {
				t.Fatal("public PIT page", err)
			}
			foreign := options
			foreign.After = page.CurrentCursor
			if _, err = br.AdvancedLoader(ctx, p.ID, ns[0].Label, index, foreign); err == nil {
				t.Fatal("another document accepted original PIT boundary")
			}
			row, err := ar.RowReader(ctx, p.ID, ns[0].Label, index, *page.RowRefs[0])
			if err != nil || !strings.Contains(row.Values["document"].Text, "9007199254740993") {
				t.Fatal("exact full source", err)
			}
			edits := []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: `{"exact":9007199254740995,"name":"saved"}`}}}}
			saved, err := ar.Saver(ctx, p.ID, ns[0].Label, index, edits)
			if err != nil || !saved.CommitConfirmed || !saved.RefreshRequired {
				t.Fatal("public original version update", err)
			}
			var output bytes.Buffer
			if err = br.JSONL(ctx, o, &output); err != nil || !strings.Contains(output.String(), "9007199254740995") {
				t.Fatal("public exact JSONL export", err)
			}
			page, err = ar.AdvancedLoader(ctx, p.ID, ns[0].Label, index, options)
			if err != nil || len(page.Rows) != 1 {
				t.Fatal("new PIT after save", err)
			}
			deleted, err := ar.Saver(ctx, p.ID, ns[0].Label, index, []contract.DataEdit{{Kind: "delete", Original: page.RowSnapshots[0]}})
			if err != nil || !deleted.CommitConfirmed {
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
			result, err := q.Execute(ctx, ns[0].Label, "GET /")
			if err != nil || result.Rows[0][0] != "200" || !strings.Contains(result.Rows[0][1], admin.Identity().ClusterUUID) || result.AffectedRows != 0 {
				t.Fatal("public native REST result", err)
			}
			second, err := m.ConnectSession(ctx, current, tc.secret)
			if err != nil || second.Generation() <= first.Generation() {
				t.Fatal("published replacement", err)
			}
			if _, err = ar.AdvancedLoader(ctx, p.ID, ns[0].Label, index, options); !errors.Is(err, contract.ErrRevoked) {
				t.Fatal("old document rebound", err)
			}
			if _, err = q.Execute(ctx, ns[0].Label, "GET /"); !errors.Is(err, contract.ErrRevoked) {
				t.Fatal("old REST lease rebound", err)
			}
			if err = a.Close(); err != nil || !m.IsConnected(p.ID) {
				t.Fatal("old close affected replacement", err)
			}
		})
	}
}
