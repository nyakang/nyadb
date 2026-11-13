package consul

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeSessionKeysRequest(id string) contract.KVHTTPSessionKeysRequest {
	return contract.KVHTTPSessionKeysRequest{ID: id, MaxRows: contract.KVHTTPSessionKeysMaxRows, MaxBytes: contract.KVHTTPSessionKeysMaxBytes}
}

func nativeSessionKeysFacet(t *testing.T, lease contract.KVRequestLease) contract.KVHTTPSessionKeysBrowsing {
	t.Helper()
	facet, ok := lease.(contract.KVHTTPSessionKeysBrowsing)
	if !ok {
		t.Fatal("original native document lease lost optional held-key observation")
	}
	return facet
}

func nativeSessionKeysAcquire(t *testing.T, base *Client, id, key string, value []byte) {
	t.Helper()
	r, _ := sessionToolRequest(t, contract.KVToolHTTPAcquire, contract.KVRequestToolInput{Key: key, ValueBase64: base64.StdEncoding.EncodeToString(value), Flags: "18446744073709551615", HTTPSession: contract.KVHTTPSessionToolInput{ID: id}})
	nativeDo(t, base, r)
}

func TestConsulNativeSessionHeldKeysWholeRootOriginalOrderBytesFlagsAndBounds(t *testing.T) {
	base, cfg, token := nativeClient(t)
	cfg.Prefix, cfg.Datacenter, cfg.Consistency = "configured-empty/", "dc1", "consistent"
	s, err := OpenNativeSession(context.Background(), 2201, cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	id := nativeSessionToolCreate(t, base, t.Name(), "10m", "release")
	defer nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, id))
	keys := []string{keyFor(t, "outside/中文 %?#"), keyFor(t, "outside/second")}
	value := []byte{0, 255, 128, 13, 10}
	for _, key := range keys {
		nativeSessionKeysAcquire(t, base, id, key, value)
	}
	other := nativeSessionToolCreate(t, base, t.Name()+"-unmatched", "10m", "release")
	defer nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, other))
	nativeSessionKeysAcquire(t, base, other, keyFor(t, "outside/unmatched"), bytes.Repeat([]byte{1}, 2048))
	facet := nativeSessionKeysFacet(t, l)
	request := nativeSessionKeysRequest(id)
	observed, err := facet.ObserveHTTPSessionKeys(context.Background(), request)
	if err != nil || !observed.ProjectionComplete || !observed.Complete || len(observed.Matches) != 2 || string(observed.Scope.Prefix) != cfg.Prefix || observed.Scope.Generation != 1 || observed.Scope.Namespace.ProfileID != 2201 || observed.Scope.HTTP.Endpoint != cfg.Endpoint || observed.Request != request {
		t.Fatal("whole-root observation narrowed configured prefix or changed owning scope", err)
	}
	h := observed.Root.HTTP
	u, err := url.Parse(h.URL)
	if err != nil || u.Path != "/v1/kv/" || !u.Query().Has("recurse") || u.Query().Get("dc") != "dc1" || !u.Query().Has("consistent") || u.Query().Has("session") || u.Query().Has("token") || !h.Acknowledged || !h.EOF {
		t.Fatal("explicit native root request invented a Session query or escaped scope")
	}
	var original []json.RawMessage
	if json.Unmarshal(h.Body, &original) != nil || len(original) != len(h.Entries) {
		t.Fatal("original root response order or rows lost")
	}
	for _, index := range observed.Matches {
		row := h.Entries[index]
		var key string
		var fields map[string]json.RawMessage
		if json.Unmarshal(original[index], &fields) != nil || json.Unmarshal(fields["Key"], &key) != nil || key != row.Key || row.Session != id || row.Flags != math.MaxUint64 || row.ModifyIndex == 0 || !bytes.Equal(row.Value, value) || !bytes.Equal(nativeRead(t, base, key).Value, value) {
			t.Fatal("matching root row replaced original bytes, indices or uint64 flags")
		}
	}
	clone := observed.Clone()
	clone.Matches[0] = -1
	clone.Root.HTTP.Entries[observed.Matches[0]].Value[0] = 99
	clone.Root.HTTP.Body[0] = 'X'
	if !reflect.DeepEqual(observed.Root, l.LastResult()) {
		t.Fatal("optional observation aliases original receipt cache")
	}
	for _, bounds := range []contract.KVHTTPSessionKeysRequest{{ID: id, MaxRows: 1, MaxBytes: request.MaxBytes}, {ID: id, MaxRows: request.MaxRows, MaxBytes: 1}} {
		failed, err := facet.ObserveHTTPSessionKeys(context.Background(), bounds)
		if !errors.Is(err, ErrBounds) || failed.Matches != nil || failed.ProjectionComplete || failed.Complete || failed.Root.HTTP == nil || !failed.Root.HTTP.Acknowledged || !failed.Root.HTTP.EOF || len(failed.Root.HTTP.Entries) < 3 || len(failed.Root.HTTP.Body) == 0 {
			t.Fatal("unmatched rows or bytes escaped budget; partial projection was published", err)
		}
	}
	// A new explicit read is evidence from its own time; earlier bounds failures
	// do not poison the original lease or silently request narrower key ranges.
	again, err := facet.ObserveHTTPSessionKeys(context.Background(), request)
	if err != nil || !again.Complete || len(again.Matches) != 2 {
		t.Fatal("new complete root observation failed", err)
	}
}

