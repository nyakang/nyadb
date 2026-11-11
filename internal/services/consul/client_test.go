package consul

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c, e := New(Config{Endpoint: s.URL}, "")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	return c, s
}
func putRequest(key string, body []byte) Request {
	path, _ := KeyPath(key)
	return Request{Method: "PUT", Path: path, Body: body}
}
func readRequest(key string) Request {
	path, _ := KeyPath(key)
	return Request{Method: "GET", Path: path}
}
func wait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("original executor did not acknowledge")
	}
}
func TestOriginalPlansCloneScopeBytesOneUseAndPreDispatchCancellation(t *testing.T) {
	var calls atomic.Int32
	c, s := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		if !bytes.Equal(b, []byte("original\x00")) || r.URL.Query().Get("flags") != "18446744073709551615" {
			t.Error("prepared request changed")
		}
		w.Write([]byte("true"))
	})
	r := putRequest("dir/key?%雪", []byte("original\x00"))
	r.Query = url.Values{"flags": {fmt.Sprint(uint64(math.MaxUint64))}}
	p, e := c.Prepare(r)
	if e != nil || calls.Load() != 0 {
		t.Fatal("prepare dispatched", e)
	}
	r.Body[0] = 'x'
	r.Query.Set("flags", "1")
	copy := p.Snapshot()
	copy.Body[0] = 'z'
	copy.Query.Set("flags", "2")
	result, e := c.Execute(context.Background(), p)
	if e != nil || !result.Attempted || !result.Ended || !result.Acknowledged || !result.Captured || !result.EOF {
		t.Fatal("native ACK not retained", e)
	}
	if _, e = c.Execute(context.Background(), p); !errors.Is(e, ErrPlanUsed) || calls.Load() != 1 {
		t.Fatal("plan replayed", e)
	}
	other, e := New(Config{Endpoint: s.URL}, "")
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	foreign, _ := other.Prepare(putRequest("key", nil))
	if _, e = c.Execute(context.Background(), foreign); !errors.Is(e, ErrPlanUsed) {
		t.Fatal("foreign plan dispatched", e)
	}
	p, _ = c.Prepare(putRequest("key", nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, e = c.Execute(ctx, p)
	if !errors.Is(e, context.Canceled) || result.Attempted || result.WriteOutcomeUnknown || calls.Load() != 1 {
		t.Fatal("cancelled preparation dispatched", e)
	}
	if _, e = c.Execute(context.Background(), p); !errors.Is(e, ErrPlanUsed) {
		t.Fatal("cancelled one-use plan revived")
	}
	if !bytes.Equal(c.LastResponse().Body, nil) { // the pre-dispatch cancellation is the most recent original receipt
		t.Fatal("last receipt replaced by an inspection")
	}
}
func TestNativeAcknowledgementSurvivesInvalidMetadataAndInspectionMutation(t *testing.T) {
	for _, header := range [][]string{{"-1"}, {"18446744073709551616"}, {"12", "12"}} {
		t.Run(strings.Join(header, "/"), func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				for _, h := range header {
					w.Header().Add("X-Consul-Index", h)
				}
				w.Header().Set("X-Original-Extension", "unmodified")
				w.Write([]byte("true\n"))
			})
			result, e := c.Do(context.Background(), putRequest("key", nil))
			if !errors.Is(e, ErrProtocol) || !result.Acknowledged || !result.Succeeded || result.WriteOutcomeUnknown || !result.Captured || result.Metadata.Index != nil || result.ProjectionError == "" {
				t.Fatal("metadata error rewrote native ACK", e)
			}
			result.Body[0] = 'x'
			result.Header.Set("X-Original-Extension", "changed")
			last := c.LastResponse()
			if !bytes.Equal(last.Body, []byte("true\n")) || last.Header.Get("X-Original-Extension") != "unmodified" {
				t.Fatal("original receipt aliased inspection")
			}
			if _, e = c.Prepare(putRequest("another", nil)); e != nil {
				t.Fatal("known ACK invented write barrier", e)
			}
		})
	}
}
func TestNativeFalseAndAtomicConflictRemainDifferentFromUnknownWrite(t *testing.T) {
	for _, v := range []struct {
		status int
		body   string
		kind   string
	}{{200, "false", "kv"}, {403, "permission denied", "kv"}, {409, `{"Results":null,"Errors":[{"OpIndex":0,"What":"index changed"}]}`, "txn"}} {
		t.Run(fmt.Sprint(v.status), func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(v.status); w.Write([]byte(v.body)) })
			r := putRequest("key", nil)
			if v.kind == "txn" {
				value := []byte("x")
				r, _ = TxnRequest([]TxnOperation{{KV: &TxnKV{Verb: "set", Key: "key", Value: &value}}})
			}
			result, e := c.Do(context.Background(), r)
			if e == nil || !result.Acknowledged || result.Succeeded || result.WriteOutcomeUnknown || !result.Captured || !c.Alive() {
				t.Fatal("native rejection not kept as known", e)
			}
			if _, e = c.Prepare(putRequest("later", nil)); e != nil {
				t.Fatal("known rejection barred later explicit request", e)
			}
		})
	}
}
func TestActualHTTPFailureAndMalformedAcknowledgementKeepStickyOriginalWrite(t *testing.T) {
	for _, v := range []struct {
		status int
		body   string
	}{{500, "request completed but reply lost"}, {200, "tru"}, {200, "true\nfalse"}} {
		t.Run(fmt.Sprint(v.status)+"/"+v.body, func(t *testing.T) {
			var calls atomic.Int32
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method == "GET" {
					w.Write([]byte(`{"original":"read"}`))
					return
				}
				w.WriteHeader(v.status)
				w.Write([]byte(v.body))
			})
			before, _ := c.Prepare(putRequest("later", nil))
			original, e := c.Do(context.Background(), putRequest("key", []byte("original")))
			if e == nil || !original.Captured || original.Acknowledged || !original.WriteOutcomeUnknown {
				t.Fatal("write failure promoted", e)
			}
			if _, e = c.Execute(context.Background(), before); !errors.Is(e, ErrWriteUnknown) {
				t.Fatal("earlier preparation bypassed barrier", e)
			}
			read, e := c.Do(context.Background(), Request{Method: "GET", Path: "/v1/agent/self"})
			if e != nil || !read.Acknowledged {
				t.Fatal("later explicit read unavailable", e)
			}
			original.Body[0] = 'x'
			unknown, ok := c.UnknownResponse()
			if !ok || !bytes.Equal(unknown.Body, []byte(v.body)) || unknown.Request.Method != "PUT" || calls.Load() != 2 {
				t.Fatal("later read or inspection changed original unknown")
			}
		})
	}
}
func TestFullBooleanBytesWithoutOriginalEOFRemainUnconfirmed(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		h := w.(http.Hijacker)
		conn, b, e := h.Hijack()
		if e != nil {
			t.Error(e)
			return
		}
		defer conn.Close()
		b.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 5\r\nX-Original: retained\r\n\r\ntrue")
		b.Flush()
	})
	r, e := c.Do(context.Background(), putRequest("key", nil))
	if e == nil || r.EOF || r.Captured || r.Acknowledged || !r.WriteOutcomeUnknown || !bytes.Equal(r.Body, []byte("true")) || r.Header.Get("X-Original") != "retained" || c.Alive() {
		t.Fatal("partial original response completed", e)
	}
}
func TestOriginalCancellationCloseAndBusyJoinTheirOwnHTTPExecutor(t *testing.T) {
	for _, closeClient := range []bool{false, true} {
		t.Run(fmt.Sprint(closeClient), func(t *testing.T) {
			entered, settled := make(chan struct{}), make(chan struct{})
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(settled) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p, _ := c.Prepare(putRequest("key", nil))
			done := make(chan struct{})
			var result Response
			var err error
			go func() { result, err = c.Execute(ctx, p); close(done) }()
			wait(t, entered)
			other, _ := c.Prepare(putRequest("other", nil))
			if _, e := c.Execute(ctx, other); !errors.Is(e, ErrBusy) {
				t.Fatal("busy request dispatched", e)
			}
			if closeClient {
				closed := make(chan struct{})
				go func() { c.Close(); close(closed) }()
				wait(t, closed)
			} else {
				cancel()
			}
			wait(t, done)
			wait(t, settled)
			if err == nil || !result.Attempted || !result.Ended || !result.WriteOutcomeUnknown || !result.Canceled || c.Alive() {
				t.Fatal("cancel/close lost original interruption", err)
			}
		})
	}
}
func TestScopedNativeURLNoTokenQueryRedirectOrResend(t *testing.T) {
	var original, destination atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destination.Add(1); w.Write([]byte("true")) }))
	defer foreign.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		original.Add(1)
		if r.URL.Query().Get("dc") != "dc-original" || r.URL.Query().Has("token") || r.Header.Get("X-Consul-Token") != "temporary-secret" {
			t.Error("original scope/credential changed")
		}
		w.Header().Set("Location", foreign.URL+"/v1/kv/foreign")
		w.WriteHeader(307)
		w.Write([]byte("redirect response retained"))
	}))
	defer source.Close()
	c, e := New(Config{Endpoint: source.URL, Auth: "token", Datacenter: "dc-original", Consistency: "consistent"}, "temporary-secret")
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	r, e := c.Do(context.Background(), putRequest("original", nil))
	if e == nil || !r.Captured || !r.WriteOutcomeUnknown || original.Load() != 1 || destination.Load() != 0 {
		t.Fatal("redirect replayed original write", e)
	}
}
func TestExactNativeRequestValidationAndUint64ValueMetadata(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("validation sent HTTP") })
	for _, r := range []Request{
		{Method: "GET", Path: "/v1/kv/key", Query: url.Values{"token": {"secret"}}},
		{Method: "GET", Path: "/v1/kv/key", Query: url.Values{"dc": {"foreign"}}},
		{Method: "GET", Path: "/v1/kv/key", Query: url.Values{"index": {"18446744073709551616"}}},
		{Method: "PUT", Path: "/v1/kv/key", Query: url.Values{"cas": {"0"}, "acquire": {"00000000-0000-0000-0000-000000000001"}}},
		{Method: "GET", Path: "/v1/kv/key", Query: url.Values{"consistent": {""}, "stale": {""}}},
		{Method: "GET", Path: "/v1/kv/key", Query: url.Values{"raw": {""}, "keys": {""}}},
		{Method: "GET", Path: "/v1/kv/%2e%2e%2foutside"},
		{Method: "PUT", Path: "/v1/txn", Body: []byte(`[{"KV":{"Verb":"set","Key":"key","Value":"","Value":""}}]`)},
		{Method: "PUT", Path: "/v1/txn", Body: []byte(`[{"KV":{"Verb":"cas","Key":"key","Value":"","Index":18446744073709551616}}]`)},
		{Method: "PUT", Path: "/v1/txn", Body: []byte(`[{"KV":{"Verb":"set","Key":"key","Value":null}}]`)},
		{Method: "PUT", Path: "/v1/kv/key", Body: make([]byte, valueLimit+1)},
	} {
		if _, e := c.Prepare(r); e == nil {
			t.Fatal("invalid native request prepared")
		}
	}
	raw := []byte(`[{"Key":"key","Value":"AP8=","Flags":18446744073709551615,"CreateIndex":9007199254740993,"ModifyIndex":18446744073709551614,"LockIndex":0,"Extra":{"native":true}}]`)
	entries, e := decodeEntries(raw)
	if e != nil || len(entries) != 1 || !bytes.Equal(entries[0].Value, []byte{0, 255}) || entries[0].Flags != math.MaxUint64 || entries[0].CreateIndex != 9007199254740993 || entries[0].ModifyIndex != math.MaxUint64-1 {
		t.Fatal("native byte/index precision lost", e)
	}
	for _, bad := range []string{`[{"Key":"key","Value":"AB=="}]`, `[{"Key":"key","Value":""},{"Key":"key","Value":""}]`, `[{"Key":"key","Value":"","Flags":-1}]`} {
		if _, e := decodeEntries([]byte(bad)); e == nil {
			t.Fatal("invalid native entry projected")
		}
	}
	null, e := decodeEntries([]byte(`[{"Key":"key","Value":null}]`))
	if e != nil || !null[0].ValueNull || !null[0].ValuePresent {
		t.Fatal("null became empty bytes")
	}
	absent, e := decodeEntries([]byte(`[{"Key":"key"}]`))
	if e != nil || absent[0].ValuePresent || absent[0].HasField("ModifyIndex") || !absent[0].HasField("Key") {
		t.Fatal("absent became null bytes")
	}
}
