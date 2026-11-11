package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func nativeConfig(t *testing.T) (Config, string) {
	t.Helper()
	if os.Getenv("NYADB_CONSUL_IT") != "1" {
		t.Skip("owned native Consul fixture required")
	}
	return Config{Endpoint: os.Getenv("NYADB_CONSUL_ENDPOINT"), Auth: os.Getenv("NYADB_CONSUL_AUTH"), CAFile: os.Getenv("NYADB_CONSUL_CA"), ClientCertFile: os.Getenv("NYADB_CONSUL_CERT"), ClientKeyFile: os.Getenv("NYADB_CONSUL_KEY"), RequestTimeout: 30 * time.Second}, os.Getenv("NYADB_CONSUL_TOKEN")
}
func nativeClient(t *testing.T) (*Client, Config, string) {
	t.Helper()
	cfg, token := nativeConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, _, e := Open(ctx, cfg, token)
	if e != nil {
		t.Fatal("original Consul open failed", e)
	}
	t.Cleanup(func() { c.Close() })
	return c, cfg, token
}
func nativeDo(t *testing.T, c *Client, r Request) Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, e := c.Do(ctx, r)
	if e != nil || !result.Captured || !result.EOF || !result.Acknowledged || !result.Ended {
		var keys []struct{ Key string }
		_ = json.Unmarshal(result.Body, &keys)
		t.Fatal("original Consul native receipt incomplete", r.Method, r.Path, result.Status, keys, e)
	}
	return result
}
func nativeRead(t *testing.T, c *Client, key string) Entry {
	t.Helper()
	request, e := KeyReadRequest(key)
	if e != nil {
		t.Fatal(e)
	}
	r := nativeDo(t, c, request)
	if r.Txn != nil {
		r.Entries = r.Txn.Results
	}
	if r.Status != 200 || len(r.Entries) != 1 {
		t.Fatal("original KV missing", r.Status)
	}
	return r.Entries[0]
}
func keyFor(t *testing.T, s string) string {
	return "native/" + strings.ReplaceAll(t.Name(), "/", "-") + "/" + s
}
func u64(v uint64) *uint64 { return &v }
func str(v string) *string { return &v }

