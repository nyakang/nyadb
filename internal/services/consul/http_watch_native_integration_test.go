package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeHTTPWatchSession(t *testing.T, cfg Config, token string) (*Session, <-chan Request, *atomic.Int32) {
	t.Helper()
	s, err := OpenNativeSession(context.Background(), 1801, cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	started := make(chan Request, 257)
	opens := &atomic.Int32{}
	s.opener = func(ctx context.Context) (*Client, OpenEvidence, error) {
		opens.Add(1)
		c, e, err := s.openCaptured(ctx)
		if err != nil {
			return c, e, err
		}
		original := c.http.Transport
		c.http.Transport = watchRoundTripper(func(r *http.Request) (*http.Response, error) {
			if r.URL.Query().Has("index") {
				started <- Request{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.Query()}
			}
			return original.RoundTrip(r)
		})
		return c, e, nil
	}
	return s, started, opens
}

func awaitHTTPWatchRead(t *testing.T, started <-chan Request) Request {
	t.Helper()
	select {
	case r := <-started:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("original blocking read not started")
		return Request{}
	}
}

func nativeWatchRequest(target, prefix string, index uint64) contract.KVHTTPWatchRequest {
	return contract.KVHTTPWatchRequest{Target: target, ObservationPrefix: prefix, StartIndex: index, Wait: 2 * time.Second, MaxObservations: 1, MaxBytes: 16 << 20}
}

func nativeWatchPut(t *testing.T, c *Client, key string, value []byte) {
	t.Helper()
	r, err := KeyPutRequest(key, value, u64(math.MaxUint64), nil)
	if !validText(key, 4096) {
		r, err = TxnRequest([]TxnOperation{{KV: &TxnKV{Verb: "set", Key: key, Value: &value, Flags: u64(math.MaxUint64)}}})
	}
	if err != nil {
		t.Fatal(err)
	}
	nativeDo(t, c, r)
}

func nativeWatchIndex(t *testing.T, c *Client, prefix string) uint64 {
	t.Helper()
	r, err := KeyListRequest(prefix, false)
	if err != nil {
		t.Fatal(err)
	}
	got := nativeDo(t, c, r)
	if got.Metadata.Index == nil || *got.Metadata.Index == 0 {
		t.Fatal("native observation index unavailable")
	}
	return *got.Metadata.Index
}

func TestConsulNativeHTTPWatchOriginalBlockingAncestorTargetsAndValues(t *testing.T) {
	base, cfg, token := nativeClient(t)
	ancestor := keyFor(t, "anchor")
	keys := []string{ancestor + "/double//key", ancestor + "/dot/./key", ancestor + "/猫\n?%", ancestor + "/empty/", ancestor + "/other"}
	for _, key := range keys {
		nativeWatchPut(t, base, key, []byte("original"))
	}
	s, started, opens := nativeHTTPWatchSession(t, cfg, token)
	for i, target := range keys {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			index := nativeWatchIndex(t, base, ancestor)
			l, err := s.ReserveKVHTTPWatch(uint64(i + 1))
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			r := nativeWatchRequest(target, ancestor, index)
			w := startHTTPWatch(t, l, r)
			original := awaitHTTPWatchRead(t, started)
			if original.Query.Get("index") != strconv.FormatUint(index, 10) || original.Query.Get("wait") != "2s" || !original.Query.Has("recurse") {
				t.Fatal("blocking request changed authored values")
			}
			time.Sleep(200 * time.Millisecond)
			if w.Snapshot().Ended {
				t.Fatal("native original ancestor read did not block")
			}
			value := []byte{0, 255, 128, '\n'}
			if i == 3 {
				value = []byte{}
			}
			nativeWatchPut(t, base, target, value)
			got := settledHTTPWatch(t, w)
			if len(got.Observations) != 1 || got.Terminal.Reason != "observation-limit" || got.Request != r {
				t.Fatal("original blocking completion lost")
			}
			o := got.Observations[0]
			if !o.ProjectionComplete || len(o.HTTP.Entries) != 5 || len(o.Matches) != 1 || o.IndexRelation != "advanced" {
				t.Fatal("native ancestor/target projection changed", o.IndexRelation)
			}
			e := o.HTTP.Entries[o.Matches[0]]
			if e.Key != target || !bytes.Equal(e.Value, value) || e.Flags != math.MaxUint64 {
				t.Fatal("noncanonical key/binary/empty/uint64 flag lost")
			}
			if token != "" && (bytes.Contains(o.HTTP.Body, []byte(token)) || strings.Contains(o.HTTP.URL, token)) {
				t.Fatal("temporary secret entered Watch evidence")
			}
		})
	}
	if opens.Load() != 5 {
		t.Fatal("Watch reselected/reopened original executor", opens.Load())
	}
}

