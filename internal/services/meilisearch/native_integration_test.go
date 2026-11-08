package meilisearch

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeFixture(t *testing.T) (connections.MeilisearchConfig, string) {
	t.Helper()
	if os.Getenv("NYADB_MEILISEARCH_IT") != "1" {
		t.Skip("requires owned native Meilisearch fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_MEILISEARCH_CONFIG"))
	if err != nil {
		t.Fatal("private native fixture config unavailable")
	}
	var probe struct {
		Endpoint, Auth string
		CAFile         string `json:"ca_file"`
		ServerName     string `json:"server_name"`
		KeyFile        string `json:"key_file"`
		ClientCertFile string `json:"client_cert_file"`
		ClientKeyFile  string `json:"client_key_file"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		t.Fatal("invalid private fixture config")
	}
	var secret []byte
	if probe.KeyFile != "" {
		secret, err = os.ReadFile(probe.KeyFile)
		if err != nil {
			t.Fatal("private native fixture key unavailable")
		}
	}
	return connections.MeilisearchConfig{Endpoint: probe.Endpoint, Auth: probe.Auth, CAFile: probe.CAFile, ServerName: probe.ServerName, ClientCertFile: probe.ClientCertFile, ClientKeyFile: probe.ClientKeyFile}, string(secret)
}

func nativeOpen(t *testing.T) *Client {
	t.Helper()
	cfg, secret := nativeFixture(t)
	c, err := Open(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal("native Meilisearch open", err)
	}
	t.Cleanup(c.Close)
	return c
}

func nativeRun(t *testing.T, c *Client, r Request) Response {
	t.Helper()
	response, err := c.Do(context.Background(), r)
	if err != nil {
		t.Fatal("native request", err)
	}
	return response
}

func nativeSubmit(t *testing.T, c *Client, request Request) Receipt {
	t.Helper()
	receipt, response, err := c.Submit(context.Background(), request)
	if err != nil || response.Status != 202 {
		t.Fatal("native task admission", err)
	}
	return receipt
}

func nativeWait(t *testing.T, c *Client, r Receipt) Task {
	t.Helper()
	task, err := c.WaitTask(context.Background(), r, 30*time.Second)
	if err != nil || task.Status != "succeeded" {
		t.Fatal("native task completion", err)
	}
	return task
}

func nativeIndex(t *testing.T, c *Client) string {
	t.Helper()
	uid := "owned_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes", Body: []byte(`{"uid":"` + uid + `","primaryKey":"id"}`)}))
	// The containing unique fixture owns all indexes and removes them on exit.
	return uid
}

func TestMeilisearchNativeAuthenticationVersionAndExactJSON(t *testing.T) {
	c := nativeOpen(t)
	if c.Identity().Version != "1.56.1" || c.Identity().CommitSHA != "421053d3f573efb3c1e5d857efb4e0408e599fd3" {
		t.Fatal("wrong official native binary observation")
	}
	cfg, secret := nativeFixture(t)
	if cfg.Auth == "bearer" {
		for _, key := range []string{"owned-wrong-key", ""} {
			bad := cfg
			if key == "" {
				bad.Auth = "none"
			}
			client, err := Open(context.Background(), bad, key)
			if err == nil {
				client.Close()
				t.Fatal("health accepted invalid Bearer")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("secret exposed")
			}
		}
	}
	if strings.HasPrefix(cfg.Endpoint, "https:") {
		for _, change := range []func(*connections.MeilisearchConfig){
			func(c *connections.MeilisearchConfig) { c.CAFile = "" },
			func(c *connections.MeilisearchConfig) { c.ServerName = "wrong.invalid" },
		} {
			bad := cfg
			change(&bad)
			client, err := Open(context.Background(), bad, secret)
			if err == nil {
				client.Close()
				t.Fatal("unverified native TLS accepted")
			}
		}
	}
	if cfg.ClientCertFile != "" {
		bad := cfg
		bad.ClientCertFile = ""
		bad.ClientKeyFile = ""
		client, err := Open(context.Background(), bad, secret)
		if err == nil {
			client.Close()
			t.Fatal("native server did not require client certificate")
		}
		t.Log("native mTLS accepted owned client certificate and rejected its absence")
	}
	uid := nativeIndex(t, c)
	body := []byte(`[{"id":"row_1","integer":9007199254740993,"decimal":1.2300e-20,"text":"原生🐱","nested":{"nil":null,"array":[false,"",42]}}]`)
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: body}))
	doc, err := nativeObject(nativeRun(t, c, Request{Method: "GET", Path: "/indexes/" + uid + "/documents/row_1"}).Body)
	var text string
	if err != nil || string(doc["integer"]) != "9007199254740993" || json.Unmarshal(doc["text"], &text) != nil || text != "原生🐱" {
		t.Fatal("native exact value changed")
	}
	if string(doc["decimal"]) != "1.23e-20" {
		t.Fatal("unexpected native decimal representation")
	}
	result, _, err := c.Search(context.Background(), uid, []byte(`{"q":"","limit":1}`))
	if err != nil || len(result.Hits) != 1 || !bytes.Contains(result.Hits[0], []byte("9007199254740993")) {
		t.Fatal("native search changed integer", err)
	}
}

func TestMeilisearchNativeTaskAdmissionAndFailureEvidence(t *testing.T) {
	c := nativeOpen(t)
	uid := nativeIndex(t, c)
	receipt := nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"other":"missing-configured-id"}]`)})
	if receipt.IndexUID == nil || *receipt.IndexUID != uid || receipt.Type != "documentAdditionOrUpdate" || len(receipt.Raw) == 0 {
		t.Fatal("original admission evidence lost")
	}
	task, err := c.WaitTask(context.Background(), receipt, 30*time.Second)
	var failure *TaskFailure
	if !errors.As(err, &failure) || task.Status != "failed" || !bytes.Equal(task.Receipt.Raw, receipt.Raw) {
		t.Fatal("accepted task failure reported as completion", err)
	}
	fields, e := nativeObject(task.Raw)
	diagnostic, e2 := nativeObject(fields["error"])
	code, e3 := nativeString(diagnostic["code"])
	if e != nil || e2 != nil || e3 != nil || code != "missing_document_id" {
		t.Fatal("complete native failure evidence unavailable")
	}
	t.Log("native admission followed by failed task with retained missing_document_id evidence")
	// The binary observation does not claim to bind an index incarnation.
	before := c.Identity()
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "DELETE", Path: "/indexes/" + uid}))
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes", Body: []byte(`{"uid":"` + uid + `","primaryKey":"id"}`)}))
	if c.Identity() != before {
		t.Fatal("unexpected binary observation changed")
	}
	t.Log("same index UID was recreated without changing binary version/commit observation")
}

