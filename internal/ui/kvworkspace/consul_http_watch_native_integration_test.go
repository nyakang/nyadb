package kvworkspace

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func consulHTTPWatchInput(t *testing.T, s *State, d *Document, target, prefix string, count int, budget int64) {
	t.Helper()
	s.SetMethod(d, HTTPWatchMethod)
	raw, err := json.Marshal(map[string]any{"target": target, "prefix": false, "observation_prefix": prefix, "start_index": "0", "wait": "1s", "max_observations": count, "max_bytes": strconv.FormatInt(budget, 10)})
	if err != nil {
		t.Fatal(err)
	}
	d.Editor.Buffer.Set(string(raw))
}

// The relay transforms only an already captured original native read. It
// preserves the original upstream TLS/mTLS and never manufactures a KV entry.
func consulHTTPWatchRelay(t *testing.T, p connections.Profile, mode string) (connections.Profile, *atomic.Int32) {
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
			t.Fatal("original CA invalid")
		}
	}
	if cfg.Options.ClientCertFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.Options.ClientCertFile, cfg.Options.ClientKeyFile)
		if err != nil {
			t.Fatal(err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	transport := &http.Transport{TLSClientConfig: tc, DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	up := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	calls := &atomic.Int32{}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, err := http.NewRequestWithContext(r.Context(), r.Method, cfg.Endpoint+r.URL.RequestURI(), r.Body)
		if err != nil {
			return
		}
		request.Header = r.Header.Clone()
		response, err := up.Do(request)
		if err != nil {
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return
		}
		isRead := r.URL.Query().Has("recurse") && r.URL.Query().Has("wait")
		if isRead {
			calls.Add(1)
		}
		for k, values := range response.Header {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
		if isRead {
			switch mode {
			case "zero":
				w.Header().Set("X-Consul-Index", "0")
			case "missing":
				w.Header().Del("X-Consul-Index")
			case "invalid":
				w.Header().Set("X-Consul-Index", "18446744073709551616")
			case "backward":
				if calls.Load() == 1 {
					w.Header().Set("X-Consul-Index", "18446744073709551615")
				} else {
					w.Header().Set("X-Consul-Index", "1")
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
			case "http500":
				w.WriteHeader(500)
				w.Write([]byte("original processed read reply withheld"))
				return
			case "http408":
				w.WriteHeader(408)
				w.Write([]byte("original processed read reply withheld"))
				return
			}
		}
		w.WriteHeader(response.StatusCode)
		w.Write(body)
	}))
	t.Cleanup(relay.Close)
	u, _ := url.Parse(relay.URL)
	port, _ := strconv.Atoi(u.Port())
	p.Host, p.Port, p.TLSMode = u.Hostname(), uint16(port), "disable"
	p.ConsulOptions.CAFile, p.ConsulOptions.ClientCertFile, p.ConsulOptions.ClientKeyFile, p.ConsulOptions.ServerName = "", "", "", ""
	return p, calls
}

func TestConsulNativePublicHTTPWatchOriginalTerminalHeadersReadFailuresAndACL(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	for _, mode := range []string{"zero", "missing", "invalid", "backward", "lost", "partial", "http500", "http408"} {
		t.Run(mode, func(t *testing.T) {
			proxy, calls := consulHTTPWatchRelay(t, p, mode)
			s, d, m := consulWorkspaceState(t, proxy, secret)
			s.deps.HTTPWatchProviderFor = m.KVHTTPWatch
			d.httpWatchAvailable = true
			provider, err := m.KVRequests(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			writer, err := provider.ReserveKV(7401)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			ancestor := "native/public-watch/terminal/" + mode
			key := ancestor + "/key"
			consulHTTPWatchWrite(t, writer, key, []byte{0, 255})
			consulHTTPWatchInput(t, s, d, key, ancestor, 4, 16<<20)
			s.StartHTTPWatch(d)
			eventEventually(t, func() bool { s.Consume(); return !d.Busy() })
			if d.httpWatch.run == nil || d.HTTPWatchStatus().Observations < 1 {
				t.Fatal("original failed read evidence lost", d.Error())
			}
			expected := int32(1)
			if mode == "backward" {
				expected = 2
			}
			if calls.Load() != expected {
				t.Fatal("read failure reset or reconnected", mode, calls.Load())
			}
			s.SelectHTTPWatchObservation(d, d.HTTPWatchStatus().Observations-1)
			if mode == "zero" || mode == "missing" || mode == "invalid" || mode == "backward" {
				o, ok := d.httpWatch.run.(contract.KVHTTPWatchIndexedRun).Observation(d.HTTPWatchStatus().Observations - 1)
				if !ok || !o.HTTP.Captured || !bytes.Contains(o.HTTP.Body, []byte("AP8=")) {
					t.Fatal("index error replaced original body")
				}
			}
			raw, err := s.HTTPWatchRecord(d)
			if err != nil || !bytes.Contains(raw, []byte("original_http")) {
				t.Fatal("terminal record export failed", err)
			}
			consulHTTPWatchScreenshot(t, s, d, "terminal-"+mode)
		})
	}
	t.Run("acl", func(t *testing.T) {
		_, _, manager := consulWorkspaceState(t, p, secret)
		provider, err := manager.KVRequests(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		writer, err := provider.ReserveKV(7501)
		if err != nil {
			t.Fatal(err)
		}
		defer writer.Close()
		consulHTTPWatchWrite(t, writer, "blocked/public-watch/key", []byte{0, 255})
		token := os.Getenv("NYADB_CONSUL_READ_TOKEN")
		if token == "" {
			token = secret
		}
		s, d, m := consulWorkspaceState(t, p, token)
		s.deps.HTTPWatchProviderFor = m.KVHTTPWatch
		d.httpWatchAvailable = true
		consulHTTPWatchInput(t, s, d, "blocked/public-watch/key", "blocked/public-watch", 1, 16<<20)
		s.StartHTTPWatch(d)
		eventEventually(t, func() bool { s.Consume(); return !d.Busy() })
		if d.httpWatch.run == nil {
			t.Fatal("native ACL observation missing", d.Error())
		}
		o, ok := d.httpWatch.run.(contract.KVHTTPWatchIndexedRun).Observation(0)
		if !ok {
			t.Fatal("original observation absent")
		}
		if os.Getenv("NYADB_CONSUL_READ_TOKEN") != "" {
			if o.HTTP.Status != 404 || len(o.Matches) != 0 {
				t.Fatal("original filtered range changed")
			}
		} else if o.HTTP.Status != 200 || len(o.Matches) != 1 {
			t.Fatal("anonymous original entry missing")
		}
		if os.Getenv("NYADB_CONSUL_READ_TOKEN") != "" && (o.HTTP.Metadata.ACLFiltered == nil || !*o.HTTP.Metadata.ACLFiltered || !d.httpWatch.aclFiltered) {
			t.Fatal("actual ACL-filtered absence lost")
		}
		consulHTTPWatchScreenshot(t, s, d, "terminal-acl")
	})
}
func consulHTTPWatchScreenshot(t *testing.T, s *State, d *Document, stage string) {
	t.Helper()
	defer i18n.SetLocaleFunc(nil)
	for _, lang := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return lang })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, size[0], size[1])
			tt.Frame()
			for _, label := range []string{"启动 HTTP Watch", "Watch 原目标", "Watch 实际范围"} {
				r, ok := tt.Find(locales.Text(label))
				if !ok || r.W <= 0 || r.H <= 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
					t.Fatal("Watch control clipped", stage, lang, size, label, r)
				}
			}
			if d.httpWatch != nil && d.httpWatch.run != nil {
				if _, ok := tt.Find(locales.Text("复制完整 Watch 记录")); !ok {
					t.Fatal("original record copy inaccessible", stage, lang)
				}
			}
			folder := os.Getenv("NYADB_CONSUL_WATCH_IMAGES")
			if folder == "" {
				continue
			}
			if err := os.MkdirAll(folder, 0700); err != nil {
				t.Fatal(err)
			}
			f, err := os.Create(filepath.Join(folder, fmt.Sprintf("%s-%dx%d-workspace-watch-%s.png", lang, size[0], size[1], stage)))
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(f, tt.Image())
			closeErr := f.Close()
			if err != nil || closeErr != nil {
				t.Fatal(err, closeErr)
			}
		}
	}
}
func consulHTTPWatchWrite(t *testing.T, l contract.KVRequestLease, key string, value []byte) {
	t.Helper()
	flags := uint64(math.MaxUint64)
	native, err := consul.KeyPutRequest(key, value, &flags, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := consul.DocumentRequest(native)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := l.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	out, err := l.Execute(context.Background(), plan)
	if err != nil || !out.Completed {
		t.Fatal("original write failed", err, out.Code)
	}
}

func TestConsulNativePublicHTTPWatchOriginalRangesIndexedRecordsParkingAndExplicitStop(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	s, d, m := consulWorkspaceState(t, p, secret)
	s.deps.HTTPWatchProviderFor = m.KVHTTPWatch
	d.httpWatchAvailable = true
	provider, err := m.KVRequests(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := provider.ReserveKV(7301)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ancestor := "native/public-watch/" + t.Name()
	target := ancestor + "/double//猫\n?%/"
	consulHTTPWatchWrite(t, writer, target, []byte{0, 255})
	consulHTTPWatchInput(t, s, d, target, ancestor, 32, 16<<20)
	consulHTTPWatchScreenshot(t, s, d, "template")
	s.StartHTTPWatch(d)
	eventEventually(t, func() bool { s.Consume(); return d.HTTPWatchStatus().Observations > 0 })
	w := d.httpWatch
	indexed, ok := w.run.(contract.KVHTTPWatchIndexedRun)
	if !ok {
		t.Fatal("native indexed archive missing")
	}
	o, ok := indexed.Observation(0)
	if !ok || len(o.Matches) != 1 {
		t.Fatal("original target not matched")
	}
	entry := o.HTTP.Entries[o.Matches[0]]
	if entry.Key != target || entry.Flags != math.MaxUint64 || !bytes.Equal(entry.Value, []byte{0, 255}) || o.HTTP.Metadata.Index == nil {
		t.Fatal("original bytes/index changed")
	}
	consulHTTPWatchScreenshot(t, s, d, "observing")
	active := false
	s.deps.Active = func(*Document) bool { return active }
	oldCount := w.status.Observations
	consulHTTPWatchWrite(t, writer, target, []byte{})
	eventEventually(t, func() bool { _, ok := indexed.Observation(oldCount); return ok })
	s.Consume()
	if w.status.Observations != oldCount {
		t.Fatal("parked private buffer consumed")
	}
	consulHTTPWatchScreenshot(t, s, d, "parked")
	active = true
	s.Consume()
	s.SelectHTTPWatchObservation(d, w.status.Observations-1)
	consulHTTPWatchScreenshot(t, s, d, "selected")
	raw, err := s.HTTPWatchRecord(d)
	if err != nil || !bytes.Contains(raw, []byte("18446744073709551615")) || !bytes.Contains(raw, []byte("original_http")) {
		t.Fatal("complete typed record missing", err)
	}
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); theme.Apply(c); s.View(c, d) }, 1280, 800)
	tt.Frame()
	s.CopyHTTPWatchRecord(services, d)
	eventEventually(t, func() bool { s.Consume(); return !d.FileBusy() })
	if !bytes.Contains([]byte(tt.Clipboard()), []byte("original_http")) {
		t.Fatal("complete clipboard missing")
	}
	s.StopHTTPWatch(d)
	eventEventually(t, func() bool { s.Consume(); return !d.Busy() })
	consulHTTPWatchScreenshot(t, s, d, "stopped")
	before := w.run.Request()
	s.StartHTTPWatch(d)
	if w.run.Request() != before || !w.status.Ended {
		t.Fatal("old archive restarted")
	}
	s.DisconnectProfile(p.ID)
	if err := m.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	s.RecoverHTTPWatch(d)
	eventEventually(t, func() bool { s.Consume(); return !d.Busy() })
	if d.NeedsRecovery() || d.httpWatch.run == nil || !d.httpWatch.status.Ended || !sameEventScope(d.httpWatch.evidenceScope, w.evidenceScope) {
		t.Fatal("recovery restarted or lost original evidence", d.Error())
	}
	consulHTTPWatchScreenshot(t, s, d, "recovered")
}

