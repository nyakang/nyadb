package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeFixture(t *testing.T) (connections.QdrantConfig, string, string) {
	t.Helper()
	if os.Getenv("NYADB_QDRANT_IT") != "1" {
		t.Skip("requires an explicitly owned native Qdrant fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_QDRANT_CONFIG"))
	if err != nil {
		t.Fatal("private native config unavailable")
	}
	var p struct {
		Endpoint, Auth  string
		KeyFile         string `json:"key_file"`
		ReadonlyKeyFile string `json:"readonly_key_file"`
		CAFile          string `json:"ca_file"`
		ServerName      string `json:"server_name"`
		ClientCertFile  string `json:"client_cert_file"`
		ClientKeyFile   string `json:"client_key_file"`
	}
	if json.Unmarshal(raw, &p) != nil {
		t.Fatal("invalid private native config")
	}
	read := func(path string) string {
		if path == "" {
			return ""
		}
		value, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("private native key unavailable")
		}
		return string(value)
	}
	return connections.QdrantConfig{Endpoint: p.Endpoint, Auth: p.Auth, CAFile: p.CAFile, ServerName: p.ServerName, ClientCertFile: p.ClientCertFile, ClientKeyFile: p.ClientKeyFile}, read(p.KeyFile), read(p.ReadonlyKeyFile)
}

func nativeOpen(t *testing.T) *Client {
	t.Helper()
	cfg, secret, _ := nativeFixture(t)
	c, err := Open(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal("original native Qdrant open", err)
	}
	t.Cleanup(c.Close)
	return c
}
func nativeRun(t *testing.T, c *Client, r Request) Response {
	t.Helper()
	response, err := c.Do(context.Background(), r)
	if err != nil {
		t.Fatal("original native request", err)
	}
	return response
}
func nativeCollection(t *testing.T, c *Client, schema string) string {
	t.Helper()
	name := "owned_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	r := nativeRun(t, c, Request{Method: "PUT", Path: "/collections/" + name, Body: []byte(schema)})
	result, err := NativeResult(r.Body)
	if err != nil || !bytes.Equal(result, []byte("true")) {
		t.Fatal("native collection creation", err)
	}
	return name
}
func nativeUpsert(t *testing.T, c *Client, collection, body string) MutationReceipt {
	t.Helper()
	receipt, response, err := c.SubmitPoints(context.Background(), collection, "upsert", []byte(body), true)
	if err != nil || receipt.Status != "completed" || response.Status != 200 {
		t.Fatal("native point completion", err, string(response.Body))
	}
	return receipt
}

func TestQdrantNativeAuthenticationVersionAndPermissions(t *testing.T) {
	c := nativeOpen(t)
	if c.Identity().Version != "1.20.0" || c.Identity().Title != "qdrant - vector search engine" || c.Identity().Commit == "" {
		t.Fatal("incorrect native binary")
	}
	cfg, secret, readonly := nativeFixture(t)
	if cfg.Auth == "api_key" {
		for _, key := range []string{"owned-wrong-key", ""} {
			bad := cfg
			if key == "" {
				bad.Auth = "none"
			}
			other, err := Open(context.Background(), bad, key)
			if err == nil {
				other.Close()
				t.Fatal("native authentication accepted wrong or missing API key")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("native credential exposed")
			}
		}
		reader, err := Open(context.Background(), cfg, readonly)
		if err != nil {
			t.Fatal("native read-only key denied browse", err)
		}
		t.Cleanup(reader.Close)
		name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
		nativeUpsert(t, c, name, `{"points":[{"id":1,"vector":[1,0],"payload":{"text":"原生🐱"}}]}`)
		points, _, err := reader.Query(context.Background(), name, []byte(`{"query":[1,0],"with_payload":true,"limit":1}`))
		if err != nil || len(points) != 1 {
			t.Fatal("native read-only query denied", err)
		}
		_, response, err := reader.SubmitPoints(context.Background(), name, "upsert", []byte(`{"points":[{"id":2,"vector":[0,1]}]}`), true)
		if err == nil || response.Status != http.StatusForbidden || len(response.Body) == 0 {
			t.Fatal("native read-only write accepted or denial evidence lost", err)
		}
	}
	if strings.HasPrefix(cfg.Endpoint, "https:") {
		for _, mutate := range []func(*connections.QdrantConfig){func(c *connections.QdrantConfig) { c.CAFile = "" }, func(c *connections.QdrantConfig) { c.ServerName = "wrong.invalid" }} {
			bad := cfg
			mutate(&bad)
			other, err := Open(context.Background(), bad, secret)
			if err == nil {
				other.Close()
				t.Fatal("unverified native TLS accepted")
			}
		}
	}
	if cfg.ClientCertFile != "" {
		bad := cfg
		bad.ClientCertFile, bad.ClientKeyFile = "", ""
		other, err := Open(context.Background(), bad, secret)
		if err == nil {
			other.Close()
			t.Fatal("native mTLS accepted missing client certificate")
		}
	}
}