func TestConsulNativeSessionHeldKeysOriginalACLAndUnknownDestroyRecovery(t *testing.T) {
	base, cfg, token := nativeClient(t)
	id := nativeSessionToolCreate(t, base, t.Name(), "10m", "release")
	defer nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, id))
	allowed, hidden := "allowed/"+keyFor(t, "visible"), keyFor(t, "hidden")
	nativeSessionKeysAcquire(t, base, id, allowed, []byte{0, 255})
	nativeSessionKeysAcquire(t, base, id, hidden, []byte{128})
	if cfg.Auth != "none" {
		secret := os.Getenv("NYADB_CONSUL_READ_TOKEN")
		if secret == "" {
			t.Fatal("original read-only fixture token missing")
		}
		restricted := cfg
		restricted.Prefix = "allowed/"
		s, err := OpenNativeSession(context.Background(), 2202, restricted, secret)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		l, err := s.ReserveKV(1)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		o, err := nativeSessionKeysFacet(t, l).ObserveHTTPSessionKeys(context.Background(), nativeSessionKeysRequest(id))
		if err != nil || !o.ProjectionComplete || o.Complete || len(o.Matches) != 1 || o.Root.HTTP.Entries[o.Matches[0]].Key != allowed || o.Root.HTTP.Metadata.ACLFiltered == nil || !*o.Root.HTTP.Metadata.ACLFiltered {
			t.Fatal("ACL-filtered root observation became complete destroy-impact evidence", err)
		}
		denied, err := l.Execute(context.Background(), documentPlan(t, l, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, id)))
		if err == nil || denied.HTTP == nil || denied.HTTP.Status != 403 || !denied.HTTP.Acknowledged || denied.WriteOutcomeUnknown {
			t.Fatal("original destroy denial lost its acknowledged receipt", err)
		}
	}
	relay, calls, processed := nativeFault(t, cfg, token, "lost")
	s, err := OpenNativeSession(context.Background(), 2203, relay, token)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	unknown, err := l.Execute(context.Background(), documentPlan(t, l, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, id)))
	wait(t, processed)
	if err == nil || !unknown.WriteOutcomeUnknown {
		t.Fatal("processed original destroy became acknowledged", err)
	}
	before := calls.Load()
	recovery, err := s.RecoverKV(l)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Lease.Close()
	if calls.Load() != before || recovery.Unknown == nil || !reflect.DeepEqual(*recovery.Unknown, unknown) {
		t.Fatal("recovery observed keys or changed original destroy evidence")
	}
	o, err := nativeSessionKeysFacet(t, recovery.Lease).ObserveHTTPSessionKeys(context.Background(), nativeSessionKeysRequest(id))
	if err != nil || !o.Complete || len(o.Matches) != 0 {
		t.Fatal("new root observation of released keys failed", err)
	}
	for _, key := range []string{allowed, hidden} {
		if nativeRead(t, base, key).Session != "" {
			t.Fatal("original destroy did not release its actual held keys")
		}
	}
	if preserved := recovery.Lease.(*documentLease).recoveryOwned().Unknown; preserved == nil || !reflect.DeepEqual(*preserved, unknown) {
		t.Fatal("later whole-root read promoted earlier unknown destroy receipt")
	}
	if _, err := recovery.Lease.Prepare(unknown.Request); !errors.Is(err, contract.ErrKVWriteUnknown) {
		t.Fatal("later absence observation authorized original destroy replay", err)
	}
}

