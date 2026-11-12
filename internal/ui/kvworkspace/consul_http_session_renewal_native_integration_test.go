package kvworkspace

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func consulHTTPSessionRenewalFault(t *testing.T, p connections.Profile, mode string) (connections.Profile, *atomic.Int32, <-chan struct{}) {
	t.Helper()
	cfg, err := connections.ConsulProfileConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.Options.CAFile != "" {
		raw, err := os.ReadFile(cfg.Options.CAFile)
		if err != nil {
			t.Fatal(err)
		}
		tc.RootCAs = x509.NewCertPool()
		if !tc.RootCAs.AppendCertsFromPEM(raw) {
			t.Fatal("invalid original CA")
		}
	}
	if cfg.Options.ClientCertFile != "" {
		pair, err := tls.LoadX509KeyPair(cfg.Options.ClientCertFile, cfg.Options.ClientKeyFile)
		if err != nil {
			t.Fatal(err)
		}
		tc.Certificates = []tls.Certificate{pair}
	}
	transport := &http.Transport{TLSClientConfig: tc, DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	up := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	calls := &atomic.Int32{}
	committed := make(chan struct{})
	var once atomic.Bool
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		request, err := http.NewRequestWithContext(r.Context(), r.Method, cfg.Endpoint+r.URL.RequestURI(), bytes.NewReader(raw))
		if err != nil {
			return
		}
		request.Header = r.Header.Clone()
		request.GetBody = nil
		response, err := up.Do(request)
		if err != nil {
			return
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			return
		}
		if r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/v1/session/renew/") {
			calls.Add(1)
		}
		if r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/v1/session/renew/") && response.StatusCode == 200 && once.CompareAndSwap(false, true) {
			close(committed)
			switch mode {
			case "uuid":
				var sessions []map[string]any
				if json.Unmarshal(body, &sessions) == nil && len(sessions) == 1 {
					sessions[0]["ID"] = renewalUUID
					response.Header.Del("Content-Length")
					body, _ = json.Marshal(sessions)
				}
			case "lost":
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
				return
			case "partial":
				conn, b, err := w.(http.Hijacker).Hijack()
				if err == nil {
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
				w.Write([]byte("actual processed reply withheld"))
				return
			case "cancel", "disconnect", "replace", "close":
				<-r.Context().Done()
				return
			}
		}
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		w.Write(body)
	}))
	t.Cleanup(relay.Close)
	u, _ := url.Parse(relay.URL)
	port, _ := strconv.Atoi(u.Port())
	proxy := p
	proxy.Host, proxy.Port, proxy.TLSMode = u.Hostname(), uint16(port), "disable"
	proxy.ConsulOptions.CAFile, proxy.ConsulOptions.ClientCertFile, proxy.ConsulOptions.ClientKeyFile, proxy.ConsulOptions.ServerName = "", "", "", ""
	return proxy, calls, committed
}
func consulRenewalWriter(t *testing.T, p connections.Profile, secret string) contract.KVRequestLease {
	t.Helper()
	_, _, m := consulWorkspaceState(t, p, secret)
	provider, err := m.KVRequests(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := provider.ReserveKV(9501)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Close() })
	return writer
}
func consulRenewalDo(t *testing.T, l contract.KVRequestLease, r consul.Request) contract.KVResult {
	t.Helper()
	request, err := consul.DocumentRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := l.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	out, err := l.Execute(context.Background(), plan)
	if err != nil || !out.Completed {
		t.Fatal("original native Session fixture request failed", err, out.Code)
	}
	return out
}
func consulRenewalSession(t *testing.T, writer contract.KVRequestLease) string {
	t.Helper()
	r := consulRenewalDo(t, writer, consul.Request{Method: "PUT", Path: "/v1/session/create", Body: []byte(`{"Name":"public-original-renewal","TTL":"10s","LockDelay":"0s","NodeChecks":[]}`)})
	if r.HTTP == nil || r.HTTP.SessionID == "" {
		t.Fatal("native UUID absent")
	}
	id := r.HTTP.SessionID
	t.Cleanup(func() { consulRenewalDo(t, writer, consul.Request{Method: "PUT", Path: "/v1/session/destroy/" + id}) })
	return id
}
func consulHTTPSessionRenewalInput(s *State, d *Document, id, interval string, count int, budget int64) {
	s.SetMethod(d, HTTPSessionRenewalMethod)
	s.SetHTTPSessionRenewalInput(d, HTTPSessionRenewalInput{id, interval, strconv.Itoa(count), strconv.FormatInt(budget, 10)})
}
func consulHTTPSessionRenewalWait(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("original public renewal did not settle")
		}
		time.Sleep(time.Millisecond)
	}
}
func consulHTTPSessionRenewalScreenshot(t *testing.T, s *State, d *Document, stage string) {
	t.Helper()
	defer i18n.SetLocaleFunc(nil)
	for _, lang := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return lang })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, size[0], size[1])
			tt.Frame()
			if _, ok := d.HTTPSessionRenewalConfirmation(); ok {
				assertHTTPSessionRenewalControls(t, tt, size, "确认 HTTP Session 续租", "我授权按以上 UUID、间隔、次数和预算续租", "确认并开始续租", "复制完整请求", "取消")
			} else {
				assertHTTPSessionRenewalControls(t, tt, size, "预览 HTTP Session 续租", "停止 HTTP Session 续租", "Session UUID", "续租间隔", "最多续租次数", "记录预算（字节）")
				if d.httpSessionRenewalContent() {
					assertHTTPSessionRenewalControls(t, tt, size, "复制完整续租记录", "导出续租记录")
				}
			}
			captureHTTPSessionRenewal(t, tt, lang, size, stage)
		}
	}
}
func TestConsulNativePublicHTTPSessionRenewalOriginalTTLConfirmationAndCompleteRecords(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	id := consulRenewalSession(t, writer)
	s, d, m := consulWorkspaceState(t, p, secret)
	s.deps.HTTPSessionRenewalProviderFor = m.KVHTTPSessionRenewal
	d.httpSessionRenewalAvailable = true
	consulHTTPSessionRenewalInput(s, d, id, "2s", 7, 16<<20)
	consulHTTPSessionRenewalScreenshot(t, s, d, "form")
	s.PrepareHTTPSessionRenewal(d)
	if _, ok := d.HTTPSessionRenewalConfirmation(); !ok || d.Busy() {
		t.Fatal("original confirmation missing", d.Error())
	}
	consulHTTPSessionRenewalScreenshot(t, s, d, "confirmation")
	s.ConfirmHTTPSessionRenewal(d, false)
	if d.Busy() || d.httpSessionRenewalContent() {
		t.Fatal("unchecked native renewal")
	}
	began := time.Now()
	s.ConfirmHTTPSessionRenewal(d, true)
	consulHTTPSessionRenewalWait(t, func() bool { s.Consume(); return d.HTTPSessionRenewalStatus().Observations >= 2 })
	consulHTTPSessionRenewalScreenshot(t, s, d, "running")
	consulHTTPSessionRenewalWait(t, func() bool { s.Consume(); return !d.Busy() })
	if time.Since(began) < 12*time.Second {
		t.Fatal("native public run did not span original TTL")
	}
	snapshot := d.httpSessionRenewal.snapshot()
	if !snapshot.Ended || snapshot.Terminal.Reason != "renewal-limit" || snapshot.Terminal.Attempts != 7 || snapshot.Terminal.Acknowledged != 7 || len(snapshot.Observations) != 8 {
		t.Fatal("bounded original receipts missing", snapshot.Terminal)
	}
	for i, o := range snapshot.Observations {
		expected := "PUT"
		if i == 0 {
			expected = "GET"
		}
		if o.Sequence != i || o.HTTP.Request.Method != expected || !o.ProjectionComplete || o.Session.ID != id || o.Session.TTL != "10s" || o.Session.LockDelay != 0 || !bytes.Contains(o.HTTP.Body, []byte(id)) {
			t.Fatal("native receipt projection changed", i)
		}
	}
	s.SelectHTTPSessionRenewalObservation(d, 0)
	consulHTTPSessionRenewalScreenshot(t, s, d, "initial-read")
	s.SelectHTTPSessionRenewalObservation(d, 7)
	consulHTTPSessionRenewalScreenshot(t, s, d, "stopped")
	read := consulRenewalDo(t, writer, consul.Request{Method: "GET", Path: "/v1/session/info/" + id})
	if !bytes.Contains(read.HTTP.Body, []byte(id)) {
		t.Fatal("stop destroyed Session")
	}
	want, err := s.HTTPSessionRenewalRecord(d)
	if err != nil {
		t.Fatal(err)
	}
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services() }, 1280, 800)
	tt.Frame()
	s.CopyHTTPSessionRenewalRecord(services, d)
	consume(t, s, d)
	if tt.Clipboard() != string(want) {
		t.Fatal("public copy incomplete")
	}
	var exported bytes.Buffer
	s.SetFiles(Files{SavePath: func(string) (string, error) { return "original-renewal.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error { return write(&exported) }})
	s.ExportHTTPSessionRenewalRecord(d)
	consume(t, s, d)
	if !bytes.Equal(want, exported.Bytes()) {
		t.Fatal("native export changed original records")
	}
	s.ClearHTTPSessionRenewal(d)
	if d.httpSessionRenewalContent() {
		t.Fatal("settled archive not cleared")
	}
	consulHTTPSessionRenewalScreenshot(t, s, d, "cleared")
}
func TestConsulNativePublicHTTPSessionRenewalProcessedWritesUnknownBarriersAndNoReplay(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	for _, mode := range []string{"lost", "partial", "http500", "http408", "uuid", "cancel", "replace", "parked"} {
		t.Run(mode, func(t *testing.T) {
			id := consulRenewalSession(t, writer)
			faultMode := mode
			if mode == "parked" {
				faultMode = "cancel"
			}
			proxy, calls, processed := consulHTTPSessionRenewalFault(t, p, faultMode)
			s, d, m := consulWorkspaceState(t, proxy, secret)
			s.deps.HTTPSessionRenewalProviderFor = m.KVHTTPSessionRenewal
			d.httpSessionRenewalAvailable = true
			s.deps.HTTPWatchProviderFor = m.KVHTTPWatch
			d.httpWatchAvailable = true
			consulHTTPSessionRenewalInput(s, d, id, "100ms", 4, 16<<20)
			original := d.Request()
			s.PrepareHTTPSessionRenewal(d)
			s.ConfirmHTTPSessionRenewal(d, true)
			await(t, processed)
			active := true
			s.deps.Active = func(*Document) bool { return active }
			if mode == "parked" {
				active = false
			}
			if mode == "cancel" || mode == "parked" {
				s.RequestCancel(d)
				if !d.Busy() || !d.CancelRequested() {
					t.Fatal("native cancel implied ACK")
				}
			}
			if mode == "replace" {
				if err := m.Connect(context.Background(), proxy, secret); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "parked" {
				before := d.HTTPSessionRenewalStatus()
				s.Consume()
				if d.HTTPSessionRenewalStatus() != before || !d.Busy() {
					t.Fatal("parked result admitted")
				}
				active = true
			}
			consulHTTPSessionRenewalWait(t, func() bool { s.Consume(); return !d.Busy() })
			status := d.HTTPSessionRenewalStatus()
			if !d.hasUnknownWrites() || status.Terminal.Attempts != 1 || status.Terminal.Acknowledged != 0 || status.Observations != 2 || calls.Load() != 1 {
				t.Fatal("processed original write promoted or replayed", status.Terminal, calls.Load())
			}
			want, _ := s.HTTPSessionRenewalRecord(d)
			s.ClearHTTPSessionRenewal(d)
			s.PrepareHTTPSessionRenewal(d)
			if d.httpSessionRenewal.confirmation != nil || !d.httpSessionRenewalContent() {
				t.Fatal("unknown discarded or reauthorized")
			}
			consulHTTPSessionRenewalScreenshot(t, s, d, "unknown-"+mode)
			s.DisconnectProfile(p.ID)
			if mode != "replace" {
				if err := m.Connect(context.Background(), proxy, secret); err != nil {
					t.Fatal(err)
				}
			}
			s.RecoverHTTPSessionRenewal(d)
			consume(t, s, d)
			got, _ := s.HTTPSessionRenewalRecord(d)
			if d.NeedsRecovery() || calls.Load() != 1 || !bytes.Equal(want, got) || !d.hasUnknownWrites() {
				t.Fatal("recovery executed or changed original evidence", d.Error())
			}
			// Recover generic document independently; its read cannot settle renewal writes.
			consulWorkspaceInput(t, s, d, consul.Request{Method: "GET", Path: "/v1/session/info/" + id})
			s.Recover(d)
			consume(t, s, d)
			s.Run(d)
			consume(t, s, d)
			if d.last == nil || !d.hasUnknownWrites() {
				t.Fatal("later native read confirmed unknown renewal")
			}
			put, err := consul.KeyPutRequest("native/workspace/renewal-barrier", []byte("no replay"), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, write := range []consul.Request{put, {Method: "PUT", Path: "/v1/txn", Body: []byte(`[{"KV":{"Verb":"set","Key":"native/workspace/renewal-barrier","Value":"eA=="}}]`)}} {
				consulWorkspaceInput(t, s, d, write)
				s.Run(d)
				if _, ok := d.Confirmation(); ok || d.Busy() {
					t.Fatal("native Put/Txn passed unknown renewal barrier")
				}
			}
			consulHTTPWatchInput(t, s, d, "native/workspace/renewal-barrier", "native/workspace", 1, 16<<20)
			s.RecoverHTTPWatch(d)
			consume(t, s, d)
			s.StartHTTPWatch(d)
			consume(t, s, d)
			if !d.hasUnknownWrites() || d.HTTPWatchStatus().Observations != 1 {
				t.Fatal("later Watch missing or changed unknown barrier", d.Error(), d.HTTPWatchStatus(), d.hasUnknownWrites())
			}
			consulHTTPSessionRenewalInput(s, d, id, "100ms", 4, 16<<20)
			if d.Request() != original {
				t.Fatal("authored original changed")
			}
			consulHTTPSessionRenewalScreenshot(t, s, d, "after-read-"+mode)
			got, _ = s.HTTPSessionRenewalRecord(d)
			if !bytes.Equal(want, got) || calls.Load() != 1 {
				t.Fatal("read substituted original write evidence")
			}
		})
	}
	t.Run("generic-unknown-blocks-renewal", func(t *testing.T) {
		id := consulRenewalSession(t, writer)
		proxy, calls, processed := consulWorkspaceFault(t, p, "lost")
		s, d, m := consulWorkspaceState(t, proxy, secret)
		s.deps.HTTPSessionRenewalProviderFor = m.KVHTTPSessionRenewal
		d.httpSessionRenewalAvailable = true
		put, err := consul.KeyPutRequest("native/workspace/generic-renewal-barrier", []byte("original"), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		consulWorkspaceInput(t, s, d, put)
		s.Run(d)
		s.Confirm(d, true)
		await(t, processed)
		consume(t, s, d)
		original := d.unknown
		if original == nil || calls.Load() != 1 {
			t.Fatal("missing original unknown generic write")
		}
		consulHTTPSessionRenewalInput(s, d, id, "100ms", 2, 16<<20)
		s.PrepareHTTPSessionRenewal(d)
		if d.httpSessionRenewal.lease != nil || d.httpSessionRenewal.confirmation != nil || d.Busy() {
			t.Fatal("generic unknown authorized renewal")
		}
		consulWorkspaceInput(t, s, d, consul.Request{Method: "GET", Path: "/v1/session/info/" + id})
		s.Recover(d)
		consume(t, s, d)
		s.Run(d)
		consume(t, s, d)
		if d.unknown == nil || !bytes.Equal(d.unknown.Result.HTTP.Body, original.Result.HTTP.Body) || d.last == nil {
			t.Fatal("later Session read lost original generic evidence")
		}
		consulHTTPSessionRenewalInput(s, d, id, "100ms", 2, 16<<20)
		s.PrepareHTTPSessionRenewal(d)
		if d.httpSessionRenewal.confirmation != nil || d.Busy() || calls.Load() != 1 {
			t.Fatal("read promoted unknown generic write")
		}
	})

}
func TestConsulNativePublicHTTPSessionRenewalBoundsMissingSessionAndOriginalACL(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	for _, mode := range []string{"overflow", "missing", "read-denied", "write-denied"} {
		t.Run(mode, func(t *testing.T) {
			id := consulRenewalSession(t, writer)
			token := secret
			if mode == "read-denied" && p.ConsulOptions.Auth != "none" {
				token = os.Getenv("NYADB_CONSUL_READ_TOKEN")
				if token == "" {
					t.Fatal("missing native read denial token")
				}
			}
			if mode == "write-denied" && p.ConsulOptions.Auth != "none" {
				token = os.Getenv("NYADB_CONSUL_SESSION_READ_TOKEN")
				if token == "" {
					t.Fatal("missing native Session read token")
				}
			}
			s, d, m := consulWorkspaceState(t, p, token)
			s.deps.HTTPSessionRenewalProviderFor = m.KVHTTPSessionRenewal
			d.httpSessionRenewalAvailable = true
			budget := int64(16 << 20)
			if mode == "overflow" {
				budget = 1
			}
			if mode == "missing" {
				consulRenewalDo(t, writer, consul.Request{Method: "PUT", Path: "/v1/session/destroy/" + id})
			}
			consulHTTPSessionRenewalInput(s, d, id, "100ms", 1, budget)
			startHTTPSessionRenewal(t, s, d)
			consume(t, s, d)
			v := d.httpSessionRenewal.snapshot()
			if mode == "overflow" && (!v.TerminalOverflow || len(v.Observations) != 1 || v.Terminal.Attempts != 0 || !bytes.Contains(v.Observations[0].HTTP.Body, []byte(id))) {
				t.Fatal("complete terminal overflow missing", v.Terminal)
			}
			if mode == "missing" && (v.Terminal.Attempts != 0 || len(v.Observations) != 1) {
				t.Fatal("missing UUID renewed")
			}
			if (mode == "read-denied" || mode == "write-denied") && p.ConsulOptions.Auth != "none" {
				last := v.Observations[len(v.Observations)-1]
				expected := 0
				if mode == "write-denied" {
					expected = 1
				}
				if v.Terminal.Attempts != expected || v.Terminal.Acknowledged != 0 || v.Terminal.WriteOutcomeUnknown {
					t.Fatal("native Session ACL denial changed", v.Terminal, last.HTTP.Status)
				}
				if mode == "write-denied" && last.HTTP.Status != 403 {
					t.Fatal("original write denial missing")
				}
				if mode == "read-denied" && (last.ProjectionComplete || len(v.Observations) != 1 || !last.HTTP.Captured || !last.HTTP.EOF || (last.HTTP.Status != 403 && last.HTTP.Status != 200)) {
					t.Fatal("original unreadable Session evidence changed")
				}
			}
			consulHTTPSessionRenewalScreenshot(t, s, d, mode)
		})
	}
}
func TestConsulNativePublicHTTPSessionRenewalOriginalParkingRetirementAndEvidenceRecovery(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	id := consulRenewalSession(t, writer)
	s, d, m := consulWorkspaceState(t, p, secret)
	s.deps.HTTPSessionRenewalProviderFor = m.KVHTTPSessionRenewal
	d.httpSessionRenewalAvailable = true
	consulHTTPSessionRenewalInput(s, d, id, "1s", 16, 16<<20)
	startHTTPSessionRenewal(t, s, d)
	consulHTTPSessionRenewalWait(t, func() bool { s.Consume(); return d.HTTPSessionRenewalStatus().Observations >= 2 })
	w := d.httpSessionRenewal
	indexed := w.run.(contract.KVHTTPSessionRenewalIndexedRun)
	active := false
	s.deps.Active = func(*Document) bool { return active }
	before := w.status
	consulHTTPSessionRenewalWait(t, func() bool { _, ok := indexed.Observation(before.Observations); return ok })
	s.Consume()
	if w.status != before {
		t.Fatal("parked original buffer consumed")
	}
	consulHTTPSessionRenewalScreenshot(t, s, d, "parked")
	if err := m.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	active = true
	consulHTTPSessionRenewalWait(t, func() bool { s.Consume(); return !d.Busy() })
	if !d.NeedsRecovery() {
		t.Fatal("retired lease remained usable")
	}
	want, _ := s.HTTPSessionRenewalRecord(d)
	s.RecoverHTTPSessionRenewal(d)
	consume(t, s, d)
	got, _ := s.HTTPSessionRenewalRecord(d)
	if d.NeedsRecovery() || !w.status.Ended || !bytes.Equal(want, got) || w.scope.Generation == w.evidenceScope.Generation {
		t.Fatal("recovery changed original generation or evidence", d.Error())
	}
	consulHTTPSessionRenewalScreenshot(t, s, d, "recovered")
}
