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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func consulWorkspaceProfile(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_CONSUL_IT") != "1" {
		t.Skip("owned native Consul fixture required")
	}
	u, err := url.Parse(os.Getenv("NYADB_CONSUL_ENDPOINT"))
	if err != nil || u.Hostname() == "" {
		t.Fatal("missing original fixture")
	}
	p, _ := connections.Defaults(connections.Consul)
	p.ID, p.Name, p.Host = 71, "original Consul HTTP", u.Hostname()
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	p.Port = uint16(port)
	p.ConsulOptions.Auth, p.ConsulOptions.Datacenter, p.ConsulOptions.Prefix = os.Getenv("NYADB_CONSUL_AUTH"), "dc1", "native/workspace/"
	if u.Scheme == "https" {
		p.TLSMode = "verify-full"
	}
	p.ConsulOptions.CAFile = os.Getenv("NYADB_CONSUL_CA")
	p.ConsulOptions.ClientCertFile = os.Getenv("NYADB_CONSUL_CERT")
	p.ConsulOptions.ClientKeyFile = os.Getenv("NYADB_CONSUL_KEY")
	return p, os.Getenv("NYADB_CONSUL_TOKEN")
}
func consulWorkspaceState(t *testing.T, p connections.Profile, secret string) (*State, *Document, *dbruntime.Manager) {
	t.Helper()
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	if err := m.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	s := New(Dependencies{ProviderFor: m.KVRequests, EventsProviderFor: m.KVEvents, RenewalProviderFor: m.KVRenewal, ProfileName: func(int64) string { return p.Name }})
	d := s.Open(1, p.ID, contract.KVRequest{})
	t.Cleanup(s.CloseAll)
	if d == nil || d.NeedsRecovery() {
		t.Fatal("public HTTP document unavailable")
	}
	return s, d, m
}
func consulWorkspaceInput(t *testing.T, s *State, d *Document, r consul.Request) {
	t.Helper()
	request, err := consul.DocumentRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	s.SetMethod(d, request.Method)
	d.Editor.Buffer.Set(request.JSON)
}
func consulWorkspaceScreenshot(t *testing.T, s *State, d *Document, stage string) {
	t.Helper()
	defer i18n.SetLocaleFunc(nil)
	for _, lang := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return lang })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, size[0], size[1])
			tt.Frame()
			labels := []string{"运行 KV 请求", "复制完整操作记录"}
			toolOpen := d.requestTools != nil && d.requestTools.open
			if s.selectedReceipt(d) == nil {
				labels = []string{"运行 KV 请求", "KV JSON 编辑器"}
			}
			if d.confirmation == nil && !toolOpen && s.selectedReceipt(d) != nil {
				r, ok := tt.Find(locales.Text("KV 原生回执"))
				if !ok || r.H < 24 || r.Y+r.H > float32(size[1])+1 {
					t.Fatal("HTTP body preview clipped", stage, lang, size, r)
				}
			}
			if d.confirmation != nil {
				labels = []string{"我确认执行此原生写入请求", "复制完整请求", "确认执行 KV 写入"}
			}
			if toolOpen {
				labels = []string{"生成请求文本", "返回请求文本", "请求工具操作"}
				if d.requestTools.kind == contract.KVToolTransaction {
					labels = append(labels, "事务 JSON（保留原始字节）")
				} else {
					labels = append(labels, "绝对键或前缀")
				}
				if d.requestTools.kind == contract.KVToolPut {
					labels = append(labels, "原值 Base64（空文本表示空字节值）", "Flags (uint64)", "CAS (uint64)")
				}
				if d.requestTools.kind == contract.KVToolDelete {
					labels = append(labels, "CAS (uint64)", "递归删除此前缀（不能与 CAS 同用）")
				}
			}
			for _, label := range labels {
				r, ok := tt.Find(locales.Text(label))
				if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
					t.Fatal("original HTTP control clipped", stage, lang, size, label, r)
				}
			}
			if dir := os.Getenv("NYADB_CONSUL_PUBLIC_IMAGES"); dir != "" {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%dx%d-workspace-%s.png", lang, size[0], size[1], stage)))
				if err != nil {
					t.Fatal(err)
				}
				err = png.Encode(f, tt.Image())
				closeErr := f.Close()
				if err != nil || closeErr != nil {
					t.Fatal("original HTTP screenshot failed")
				}
			}
		}
	}
}
func TestConsulNativePublicWorkspaceOriginalHTTPConfirmationReceiptsFilesAndRecovery(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	s, d, m := consulWorkspaceState(t, p, secret)
	nativeTemplate, _ := consul.KeyListRequest(p.ConsulOptions.Prefix, true)
	expectedTemplate, _ := consul.DocumentRequest(nativeTemplate)
	if methods := s.MethodsFor(d); len(methods) != 1 || methods[0].Name != "HTTP" || d.Request() != expectedTemplate || d.eventsAvailable || d.LeaseBrowsingAvailable() {
		t.Fatal("public document exposed foreign methods")
	}
	key := p.ConsulOptions.Prefix + t.Name()
	flags := uint64(math.MaxUint64)
	put, err := consul.KeyPutRequest(key, []byte{0, 255, 128}, &flags, nil)
	if err != nil {
		t.Fatal(err)
	}
	consulWorkspaceInput(t, s, d, put)
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, 1280, 800)
	if err = tt.Click(locales.Text("运行 KV 请求")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if d.confirmation == nil || d.Busy() {
		t.Fatal("original HTTP write bypassed confirmation")
	}
	consulWorkspaceScreenshot(t, s, d, "write-confirmation")
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	tt.Frame()
	if err = tt.Click(locales.Text("确认执行 KV 写入")); err != nil {
		t.Fatal(err)
	}
	if d.Busy() {
		t.Fatal("unchecked HTTP request dispatched")
	}
	for _, label := range []string{"我确认执行此原生写入请求", "确认执行 KV 写入"} {
		if err = tt.Click(locales.Text(label)); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	consume(t, s, d)
	write, ok := d.LastReceipt()
	if !ok || write.Result.HTTP == nil || !write.Result.HTTP.Acknowledged || !write.Result.HTTP.Succeeded || !write.Result.Completed || write.Result.Native != nil {
		t.Fatal("original HTTP write receipt missing")
	}
	read, _ := consul.KeyReadRequest(key)
	consulWorkspaceInput(t, s, d, read)
	s.Run(d)
	consume(t, s, d)
	receipt, _ := d.LastReceipt()
	h := receipt.Result.HTTP
	if h == nil || len(h.Entries) != 1 || h.Entries[0].Flags != math.MaxUint64 || !bytes.Equal(h.Entries[0].Value, []byte{0, 255, 128}) {
		t.Fatal("original HTTP typed bytes/precision missing")
	}
	consulWorkspaceScreenshot(t, s, d, "complete-body")
	d.httpHeaders = true
	consulWorkspaceScreenshot(t, s, d, "headers")
	d.httpHeaders = false
	raw, err := s.OperationRecord(d)
	if err != nil || !bytes.Contains(raw, []byte(`"18446744073709551615"`)) || bytes.Contains(raw, []byte("protobuf_json")) {
		t.Fatal("original HTTP operation record changed", err)
	}
	var exported []byte
	s.SetFiles(Files{SavePath: func(string) (string, error) { return "original", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var b bytes.Buffer
		err := write(&b)
		exported = b.Bytes()
		return err
	}})
	s.ExportReceipt(d, true)
	consume(t, s, d)
	if !bytes.Equal(exported, h.Body) {
		t.Fatal("actual original body export changed")
	}
	original := d.Request()
	encoded, err := EncodeRequest(original)
	if err != nil {
		t.Fatal(err)
	}
	s.SetFiles(Files{OpenPath: func() (string, error) { return "original.nyakv", nil }, OpenReader: func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(encoded)), nil }})
	d.Editor.Buffer.Set("unfinished {")
	s.OpenRequest(d)
	consume(t, s, d)
	if d.Request() != original {
		t.Fatal("HTTP request file changed authored text")
	}
	generation := d.scope.Generation
	m.Disconnect(p.ID)
	s.DisconnectProfile(p.ID)
	if !d.NeedsRecovery() || d.Request() != original {
		t.Fatal("disconnect lost original draft")
	}
	if err = m.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	s.Recover(d)
	consume(t, s, d)
	if d.NeedsRecovery() || d.scope.Generation <= generation || d.Request() != original {
		t.Fatal("pure recovery changed original document")
	}
	before, _ := d.LastReceipt()
	if !bytes.Equal(before.Result.HTTP.Body, h.Body) {
		t.Fatal("recovery changed original receipt")
	}
	active := true
	s.deps.Active = func(*Document) bool { return active }
	s.Run(d)
	job := d.job
	await(t, job.finished)
	active = false
	s.Consume()
	if d.job != job {
		t.Fatal("parked HTTP receipt consumed")
	}
	active = true
	s.Consume()
	s.Close(d)
	if d.Editor != nil || d.last != nil || d.unknown != nil || d.lease != nil {
		t.Fatal("close retained original HTTP caches")
	}
}