func TestMeilisearchNativeSearchLimitsAndReadonlyErrors(t *testing.T) {
	c := nativeOpen(t)
	uid := nativeIndex(t, c)
	docs := make([]map[string]any, 1017)
	for i := range docs {
		docs[i] = map[string]any{"id": "row_" + strconv.Itoa(i), "text": "native", "n": i}
	}
	body, _ := json.Marshal(docs)
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: body}))
	result, _, err := c.Search(context.Background(), uid, []byte(`{"q":"","offset":1000,"limit":20}`))
	if err != nil || len(result.Hits) != 0 {
		t.Fatal("native default maxTotalHits limit not observed", err)
	}
	fields, e := nativeObject(nativeRun(t, c, Request{Method: "GET", Path: "/indexes/" + uid + "/documents?offset=1000&limit=20"}).Body)
	var remaining []json.RawMessage
	total, e2 := nativeUint(fields["total"], 64)
	if e != nil || e2 != nil || total != 1017 || decodeJSON(fields["results"], &remaining) != nil || len(remaining) != 17 {
		t.Fatal("document enumeration confused with bounded search")
	}
	_, response, err := c.Search(context.Background(), uid, []byte(`{"q":"","filter":"undeclared = 1"}`))
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != 400 || errors.Is(err, ErrRetired) || !bytes.Contains(response.Body, []byte("invalid_search_filter")) {
		t.Fatal("complete native readonly error lost")
	}
	_, _, err = c.Search(context.Background(), uid, []byte(`{"q":"","limit":2}`))
	if err != nil {
		t.Fatal("complete search error retired healthy client", err)
	}
	t.Log("search stopped at default 1000; document listing still returned rows 1000..1016 of 1017")
}

