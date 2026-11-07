package solr

import (
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

func nativeFixture(t *testing.T) (connections.SolrConfig, string) {
	t.Helper()
	if os.Getenv("NYADB_SOLR_IT") != "1" {
		t.Skip("requires owned native Solr fixture")
	}
	raw, e := os.ReadFile(os.Getenv("NYADB_SOLR_CONFIG"))
	if e != nil {
		t.Fatal("private probe unavailable")
	}
	var probe struct {
		Endpoint, Auth, Username string
		CAFile                   string `json:"ca_file"`
		ServerName               string `json:"server_name"`
		PasswordFile             string `json:"password_file"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		t.Fatal("invalid private probe")
	}
	secret, e := os.ReadFile(probe.PasswordFile)
	if e != nil {
		t.Fatal("private password unavailable")
	}
	return connections.SolrConfig{Endpoint: probe.Endpoint, Auth: probe.Auth, Username: probe.Username, CAFile: probe.CAFile, ServerName: probe.ServerName}, string(secret)
}
func nativeOpen(t *testing.T, ctx context.Context) *Client {
	t.Helper()
	cfg, secret := nativeFixture(t)
	c, e := Open(ctx, cfg, secret)
	if e != nil {
		t.Fatal("native Solr open", e)
	}
	t.Cleanup(c.Close)
	return c
}
func nativeRun(t *testing.T, c *Client, ctx context.Context, r Request) Response {
	t.Helper()
	v, e := c.Do(ctx, r)
	if e != nil {
		t.Fatal("native request failed", e)
	}
	return v
}
func nativeDocument(t *testing.T, c *Client, ctx context.Context, id string) map[string]json.RawMessage {
	t.Helper()
	v := nativeRun(t, c, ctx, Request{Method: "GET", Path: "/nyadb_fixture/get?id=" + url.QueryEscape(id) + "&wt=json"})
	var result struct {
		Doc map[string]json.RawMessage `json:"doc"`
	}
	if decodeJSON(v.Body, &result) != nil || result.Doc == nil {
		t.Fatal("native document receipt")
	}
	return result.Doc
}
func nativeRelay(t *testing.T, admin *Client, after func(http.ResponseWriter, *http.Request, *Response) bool) *Client {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, e := io.ReadAll(io.LimitReader(r.Body, requestLimit+1))
		if e != nil {
			w.WriteHeader(502)
			return
		}
		path := strings.TrimPrefix(r.URL.RequestURI(), "/proxy/solr")
		response, e := admin.Do(r.Context(), Request{Method: r.Method, Path: path, ContentType: r.Header.Get("Content-Type"), Body: body})
		if e != nil {
			w.WriteHeader(502)
			return
		}
		if after(w, r, &response) {
			return
		}
		w.Header().Set("Content-Type", response.ContentType)
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
	}))
	t.Cleanup(server.Close)
	ca := filepath.Join(t.TempDir(), "relay-ca.pem")
	if e := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); e != nil {
		t.Fatal(e)
	}
	c, e := Open(context.Background(), connections.SolrConfig{Endpoint: server.URL + "/proxy/solr", CAFile: ca, ServerName: "example.com"}, "")
	if e != nil {
		t.Fatal("verified relay context", e)
	}
	t.Cleanup(c.Close)
	return c
}
func nativeID() string { return "owned_" + strconv.FormatInt(time.Now().UnixNano(), 36) }
func TestSolrNativeTransportAuthenticationIdentityAndExactDocuments(t *testing.T) {
	cfg, secret := nativeFixture(t)
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	c := nativeOpen(t, ctx)
	if v := c.Identity(); v.Mode != "std" || v.Lucene.SolrVersion != "9.9.0" {
		t.Fatal("native product identity")
	}
	if cfg.Auth == "basic" {
		for name, change := range map[string]func(*connections.SolrConfig, *string){"wrong_password": func(_ *connections.SolrConfig, s *string) { *s = "wrong-password" }, "wrong_server_name": func(c *connections.SolrConfig, _ *string) { c.ServerName = "wrong.invalid" }, "missing_CA": func(c *connections.SolrConfig, _ *string) { c.CAFile = "" }} {
			t.Run(name, func(t *testing.T) {
				test, pass := cfg, secret
				change(&test, &pass)
				bad, e := Open(ctx, test, pass)
				if e == nil {
					bad.Close()
					t.Fatal("wrong native authentication/TLS accepted")
				}
				if strings.Contains(e.Error(), secret) {
					t.Fatal("password exposed in diagnostic")
				}
			})
		}
	}
	status := nativeRun(t, c, ctx, Request{Method: "GET", Path: "/admin/cores?action=STATUS&wt=json"})
	var cores struct {
		Status map[string]json.RawMessage `json:"status"`
	}
	if decodeJSON(status.Body, &cores) != nil || cores.Status["nyadb_fixture"] == nil {
		t.Fatal("native core enumeration")
	}
	schema := nativeRun(t, c, ctx, Request{Method: "GET", Path: "/nyadb_fixture/schema/uniquekey?wt=json"})
	var key struct {
		UniqueKey string `json:"uniqueKey"`
	}
	if decodeJSON(schema.Body, &key) != nil || key.UniqueKey != "id" {
		t.Fatal("native unique key")
	}
	id := nativeID()
	body := `[{"id":"` + id + `","exact_l":9007199254740993,"text_s":"原生 Solr🐱"}]`
	response := nativeRun(t, c, ctx, Request{Method: "POST", Path: "/nyadb_fixture/update?commit=true&wt=json", Body: []byte(body)})
	var receipt struct {
		Header struct {
			Status int `json:"status"`
		} `json:"responseHeader"`
	}
	if decodeJSON(response.Body, &receipt) != nil || receipt.Header.Status != 0 {
		t.Fatal("native update receipt")
	}
	doc := nativeDocument(t, c, ctx, id)
	var text string
	if string(doc["exact_l"]) != "9007199254740993" || json.Unmarshal(doc["text_s"], &text) != nil || text != "原生 Solr🐱" {
		t.Fatal("native JSON lost precision or Unicode")
	}
	var version int64
	if json.Unmarshal(doc["_version_"], &version) != nil || version <= 0 {
		t.Fatal("native optimistic version")
	}
	nativeRun(t, c, ctx, Request{Method: "POST", Path: "/nyadb_fixture/update?commit=true&wt=json", Body: []byte(`[{"id":"` + id + `","_version_":` + strconv.FormatInt(version, 10) + `,"exact_l":9007199254740995}]`)})
	latest := nativeDocument(t, c, ctx, id)
	if string(latest["exact_l"]) != "9007199254740995" {
		t.Fatal("native version update not applied")
	}
	_, e := c.Do(ctx, Request{Method: "POST", Path: "/nyadb_fixture/update?commit=true&wt=json", Body: []byte(`[{"id":"` + id + `","_version_":` + strconv.FormatInt(version, 10) + `,"exact_l":1}]`)})
	var nativeError *HTTPError
	var lost *Interrupted
	if !errors.As(e, &nativeError) || nativeError.Status != 409 || !errors.As(e, &lost) || !lost.WriteOutcomeUnknown {
		t.Fatal("native 409 did not retain generic unknown batch outcome", e)
	}
	fresh := nativeOpen(t, ctx)
	if string(nativeDocument(t, fresh, ctx, id)["exact_l"]) != "9007199254740995" {
		t.Fatal("stale version changed original document")
	}
}
func TestSolrNativeLostPostAndGETCoreMutationsNeverReplay(t *testing.T) {
	for _, method := range []string{"POST", "GET"} {
		t.Run(method, func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
			defer stop()
			admin := nativeOpen(t, ctx)
			id := nativeID()
			core := nativeID()
			var writes atomic.Int32
			c := nativeRelay(t, admin, func(w http.ResponseWriter, r *http.Request, _ *Response) bool {
				match := r.Method == method && ((method == "POST" && strings.HasSuffix(r.URL.Path, "/update")) || (method == "GET" && r.URL.Query().Get("action") == "CREATE"))
				if !match {
					return false
				}
				writes.Add(1)
				conn, _, e := w.(http.Hijacker).Hijack()
				if e != nil {
					t.Error(e)
					return true
				}
				_ = conn.Close()
				return true
			})
			request := Request{Method: "POST", Path: "/nyadb_fixture/update?commit=true&wt=json", Body: []byte(`[{"id":"` + id + `","exact_l":9007199254740993}]`)}
			if method == "GET" {
				request = Request{Method: "GET", Path: "/admin/cores?action=CREATE&name=" + core + "&configSet=_default&wt=json"}
			}
			_, e := c.Do(ctx, request)
			var lost *Interrupted
			if !errors.As(e, &lost) || !lost.WriteOutcomeUnknown || writes.Load() != 1 {
				t.Fatal("lost native write not retained", e, writes.Load())
			}
			if _, e = c.Do(ctx, request); !errors.Is(e, ErrRetired) || writes.Load() != 1 {
				t.Fatal("lost native write replayed")
			}
			fresh := nativeOpen(t, ctx)
			if writes.Load() != 1 {
				t.Fatal("explicit open replayed lost request")
			}
			if method == "POST" {
				if string(nativeDocument(t, fresh, ctx, id)["exact_l"]) != "9007199254740993" {
					t.Fatal("native lost write not actually committed")
				}
			} else {
				response := nativeRun(t, fresh, ctx, Request{Method: "GET", Path: "/admin/cores?action=STATUS&core=" + core + "&wt=json"})
				var v struct {
					Status map[string]json.RawMessage `json:"status"`
				}
				if decodeJSON(response.Body, &v) != nil || v.Status[core] == nil {
					t.Fatal("native lost GET did not create core")
				}
				nativeRun(t, fresh, ctx, Request{Method: "GET", Path: "/admin/cores?action=UNLOAD&core=" + core + "&deleteInstanceDir=true&deleteDataDir=true&wt=json"})
			}
			if writes.Load() != 1 {
				t.Fatal("explicit verification submitted another write")
			}
		})
	}
}
func TestSolrNativeCancelSettlesOriginalWorkerAndKeepsOtherClient(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	admin := nativeOpen(t, ctx)
	other := nativeOpen(t, ctx)
	id := nativeID()
	committed, acknowledged := make(chan struct{}), make(chan struct{})
	var writes atomic.Int32
	c := nativeRelay(t, admin, func(_ http.ResponseWriter, r *http.Request, _ *Response) bool {
		if r.Method != "POST" {
			return false
		}
		writes.Add(1)
		close(committed)
		<-r.Context().Done()
		close(acknowledged)
		return true
	})
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, e := c.Do(requestCtx, Request{Method: "POST", Path: "/nyadb_fixture/update?commit=true&wt=json", Body: []byte(`[{"id":"` + id + `","exact_l":9007199254740993}]`)})
		done <- e
	}()
	select {
	case <-committed:
	case <-time.After(15 * time.Second):
		t.Fatal("native write not acknowledged")
	}
	cancel()
	select {
	case e := <-done:
		var lost *Interrupted
		if !errors.Is(e, context.Canceled) || !errors.As(e, &lost) || !lost.WriteOutcomeUnknown {
			t.Fatal("cancel discarded unknown native write", e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("original HTTP worker did not settle")
	}
	select {
	case <-acknowledged:
	case <-time.After(10 * time.Second):
		t.Fatal("relay cancellation not acknowledged")
	}
	c.Close()
	if string(nativeDocument(t, other, ctx, id)["exact_l"]) != "9007199254740993" || writes.Load() != 1 {
		t.Fatal("other client lost native committed write or replayed it")
	}
}
func TestSolrNativeConflictCanApplyPrefixWithoutReplay(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	c := nativeOpen(t, ctx)
	reader := nativeOpen(t, ctx)
	a, b := nativeID(), nativeID()
	nativeRun(t, c, ctx, Request{Method: "POST", Path: "/nyadb_fixture/update?commit=true&wt=json", Body: []byte(`[{"id":"` + a + `","exact_l":1},{"id":"` + b + `","exact_l":2}]`)})
	bv := nativeDocument(t, reader, ctx, b)["_version_"]
	var version int64
	if json.Unmarshal(bv, &version) != nil {
		t.Fatal("native conflict version")
	}
	_, e := c.Do(ctx, Request{Method: "POST", Path: "/nyadb_fixture/update?commit=true&wt=json", Body: []byte(`[{"id":"` + a + `","exact_l":9007199254740993},{"id":"` + b + `","_version_":` + strconv.FormatInt(version+1, 10) + `,"exact_l":3}]`)})
	var nativeError *HTTPError
	var lost *Interrupted
	if !errors.As(e, &nativeError) || nativeError.Status != 409 || !errors.As(e, &lost) || !lost.WriteOutcomeUnknown {
		t.Fatal("native batch conflict was misreported", e)
	}
	if string(nativeDocument(t, reader, ctx, a)["exact_l"]) != "9007199254740993" || string(nativeDocument(t, reader, ctx, b)["exact_l"]) != "2" {
		t.Fatal("native prefix/conflict semantics differ from retained unknown outcome")
	}
	if _, e = c.Do(ctx, Request{Method: "POST", Path: "/nyadb_fixture/update", Body: []byte(`[]`)}); !errors.Is(e, ErrRetired) {
		t.Fatal("unknown partial native batch allowed more writes")
	}
}