func TestConsulNativePublicHTTPWatchCompleteOverflowAndIndependentUnknownWrite(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	proxy, calls, committed := consulWorkspaceFault(t, p, "lost")
	s, d, m := consulWorkspaceState(t, proxy, secret)
	s.deps.HTTPWatchProviderFor = m.KVHTTPWatch
	d.httpWatchAvailable = true
	target := "native/public-watch/" + t.Name() + "/key"
	put, _ := consul.KeyPutRequest(target, bytes.Repeat([]byte{0, 255}, 50000), nil, nil)
	consulWorkspaceInput(t, s, d, put)
	s.Run(d)
	s.Confirm(d, true)
	await(t, committed)
	eventEventually(t, func() bool { s.Consume(); return !d.Busy() })
	unknown := d.unknown
	if unknown == nil || calls.Load() != 1 {
		t.Fatal("actual unknown write missing")
	}
	consulHTTPWatchInput(t, s, d, target, "native/public-watch/"+t.Name(), 4, 1)
	s.StartHTTPWatch(d)
	eventEventually(t, func() bool { s.Consume(); return !d.Busy() })
	if d.httpWatch.run == nil || !d.HTTPWatchStatus().TerminalOverflow || d.HTTPWatchStatus().Observations != 1 || d.unknown != unknown || calls.Load() != 1 {
		t.Fatal("overflow evicted evidence or confirmed/replayed original write", d.Error())
	}
	o, ok := d.httpWatch.run.(contract.KVHTTPWatchIndexedRun).Observation(0)
	if !ok || len(o.Matches) != 1 || len(o.HTTP.Entries[o.Matches[0]].Value) != 100000 || !o.HTTP.EOF {
		t.Fatal("complete original terminal response lost")
	}
	consulHTTPWatchScreenshot(t, s, d, "overflow")
	raw, err := s.HTTPWatchRecord(d)
	if err != nil || len(raw) < 100000 {
		t.Fatal("overflow export truncated", err)
	}
	consulHTTPWatchScreenshot(t, s, d, "unknown")
	s.ClearHTTPWatch(d)
	if d.unknown != unknown || d.httpWatch.run != nil {
		t.Fatal("explicit read archive clear changed original unknown")
	}
	s.SetMethod(d, contract.KVConsulHTTPRequestMethod)
	consulWorkspaceInput(t, s, d, put)
	s.Run(d)
	if d.confirmation != nil || d.Busy() || calls.Load() != 1 {
		t.Fatal("later observation removed write barrier")
	}
}