// Forward the actual server read first, then withhold or corrupt that response.
// The configured prefix keeps handshake probes separate from explicit root I/O.
func nativeSessionKeysRelay(t *testing.T, cfg Config, token, mode string) (Config, *atomic.Int32, <-chan struct{}) {
	t.Helper()
	upstream, err := New(cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { upstream.Close() })
	calls := &atomic.Int32{}
	processed := make(chan struct{})
	var once atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, err := http.NewRequestWithContext(r.Context(), r.Method, cfg.Endpoint+r.URL.RequestURI(), r.Body)
		if err != nil {
			t.Error("invalid original root relay request")
			return
		}
		request.Header = r.Header.Clone()
		response, err := upstream.http.Do(request)
		if err != nil {
			return
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Error("incomplete upstream root response")
			return
		}
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		status := response.StatusCode
		if r.Method == "GET" && r.URL.Path == "/v1/kv/" && r.URL.Query().Has("recurse") {
			calls.Add(1)
			if once.CompareAndSwap(false, true) {
				close(processed)
				switch mode {
				case "lost":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						conn.Close()
					}
					return
				case "partial":
					w.Header().Set("Content-Length", strconv.Itoa(len(body)+1))
					w.WriteHeader(status)
					w.Write(body)
					return
				case "cancel", "retire", "close":
					<-r.Context().Done()
					return
				case "http500":
					status, body = 500, []byte("original read reply withheld")
				case "denied":
					status, body = 403, []byte("original root read denied")
				case "malformed":
					body = []byte(`[{"Key":"broken","Value":"invalid_base64"}]`)
				case "capture-overflow":
					body = bytes.Repeat([]byte(" "), responseLimit+1)
				case "empty":
					status, body = 200, []byte(" \n[ \t\r\n]\n")
				case "absent":
					status, body = 404, nil
				}
			}
		}
		w.Header().Del("Content-Length")
		w.WriteHeader(status)
		w.Write(body)
	}))
	t.Cleanup(server.Close)
	relay := cfg
	relay.Endpoint, relay.CAFile, relay.ServerName, relay.ClientCertFile, relay.ClientKeyFile = server.URL, "", "", "", ""
	return relay, calls, processed
}