func nativeRelay(t *testing.T, upstream *Client, after func(http.ResponseWriter, *http.Request, Response) bool) *Client {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, requestLimit+1))
		if err != nil {
			w.WriteHeader(502)
			return
		}
		path := strings.TrimPrefix(r.URL.RequestURI(), "/proxy/meili")
		response, err := upstream.Do(r.Context(), Request{Method: r.Method, Path: path, Body: body})
		if err != nil && response.Status == 0 {
			w.WriteHeader(502)
			return
		}
		if after(w, r, response) {
			return
		}
		w.Header().Set("Content-Type", response.ContentType)
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
	}))
	t.Cleanup(server.Close)
	ca := filepath.Join(t.TempDir(), "relay-ca.pem")
	if os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600) != nil {
		t.Fatal("relay CA unavailable")
	}
	cfg, secret := nativeFixture(t)
	cfg.Endpoint, cfg.CAFile, cfg.ServerName = server.URL+"/proxy/meili", ca, "example.com"
	cfg.ClientCertFile, cfg.ClientKeyFile = "", ""
	c, err := Open(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal("verified TLS relay open", err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestMeilisearchNativeLostAdmissionResponseNeverReplays(t *testing.T) {
	upstream := nativeOpen(t)
	uid := nativeIndex(t, upstream)
	var submissions atomic.Int32
	accepted := make(chan Receipt, 1)
	c := nativeRelay(t, upstream, func(w http.ResponseWriter, r *http.Request, response Response) bool {
		if r.Method != "POST" {
			return false
		}
		submissions.Add(1)
		var err error
		receipt, err := parseReceipt(response.Body)
		if err != nil || response.Status != 202 {
			t.Error("real admission before reply loss was not demonstrated")
		}
		accepted <- receipt
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return true
		}
		_ = conn.Close()
		return true
	})
	request := Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"lost","n":9007199254740993}]`)}
	_, _, err := c.Submit(context.Background(), request)
	var unknown *Interrupted
	if !errors.As(err, &unknown) || !unknown.WriteOutcomeUnknown || submissions.Load() != 1 {
		t.Fatal("lost admission replayed or accepted as completion")
	}
	if _, _, err := c.Submit(context.Background(), request); !errors.Is(err, ErrRetired) || submissions.Load() != 1 {
		t.Fatal("original retired client resubmitted")
	}
	// The relay's captured native receipt is independent test evidence, not an
	// application reconstruction of a missing receipt or an automatic recovery.
	fresh := nativeOpen(t)
	nativeWait(t, fresh, <-accepted)
	fields, e := nativeObject(nativeRun(t, fresh, Request{Method: "GET", Path: "/indexes/" + uid + "/documents/lost"}).Body)
	if e != nil || string(fields["n"]) != "9007199254740993" {
		t.Fatal("real admitted write did not finish")
	}
	tasks, e := nativeObject(nativeRun(t, fresh, Request{Method: "GET", Path: "/tasks?indexUids=" + uid + "&types=documentAdditionOrUpdate"}).Body)
	total, e2 := nativeUint(tasks["total"], 64)
	if e != nil || e2 != nil || total != 1 {
		t.Fatal("native task history did not prove exactly one submission")
	}
}

func TestMeilisearchNativeCanceledOriginalSearchAndWaitClose(t *testing.T) {
	for _, mode := range []string{"search_cancel", "task_close"} {
		t.Run(mode, func(t *testing.T) {
			upstream := nativeOpen(t)
			uid := nativeIndex(t, upstream)
			receipt := nativeSubmit(t, upstream, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"one"}]`)})
			nativeWait(t, upstream, receipt)
			entered := make(chan struct{})
			var observed atomic.Int32
			c := nativeRelay(t, upstream, func(w http.ResponseWriter, r *http.Request, response Response) bool {
				if mode == "search_cancel" && !strings.HasSuffix(r.URL.Path, "/search") || mode == "task_close" && !strings.Contains(r.URL.Path, "/tasks/") {
					return false
				}
				observed.Add(1)
				close(entered)
				<-r.Context().Done()
				return true
			})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			settled := make(chan error, 1)
			go func() {
				if mode == "search_cancel" {
					_, _, err := c.Search(ctx, uid, []byte(`{"q":""}`))
					settled <- err
				} else {
					_, err := c.WaitTask(ctx, receipt, 30*time.Second)
					settled <- err
				}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("original native read did not enter")
			}
			if mode == "search_cancel" {
				cancel()
			} else {
				c.Close()
			}
			select {
			case err := <-settled:
				var interrupted *Interrupted
				if !errors.As(err, &interrupted) || interrupted.WriteOutcomeUnknown || !errors.Is(err, context.Canceled) {
					t.Fatal("original read did not settle", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("original worker did not acknowledge cancellation")
			}
			fresh := nativeOpen(t)
			result, _, err := fresh.Search(context.Background(), uid, []byte(`{"q":""}`))
			if err != nil || len(result.Hits) != 1 || observed.Load() != 1 {
				t.Fatal("old cancellation replayed or affected independent client", err)
			}
		})
	}
}

