package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeConfig(t *testing.T) (connections.ElasticsearchConfig, string) {
	t.Helper()
	if os.Getenv("NYADB_ELASTICSEARCH_IT") != "1" {
		t.Skip("enable NYADB_ELASTICSEARCH_IT with an owned disposable fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_ELASTICSEARCH_CONFIG"))
	if err != nil {
		t.Fatal("private Elasticsearch probe cannot be read")
	}
	var probe struct {
		Endpoint     string `json:"endpoint"`
		Username     string `json:"username"`
		Auth         string `json:"auth"`
		PasswordFile string `json:"password_file"`
		CAFile       string `json:"ca_file"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		t.Fatal("invalid private Elasticsearch probe")
	}
	secret, err := os.ReadFile(probe.PasswordFile)
	if err != nil {
		t.Fatal("private Elasticsearch secret cannot be read")
	}
	return connections.ElasticsearchConfig{Endpoint: probe.Endpoint, Username: probe.Username, Auth: probe.Auth, CAFile: probe.CAFile}, string(secret)
}

func nativeOpen(t *testing.T, ctx context.Context) *Client {
	t.Helper()
	cfg, secret := nativeConfig(t)
	client, err := Open(ctx, cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func nativeRun(t *testing.T, c *Client, ctx context.Context, method, path string, body []byte) Response {
	t.Helper()
	result, err := c.Do(ctx, Request{Method: method, Path: path, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func nativeIndex(t *testing.T, c *Client, ctx context.Context) string {
	t.Helper()
	index := "nyadb_native_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	nativeRun(t, c, ctx, "PUT", "/"+index, []byte(`{"settings":{"number_of_shards":2,"number_of_replicas":0},"mappings":{"dynamic":false,"properties":{"n":{"type":"long"},"unsigned":{"type":"unsigned_long"},"decimal":{"type":"keyword"},"text":{"type":"keyword","ignore_above":128}}}}`))
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = c.Do(cleanup, Request{Method: "DELETE", Path: "/" + index})
	})
	return index
}

func TestElasticsearchNativeAuthenticationAndAPIKey(t *testing.T) {
	cfg, secret := nativeConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := nativeOpen(t, ctx)
	if c.Identity().Version.Number != "9.2.0" || c.Identity().Version.Flavor != "default" {
		t.Fatal("native kernel identity changed")
	}
	if cfg.Auth == "none" {
		return
	}
	if wrong, err := Open(ctx, cfg, "wrong fixture password"); err == nil {
		_ = wrong.Close()
		t.Fatal("wrong password authenticated")
	}
	bad := cfg
	bad.ServerName = "wrong-nyadb.invalid"
	if wrong, err := Open(ctx, bad, secret); err == nil {
		_ = wrong.Close()
		t.Fatal("wrong TLS server identity accepted")
	}
	bad = cfg
	bad.CAFile = ""
	if wrong, err := Open(ctx, bad, secret); err == nil {
		_ = wrong.Close()
		t.Fatal("untrusted private fixture CA accepted")
	}
	created := nativeRun(t, c, ctx, "POST", "/_security/api_key", []byte(`{"name":"nyadb-owned-native-check","expiration":"5m","role_descriptors":{"reader":{"cluster":["monitor"],"indices":[]}}}`))
	var key struct {
		ID      string `json:"id"`
		Encoded string `json:"encoded"`
	}
	if decodeJSON(created.Body, &key) != nil || key.ID == "" || key.Encoded == "" {
		t.Fatal("native API key receipt missing")
	}
	defer func() {
		body, _ := json.Marshal(map[string][]string{"ids": {key.ID}})
		_, _ = c.Do(context.Background(), Request{Method: "DELETE", Path: "/_security/api_key", Body: body})
	}()
	apiConfig := cfg
	apiConfig.Username, apiConfig.Auth = "", "api_key"
	api, err := Open(ctx, apiConfig, key.Encoded)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	if api.Identity().ClusterUUID != c.Identity().ClusterUUID {
		t.Fatal("API key reached another cluster")
	}
	_, err = api.Do(ctx, Request{Method: "PUT", Path: "/nyadb_forbidden_" + strconv.FormatInt(time.Now().UnixNano(), 36), Body: []byte(`{}`)})
	var native *HTTPError
	if !errors.As(err, &native) || native.Status != 403 {
		t.Fatal("limited key unexpectedly allowed index creation", err)
	}
}

func TestElasticsearchNativeExactDocumentsRoutingAndOptimisticConcurrency(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := nativeOpen(t, ctx)
	index := nativeIndex(t, c, ctx)
	// Resolve two actual shards rather than assume arbitrary routing strings
	// hash to different places. Equal IDs then represent distinct documents.
	routes := map[int]string{}
	for i := 0; len(routes) < 2 && i < 50; i++ {
		routing := "tenant-" + strconv.Itoa(i)
		r := nativeRun(t, c, ctx, "GET", "/"+index+"/_search_shards?routing="+routing, nil)
		var shards struct {
			Shards [][]struct {
				Shard int `json:"shard"`
			} `json:"shards"`
		}
		if decodeJSON(r.Body, &shards) != nil || len(shards.Shards) != 1 || len(shards.Shards[0]) != 1 {
			t.Fatal("native routing shard response missing")
		}
		routes[shards.Shards[0][0].Shard] = routing
	}
	if len(routes) != 2 {
		t.Fatal("could not resolve two native routing shards")
	}
	id := DocumentID{Index: index, ID: "native/精确 & one", Routing: routes[0]}
	other := id
	other.Routing = routes[1]
	source := json.RawMessage(`{"n":9007199254740993,"unsigned":18446744073709551615,"decimal":12345678901234567890.123456789,"nested":[null,"",false,{"x":"雪"}],"text":"` + strings.Repeat("长", 3000) + `"}`)
	created, err := c.CreateDocument(ctx, id, source)
	if err != nil || created.Result != "created" || created.Snapshot.Sequence < 0 || created.Snapshot.PrimaryTerm <= 0 {
		t.Fatal("native create receipt incomplete", err)
	}
	if _, err = c.CreateDocument(ctx, other, json.RawMessage(`{"n":22}`)); err != nil {
		t.Fatal("routing identities collapsed", err)
	}
	original, err := c.GetDocument(ctx, id)
	if err != nil || !bytes.Equal(original.Source, source) || original.Identity.Routing != id.Routing {
		t.Fatal("native source/routing precision changed", err)
	}
	if _, err = c.CreateDocument(ctx, id, json.RawMessage(`{"n":99}`)); err == nil {
		t.Fatal("create overwrote existing ID")
	}
	updated, err := c.ReplaceDocument(ctx, original, json.RawMessage(`{"n":123,"text":"changed"}`))
	if err != nil || updated.Snapshot.Sequence <= original.Sequence {
		t.Fatal("native conditional replacement failed", err)
	}
	for _, operation := range []func() error{
		func() error { _, e := c.ReplaceDocument(ctx, original, json.RawMessage(`{"n":999}`)); return e },
		func() error { _, e := c.DeleteDocument(ctx, original); return e },
	} {
		var conflict *HTTPError
		if e := operation(); !errors.As(e, &conflict) || conflict.Status != 409 {
			t.Fatal("stale snapshot was applied", e)
		}
	}
	untouched, err := c.GetDocument(ctx, other)
	if err != nil || string(untouched.Source) != `{"n":22}` {
		t.Fatal("other routing document changed", err)
	}
	if _, err = c.DeleteDocument(ctx, updated.Snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestElasticsearchNativePITPagingPreservesSnapshotAndClosesOriginalResources(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin := nativeOpen(t, ctx)
	index := nativeIndex(t, admin, ctx)
	for i := 0; i < 16; i++ {
		id := DocumentID{Index: index, ID: strconv.Itoa(i), Routing: "route-" + strconv.Itoa(i%3)}
		source := json.RawMessage(`{"n":` + strconv.Itoa(i) + `}`)
		if _, err := admin.CreateDocument(ctx, id, source); err != nil {
			t.Fatal(err)
		}
	}
	nativeRun(t, admin, ctx, "POST", "/"+index+"/_refresh", nil)
	reader := nativeOpen(t, ctx)
	cursor, err := reader.OpenCursor(ctx, index, json.RawMessage(`{"match_all":{}}`), 3)
	if err != nil {
		t.Fatal(err)
	}
	defer cursor.Close(context.Background())
	seen := map[string]bool{}
	page, err := cursor.Next(ctx)
	if err != nil || page.Total != 16 || len(page.Documents) != 3 || !page.HasMore {
		t.Fatal("native first PIT page failed", err)
	}
	removed := page.Documents[0]
	if removed.Identity.Routing == "" {
		t.Fatal("search lost actual routing metadata")
	}
	if _, err = admin.DeleteDocument(ctx, removed); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.CreateDocument(ctx, DocumentID{Index: index, ID: "late"}, json.RawMessage(`{"n":99}`)); err != nil {
		t.Fatal(err)
	}
	nativeRun(t, admin, ctx, "POST", "/"+index+"/_refresh", nil)
	for {
		for _, document := range page.Documents {
			if seen[document.Identity.ID] || document.Identity.ID == "late" || document.Identity.Routing == "" {
				t.Fatal("PIT changed snapshot, duplicated row or lost routing")
			}
			seen[document.Identity.ID] = true
		}
		if !page.HasMore {
			break
		}
		page, err = cursor.Next(ctx)
		if err != nil || page.Total != 16 {
			t.Fatal("native continuation changed snapshot", err)
		}
	}
	if len(seen) != 16 {
		t.Fatal("native PIT missed original documents")
	}
	if err = cursor.Close(ctx); err != nil {
		t.Fatal("PIT deletion was not acknowledged", err)
	}
	parked, err := reader.OpenCursor(ctx, index, json.RawMessage(`{"match_all":{}}`), 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = parked.Next(ctx); err != nil {
		t.Fatal(err)
	}
	if err = reader.Close(); err != nil {
		t.Fatal("original client did not free PIT", err)
	}
	if _, err = parked.Next(ctx); !errors.Is(err, ErrRetired) {
		t.Fatal("old cursor implicitly recovered", err)
	}
	r := nativeRun(t, admin, ctx, "GET", "/"+index+"/_stats/search?filter_path=indices.*.total.search.open_contexts", nil)
	var stats struct {
		Indices map[string]struct {
			Total struct {
				Search struct {
					Open *int `json:"open_contexts"`
				} `json:"search"`
			} `json:"total"`
		} `json:"indices"`
	}
	if decodeJSON(r.Body, &stats) != nil || stats.Indices[index].Total.Search.Open == nil || *stats.Indices[index].Total.Search.Open != 0 {
		t.Fatal("native PIT resources remain after Close")
	}
}

func TestElasticsearchNativeCommittedWriteLostReplyNeverReplays(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	origin := nativeOpen(t, ctx)
	index := nativeIndex(t, origin, ctx)
	base, _ := url.Parse(origin.config.Endpoint)
	transport := origin.http.Transport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	upstream := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var calls atomic.Int32
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		u := *request.URL
		u.Scheme, u.Host = base.Scheme, base.Host
		forward, err := http.NewRequestWithContext(request.Context(), request.Method, u.String(), request.Body)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		forward.Header = request.Header.Clone()
		response, err := upstream.Do(forward)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		raw, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			w.WriteHeader(502)
			return
		}
		if request.Method == "PUT" && strings.HasSuffix(request.URL.Path, "/_create/lost") {
			calls.Add(1)
			if response.StatusCode != 201 {
				t.Error("native proxy write did not actually commit")
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		for key, values := range response.Header {
			w.Header()[key] = values
		}
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(raw)
	}))
	defer proxy.Close()
	ca := filepath.Join(t.TempDir(), "proxy-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: proxy.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := origin.config
	cfg.Endpoint, cfg.CAFile, cfg.ServerName = proxy.URL, ca, ""
	client, err := Open(ctx, cfg, origin.secret)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	id := DocumentID{Index: index, ID: "lost"}
	_, err = client.CreateDocument(ctx, id, json.RawMessage(`{"n":9007199254740993}`))
	var interrupted *Interrupted
	if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown || calls.Load() != 1 {
		t.Fatal("native uncertain write replayed or falsely acknowledged", err)
	}
	if _, err = client.GetDocument(ctx, id); !errors.Is(err, ErrRetired) {
		t.Fatal("retired connection automatically recovered")
	}
	verified, err := origin.GetDocument(ctx, id)
	if err != nil || string(verified.Source) != `{"n":9007199254740993}` || calls.Load() != 1 {
		t.Fatal("original committed write not found by independent read", err)
	}
}

func TestElasticsearchNativeRESTJSONAndNDJSONKeepPerItemOutcomes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := nativeOpen(t, ctx)
	index := nativeIndex(t, c, ctx)
	source := "POST /" + index + "/_bulk?refresh=wait_for\n{\"create\":{\"_id\":\"first\"}}\n{\"n\":9007199254740993}\n{\"create\":{\"_id\":\"first\"}}\n{\"n\":2}\n"
	request, err := ParseRequest(source)
	if err != nil || request.ContentType != "application/x-ndjson" {
		t.Fatal("native NDJSON request rejected", err)
	}
	response, err := c.Do(ctx, request)
	if err != nil || response.Status != 200 {
		t.Fatal("native bulk response lost", err)
	}
	var bulk struct {
		Errors *bool `json:"errors"`
		Items  []struct {
			Create struct {
				Status int `json:"status"`
			} `json:"create"`
		} `json:"items"`
	}
	if decodeJSON(response.Body, &bulk) != nil || bulk.Errors == nil || !*bulk.Errors || len(bulk.Items) != 2 || bulk.Items[0].Create.Status != 201 || bulk.Items[1].Create.Status != 409 {
		t.Fatal("partial native bulk outcome became atomic success")
	}
	request, err = ParseRequest("GET /" + index + "/_search\n{\"query\":{\"term\":{\"n\":9007199254740993}}}")
	if err != nil {
		t.Fatal(err)
	}
	response, err = c.Do(ctx, request)
	if err != nil || !strings.Contains(string(response.Body), `"n":9007199254740993`) || strings.Contains(string(response.Body), `"n":2`) {
		t.Fatal("native query literal or document response lost precision", err)
	}
	metadata := nativeRun(t, c, ctx, "GET", "/"+index+"/_mapping", nil)
	if !strings.Contains(string(metadata.Body), `"type":"long"`) || !strings.Contains(string(metadata.Body), `"dynamic":"false"`) {
		t.Fatal("native mapping response lost")
	}
}