// The relay faults only after observing a successful write from the actual
// original fixture. Upstream TLS and mTLS use its verified owned certificates.
func consulWorkspaceFault(t *testing.T, p connections.Profile, mode string) (connections.Profile, *atomic.Int32, <-chan struct{}) {
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
		if r.Method == "PUT" && response.StatusCode == 200 && bytes.Equal(bytes.TrimSpace(body), []byte("true")) && once.CompareAndSwap(false, true) {
			calls.Add(1)
			close(committed)
			switch mode {
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
func TestConsulNativePublicWorkspaceActualProcessedReplyFaultsNeverPromoteUnknownOrReplay(t *testing.T) {
	testConsulWorkspaceProcessedFaults(t, false)
}

func TestConsulNativePublicWorkspaceStructuredToolsProcessedReplyFaultsNeverReplay(t *testing.T) {
	testConsulWorkspaceProcessedFaults(t, true)
}

func testConsulWorkspaceProcessedFaults(t *testing.T, tools bool) {
	p, secret := consulWorkspaceProfile(t)
	for _, mode := range []string{"lost", "partial", "http500", "http408", "cancel", "disconnect", "replace", "close"} {
		t.Run(mode, func(t *testing.T) {
			proxy, calls, committed := consulWorkspaceFault(t, p, mode)
			s, d, m := consulWorkspaceState(t, proxy, secret)
			key := "native/workspace/fault/" + strings.ReplaceAll(t.Name(), "/", "-")
			put, _ := consul.KeyPutRequest(key, []byte{0, 255}, nil, nil)
			if tools {
				consulWorkspaceToolInput(t, s, d, contract.KVToolPut, contract.KVRequestToolInput{Key: key, ValueBase64: "AP8="})
			} else {
				consulWorkspaceInput(t, s, d, put)
			}
			s.Run(d)
			s.Confirm(d, true)
			job := d.job
			lease := d.lease
			await(t, committed)
			switch mode {
			case "cancel":
				s.RequestCancel(d)
			case "disconnect":
				m.Disconnect(p.ID)
				s.DisconnectProfile(p.ID)
			case "replace":
				if err := m.Connect(context.Background(), proxy, secret); err != nil {
					t.Fatal(err)
				}
			case "close":
				s.Close(d)
			}
			await(t, job.finished)
			if mode == "close" {
				if lease.CheckKVLease() == nil || d.Editor != nil || d.last != nil || d.unknown != nil || calls.Load() != 1 {
					t.Fatal("closed original executor lost unknown or replayed")
				}
				return
			}
			s.Consume()
			if d.unknown == nil || !d.unknown.Result.WriteOutcomeUnknown || d.unknown.Result.HTTP.Acknowledged || calls.Load() != 1 {
				t.Fatal("actual processed write was promoted/replayed")
			}
			original := d.unknown.Clone()
			stagePrefix := ""
			if tools {
				stagePrefix = "tool-"
			}
			consulWorkspaceScreenshot(t, s, d, stagePrefix+"unknown-"+mode)
			if mode == "disconnect" {
				if err := m.Connect(context.Background(), proxy, secret); err != nil {
					t.Fatal(err)
				}
			}
			s.Recover(d)
			consume(t, s, d)
			if d.NeedsRecovery() || calls.Load() != 1 {
				t.Fatal("recovery replayed original write")
			}
			s.Run(d)
			if d.confirmation != nil || d.Busy() {
				t.Fatal("unknown write became repeatable")
			}
			read, _ := consul.KeyReadRequest(key)
			if tools {
				consulWorkspaceToolInput(t, s, d, contract.KVToolRead, contract.KVRequestToolInput{Key: key})
			} else {
				consulWorkspaceInput(t, s, d, read)
			}
			s.Run(d)
			consume(t, s, d)
			h := d.last.Result.HTTP
			if h == nil || !h.Acknowledged || !h.Succeeded || len(h.Entries) != 1 || !bytes.Equal(h.Entries[0].Value, []byte{0, 255}) || calls.Load() != 1 || d.unknown.Result.HTTP.Acknowledged || !bytes.Equal(d.unknown.Result.HTTP.Body, original.Result.HTTP.Body) {
				t.Fatal("actual later read erased/promoted original unknown")
			}
			d.selectedUnknown = true
			record, err := s.OperationRecord(d)
			var decoded map[string]json.RawMessage
			if err != nil || json.Unmarshal(record, &decoded) != nil || string(decoded["write_outcome_unknown"]) != "true" {
				t.Fatal("original unknown record changed", err)
			}
			consulWorkspaceScreenshot(t, s, d, stagePrefix+"unknown-after-read-"+mode)
		})
	}
}