func TestMeilisearchNativeScopedKeyPermissionsAndRevocation(t *testing.T) {
	master := nativeOpen(t)
	cfg, _ := nativeFixture(t)
	if cfg.Auth != "bearer" {
		t.Fatal("scoped-key test requires selected authenticated fixture")
	}
	uid := nativeIndex(t, master)
	nativeWait(t, master, nativeSubmit(t, master, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"one"}]`)}))
	body, _ := json.Marshal(map[string]any{"name": "owned-scope", "actions": []string{"indexes.get", "documents.get", "search", "tasks.get"}, "indexes": []string{uid}, "expiresAt": nil})
	response, createErr := master.Do(context.Background(), Request{Method: "POST", Path: "/keys", Body: body})
	if createErr != nil {
		diagnostic, _ := nativeObject(response.Body)
		code, _ := nativeString(diagnostic["code"])
		t.Fatal("native scoped key creation", createErr, "status", response.Status, "code", code)
	}
	fields, e := nativeObject(response.Body)
	key, e2 := nativeString(fields["key"])
	keyUID, e3 := nativeString(fields["uid"])
	if e != nil || e2 != nil || e3 != nil || response.Status != 201 || key == "" || keyUID == "" {
		t.Fatal("native scoped key unavailable")
	}
	// 1.56.1 forbids global version permission on index-scoped keys. Require
	// an explicitly configured index, with no fabricated binary observation.
	cfg.IndexUID = uid
	c, err := Open(context.Background(), cfg, key)
	if err != nil {
		t.Fatal("scoped-key explicit index handshake", err)
	}
	t.Cleanup(c.Close)
	if c.Identity() != (Identity{}) {
		t.Fatal("scoped handshake invented global binary identity")
	}
	global := cfg
	global.IndexUID = ""
	if rejected, err := Open(context.Background(), global, key); err == nil {
		rejected.Close()
		t.Fatal("scoped key obtained forbidden global version observation")
	}
	result, _, err := c.Search(context.Background(), uid, []byte(`{"q":""}`))
	if err != nil || len(result.Hits) != 1 {
		t.Fatal("scoped search failed", err)
	}
	_, err = c.Do(context.Background(), Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"forbidden"}]`)})
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != 403 {
		t.Fatal("read-only scoped key admitted write")
	}
	fresh, err := Open(context.Background(), cfg, key)
	if err != nil {
		t.Fatal("scoped reopen", err)
	}
	t.Cleanup(fresh.Close)
	nativeRun(t, master, Request{Method: "DELETE", Path: "/keys/" + keyUID})
	_, _, err = fresh.Search(context.Background(), uid, []byte(`{"q":""}`))
	if !errors.As(err, &status) || status.Status != 403 {
		t.Fatal("revoked key fell back to master permissions")
	}
	t.Log("native explicit scoped index/search permissions, forbidden global version/write and key revocation verified without master fallback")
}