func TestConsulNativeHTTPWatchNativeTimeoutUnrelatedChangeAndMissingTarget(t *testing.T) {
	base, cfg, token := nativeClient(t)
	ancestor := keyFor(t, "range")
	target := ancestor + "/target"
	other := ancestor + "/other"
	nativeWatchPut(t, base, target, []byte("target"))
	nativeWatchPut(t, base, other, []byte("other"))
	s, started, _ := nativeHTTPWatchSession(t, cfg, token)
	for i, kind := range []string{"timeout", "unrelated", "missing", "prefix"} {
		t.Run(kind, func(t *testing.T) {
			l, _ := s.ReserveKVHTTPWatch(uint64(i + 1))
			defer l.Close()
			r := nativeWatchRequest(target, ancestor, nativeWatchIndex(t, base, ancestor))
			r.Wait = 300 * time.Millisecond
			if kind == "prefix" {
				r.Target = ancestor
				r.Prefix = true
			}
			w := startHTTPWatch(t, l, r)
			awaitHTTPWatchRead(t, started)
			began := time.Now()
			if kind != "timeout" {
				time.Sleep(100 * time.Millisecond)
				if w.Snapshot().Ended {
					t.Fatal("read failed to block")
				}
				if kind == "missing" {
					d, _ := KeyDeleteRequest(target, nil, false)
					nativeDo(t, base, d)
				} else {
					nativeWatchPut(t, base, other, []byte("changed-"+kind))
				}
			}
			got := settledHTTPWatch(t, w)
			o := got.Observations[0]
			if !o.ProjectionComplete || got.Terminal.Reason != "observation-limit" {
				t.Fatal("blocking native completion failed")
			}
			if kind == "timeout" && (time.Since(began) < 290*time.Millisecond || o.IndexRelation != "unchanged") {
				t.Fatal("unchanged native blocking timeout became rapid polling")
			}
			if kind == "unrelated" && (len(o.Matches) != 1 || string(o.HTTP.Entries[o.Matches[0]].Value) != "target" || o.IndexRelation != "advanced") {
				t.Fatal("unrelated change fabricated target event")
			}
			if kind == "missing" && (len(o.Matches) != 0 || len(o.HTTP.Entries) != 1) {
				t.Fatal("missing exact target broadened or erased original ancestor")
			}
			if kind == "prefix" && (len(o.Matches) != 1 || o.HTTP.Entries[o.Matches[0]].Key != other) {
				t.Fatal("authored prefix matching changed")
			}
		})
	}
}