func TestQdrantNativeNamedSparseMultiVectorAndAliases(t *testing.T) {
	c := nativeOpen(t)
	name := nativeCollection(t, c, `{"vectors":{"dense":{"size":2,"distance":"Dot"},"multi":{"size":2,"distance":"Dot","multivector_config":{"comparator":"max_sim"}}},"sparse_vectors":{"sparse":{}}}`)
	info, _, err := c.Collection(context.Background(), name)
	if err != nil || !bytes.Contains(info.Result, []byte(`"sparse_vectors"`)) || !bytes.Contains(info.Result, []byte(`"max_sim"`)) {
		t.Fatal("native complete vector schema lost", err)
	}
	// Qdrant 1.20's untagged Sparse-before-MultiDense deserializer can
	// interpret a two-row integer matrix as sparse indices/values. Preserve
	// the actual error; the transport must never rewrite the user's JSON.
	integerWriter := nativeOpen(t)
	_, rejected, err := integerWriter.SubmitPoints(context.Background(), name, "upsert", []byte(`{"points":[{"id":1,"vector":{"dense":[1,0],"multi":[[1,0],[0,1]],"sparse":{"indices":[2,8],"values":[0.25,0.5]}}}]}`), true)
	if err == nil || rejected.Status != 400 || !bytes.Contains(rejected.Body, []byte("Conversion between sparse and regular vectors failed")) {
		t.Fatal("native ambiguous integer matrix error was lost", err)
	}
	t.Log("native integer two-row matrix rejected; original float matrix accepted without request rewriting")
	nativeUpsert(t, c, name, `{"points":[{"id":1,"vector":{"dense":[1,0],"multi":[[0.75,0.25],[0.25,0.75]],"sparse":{"indices":[2,8],"values":[0.25,0.5]}},"payload":{"kind":"native"}}]}`)
	for _, query := range []string{`{"query":[1,0],"using":"dense","with_payload":true,"with_vector":true}`, `{"query":{"indices":[2,8],"values":[0.25,0.5]},"using":"sparse","with_vector":true}`, `{"query":[[0.75,0.25],[0.25,0.75]],"using":"multi","with_vector":true}`} {
		points, _, err := c.Query(context.Background(), name, []byte(query))
		if err != nil || len(points) != 1 || !bytes.Contains(points[0].Vector, []byte(`"sparse"`)) || !bytes.Contains(points[0].Vector, []byte(`"multi"`)) {
			t.Fatal("native named vector query or output corrupted", err)
		}
	}
	alias := name + "_alias"
	body := `{"actions":[{"create_alias":{"collection_name":"` + name + `","alias_name":"` + alias + `"}}]}`
	nativeRun(t, c, Request{Method: "POST", Path: "/collections/aliases", Body: []byte(body)})
	aliases, _, err := c.Aliases(context.Background())
	found := false
	for _, item := range aliases {
		found = found || item.Alias == alias && item.Collection == name
	}
	if err != nil || !found {
		t.Fatal("native alias target lost", err)
	}
	page, _, err := c.Scroll(context.Background(), alias, nil, 1, nil)
	if err != nil || len(page.Points) != 1 {
		t.Fatal("native alias read", err)
	}
}