func TestConsulNativeIdentityScopeTransportAndNativeHeaders(t *testing.T) {
	c, cfg, token := nativeClient(t)
	v, e := c.Observe(context.Background())
	if e != nil || !v.Identity.Available || v.Identity.Version != os.Getenv("NYADB_CONSUL_VERSION") || v.Identity.NodeID != os.Getenv("NYADB_CONSUL_NODE_ID") || v.Identity.Datacenter != "dc1" || v.Keys.Metadata.Index == nil || v.Self.Status != 200 || !v.Self.Captured {
		t.Fatal("original identity/probe changed", e)
	}
	scoped := cfg
	scoped.Datacenter = "dc1"
	scoped.Prefix = "absent-original/"
	scoped.Consistency = "consistent"
	other, proof, e := Open(context.Background(), scoped, token)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	if other.Config().Datacenter != "dc1" || proof.Keys.Kind != "txn" && proof.Keys.Metadata.Index == nil {
		t.Fatal("original configured scope changed")
	}
	if cfg.CAFile != "" {
		for _, change := range []string{"hostname", "untrusted", "client"} {
			t.Run(change, func(t *testing.T) {
				bad := cfg
				if change == "hostname" {
					bad.ServerName = "foreign.invalid"
				} else if change == "untrusted" {
					bad.CAFile = ""
				} else {
					if cfg.ClientCertFile == "" {
						if _, e := New(Config{Endpoint: cfg.Endpoint, ClientCertFile: "missing"}, ""); e == nil {
							t.Fatal("unpaired client certificate accepted")
						}
						return
					}
					bad.ClientCertFile, bad.ClientKeyFile = "", ""
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				client, _, e := Open(ctx, bad, token)
				if client != nil {
					client.Close()
				}
				if e == nil {
					t.Fatal("invalid original TLS identity published")
				}
			})
		}
	}
	if _, e := New(Config{Endpoint: cfg.Endpoint, Namespace: "foreign"}, ""); e == nil {
		t.Fatal("unverified Enterprise namespace accepted")
	}
	for _, path := range []string{"/v1/status/peers", "/v1/catalog/datacenters", "/v1/catalog/nodes", "/v1/catalog/services", "/v1/health/state/any", "/v1/health/state/passing", "/v1/health/state/warning", "/v1/health/state/critical", "/v1/session/list"} {
		response := nativeDo(t, c, Request{Method: "GET", Path: path})
		if response.Status != 200 || !response.Succeeded {
			t.Fatal("native read endpoint not acknowledged", path)
		}
	}
}
func TestConsulNativeBinaryValuesIndicesRawAndConditionalWrites(t *testing.T) {
	c, _, _ := nativeClient(t)
	for _, key := range []string{keyFor(t, "unicode 雪/%?#+"), keyFor(t, "literal/../dot"), keyFor(t, "repeated//slash"), keyFor(t, "trailing/"), "/" + keyFor(t, "leading")} {
		value := []byte{0, 255, 128, 13, 10, 0, 'x'}
		r, buildErr := KeyPutRequest(key, value, u64(math.MaxUint64), u64(0))
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		ack, err := c.Do(context.Background(), r)
		if (strings.Contains(key, "/../") || strings.HasPrefix(key, "/")) && os.Getenv("NYADB_CONSUL_VERSION") == "2.0.2" {
			if err == nil || ack.Status != 400 || !ack.Captured || !ack.Acknowledged || ack.Succeeded || ack.WriteOutcomeUnknown {
				t.Fatal("native traversal rejection changed", err)
			}
			cleaned := keyFor(t, "dot")
			if strings.HasPrefix(key, "/") {
				cleaned = strings.TrimPrefix(key, "/")
			}
			if nativeDo(t, c, readRequest(cleaned)).Status != 404 {
				t.Fatal("rejected traversal was silently rewritten")
			}
			continue
		}
		if err != nil {
			t.Fatalf("key %q: native write %d: %s (%v)", key, ack.Status, ack.Body, err)
		}
		if !ack.Succeeded || ack.Txn == nil && !bytes.Equal(bytes.TrimSpace(ack.Body), []byte("true")) || ack.Txn != nil && !ack.Txn.Committed {
			t.Fatal("native creation false")
		}
		entry := nativeRead(t, c, key)
		if entry.Key != key || !bytes.Equal(entry.Value, value) || entry.Flags != math.MaxUint64 || entry.ModifyIndex == 0 || entry.CreateIndex == 0 || entry.Session != "" || !entry.ValuePresent || entry.ValueNull {
			t.Fatal("native bytes/indices/flags changed")
		}
		raw, rawErr := KeyPath(key)
		var rawRequest Request
		if rawErr == nil {
			rawRequest = Request{Method: "GET", Path: raw, Query: url.Values{"raw": {""}}}
		} else {
			rawRequest, _ = KeyReadRequest(key)
		}
		original := nativeDo(t, c, rawRequest)
		rawValue := original.Body
		if original.Txn != nil {
			rawValue = original.Txn.Results[0].Value
		}
		if !bytes.Equal(rawValue, value) {
			t.Fatal("raw bytes altered")
		}
		stale, _ := KeyPutRequest(key, []byte("foreign"), nil, u64(entry.ModifyIndex+100000))
		failed, e := c.Do(context.Background(), stale)
		if !errors.Is(e, ErrConflict) || !failed.Acknowledged || failed.WriteOutcomeUnknown || failed.Succeeded {
			t.Fatal("false native CAS promoted", e)
		}
		if !bytes.Equal(nativeRead(t, c, key).Value, value) {
			t.Fatal("failed original CAS applied")
		}
		update, _ := KeyPutRequest(key, []byte{}, u64(0), u64(entry.ModifyIndex))
		nativeDo(t, c, update)
		empty := nativeRead(t, c, key)
		if !empty.ValuePresent || len(empty.Value) != 0 || empty.Flags != 0 || empty.CreateIndex != entry.CreateIndex || empty.ModifyIndex <= entry.ModifyIndex {
			t.Fatalf("empty/zero metadata changed: original=%+v updated=%+v", entry, empty)
		}
		emptyRaw := nativeDo(t, c, rawRequest)
		emptyBytes := emptyRaw.Body
		if emptyRaw.Txn != nil {
			emptyBytes = emptyRaw.Txn.Results[0].Value
		}
		if len(emptyBytes) != 0 {
			t.Fatal("native null empty value had nonempty raw bytes")
		}
		del, _ := KeyDeleteRequest(key, u64(empty.ModifyIndex), false)
		nativeDo(t, c, del)
		missingRequest, _ := KeyReadRequest(key)
		missing, missingErr := c.Do(context.Background(), missingRequest)
		if (missing.Kind == "txn" && (!errors.Is(missingErr, ErrConflict) || missing.Status != 409 || !missing.Acknowledged || missing.WriteOutcomeUnknown)) || (missing.Kind != "txn" && (missingErr != nil || missing.Status != 404 || len(missing.Entries) > 0)) {
			t.Fatal("missing key not original 404")
		}
	}
	limit := putRequest(keyFor(t, "limit"), bytes.Repeat([]byte{255}, valueLimit))
	nativeDo(t, c, limit)
	if len(nativeRead(t, c, keyFor(t, "limit")).Value) != valueLimit {
		t.Fatal("native maximum value truncated")
	}
	prefix := keyFor(t, "")
	listRequest, err := KeyListRequest(prefix, true)
	if err != nil {
		t.Fatal(err)
	}
	list := nativeDo(t, c, listRequest)
	if list.Kind != "txn" && (list.Metadata.Index == nil || list.Metadata.KnownLeader == nil || !*list.Metadata.KnownLeader) {
		t.Fatal("native read headers absent")
	}
	recurseRequest, _ := KeyListRequest(prefix, false)
	recurseRequest.Query = url.Values{"consistent": {""}}
	if recurseRequest.Method == "GET" {
		recurseRequest.Query.Set("recurse", "")
	}
	recursive := nativeDo(t, c, recurseRequest)
	if recursive.Txn != nil {
		recursive.Entries = recursive.Txn.Results
	}
	if len(recursive.Entries) != 1 {
		t.Fatal("native recursive scope changed", len(recursive.Entries))
	}
	staleRead := listRequest.Clone()
	if staleRead.Query == nil {
		staleRead.Query = url.Values{}
	}
	staleRead.Query.Set("stale", "")
	nativeDo(t, c, staleRead)
}
func TestConsulNativeAtomicTransactionsComparisonsAndOriginalResults(t *testing.T) {
	c, _, _ := nativeClient(t)
	key := keyFor(t, "source")
	dst := keyFor(t, "destination")
	old := []byte("original\x00")
	r := putRequest(key, old)
	r.Query = url.Values{"flags": {"18446744073709551615"}}
	nativeDo(t, c, r)
	e := nativeRead(t, c, key)
	value := []byte{255, 0, 'n'}
	makeTxn := func(index uint64) Request {
		r, err := TxnRequest([]TxnOperation{{KV: &TxnKV{Verb: "check-index", Key: key, Index: u64(index)}}, {KV: &TxnKV{Verb: "check-not-exists", Key: dst}}, {KV: &TxnKV{Verb: "set", Key: dst, Value: &value, Flags: u64(e.Flags)}}, {KV: &TxnKV{Verb: "delete-cas", Key: key, Index: u64(index)}}, {KV: &TxnKV{Verb: "get", Key: dst}}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	rejected, err := c.Do(context.Background(), makeTxn(e.ModifyIndex+10000))
	if !errors.Is(err, ErrConflict) || rejected.Txn == nil || rejected.Txn.Committed || !rejected.Acknowledged || rejected.WriteOutcomeUnknown || len(rejected.Txn.Errors) == 0 {
		t.Fatal("native atomic rejection unknown", err)
	}
	if !bytes.Equal(nativeRead(t, c, key).Value, old) || nativeDo(t, c, readRequest(dst)).Status != 404 {
		t.Fatal("rejected txn left partial writes")
	}
	committed := nativeDo(t, c, makeTxn(e.ModifyIndex))
	if committed.Txn == nil || !committed.Txn.Committed || len(committed.Txn.Results) == 0 || nativeDo(t, c, readRequest(key)).Status != 404 {
		t.Fatal("original atomic txn not confirmed")
	}
	target := nativeRead(t, c, dst)
	if !bytes.Equal(target.Value, value) || target.Flags != math.MaxUint64 {
		t.Fatal("atomic original bytes/flags lost")
	}
	readOnly, err := TxnRequest([]TxnOperation{{KV: &TxnKV{Verb: "get", Key: dst}}, {KV: &TxnKV{Verb: "get-tree", Key: keyFor(t, "")}}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := c.Prepare(readOnly)
	if err != nil || plan.Writes() {
		t.Fatal("read-only native txn classified as write", err)
	}
	nativeDo(t, c, readOnly)
}
func createSession(t *testing.T, c *Client, behavior string) string {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"Name": t.Name(), "TTL": "10s", "LockDelay": "0s", "Behavior": behavior, "NodeChecks": []string{}})
	r := nativeDo(t, c, Request{Method: "PUT", Path: "/v1/session/create", Body: raw})
	if !validID(r.SessionID) {
		t.Fatal("original session ID missing")
	}
	return r.SessionID
}
func TestConsulNativeSessionLocksRenewalAndNativeRejections(t *testing.T) {
	c, _, _ := nativeClient(t)
	first, second := createSession(t, c, "release"), createSession(t, c, "release")
	key := keyFor(t, "locked")
	value := []byte("original lock\x00")
	lock := putRequest(key, value)
	lock.Query = url.Values{"acquire": {first}, "flags": {"18446744073709551615"}}
	nativeDo(t, c, lock)
	original := nativeRead(t, c, key)
	if original.Session != first || original.LockIndex == 0 || original.Flags != math.MaxUint64 {
		t.Fatal("native lock metadata changed")
	}
	other := []byte("other")
	txn, err := TxnRequest([]TxnOperation{{KV: &TxnKV{Verb: "check-index", Key: key, Index: u64(original.ModifyIndex)}}, {KV: &TxnKV{Verb: "lock", Key: key, Value: &other, Session: str(second)}}})
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := c.Do(context.Background(), txn)
	if !errors.Is(err, ErrConflict) || rejected.Txn == nil || !rejected.Acknowledged || rejected.WriteOutcomeUnknown {
		t.Fatal("native lock conflict not known", err)
	}
	if !bytes.Equal(nativeRead(t, c, key).Value, value) || nativeRead(t, c, key).Session != first {
		t.Fatal("failed original lock changed state")
	}
	renew := nativeDo(t, c, Request{Method: "PUT", Path: "/v1/session/renew/" + first})
	if renew.SessionID != first {
		t.Fatal("renewal changed original identity")
	}
	unlock, err := TxnRequest([]TxnOperation{{KV: &TxnKV{Verb: "check-session", Key: key, Session: str(first)}}, {KV: &TxnKV{Verb: "unlock", Key: key, Value: &value, Flags: u64(original.Flags), Session: str(first)}}})
	if err != nil {
		t.Fatal(err)
	}
	nativeDo(t, c, unlock)
	released := nativeRead(t, c, key)
	if released.Session != "" || !bytes.Equal(released.Value, value) || released.Flags != original.Flags {
		t.Fatal("native unlock changed bytes/flags")
	}
	for _, id := range []string{first, second} {
		nativeDo(t, c, Request{Method: "PUT", Path: "/v1/session/destroy/" + id})
	}
	expiry := createSession(t, c, "delete")
	expireKey := keyFor(t, "natural-expiry")
	r := putRequest(expireKey, nil)
	r.Query = url.Values{"acquire": {expiry}}
	nativeDo(t, c, r)
	deadline := time.Now().Add(40 * time.Second)
	for {
		r := nativeDo(t, c, readRequest(expireKey))
		if r.Status == 404 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("original session did not naturally expire")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// The relay uses the actual original upstream HTTP/TLS/mTLS configuration. It
// withholds or faults the client reply only after Consul returns a real PUT ACK.
func nativeFault(t *testing.T, cfg Config, token, mode string) (Config, *atomic.Int32, <-chan struct{}) {
	t.Helper()
	up, e := New(cfg, token)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { up.Close() })
	calls := &atomic.Int32{}
	committed := make(chan struct{})
	var once atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		request, e := http.NewRequestWithContext(r.Context(), r.Method, cfg.Endpoint+r.URL.RequestURI(), bytes.NewReader(raw))
		if e != nil {
			t.Error(e)
			return
		}
		request.Header = r.Header.Clone()
		request.GetBody = nil
		request.Close = true
		response, e := up.http.Do(request)
		if e != nil {
			return
		}
		body, e := io.ReadAll(response.Body)
		response.Body.Close()
		if e != nil {
			return
		}
		ack := bytes.Equal(bytes.TrimSpace(body), []byte("true")) || r.URL.Path == "/v1/txn" || strings.HasPrefix(r.URL.Path, "/v1/session/") && !bytes.Equal(bytes.TrimSpace(body), []byte("false"))
		if (r.Method == "PUT" || r.Method == "DELETE") && response.StatusCode == 200 && ack && once.CompareAndSwap(false, true) {
			close(committed)
			switch mode {
			case "lost":
				conn, _, e := w.(http.Hijacker).Hijack()
				if e == nil {
					conn.Close()
				}
				return
			case "partial":
				conn, b, e := w.(http.Hijacker).Hijack()
				if e == nil {
					defer conn.Close()
					fmt.Fprintf(b, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", len(body)+1)
					b.Write(body)
					b.Flush()
				}
				return
			case "http500", "http408":
				status := 500
				if mode == "http408" {
					status = 408
				}
				w.WriteHeader(status)
				w.Write([]byte("actual committed reply withheld"))
				return
			case "cancel", "close":
				<-r.Context().Done()
				return
			}
		}
		for k, values := range response.Header {
			for _, value := range values {
				w.Header().Add(k, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		w.Write(body)
	}))
	t.Cleanup(s.Close)
	proxy := cfg
	proxy.Endpoint = s.URL
	proxy.CAFile, proxy.ServerName, proxy.ClientCertFile, proxy.ClientKeyFile = "", "", "", ""
	return proxy, calls, committed
}
func TestConsulNativeActualCommittedReplyLossCancellationAndOriginalClose(t *testing.T) {
	base, cfg, token := nativeClient(t)
	for _, mode := range []string{"lost", "partial", "http500", "http408", "cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			relay, calls, committed := nativeFault(t, cfg, token, mode)
			c, e := New(relay, token)
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			key := keyFor(t, "written")
			value := []byte("actually committed " + mode)
			p, e := c.Prepare(putRequest(key, value))
			if e != nil {
				t.Fatal(e)
			}
			earlier, _ := c.Prepare(putRequest(key+"-later", nil))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			var r Response
			var err error
			go func() { r, err = c.Execute(ctx, p); close(done) }()
			wait(t, committed)
			if mode == "cancel" {
				cancel()
			} else if mode == "close" {
				closed := make(chan struct{})
				go func() { c.Close(); close(closed) }()
				wait(t, closed)
			}
			wait(t, done)
			if err == nil || !r.Attempted || !r.Ended || !r.WriteOutcomeUnknown || r.Acknowledged || calls.Load() != 1 || !bytes.Equal(nativeRead(t, base, key).Value, value) {
				t.Fatal("actual applied write lost original unknown state", err)
			}
			if mode == "http500" || mode == "http408" {
				if !r.Captured || !c.Alive() {
					t.Fatal("complete native HTTP error became missing transport")
				}
				if _, e = c.Execute(context.Background(), earlier); !errors.Is(e, ErrWriteUnknown) {
					t.Fatal("prepared later write bypassed original unknown", e)
				}
				nativeDo(t, c, readRequest(key))
				unknown, ok := c.UnknownResponse()
				if !ok || unknown.Status != r.Status || !unknown.WriteOutcomeUnknown || unknown.Request.Path != r.Request.Path || calls.Load() != 2 {
					t.Fatal("later real read rewrote original write")
				}
			}
			if mode == "partial" && (!bytes.Equal(r.Body, []byte("true")) || r.Captured || r.EOF) {
				t.Fatal("full ACK bytes without EOF completed")
			}
		})
	}
	key := keyFor(t, "blocking")
	nativeDo(t, base, putRequest(key, []byte("original")))
	observed := nativeDo(t, base, readRequest(key))
	if observed.Metadata.Index == nil {
		t.Fatal("original blocking index absent")
	}
	for _, closeClient := range []bool{false, true} {
		t.Run(fmt.Sprint(closeClient), func(t *testing.T) {
			c, e := New(cfg, token)
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := readRequest(key)
			r.Query = url.Values{"index": {fmt.Sprint(*observed.Metadata.Index)}, "wait": {"5m"}}
			p, e := c.Prepare(r)
			if e != nil {
				t.Fatal(e)
			}
			done := make(chan struct{})
			var result Response
			var err error
			go func() { result, err = c.Execute(ctx, p); close(done) }()
			select {
			case <-done:
				t.Fatal("actual blocking query returned before its original wait")
			case <-time.After(150 * time.Millisecond):
			}
			if closeClient {
				c.Close()
			} else {
				cancel()
			}
			wait(t, done)
			if err == nil || !result.Attempted || !result.Ended || result.WriteOutcomeUnknown || !result.Canceled || c.Alive() {
				t.Fatal("original blocking read cancellation changed write state", err)
			}
		})
	}
}
func TestConsulNativeRestrictedKVOnlyPermissionsAndOriginalDenial(t *testing.T) {
	root, cfg, token := nativeClient(t)
	nativeDo(t, root, putRequest("allowed/native", []byte{0, 255}))
	nativeDo(t, root, putRequest("outside/native", []byte("private")))
	if cfg.Auth == "none" {
		bad := cfg
		bad.Auth = "none"
		if _, e := New(bad, "unexpected-token"); e == nil {
			t.Fatal("anonymous config silently authenticated")
		}
		return
	}
	readonly := os.Getenv("NYADB_CONSUL_READ_TOKEN")
	limited, proof, e := Open(context.Background(), cfg, readonly)
	if e != nil {
		t.Fatal("KV-only token could not connect", e)
	}
	defer limited.Close()
	if proof.Identity.Available || proof.Identity.Version != "" || proof.Self.Status != 403 || !proof.Self.Acknowledged || proof.Keys.Metadata.ACLFiltered == nil || !*proof.Keys.Metadata.ACLFiltered {
		t.Fatal("denied original identity/filtered scope invented")
	}
	if !bytes.Equal(nativeRead(t, limited, "allowed/native").Value, []byte{0, 255}) {
		t.Fatal("allowed original bytes unavailable")
	}
	denied, e := limited.Do(context.Background(), putRequest("allowed/native", []byte("forbidden")))
	if e == nil || denied.Status != 403 || !denied.Acknowledged || denied.WriteOutcomeUnknown || !denied.Captured || !bytes.Equal(nativeRead(t, root, "allowed/native").Value, []byte{0, 255}) {
		t.Fatal("actual ACL denial changed native write evidence", e)
	}
	bad := cfg
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	invalid, _, e := Open(ctx, bad, "00000000-0000-0000-0000-000000000001")
	if invalid != nil {
		invalid.Close()
	}
	if e == nil {
		t.Fatal("invalid original token published")
	}
	_ = token
}