// Fault only an actual original indexed recurse response, after upstream read
// completion. Opening evidence and all other original parents remain untouched.
func nativeHTTPWatchRelay(t *testing.T, cfg Config, token, mode string) (Config, *atomic.Int32, <-chan struct{}) {
	t.Helper()
	up, err := New(cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { up.Close() })
	reads := &atomic.Int32{}
	processed := make(chan struct{})
	var once atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
		r, err := http.NewRequestWithContext(incoming.Context(), incoming.Method, cfg.Endpoint+incoming.URL.RequestURI(), incoming.Body)
		if err != nil {
			t.Error(err)
			return
		}
		r.Header = incoming.Header.Clone()
		r.GetBody = nil
		r.Close = true
		response, err := up.http.Do(r)
		if err != nil {
			return
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			return
		}
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.Header().Del("Content-Length")
		if incoming.URL.Query().Has("index") && once.CompareAndSwap(false, true) {
			reads.Add(1)
			close(processed)
			switch mode {
			case "lost":
				c, _, _ := w.(http.Hijacker).Hijack()
				c.Close()
				return
			case "partial":
				w.Header().Set("Content-Length", strconv.Itoa(len(body)+1))
				w.WriteHeader(response.StatusCode)
				w.Write(body)
				return
			case "cancel", "close", "session-close", "timeout":
				<-incoming.Context().Done()
				return
			case "http408":
				w.WriteHeader(408)
				w.Write(body)
				return
			case "http500":
				w.WriteHeader(500)
				w.Write(body)
				return
			case "missing-index":
				w.Header().Del("X-Consul-Index")
			case "zero-index":
				w.Header().Set("X-Consul-Index", "0")
			case "backward-index":
				w.Header().Set("X-Consul-Index", "1")
			case "invalid-index":
				w.Header().Set("X-Consul-Index", "18446744073709551616")
			case "invalid-leader":
				w.Header().Set("X-Consul-Knownleader", "not-native-bool")
			}
		}
		w.WriteHeader(response.StatusCode)
		w.Write(body)
	}))
	t.Cleanup(server.Close)
	proxy := cfg
	proxy.Endpoint = server.URL
	proxy.CAFile, proxy.ServerName, proxy.ClientCertFile, proxy.ClientKeyFile = "", "", "", ""
	return proxy, reads, processed
}

func TestConsulNativeHTTPWatchOriginalProcessedReplyFailuresNeverReconnect(t *testing.T) {
	base, cfg, token := nativeClient(t)
	ancestor := keyFor(t, "range")
	target := ancestor + "/key"
	nativeWatchPut(t, base, target, []byte{0, 255})
	for _, mode := range []string{"lost", "partial", "http408", "http500", "cancel", "close", "session-close", "timeout", "missing-index", "zero-index", "backward-index", "invalid-index", "invalid-leader"} {
		t.Run(mode, func(t *testing.T) {
			proxy, reads, processed := nativeHTTPWatchRelay(t, cfg, token, mode)
			if mode == "timeout" {
				proxy.ConnectTimeout, proxy.RequestTimeout = time.Second, 2*time.Second
			}
			s, _, opens := nativeHTTPWatchSession(t, proxy, token)
			l, _ := s.ReserveKVHTTPWatch(1)
			defer l.Close()
			r := nativeWatchRequest(target, ancestor, 0)
			if mode == "backward-index" {
				r.StartIndex = nativeWatchIndex(t, base, ancestor)
				r.Wait = 100 * time.Millisecond
			}
			r.MaxObservations = 20
			if mode == "timeout" {
				r.Wait = 100 * time.Millisecond
			}
			p, prepareErr := l.Prepare(r)
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			requestDeadline := 2 * time.Second
			if mode == "timeout" {
				requestDeadline = 5 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), requestDeadline)
			defer cancel()
			w, err := l.Start(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			wait(t, processed)
			switch mode {
			case "cancel":
				w.Stop(context.Background())
			case "close":
				l.Close()
			case "session-close":
				s.Close()
			}
			got := settledHTTPWatch(t, w)
			if reads.Load() != 1 || opens.Load() != 1 {
				t.Fatal("original failed indexed read reconnected/repeated")
			}
			if mode == "close" {
				if len(got.Observations) != 0 {
					t.Fatal("original close retained caches")
				}
				return
			}
			if len(got.Observations) != 1 || got.Terminal.Reason == "observation-limit" {
				t.Fatal("failure lost complete original observation or continued", got.Terminal)
			}
			o := got.Observations[0]
			if !o.HTTP.Ended || !o.HTTP.Attempted || o.HTTP.WriteOutcomeUnknown {
				t.Fatal("read receipt invented write ACK")
			}
			if mode == "partial" && (!bytes.Contains(o.HTTP.Body, []byte(`"Key"`)) || o.HTTP.Captured || o.HTTP.EOF) {
				t.Fatal("original partial body lost")
			}
			if mode == "http408" || mode == "http500" || strings.HasSuffix(mode, "index") || mode == "invalid-leader" {
				if !o.HTTP.Captured || !bytes.Contains(o.HTTP.Body, []byte(`"Key"`)) {
					t.Fatal("complete original failed result lost")
				}
			}
			if _, err = l.Start(context.Background(), p); err == nil || reads.Load() != 1 || opens.Load() != 1 {
				t.Fatal("original failed Watch preparation replayed")
			}
		})
	}
}