func TestQdrantNativeExactIDsScrollQueryCountAndCursor(t *testing.T) {
	c := nativeOpen(t)
	name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	body := `{"points":[{"id":1,"vector":[1,0],"payload":{"group":"native","n":9007199254740993}},{"id":9007199254740993,"vector":[0.5,0.5],"payload":{"group":"native","n":9007199254740995}},{"id":18446744073709551615,"vector":[0,1],"payload":{"group":"other","text":"原生🐱","nested":{"nil":null}}},{"id":"550e8400-e29b-41d4-a716-446655440000","vector":[1,1],"payload":{"group":"native"}}]}`
	nativeUpsert(t, c, name, body)
	seen := map[string]bool{}
	var cursor json.RawMessage
	for pageNumber := 0; pageNumber < 8; pageNumber++ {
		page, _, err := c.Scroll(context.Background(), name, cursor, 1, nil)
		if err != nil || len(page.Points) != 1 {
			t.Fatal("native ordered point page", err)
		}
		p := page.Points[0]
		id := string(p.ID)
		if seen[id] {
			t.Fatal("native cursor returned a duplicate point")
		}
		seen[id] = true
		if id == "1" && !bytes.Contains(p.Payload, []byte("9007199254740993")) || id == "9007199254740993" && !bytes.Contains(p.Payload, []byte("9007199254740995")) {
			t.Fatal("native payload precision changed")
		}
		if len(page.NextOffset) == 0 {
			break
		}
		cursor = bytes.Clone(page.NextOffset)
	}
	if len(seen) != 4 || !seen["18446744073709551615"] || !seen[`"550e8400-e29b-41d4-a716-446655440000"`] {
		t.Fatal("native uint64/UUID cursor missed points")
	}
	filter := json.RawMessage(`{"must":[{"key":"group","match":{"value":"native"}}]}`)
	count, _, err := c.Count(context.Background(), name, filter)
	if err != nil || count != 3 {
		t.Fatal("native exact filtered count", err)
	}
	points, response, err := c.Query(context.Background(), name, []byte(`{"query":[1,0],"filter":`+string(filter)+`,"limit":4,"with_payload":true,"with_vector":true}`))
	if err != nil || len(points) != 3 || !bytes.Contains(response.Body, []byte(`"score"`)) {
		t.Fatal("native filtered score results", err)
	}
}

func TestQdrantNativeAdmissionCompletionAndNativeFailure(t *testing.T) {
	c := nativeOpen(t)
	name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	receipt, response, err := c.SubmitPoints(context.Background(), name, "upsert", []byte(`{"points":[{"id":1,"vector":[1,0]}]}`), false)
	if err != nil || receipt.Status != "acknowledged" || response.Status != 200 || len(receipt.Raw) == 0 {
		t.Fatal("native asynchronous admission incorrectly reported", err)
	}
	completed := nativeUpsert(t, c, name, `{"points":[{"id":2,"vector":[0,1]}]}`)
	if completed.OperationID <= receipt.OperationID || completed.Status != "completed" {
		t.Fatal("native original operation identities changed")
	}
	deleted, _, err := c.SubmitPoints(context.Background(), name, "delete", []byte(`{"points":[2]}`), true)
	if err != nil || deleted.Status != "completed" {
		t.Fatal("native delete completion", err)
	}
	count, _, err := c.Count(context.Background(), name, nil)
	if err != nil || count != 1 {
		t.Fatal("native completed delete readback", err)
	}
	_, response, err = c.SubmitPoints(context.Background(), name, "upsert", []byte(`{"points":[{"id":3,"vector":[1,0,1]}]}`), true)
	if err == nil || response.Status != 400 || !bytes.Contains(response.Body, []byte("error")) {
		t.Fatal("native vector-dimension error lost", err)
	}
	if _, err := c.Do(context.Background(), Request{Method: "GET", Path: "/collections"}); !errors.Is(err, ErrRetired) {
		t.Fatal("raw failed mutation did not require explicit recovery")
	}
}

// Fault injection forwards to the original authenticated/TLS native node, then
// withholds its actual response. It does not synthesize server success.
func nativeFaultProxy(t *testing.T, upstream *Client, match func(*http.Request) bool, fault func(http.ResponseWriter, *http.Request, Response)) (connections.QdrantConfig, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error("fault proxy native source read")
			return
		}
		response, err := upstream.Do(context.Background(), Request{Method: r.Method, Path: r.URL.RequestURI(), Body: body})
		if err != nil {
			t.Error("fault proxy original native request", err)
			w.WriteHeader(502)
			return
		}
		if match(r) {
			calls.Add(1)
			fault(w, r, response)
			return
		}
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
	}))
	t.Cleanup(s.Close)
	cfg := upstream.config
	cfg.Endpoint, cfg.CAFile, cfg.ServerName, cfg.ClientCertFile, cfg.ClientKeyFile = s.URL, "", "", "", ""
	return cfg, &calls
}

