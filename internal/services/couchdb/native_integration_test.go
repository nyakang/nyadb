package couchdb

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

func nativeFixture(t *testing.T, name string) (connections.CouchDBConfig, string) {
	t.Helper()
	if os.Getenv("NYADB_COUCHDB_IT") != "1" {
		t.Skip("requires owned native CouchDB fixture")
	}
	raw, err := os.ReadFile(os.Getenv(name))
	if err != nil {
		t.Fatal("private fixture configuration unavailable")
	}
	var probe struct {
		Endpoint, Auth, Username string
		CAFile                   string `json:"ca_file"`
		ServerName               string `json:"server_name"`
		PasswordFile             string `json:"password_file"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		t.Fatal("invalid private fixture configuration")
	}
	var secret []byte
	if probe.PasswordFile != "" {
		secret, err = os.ReadFile(probe.PasswordFile)
		if err != nil {
			t.Fatal("private password unavailable")
		}
	}
	return connections.CouchDBConfig{Endpoint: probe.Endpoint, Auth: probe.Auth, Username: probe.Username, CAFile: probe.CAFile, ServerName: probe.ServerName}, string(secret)
}

func nativeOpen(t *testing.T, name string) *Client {
	t.Helper()
	cfg, secret := nativeFixture(t, name)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := Open(ctx, cfg, secret)
	if err != nil {
		t.Fatal("native CouchDB open", err)
	}
	t.Cleanup(c.Close)
	return c
}

func nativeRun(t *testing.T, c *Client, r Request) Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	v, err := c.Do(ctx, r)
	if err != nil {
		t.Fatal("native CouchDB request", err)
	}
	return v
}

func nativeID() string { return "owned_" + strconv.FormatInt(time.Now().UnixNano(), 36) }

func nativeDocument(t *testing.T, c *Client, path string) map[string]json.RawMessage {
	t.Helper()
	r := nativeRun(t, c, Request{Method: "GET", Path: path})
	var doc map[string]json.RawMessage
	if decodeJSON(r.Body, &doc) != nil || doc["_id"] == nil || doc["_rev"] == nil {
		t.Fatal("invalid native document")
	}
	return doc
}

func nativeRevision(t *testing.T, response Response) string {
	t.Helper()
	var receipt struct {
		OK  bool   `json:"ok"`
		ID  string `json:"id"`
		Rev string `json:"rev"`
	}
	if decodeJSON(response.Body, &receipt) != nil || !receipt.OK || receipt.ID == "" || receipt.Rev == "" {
		t.Fatal("invalid native document receipt")
	}
	return receipt.Rev
}

func TestCouchDBNativeAuthenticationNodeObservationAndExactJSON(t *testing.T) {
	c := nativeOpen(t, "NYADB_COUCHDB_CONFIG")
	if c.Identity().Version != "3.5.3" || !validIdentity(c.Identity()) {
		t.Fatal("wrong official native identity")
	}
	cfg, secret := nativeFixture(t, "NYADB_COUCHDB_ADMIN_CONFIG")
	for name, change := range map[string]func(*connections.CouchDBConfig, *string){
		"wrong_password":  func(_ *connections.CouchDBConfig, p *string) { *p = "wrong" },
		"wrong_principal": func(c *connections.CouchDBConfig, _ *string) { c.Username = "different" },
	} {
		t.Run(name, func(t *testing.T) {
			bad, pass := cfg, secret
			change(&bad, &pass)
			client, err := Open(context.Background(), bad, pass)
			if err == nil {
				client.Close()
				t.Fatal("wrong Basic identity accepted")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("secret exposed")
			}
		})
	}
	if strings.HasPrefix(cfg.Endpoint, "https:") {
		for name, change := range map[string]func(*connections.CouchDBConfig){
			"wrong_server_name": func(c *connections.CouchDBConfig) { c.ServerName = "wrong.invalid" },
			"missing_CA":        func(c *connections.CouchDBConfig) { c.CAFile = "" },
		} {
			t.Run(name, func(t *testing.T) {
				bad := cfg
				change(&bad)
				client, err := Open(context.Background(), bad, secret)
				if err == nil {
					client.Close()
					t.Fatal("unverified native TLS accepted")
				}
			})
		}
	}
	id := nativeID()
	body := []byte(`{"_id":"` + id + `","integer":9007199254740993,"decimal":1.2300e-20,"text":"原生 CouchDB🐱","nested":{"null":null,"array":[false,"",42]}}`)
	r := nativeRun(t, c, Request{Method: "PUT", Path: "/nyadb_fixture/" + id, Body: body})
	rev := nativeRevision(t, r)
	doc := nativeDocument(t, c, "/nyadb_fixture/"+id)
	var text string
	if string(doc["integer"]) != "9007199254740993" || json.Unmarshal(doc["text"], &text) != nil || text != "原生 CouchDB🐱" {
		t.Fatal("native integer or Unicode changed")
	}
	// CouchDB normalizes a stored decimal. Retain the actual response spelling;
	// transport does not claim that the server preserves original input spelling.
	if string(doc["decimal"]) != "1.23e-20" {
		t.Fatal("unexpected native decimal representation", string(doc["decimal"]))
	}
	r = nativeRun(t, c, Request{Method: "GET", Path: "/nyadb_fixture/" + id})
	if r.ETag != `"`+rev+`"` || !bytes.Contains(r.Body, []byte(`9007199254740993`)) {
		t.Fatal("native ETag or response bytes lost")
	}
	_, err := c.Do(context.Background(), Request{Method: "GET", Path: "/nyadb_fixture/absent_" + id})
	var httpError *HTTPError
	if !errors.As(err, &httpError) || httpError.Status != 404 || errors.Is(err, ErrRetired) {
		t.Fatal("native missing read incorrectly retired connection")
	}
	nativeRun(t, c, Request{Method: "GET", Path: "/nyadb_fixture"})
}

func TestCouchDBNativeExactPaddedPrincipal(t *testing.T) {
	cfg, secret := nativeFixture(t, "NYADB_COUCHDB_PADDED_CONFIG")
	c := nativeOpen(t, "NYADB_COUCHDB_PADDED_CONFIG")
	p := c.Principal()
	if p.Name == nil || *p.Name != cfg.Username || cfg.Username == strings.TrimSpace(cfg.Username) || secret == strings.TrimSpace(secret) {
		t.Fatal("native padded principal or password bytes changed")
	}
	*p.Name = "changed"
	if *c.Principal().Name != cfg.Username {
		t.Fatal("principal snapshot aliases client")
	}
	for _, change := range []func(*connections.CouchDBConfig, *string){
		func(c *connections.CouchDBConfig, _ *string) { c.Username = strings.TrimSpace(c.Username) },
		func(_ *connections.CouchDBConfig, s *string) { *s = strings.TrimSpace(*s) },
	} {
		bad, pass := cfg, secret
		change(&bad, &pass)
		client, err := Open(context.Background(), bad, pass)
		if err == nil {
			client.Close()
			t.Fatal("trimmed native credential authenticated")
		}
	}
}

func TestCouchDBNativeRevisionConflictAndPartialBulkReceipt(t *testing.T) {
	c := nativeOpen(t, "NYADB_COUCHDB_CONFIG")
	id := nativeID()
	path := "/nyadb_fixture/" + id
	rev := nativeRevision(t, nativeRun(t, c, Request{Method: "PUT", Path: path, Body: []byte(`{"_id":"` + id + `","value":1}`)}))
	nativeRevision(t, nativeRun(t, c, Request{Method: "PUT", Path: path, Body: []byte(`{"_id":"` + id + `","_rev":"` + rev + `","value":2}`)}))
	r, err := c.Do(context.Background(), Request{Method: "PUT", Path: path, Body: []byte(`{"_id":"` + id + `","_rev":"` + rev + `","value":3}`)})
	var status *HTTPError
	var uncertain *Interrupted
	if !errors.As(err, &status) || status.Status != 409 || !errors.As(err, &uncertain) || !uncertain.WriteOutcomeUnknown || !bytes.Contains(r.Body, []byte(`"conflict"`)) {
		t.Fatal("generic stale write did not preserve conflict body/conservative outcome")
	}
	fresh := nativeOpen(t, "NYADB_COUCHDB_CONFIG")
	if string(nativeDocument(t, fresh, path)["value"]) != "2" {
		t.Fatal("stale revision changed document")
	}
	other := nativeID()
	body := `{"docs":[{"_id":"` + id + `","_rev":"` + rev + `","value":4},{"_id":"` + other + `","value":5}]}`
	r = nativeRun(t, fresh, Request{Method: "POST", Path: "/nyadb_fixture/_bulk_docs", Body: []byte(body)})
	var receipts []struct {
		OK      bool `json:"ok"`
		ID, Rev string
		Error   string `json:"error"`
	}
	if r.Status != 201 || decodeJSON(r.Body, &receipts) != nil || len(receipts) != 2 || receipts[0].ID != id || receipts[0].Error != "conflict" || receipts[1].ID != other || receipts[1].Rev == "" || !receipts[1].OK {
		t.Fatal("native non-atomic bulk receipt lost")
	}
	if string(nativeDocument(t, fresh, path)["value"]) != "2" || string(nativeDocument(t, fresh, "/nyadb_fixture/"+other)["value"]) != "5" {
		t.Fatal("bulk partial success misunderstood")
	}
}

func TestCouchDBNativeDatabaseRecreationCanAliasOldRevision(t *testing.T) {
	admin := nativeOpen(t, "NYADB_COUCHDB_ADMIN_CONFIG")
	db := nativeID()
	path := "/" + db
	nativeRun(t, admin, Request{Method: "PUT", Path: path})
	t.Cleanup(func() {
		cleanup := nativeOpen(t, "NYADB_COUCHDB_ADMIN_CONFIG")
		nativeRun(t, cleanup, Request{Method: "DELETE", Path: path})
	})
	body := []byte(`{"_id":"same","value":7}`)
	var rev string
	var collision bool
	// Native 3.5.3 derives this field from a second-resolution shard creation
	// time. Retry only this evidence setup if an attempt crosses a second.
	for attempt := 0; attempt < 8; attempt++ {
		before := nativeRun(t, admin, Request{Method: "GET", Path: path})
		rev = nativeRevision(t, nativeRun(t, admin, Request{Method: "PUT", Path: path + "/same", Body: body}))
		nativeRun(t, admin, Request{Method: "DELETE", Path: path})
		nativeRun(t, admin, Request{Method: "PUT", Path: path})
		after := nativeRun(t, admin, Request{Method: "GET", Path: path})
		var oldInfo, newInfo map[string]json.RawMessage
		if decodeJSON(before.Body, &oldInfo) != nil || decodeJSON(after.Body, &newInfo) != nil || oldInfo["uuid"] != nil || newInfo["uuid"] != nil || len(oldInfo["instance_start_time"]) == 0 {
			t.Fatal("unexpected native database information")
		}
		if bytes.Equal(oldInfo["instance_start_time"], newInfo["instance_start_time"]) {
			collision = true
			t.Log("native database creation-time collision", string(oldInfo["instance_start_time"]))
			break
		}
	}
	if !collision {
		t.Fatal("same-second database creation-time collision was not demonstrated")
	}
	newRev := nativeRevision(t, nativeRun(t, admin, Request{Method: "PUT", Path: path + "/same", Body: body}))
	if rev != newRev {
		t.Fatal("expected real deterministic revision alias was not demonstrated")
	}
	// Explicitly demonstrate the hazard in this owned database: the old revision
	// can update the replacement database. This is evidence, not a grid binding.
	nativeRevision(t, nativeRun(t, admin, Request{Method: "PUT", Path: path + "/same", Body: []byte(`{"_id":"same","_rev":"` + rev + `","value":8}`)}))
	if string(nativeDocument(t, admin, path+"/same")["value"]) != "8" {
		t.Fatal("old revision alias demonstration failed")
	}
}

func TestCouchDBNativeMangoOpaqueBookmarksAndChangesLifetime(t *testing.T) {
	admin := nativeOpen(t, "NYADB_COUCHDB_ADMIN_CONFIG")
	db := nativeID()
	path := "/" + db
	nativeRun(t, admin, Request{Method: "PUT", Path: path})
	t.Cleanup(func() { nativeRun(t, admin, Request{Method: "DELETE", Path: path}) })
	for i := range 7 {
		id := "row_" + strconv.Itoa(i)
		nativeRun(t, admin, Request{Method: "PUT", Path: path + "/" + id, Body: []byte(`{"_id":"` + id + `","value":` + strconv.Itoa(i) + `}`)})
	}
	bookmark := ""
	seen := map[string]bool{}
	for page := 0; page < 5; page++ {
		body, _ := json.Marshal(map[string]any{"selector": map[string]any{"_id": map[string]any{"$gte": "row_", "$lt": "row_z"}}, "limit": 3, "bookmark": bookmark})
		r := nativeRun(t, admin, Request{Method: "POST", Path: path + "/_find", Body: body})
		var result struct {
			Docs []struct {
				ID string `json:"_id"`
			} `json:"docs"`
			Bookmark string `json:"bookmark"`
		}
		if decodeJSON(r.Body, &result) != nil || result.Bookmark == "" {
			t.Fatal("native Mango bookmark missing")
		}
		for _, doc := range result.Docs {
			if seen[doc.ID] {
				t.Fatal("native bookmark repeated rows")
			}
			seen[doc.ID] = true
		}
		bookmark = result.Bookmark // opaque: never decode guessed internal UUIDs.
		if len(result.Docs) < 3 {
			break
		}
	}
	if len(seen) != 7 {
		t.Fatal("native Mango pagination incomplete")
	}
	// Observe the original continuous request. Its lifetime does not provide an
	// atomic guard for a separate document PUT, even if deletion closes it.
	cfg, secret := nativeFixture(t, "NYADB_COUCHDB_ADMIN_CONFIG")
	observer := nativeOpen(t, "NYADB_COUCHDB_ADMIN_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", cfg.Endpoint+path+"/_changes?feed=continuous&since=0&heartbeat=1000", nil)
	req.SetBasicAuth(cfg.Username, secret)
	response, err := observer.http.Do(req)
	if err != nil || response.StatusCode != 200 {
		t.Fatal("native continuous changes open failed")
	}
	defer response.Body.Close()
	first := make([]byte, 1)
	if _, err := io.ReadFull(response.Body, first); err != nil {
		t.Fatal("native changes initial observation failed")
	}
	settled := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, response.Body); settled <- err }()
	nativeRun(t, admin, Request{Method: "DELETE", Path: path})
	nativeRun(t, admin, Request{Method: "PUT", Path: path})
	select {
	case <-settled:
		t.Log("original continuous changes request settled after database deletion; no atomic cross-request binding claimed")
	case <-ctx.Done():
		<-settled
		t.Fatal("original native changes request survived deletion/recreation through observation deadline")
	}
}

func nativeRelay(t *testing.T, admin *Client, after func(http.ResponseWriter, *http.Request, Response) bool) *Client {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, requestLimit+1))
		if err != nil {
			w.WriteHeader(502)
			return
		}
		path := strings.TrimPrefix(r.URL.RequestURI(), "/proxy/couchdb")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		response, err := admin.Do(ctx, Request{Method: r.Method, Path: path, Body: body})
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
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, secret := nativeFixture(t, "NYADB_COUCHDB_ADMIN_CONFIG")
	cfg.Endpoint, cfg.CAFile, cfg.ServerName = server.URL+"/proxy/couchdb", ca, "example.com"
	c, err := Open(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal("verified native relay context", err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestCouchDBNativeLostWriteResponseNeverReplays(t *testing.T) {
	admin := nativeOpen(t, "NYADB_COUCHDB_ADMIN_CONFIG")
	id := nativeID()
	var writes atomic.Int32
	c := nativeRelay(t, admin, func(w http.ResponseWriter, r *http.Request, receipt Response) bool {
		if r.Method != "PUT" {
			return false
		}
		writes.Add(1)
		if receipt.Status != 201 {
			t.Error("native write did not commit before response loss")
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return true
		}
		_ = conn.Close()
		return true
	})
	request := Request{Method: "PUT", Path: "/nyadb_fixture/" + id, Body: []byte(`{"_id":"` + id + `","value":9}`)}
	_, err := c.Do(context.Background(), request)
	var lost *Interrupted
	if !errors.As(err, &lost) || !lost.WriteOutcomeUnknown || writes.Load() != 1 {
		t.Fatal("native lost write did not retire original worker exactly once")
	}
	if _, err := c.Do(context.Background(), request); !errors.Is(err, ErrRetired) || writes.Load() != 1 {
		t.Fatal("retired write was replayed")
	}
	fresh := nativeOpen(t, "NYADB_COUCHDB_CONFIG")
	if string(nativeDocument(t, fresh, request.Path)["value"]) != "9" {
		t.Fatal("native committed write lost or replayed")
	}
}

func TestCouchDBNativeCancellationSettlesOriginalWorker(t *testing.T) {
	for _, method := range []string{"GET", "PUT"} {
		t.Run(method, func(t *testing.T) {
			admin := nativeOpen(t, "NYADB_COUCHDB_ADMIN_CONFIG")
			id := nativeID()
			path := "/nyadb_fixture/" + id
			body := []byte(`{"_id":"` + id + `","value":10}`)
			if method == "GET" {
				nativeRun(t, admin, Request{Method: "PUT", Path: path, Body: body})
			}
			ack := make(chan struct{})
			var requests atomic.Int32
			c := nativeRelay(t, admin, func(_ http.ResponseWriter, r *http.Request, _ Response) bool {
				if r.Method != method || !strings.HasSuffix(r.URL.Path, "/"+id) {
					return false
				}
				requests.Add(1)
				close(ack)
				<-r.Context().Done()
				return true
			})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			request := Request{Method: method, Path: path}
			if method == "PUT" {
				request.Body = body
			}
			settled := make(chan error, 1)
			go func() { _, err := c.Do(ctx, request); settled <- err }()
			select {
			case <-ack:
			case <-ctx.Done():
				t.Fatal("native worker did not acknowledge original request")
			}
			if _, err := c.Do(context.Background(), Request{Method: "GET", Path: "/"}); !errors.Is(err, ErrBusy) {
				t.Fatal("concurrent request entered original client")
			}
			cancel()
			var interrupted *Interrupted
			select {
			case err := <-settled:
				if !errors.As(err, &interrupted) || !errors.Is(err, context.Canceled) || interrupted.WriteOutcomeUnknown != (method == "PUT") {
					t.Fatal("native cancel did not settle with correct outcome", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancel failed to settle original worker")
			}
			c.Close()
			c.Close()
			fresh := nativeOpen(t, "NYADB_COUCHDB_CONFIG")
			if string(nativeDocument(t, fresh, path)["value"]) != "10" || requests.Load() != 1 {
				t.Fatal("original cancellation changed independent client or replayed")
			}
		})
	}
}