func TestConsulNativeHTTPWatchNativeBoundsCompleteTerminalAndUnsignedHeaders(t *testing.T) {
	base, cfg, token := nativeClient(t)
	ancestor := keyFor(t, "range")
	target := ancestor + "/large"
	value := bytes.Repeat([]byte{0, 255}, valueLimit/2)
	nativeWatchPut(t, base, target, value)
	s, _, opens := nativeHTTPWatchSession(t, cfg, token)
	l, _ := s.ReserveKVHTTPWatch(1)
	defer l.Close()
	r := nativeWatchRequest(target, ancestor, 0)
	r.MaxBytes = 1
	r.MaxObservations = 200
	got := settledHTTPWatch(t, startHTTPWatch(t, l, r))
	if !got.TerminalOverflow || got.Terminal.Reason != "byte-limit" || len(got.Observations) != 1 || opens.Load() != 1 {
		t.Fatal("native terminal overflow dropped/continued")
	}
	o := got.Observations[0]
	if len(o.Matches) != 1 || !o.HTTP.Captured || !bytes.Equal(o.HTTP.Entries[o.Matches[0]].Value, value) || o.HTTP.Entries[o.Matches[0]].Flags != math.MaxUint64 || len(o.HTTP.Body) < valueLimit {
		t.Fatal("complete terminal native bytes/flags lost")
	}
	var raw []struct {
		Key, Value string
		Flags      uint64
	}
	if json.Unmarshal(o.HTTP.Body, &raw) != nil || len(raw) != 1 || raw[0].Flags != math.MaxUint64 {
		t.Fatal("original body precision changed")
	}
}

func TestConsulNativeHTTPWatchRestrictedACLPositiveObservationsAndOriginalDenial(t *testing.T) {
	base, cfg, token := nativeClient(t)
	allowed := "allowed/watch/" + strings.ReplaceAll(t.Name(), "/", "-")
	target := allowed + "/key"
	nativeWatchPut(t, base, target, []byte{0, 255})
	nativeWatchPut(t, base, keyFor(t, "forbidden"), []byte("private"))
	readToken := os.Getenv("NYADB_CONSUL_READ_TOKEN")
	if readToken == "" {
		s, _, _ := nativeHTTPWatchSession(t, cfg, token)
		l, _ := s.ReserveKVHTTPWatch(1)
		defer l.Close()
		got := settledHTTPWatch(t, startHTTPWatch(t, l, nativeWatchRequest(target, allowed, 0)))
		if len(got.Observations[0].Matches) != 1 {
			t.Fatal("anonymous original observation lost")
		}
		return
	}
	cfg.Auth = "token"
	cfg.Prefix = allowed
	s, _, _ := nativeHTTPWatchSession(t, cfg, readToken)
	if s.KVScope().HTTP.AgentAvailable {
		t.Fatal("restricted token invented original agent evidence")
	}
	l, _ := s.ReserveKVHTTPWatch(1)
	defer l.Close()
	r := nativeWatchRequest(target, "", 0)
	r.MaxBytes = 128 << 20
	got := settledHTTPWatch(t, startHTTPWatch(t, l, r))
	o := got.Observations[0]
	if !o.ProjectionComplete || len(o.Matches) != 1 || o.HTTP.Metadata.ACLFiltered == nil || !*o.HTTP.Metadata.ACLFiltered || !bytes.Equal(o.HTTP.Entries[o.Matches[0]].Value, []byte{0, 255}) {
		t.Fatal("ACL filtered current read fabricated complete keyspace")
	}
	denied, _ := s.ReserveKVHTTPWatch(2)
	defer denied.Close()
	r = nativeWatchRequest(keyFor(t, "forbidden"), keyFor(t, "forbidden"), 0)
	got = settledHTTPWatch(t, startHTTPWatch(t, denied, r))
	if len(got.Observations) != 1 || !got.Observations[0].HTTP.Captured {
		t.Fatal("original forbidden range lost its actual receipt", got.Terminal)
	}
	h := got.Observations[0].HTTP
	t.Logf("Original restricted recurse status %d; original headers %v", h.Status, h.Header)
	if h.Status == 403 {
		if got.Terminal.Reason != "read-failed" {
			t.Fatal("native denial was promoted")
		}
	} else if h.Status != 404 || len(got.Observations[0].Matches) != 0 || h.Metadata.ACLFiltered == nil || !*h.Metadata.ACLFiltered {
		t.Fatal("restricted absent range lost filtering evidence", h.Status, h.Header)
	}
}

