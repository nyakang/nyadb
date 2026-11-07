package solr

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

const testIdentity = `{"mode":"std","solr_home":"/owned/solr","lucene":{"solr-spec-version":"9.9.0","solr-impl-version":"9.9.0 386ed096a1946c488cfe576a19a147bdb1153508 - native","lucene-spec-version":"9.12.2"}}`

func tlsConfig(t *testing.T, server *httptest.Server) connections.SolrConfig {
	t.Helper()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if e := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); e != nil {
		t.Fatal(e)
	}
	return connections.SolrConfig{Endpoint: server.URL + "/solr", CAFile: ca, ServerName: "example.com"}
}
func TestSolrRequestKeepsExactJSONAndRejectsAmbiguity(t *testing.T) {
	source := "# original request\nPOST /owned/update?commit=true\n[{\"id\":\"a🐱\",\"exact_l\":9007199254740993,\"decimal\":1.123456789012345678901}]\n"
	r, e := ParseRequest(source)
	if e != nil || !strings.Contains(string(r.Body), "9007199254740993") || r.Body[len(r.Body)-1] != '\n' {
		t.Fatal("exact request changed", e)
	}
	var raw json.RawMessage
	if e = decodeJSON(r.Body, &raw); e != nil || !bytes.Equal(bytes.TrimSpace(r.Body), raw) {
		t.Fatal("raw values rounded or normalized", e)
	}
	var generic map[string]any
	if e = decodeJSON([]byte(`{"large":9007199254740993,"nested":[1.123456789012345678901]}`), &generic); e != nil {
		t.Fatal(e)
	}
	if generic["large"].(json.Number).String() != "9007199254740993" || generic["nested"].([]any)[0].(json.Number).String() != "1.123456789012345678901" {
		t.Fatal("generic native JSON was rounded to float64")
	}
	for _, v := range []string{"GET https://other.invalid/", "GET //other.invalid/", "GET /%2e%2e/private", "GET /a%00", "GET /a%252e%252e", "GET /\nPOST /other", "POST /owned/update\n{\"id\":1,\"id\":2}", "POST /owned/update\n{}\nDELETE /original", "POST /owned/update\n" + strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66)} {
		if _, e = ParseRequest(v); e == nil {
			t.Fatal("ambiguous native request accepted")
		}
	}
	for _, v := range []string{`{"mode":"std","mode":"solrcloud"}`, `{"n":1} {"n":2}`, string([]byte{0xff})} {
		if decodeJSON([]byte(v), &raw) == nil {
			t.Fatal("ambiguous response accepted")
		}
	}
}
func TestSolrGETMutationsAndUnknownHandlersAreConservative(t *testing.T) {
	for _, path := range []string{"/admin/cores?action=CREATE&name=owned", "/admin/cores?action=UNLOAD", "/admin/cores?action=STATUS&action=UNLOAD", "/owned/replication?command=fetchindex", "/admin/info/logging?set=root:DEBUG", "/admin/info/system?unexpected=1", "/owned/select?qt=/update", "/owned/custom"} {
		if readOnly(Request{Method: "GET", Path: path}) {
			t.Fatal("GET mutation certified as read-only", path)
		}
	}
	for _, path := range []string{"/admin/info/system?wt=json", "/admin/cores?action=STATUS&wt=json&core=owned"} {
		if !readOnly(Request{Method: "GET", Path: path}) {
			t.Fatal("fixed metadata read treated as write", path)
		}
	}
}
func TestSolrTLSAuthenticationContextAndNoRedirectOrRetry(t *testing.T) {
	const secret = "private-password"
	var writes, leaked atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer sink.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "native-user" || password != secret {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/gateway/solr/admin/info/system" {
			_, _ = w.Write([]byte(testIdentity))
			return
		}
		if r.URL.Path == "/gateway/solr/admin/cores" && r.URL.Query().Get("action") == "CREATE" {
			writes.Add(1)
			conn, _, e := w.(http.Hijacker).Hijack()
			if e != nil {
				t.Error(e)
				return
			}
			_ = conn.Close()
			return
		}
		http.Redirect(w, r, sink.URL+"/credentials", 302)
	}))
	defer server.Close()
	cfg := tlsConfig(t, server)
	cfg.Endpoint = server.URL + "/gateway/solr"
	cfg.Auth = "basic"
	cfg.Username = "native-user"
	c, e := Open(context.Background(), cfg, secret)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	response, e := c.Do(context.Background(), Request{Method: "GET", Path: "/redirect"})
	var lost *Interrupted
	if response.Status != 302 || !errors.As(e, &lost) || !lost.WriteOutcomeUnknown || leaked.Load() != 0 {
		t.Fatal("redirect followed or unknown operation certified")
	}
	if _, e = c.Do(context.Background(), Request{Method: "GET", Path: "/admin/cores?action=CREATE&name=owned"}); !errors.Is(e, ErrRetired) || writes.Load() != 0 {
		t.Fatal("retired request replayed")
	}
	fresh, e := Open(context.Background(), cfg, secret)
	if e != nil {
		t.Fatal(e)
	}
	defer fresh.Close()
	_, e = fresh.Do(context.Background(), Request{Method: "GET", Path: "/admin/cores?action=CREATE&name=owned"})
	if !errors.As(e, &lost) || !lost.WriteOutcomeUnknown || writes.Load() != 1 || strings.Contains(e.Error(), secret) {
		t.Fatal("GET mutation replayed or secret leaked")
	}
	if _, e = fresh.Do(context.Background(), Request{Method: "GET", Path: "/admin/cores?action=CREATE&name=owned"}); !errors.Is(e, ErrRetired) || writes.Load() != 1 {
		t.Fatal("lost GET write replayed")
	}
	for _, change := range []func(*connections.SolrConfig){func(c *connections.SolrConfig) { c.ServerName = "wrong.invalid" }, func(c *connections.SolrConfig) { c.CAFile = "" }} {
		candidate := cfg
		change(&candidate)
		bad, e := Open(context.Background(), candidate, secret)
		if e == nil {
			bad.Close()
			t.Fatal("bad TLS accepted")
		}
	}
	if bad, e := Open(context.Background(), cfg, "wrong-password"); e == nil {
		bad.Close()
		t.Fatal("bad authentication accepted")
	}
}
func TestSolrCloseSettlesOriginalReadAndDoesNotCloseOtherClient(t *testing.T) {
	entered, acknowledged := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/solr/admin/cores" {
			close(entered)
			<-r.Context().Done()
			close(acknowledged)
			return
		}
		_, _ = w.Write([]byte(testIdentity))
	}))
	defer server.Close()
	cfg := connections.SolrConfig{Endpoint: server.URL}
	first, e := Open(context.Background(), cfg, "")
	if e != nil {
		t.Fatal(e)
	}
	second, e := Open(context.Background(), cfg, "")
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	done := make(chan error, 1)
	go func() {
		_, e := first.Do(context.Background(), Request{Method: "GET", Path: "/admin/cores?action=STATUS&wt=json"})
		done <- e
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("original worker never entered")
	}
	if _, e = first.Do(context.Background(), Request{Method: "GET", Path: "/admin/info/system?wt=json"}); !errors.Is(e, ErrBusy) {
		t.Fatal("parallel request accepted on owning client")
	}
	first.Close()
	select {
	case e = <-done:
		var lost *Interrupted
		if !errors.As(e, &lost) || lost.WriteOutcomeUnknown || !errors.Is(e, context.Canceled) {
			t.Fatal("original read cancellation did not settle", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owning worker did not acknowledge")
	}
	select {
	case <-acknowledged:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not acknowledge cancellation")
	}
	if _, e = second.Do(context.Background(), Request{Method: "GET", Path: "/admin/info/system?wt=json"}); e != nil {
		t.Fatal("independent client was closed", e)
	}
	first.Close()
}
func TestSolrPreCanceledInvalidAndOversizedRequestsDoNotDispatch(t *testing.T) {
	var dispatches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { dispatches.Add(1); _, _ = w.Write([]byte(testIdentity)) }))
	defer server.Close()
	c, e := Open(context.Background(), connections.SolrConfig{Endpoint: server.URL}, "")
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	ctx, stop := context.WithCancel(context.Background())
	stop()
	if _, e = c.Do(ctx, Request{Method: "POST", Path: "/update"}); !errors.Is(e, context.Canceled) {
		t.Fatal("pre-canceled dispatch", e)
	}
	for _, r := range []Request{{Method: "GET", Path: "//foreign"}, {Method: "POST", Path: "/update", Body: make([]byte, requestLimit+1)}, {Method: "GET", Path: "/admin/info/system?bad=%xx"}} {
		if _, e = c.Do(context.Background(), r); !errors.Is(e, ErrInvalid) {
			t.Fatal("invalid request dispatched")
		}
	}
	if dispatches.Load() != 1 {
		t.Fatal("invalid/canceled request dispatched")
	}
	if _, e = c.Do(context.Background(), Request{Method: "GET", Path: "/admin/info/system?wt=json"}); e != nil {
		t.Fatal("pre-dispatch failure retired client", e)
	}
}