func TestConsulNativeSessionHeldKeysProcessedReadFaultsCancellationRetirementAndClose(t *testing.T) {
	base, cfg, token := nativeClient(t)
	cfg.Prefix = "configured-empty/"
	id := nativeSessionToolCreate(t, base, t.Name(), "10m", "release")
	defer nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, id))
	nativeSessionKeysAcquire(t, base, id, keyFor(t, "outside"), []byte{0, 255})
	for _, mode := range []string{"lost", "partial", "http500", "denied", "malformed", "capture-overflow", "empty", "absent", "cancel", "retire", "close"} {
		t.Run(mode, func(t *testing.T) {
			relay, calls, processed := nativeSessionKeysRelay(t, cfg, token, mode)
			s, err := OpenNativeSession(context.Background(), 2204, relay, token)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			l, err := s.ReserveKV(1)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			facet := nativeSessionKeysFacet(t, l)
			if _, err := facet.ObserveHTTPSessionKeys(context.Background(), nativeSessionKeysRequest("42")); !errors.Is(err, ErrInvalid) || calls.Load() != 0 {
				t.Fatal("invalid target performed root I/O", err)
			}
			canceled, revoke := context.WithCancel(context.Background())
			revoke()
			if o, err := facet.ObserveHTTPSessionKeys(canceled, nativeSessionKeysRequest(id)); !errors.Is(err, context.Canceled) || calls.Load() != 0 || o.Root.HTTP != nil || o.Complete {
				t.Fatal("pre-dispatch cancellation performed root I/O or published evidence", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				observation contract.KVHTTPSessionKeysObservation
				err         error
			}
			done := make(chan outcome, 1)
			request := nativeSessionKeysRequest(id)
			go func() { o, err := facet.ObserveHTTPSessionKeys(ctx, request); done <- outcome{o, err} }()
			wait(t, processed)
			switch mode {
			case "cancel":
				cancel()
			case "retire":
				s.Close()
			case "close":
				l.Close()
			}
			var out outcome
			select {
			case out = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("original root executor failed to acknowledge cancellation")
			}
			o, h := out.observation, out.observation.Root.HTTP
			if h == nil || !h.Ended || h.WriteOutcomeUnknown || o.Root.WriteOutcomeUnknown || calls.Load() != 1 || o.Scope.Namespace.ProfileID != 2204 || o.Request != request {
				t.Fatal("root observation lost original ownership, became unknown write or replayed")
			}
			if mode == "empty" || mode == "absent" {
				if out.err != nil || !o.Complete || !o.ProjectionComplete || len(o.Matches) != 0 || !h.Acknowledged || !h.EOF {
					t.Fatal("acknowledged empty root evidence rejected", out.err)
				}
			} else if out.err == nil || o.Complete || o.ProjectionComplete || o.Matches != nil {
				t.Fatal("incomplete or denied root read produced impact evidence", out.err)
			}
			if mode == "malformed" && (h.Acknowledged || h.Succeeded || !bytes.Contains(h.Body, []byte("invalid_base64"))) || mode == "denied" && (!h.Acknowledged || h.Succeeded || h.Status != 403) || mode == "partial" && (len(h.Body) == 0 || h.EOF) {
				t.Fatal("failed optional projection or denial discarded original receipt")
			}
			if mode == "capture-overflow" && (len(h.Body) != responseLimit+1 || h.Captured || h.EOF || !errors.Is(out.err, ErrBounds)) {
				t.Fatal("oversized original root capture became complete or lost its bounded observed prefix", out.err)
			}
			if mode == "close" {
				if _, err := facet.ObserveHTTPSessionKeys(context.Background(), request); err == nil || calls.Load() != 1 {
					t.Fatal("closed original lease authorized another read")
				}
				return
			}
			original := l.LastResult()
			owner := s
			if mode == "retire" {
				owner, err = OpenNativeSession(context.Background(), 2204, relay, token)
				if err != nil {
					t.Fatal(err)
				}
				defer owner.Close()
			}
			recovery, err := owner.RecoverKV(l)
			if err != nil {
				t.Fatal(err)
			}
			defer recovery.Lease.Close()
			if calls.Load() != 1 || recovery.Unknown != nil || !reflect.DeepEqual(recovery.Last, original) {
				t.Fatal("evidence-only recovery executed or changed original root receipt")
			}
			fresh, err := nativeSessionKeysFacet(t, recovery.Lease).ObserveHTTPSessionKeys(context.Background(), request)
			if err != nil || !fresh.Complete || len(fresh.Matches) != 1 || calls.Load() != 2 {
				t.Fatal("explicit fresh original-session observation failed", err)
			}
		})
	}
}