func TestConsulNativeHTTPWatchOriginalBlockingRetirementAndEvidenceOnlyRecovery(t *testing.T) {
	base, cfg, token := nativeClient(t)
	ancestor := keyFor(t, "range")
	target := ancestor + "/key"
	nativeWatchPut(t, base, target, []byte("original"))
	s, started, opens := nativeHTTPWatchSession(t, cfg, token)
	l, _ := s.ReserveKVHTTPWatch(1)
	defer l.Close()
	w := startHTTPWatch(t, l, nativeWatchRequest(target, ancestor, nativeWatchIndex(t, base, ancestor)))
	awaitHTTPWatchRead(t, started)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	original := settledHTTPWatch(t, w)
	if len(original.Observations) != 1 || !original.Observations[0].HTTP.Canceled || !original.Observations[0].HTTP.Ended || opens.Load() != 1 {
		t.Fatal("original blocking retirement did not ACK/retain")
	}
	fresh, freshStarted, freshOpens := nativeHTTPWatchSession(t, cfg, token)
	r, err := fresh.RecoverKVHTTPWatch(l)
	if err != nil || r.Lease == nil || r.Watch == nil || freshOpens.Load() != 0 || r.Watch.Request != w.Request() {
		t.Fatal("evidence recovery reconnected/resumed", err)
	}
	defer r.Lease.Close()
	if len(r.Watch.Observations) != 1 || !bytes.Equal(r.Watch.Observations[0].HTTP.Body, original.Observations[0].HTTP.Body) {
		t.Fatal("original canceled evidence substituted")
	}
	if err = r.Lease.ClearObservations(); err != nil {
		t.Fatal(err)
	}
	p, err := r.Lease.Prepare(nativeWatchRequest(target, ancestor, 0))
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := r.Lease.Start(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	got := settledHTTPWatch(t, resumed)
	if freshOpens.Load() != 1 || len(got.Observations[0].Matches) != 1 || string(got.Observations[0].HTTP.Entries[got.Observations[0].Matches[0]].Value) != "original" {
		t.Fatal("explicit new start did not own fresh original read")
	}
	if _, err = fresh.RecoverKVHTTPWatch(l); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("original recovery replayed")
	}
	// A settled original run can be released explicitly before a new run.
	// Its late Close must not wait on, cancel or erase that new actual read.
	resumed.Stop(context.Background())
	if err = r.Lease.ClearObservations(); err != nil {
		t.Fatal(err)
	}
	for len(freshStarted) > 0 {
		<-freshStarted
	}
	newRequest := nativeWatchRequest(target, ancestor, nativeWatchIndex(t, base, ancestor))
	p, err = r.Lease.Prepare(newRequest)
	if err != nil {
		t.Fatal(err)
	}
	newRun, err := r.Lease.Start(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer newRun.Close()
	awaitHTTPWatchRead(t, freshStarted)
	closed := make(chan error, 1)
	go func() { closed <- resumed.Close() }()
	late := false
	select {
	case err = <-closed:
	case <-time.After(100 * time.Millisecond):
		late = true
	}
	changed := []byte{0, 255, 'x'}
	nativeWatchPut(t, base, target, changed)
	got = settledHTTPWatch(t, newRun)
	if late {
		err = <-closed
	}
	if late || err != nil || len(got.Observations) != 1 || !got.Observations[0].ProjectionComplete || len(got.Observations[0].Matches) != 1 || !bytes.Equal(got.Observations[0].HTTP.Entries[got.Observations[0].Matches[0]].Value, changed) {
		t.Fatal("late old run Close waited/canceled/erased the new original native read", err)
	}
}