func TestQdrantNativeLostResponseNeverReplays(t *testing.T) {
	upstream := nativeOpen(t)
	name := nativeCollection(t, upstream, `{"vectors":{"size":2,"distance":"Dot"}}`)
	settled := make(chan Response, 1)
	cfg, calls := nativeFaultProxy(t, upstream, func(r *http.Request) bool { return r.Method == "PUT" && r.URL.Path == "/collections/"+name+"/points" }, func(w http.ResponseWriter, r *http.Request, native Response) {
		settled <- native
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	})
	_, secret, _ := nativeFixture(t)
	c, err := Open(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal("fault proxy original handshake", err)
	}
	t.Cleanup(c.Close)
	_, _, err = c.SubmitPoints(context.Background(), name, "upsert", []byte(`{"points":[{"id":9007199254740993,"vector":[1,0],"payload":{"text":"原生写入🐱"}}]}`), true)
	var interrupted *Interrupted
	if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown {
		t.Fatal("lost native write evidence not uncertain", err)
	}
	native := <-settled
	result, err := NativeResult(native.Body)
	if err != nil || !bytes.Contains(result, []byte(`"completed"`)) {
		t.Fatal("fault did not occur after actual native write completion")
	}
	_, _, err = c.SubmitPoints(context.Background(), name, "upsert", []byte(`{"points":[]}`), true)
	if !errors.Is(err, ErrRetired) || calls.Load() != 1 {
		t.Fatal("lost write automatically replayed")
	}
	page, _, err := upstream.Scroll(context.Background(), name, nil, 10, nil)
	if err != nil || len(page.Points) != 1 || string(page.Points[0].ID) != "9007199254740993" {
		t.Fatal("actual native committed write disappeared", err)
	}
	// Recovery is a newly opened original native client. No mutation is replayed.
	recovered := nativeOpen(t)
	count, _, err := recovered.Count(context.Background(), name, nil)
	if err != nil || count != 1 || calls.Load() != 1 {
		t.Fatal("explicit recovery replayed a mutation", err)
	}
}

func TestQdrantNativeCanceledOriginalReadAndClose(t *testing.T) {
	for _, action := range []string{"read-cancel", "write-cancel", "write-close"} {
		t.Run(action, func(t *testing.T) {
			upstream := nativeOpen(t)
			name := nativeCollection(t, upstream, `{"vectors":{"size":2,"distance":"Dot"}}`)
			settled, released := make(chan Response, 1), make(chan struct{})
			cfg, calls := nativeFaultProxy(t, upstream, func(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/collections/"+name+"/points") }, func(w http.ResponseWriter, r *http.Request, native Response) {
				settled <- native
				<-released
				w.WriteHeader(native.Status)
				_, _ = w.Write(native.Body)
			})
			_, secret, _ := nativeFixture(t)
			c, err := Open(context.Background(), cfg, secret)
			if err != nil {
				t.Fatal("original fault proxy handshake", err)
			}
			t.Cleanup(c.Close)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				if action == "read-cancel" {
					_, _, err := c.Scroll(ctx, name, nil, 10, nil)
					result <- err
				} else {
					_, _, err := c.SubmitPoints(ctx, name, "upsert", []byte(`{"points":[{"id":1,"vector":[1,0]}]}`), true)
					result <- err
				}
			}()
			select {
			case native := <-settled:
				if native.Status != 200 {
					t.Fatal("original native executor did not settle successfully")
				}
			case <-time.After(15 * time.Second):
				close(released)
				t.Fatal("original native executor did not settle")
			}
			if action == "write-close" {
				c.Close()
			} else {
				cancel()
			}
			select {
			case err := <-result:
				var interrupted *Interrupted
				if !errors.As(err, &interrupted) || interrupted.WriteOutcomeUnknown != (action != "read-cancel") {
					t.Error("original cancellation outcome", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("original HTTP executor did not acknowledge cancellation")
			}
			close(released)
			if calls.Load() != 1 {
				t.Fatal("canceled native operation was replayed")
			}
			if _, err := c.Do(context.Background(), Request{Method: "GET", Path: "/collections"}); !errors.Is(err, ErrRetired) {
				t.Fatal("canceled original client remained usable")
			}
			c.Close()
			count, _, err := upstream.Count(context.Background(), name, nil)
			want := uint64(1)
			if action == "read-cancel" {
				want = 0
			}
			if err != nil || count != want {
				t.Fatal("actual native write outcome was lost", err)
			}
		})
	}
}
