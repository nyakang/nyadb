package kvworkspace

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/consul"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func consulSessionToolFault(t *testing.T, p connections.Profile, mode string) (connections.Profile, *atomic.Int32, <-chan struct{}) {
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
		if r.Method == "PUT" && (r.URL.Path == "/v1/session/create" || strings.HasPrefix(r.URL.Path, "/v1/session/destroy/") || strings.HasPrefix(r.URL.Path, "/v1/kv/") && (r.URL.Query().Has("acquire") || r.URL.Query().Has("release"))) {
			calls.Add(1)
		}
		if r.Method == "PUT" && (r.URL.Path == "/v1/session/create" || strings.HasPrefix(r.URL.Path, "/v1/session/destroy/") || strings.HasPrefix(r.URL.Path, "/v1/kv/") && (r.URL.Query().Has("acquire") || r.URL.Query().Has("release"))) && response.StatusCode == 200 && once.CompareAndSwap(false, true) {
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

func TestConsulNativePublicSessionToolsProcessedWritesParkingACKRecoveryAndNoReplay(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPSessionCreate, contract.KVToolHTTPSessionDestroy, contract.KVToolHTTPAcquire, contract.KVToolHTTPRelease} {
		for _, mode := range []string{"lost", "partial", "http500", "http408", "cancel", "disconnect", "replace", "close"} {
			t.Run(string(kind)+"/"+mode, func(t *testing.T) {
				key := "native/workspace/session-tool-fault/" + strings.ReplaceAll(t.Name(), "/", "-")
				id := ""
				input := contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{IncludeName: true, Name: t.Name(), TTL: "10m", NodeChecksJSON: "[]", LockDelay: "0s"}}
				if kind != contract.KVToolHTTPSessionCreate {
					id = consulSessionToolCreate(t, writer, t.Name())
					input = contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{ID: id}}
					if kind == contract.KVToolHTTPAcquire || kind == contract.KVToolHTTPRelease {
						input.Key, input.ValueBase64, input.Flags = key, "AP8=", "18446744073709551615"
						if kind == contract.KVToolHTTPRelease {
							path, err := consul.KeyPath(key)
							if err != nil {
								t.Fatal(err)
							}
							consulRenewalDo(t, writer, consul.Request{Method: "PUT", Path: path, Query: url.Values{"acquire": {id}}, Body: []byte{0, 255}})
						}
					}
				}
				proxy, calls, processed := consulSessionToolFault(t, p, mode)
				s, d, m := consulWorkspaceState(t, proxy, secret)
				s.deps.HTTPSessionRenewalProviderFor = m.KVHTTPSessionRenewal
				d.httpSessionRenewalAvailable = true
				consulWorkspaceToolInput(t, s, d, kind, input)
				original := d.Request()
				s.Run(d)
				if d.confirmation == nil || d.confirmation.authored != original || calls.Load() != 0 {
					t.Fatal("original tool lacked independent confirmation")
				}
				s.Confirm(d, true)
				s.Confirm(d, true)
				job, lease := d.job, d.lease
				await(t, processed)
				active := false
				s.deps.Active = func(*Document) bool { return active }
				switch mode {
				case "cancel":
					s.RequestCancel(d)
					if !d.Busy() || !d.CancelRequested() {
						t.Fatal("cancel falsely settled before original ACK")
					}
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
				// Verify actual service-side effects through an independent original
				// connection; these observations do not authorize the unknown document.
				switch kind {
				case contract.KVToolHTTPSessionCreate:
					result := consulRenewalDo(t, writer, consul.Request{Method: "GET", Path: "/v1/session/list"})
					var sessions []struct{ ID, Name string }
					if json.Unmarshal(result.HTTP.Body, &sessions) != nil {
						t.Fatal("native Session list invalid")
					}
					count := 0
					for _, session := range sessions {
						if session.Name == t.Name() {
							count++
							id = session.ID
						}
					}
					if count != 1 {
						t.Fatal("actual create missing or replayed", count)
					}
					t.Cleanup(func() { consulRenewalDo(t, writer, consul.Request{Method: "PUT", Path: "/v1/session/destroy/" + id}) })
				case contract.KVToolHTTPSessionDestroy:
					result := consulRenewalDo(t, writer, consul.Request{Method: "GET", Path: "/v1/session/info/" + id})
					if !bytes.Equal(bytes.TrimSpace(result.HTTP.Body), []byte("[]")) {
						t.Fatal("actual original destroy missing")
					}
				default:
					read, _ := consul.KeyReadRequest(key)
					result := consulRenewalDo(t, writer, read)
					expectedID := id
					if kind == contract.KVToolHTTPRelease {
						expectedID = ""
					}
					if result.HTTP == nil || len(result.HTTP.Entries) != 1 {
						t.Fatal("actual key missing")
					}
					e := result.HTTP.Entries[0]
					if e.Session != expectedID || e.Flags != math.MaxUint64 || !bytes.Equal(e.Value, []byte{0, 255}) {
						t.Fatal("actual lock effect differs from authored request")
					}
				}
				if mode == "close" {
					if lease.CheckKVLease() == nil || d.Editor != nil || d.unknown != nil || d.requestTools != nil || calls.Load() != 1 {
						t.Fatal("closed original tool retained caches or replayed")
					}
					return
				}
				s.Consume()
				if d.job != job || d.unknown != nil || !d.Busy() {
					t.Fatal("parked original completion admitted")
				}
				active = true
				s.Consume()
				if d.unknown == nil || d.unknown.Authored != original || !d.unknown.Result.WriteOutcomeUnknown || d.unknown.Result.HTTP.Acknowledged || d.unknown.Result.HTTP.SessionID != "" || calls.Load() != 1 {
					t.Fatal("processed tool guessed UUID, settled or replayed")
				}
				unknown := d.unknown.Clone()
				if mode == "partial" && (len(unknown.Result.HTTP.Body) == 0 || unknown.Result.HTTP.Captured || unknown.Result.HTTP.EOF) {
					t.Fatal("partial original reply changed")
				}
				consulSessionToolScreenshot(t, s, d, string(kind)+"-unknown-"+mode)
				if mode == "disconnect" {
					if err := m.Connect(context.Background(), proxy, secret); err != nil {
						t.Fatal(err)
					}
				}
				s.Recover(d)
				consume(t, s, d)
				if d.NeedsRecovery() || d.Request() != original || d.unknown == nil || !bytes.Equal(d.unknown.Result.HTTP.Body, unknown.Result.HTTP.Body) || calls.Load() != 1 {
					t.Fatal("original tool recovery executed or lost evidence")
				}
				s.Run(d)
				if d.confirmation != nil || d.Busy() {
					t.Fatal("recovered unknown tool authorized replay")
				}
				consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPSessionInfo, contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{ID: id}})
				s.Run(d)
				consume(t, s, d)
				if d.last == nil || d.unknown == nil || d.unknown.Result.HTTP.SessionID != "" || !bytes.Equal(d.unknown.Result.HTTP.Body, unknown.Result.HTTP.Body) {
					t.Fatal("later Session read confirmed original unknown")
				}
				consulHTTPSessionRenewalInput(s, d, id, "100ms", 2, 16<<20)
				s.PrepareHTTPSessionRenewal(d)
				if d.httpSessionRenewal.confirmation != nil || d.Busy() {
					t.Fatal("unknown generated tool authorized renewal")
				}
				consulWorkspaceToolInput(t, s, d, kind, input)
				s.Run(d)
				if d.confirmation != nil || d.Busy() || calls.Load() != 1 {
					t.Fatal("regeneration cleared original write barrier")
				}
			})
		}
	}
}
