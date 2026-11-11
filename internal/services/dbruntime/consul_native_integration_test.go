package dbruntime_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

func consulRuntimeConfig(t *testing.T) (consul.Config, string) {
	t.Helper()
	if os.Getenv("NYADB_CONSUL_IT") != "1" {
		t.Skip("owned native Consul fixture required")
	}
	return consul.Config{Endpoint: os.Getenv("NYADB_CONSUL_ENDPOINT"), Auth: os.Getenv("NYADB_CONSUL_AUTH"), CAFile: os.Getenv("NYADB_CONSUL_CA"), ClientCertFile: os.Getenv("NYADB_CONSUL_CERT"), ClientKeyFile: os.Getenv("NYADB_CONSUL_KEY"), RequestTimeout: 30 * time.Second}, os.Getenv("NYADB_CONSUL_TOKEN")
}

// This private injected feature test deliberately uses an existing profile kind;
// it does not publish a Consul descriptor, builtin provider, URI or chooser item.
func consulRuntimeProfile() connections.Profile {
	return connections.Profile{ID: 901, Name: "private Consul feature fixture", Kind: connections.SQLite, Database: ":memory:"}
}
func consulRuntimeManager(t *testing.T, cfg consul.Config) *dbruntime.Manager {
	t.Helper()
	m := dbruntime.New()
	t.Cleanup(func() { m.Close() })
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
		return consul.OpenNativeSession(ctx, p.ID, cfg, secret)
	}))
	return m
}
func consulRuntimeReserve(t *testing.T, m *dbruntime.Manager, document uint64) contract.KVRequestLease {
	t.Helper()
	provider, err := m.KVRequests(consulRuntimeProfile().ID)
	if err != nil {
		t.Fatal(err)
	}
	l, err := provider.ReserveKV(document)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}
func consulRuntimePlan(t *testing.T, l contract.KVRequestLease, request consul.Request) contract.KVPreparation {
	t.Helper()
	r, err := consul.DocumentRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	p, err := l.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func consulRuntimeDo(t *testing.T, l contract.KVRequestLease, request consul.Request) contract.KVResult {
	t.Helper()
	r, err := l.Execute(context.Background(), consulRuntimePlan(t, l, request))
	if err != nil || !r.Completed || r.HTTP == nil || !r.HTTP.Acknowledged || !r.HTTP.Captured || !r.HTTP.Ended {
		t.Fatal("original HTTP document receipt missing", err)
	}
	return r
}
func consulRuntimeRead(t *testing.T, key string) consul.Request {
	t.Helper()
	r, err := consul.KeyReadRequest(key)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func consulRuntimePut(t *testing.T, key string, value []byte) consul.Request {
	t.Helper()
	flags := uint64(math.MaxUint64)
	r, err := consul.KeyPutRequest(key, value, &flags, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func consulRuntimeAwait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(15 * time.Second):
		t.Fatal("original native executor did not settle")
	}
}

func TestConsulNativeRuntimeGenerationsRequestsAndOriginalRecovery(t *testing.T) {
	cfg, secret := consulRuntimeConfig(t)
	m := consulRuntimeManager(t, cfg)
	p := consulRuntimeProfile()
	first, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	l := consulRuntimeReserve(t, m, 1)
	key := "native/runtime/" + t.Name()
	value := []byte{0, 255, 128}
	plan := consulRuntimePlan(t, l, consulRuntimePut(t, key, value))
	if l.Scope().Generation != first.Generation() || l.Scope().HTTP.AgentID != os.Getenv("NYADB_CONSUL_NODE_ID") || l.Scope().Header != (contract.KVHeader{}) {
		t.Fatal("public generation or original HTTP identity changed")
	}
	value[0] = 9
	r, err := l.Execute(context.Background(), plan)
	if err != nil || r.Native != nil || r.NativeCaptured || r.Reply != nil || r.Header != (contract.KVHeader{}) {
		t.Fatal("HTTP receipt projected as protobuf", err)
	}
	read := consulRuntimeDo(t, l, consulRuntimeRead(t, key))
	if len(read.HTTP.Entries) != 1 || read.HTTP.Entries[0].Flags != math.MaxUint64 || !bytes.Equal(read.HTTP.Entries[0].Value, []byte{0, 255, 128}) {
		t.Fatal("original bytes or unsigned flags changed")
	}
	fresh, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil || fresh.Generation() <= first.Generation() || l.CheckKVLease() == nil {
		t.Fatal("replacement failed to revoke original wrapper", err)
	}
	provider, _ := m.KVRequests(p.ID)
	recovery, err := provider.RecoverKV(l)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Lease.Close()
	if recovery.Lease.Scope().Generation != fresh.Generation() || recovery.Last.Request != read.Request || !bytes.Equal(recovery.Last.HTTP.Body, read.HTTP.Body) || recovery.Unknown != nil {
		t.Fatal("pure recovery discarded original evidence")
	}
	if _, err = recovery.Lease.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("replacement authorized original write plan")
	}
	recovery.Last.HTTP.Entries[0].Value[0] = 9
	if recovery.Lease.LastResult().HTTP.Entries[0].Value[0] != 0 {
		t.Fatal("recovery inspection mutated retained receipt")
	}
	consulRuntimeDo(t, recovery.Lease, consulRuntimeRead(t, key))
}

// Forward to the actual original service, then fault only an observed successful
// write reply. TLS/mTLS on the upstream is verified using owned fixture material.
func consulRuntimeFault(t *testing.T, cfg consul.Config, mode string, dataArmed ...*atomic.Bool) (consul.Config, *atomic.Int32, <-chan struct{}) {
	t.Helper()
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CAFile != "" {
		raw, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			t.Fatal(err)
		}
		tc.RootCAs = x509.NewCertPool()
		if !tc.RootCAs.AppendCertsFromPEM(raw) {
			t.Fatal("invalid original fixture CA")
		}
	}
	if cfg.ClientCertFile != "" {
		pair, err := tls.LoadX509KeyPair(cfg.ClientCertFile, cfg.ClientKeyFile)
		if err != nil {
			t.Fatal(err)
		}
		tc.Certificates = []tls.Certificate{pair}
	}
	transport := &http.Transport{TLSClientConfig: tc, DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	up := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 30 * time.Second}
	calls := &atomic.Int32{}
	committed := make(chan struct{})
	var once atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error("original relay request body unavailable")
			return
		}
		var operations []consul.TxnOperation
		treeRead := r.URL.Path == "/v1/txn" && json.Unmarshal(raw, &operations) == nil && len(operations) == 1 && operations[0].KV != nil && operations[0].KV.Verb == "get-tree"
		dataRead := strings.HasPrefix(mode, "data-") && (r.URL.Query().Has("recurse") || treeRead) && (len(dataArmed) == 0 || dataArmed[0].Load())
		if dataRead || !strings.HasPrefix(mode, "data-") && (r.Method == "PUT" || r.Method == "DELETE") {
			calls.Add(1)
		}
		request, err := http.NewRequestWithContext(r.Context(), r.Method, cfg.Endpoint+r.URL.RequestURI(), bytes.NewReader(raw))
		if err != nil {
			t.Error("original relay URL unavailable")
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
		if (dataRead || r.Method == "PUT" && response.StatusCode == 200 && bytes.Equal(bytes.TrimSpace(body), []byte("true"))) && once.CompareAndSwap(false, true) {
			close(committed)
			switch strings.TrimPrefix(mode, "data-") {
			case "lost":
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
				return
			case "partial":
				conn, buffer, err := w.(http.Hijacker).Hijack()
				if err == nil {
					defer conn.Close()
					fmt.Fprintf(buffer, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", len(body)+1)
					buffer.Write(body)
					buffer.Flush()
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
			case "cancel", "replace", "close":
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

func TestConsulNativeRuntimeOriginalCommittedReplyLossAndRetirement(t *testing.T) {
	cfg, secret := consulRuntimeConfig(t)
	base, _, err := consul.Open(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	for _, mode := range []string{"lost", "partial", "http500", "http408", "cancel", "replace", "close"} {
		t.Run(mode, func(t *testing.T) {
			proxy, calls, committed := consulRuntimeFault(t, cfg, mode)
			m := consulRuntimeManager(t, proxy)
			profile := consulRuntimeProfile()
			if err := m.Connect(context.Background(), profile, secret); err != nil {
				t.Fatal(err)
			}
			l := consulRuntimeReserve(t, m, 1)
			key := "native/runtime/" + strings.ReplaceAll(t.Name(), "/", "-")
			plan := consulRuntimePlan(t, l, consulRuntimePut(t, key, []byte{0, 255}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			var original contract.KVResult
			var executionError error
			go func() { original, executionError = l.Execute(ctx, plan); close(done) }()
			consulRuntimeAwait(t, committed)
			if mode == "cancel" {
				cancel()
			} else if mode == "replace" {
				if err := m.Connect(context.Background(), profile, secret); err != nil {
					t.Fatal(err)
				}
			} else if mode == "close" {
				if err := m.Disconnect(profile.ID); err != nil {
					t.Fatal(err)
				}
			}
			consulRuntimeAwait(t, done)
			if executionError == nil || !original.WriteOutcomeUnknown || original.HTTP == nil || !original.HTTP.Ended || original.HTTP.Acknowledged || calls.Load() != 1 || !bytes.Equal(l.LastResult().HTTP.Body, original.HTTP.Body) {
				t.Fatal("runtime erased or replayed original processed receipt", executionError)
			}
			if mode == "close" {
				if err := m.Connect(context.Background(), profile, secret); err != nil {
					t.Fatal(err)
				}
			}
			provider, err := m.KVRequests(profile.ID)
			if err != nil {
				t.Fatal(err)
			}
			recovery, err := provider.RecoverKV(l)
			if err != nil {
				t.Fatal(err)
			}
			defer recovery.Lease.Close()
			if recovery.Unknown == nil || !recovery.Unknown.WriteOutcomeUnknown || recovery.Unknown.Request != plan.Snapshot() || !bytes.Equal(recovery.Unknown.HTTP.Body, original.HTTP.Body) || calls.Load() != 1 {
				t.Fatal("pure runtime recovery altered original unknown")
			}
			if _, err = recovery.Lease.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
				t.Fatal("recovery replayed original authorization")
			}
			later, _ := consul.DocumentRequest(consulRuntimePut(t, key+"-later", nil))
			if _, err = recovery.Lease.Prepare(later); !errors.Is(err, contract.ErrKVWriteUnknown) {
				t.Fatal("runtime recovery removed write barrier")
			}
			read := consulRuntimeDo(t, recovery.Lease, consulRuntimeRead(t, key))
			if !bytes.Equal(read.HTTP.Entries[0].Value, []byte{0, 255}) || calls.Load() != 1 {
				t.Fatal("actual processed value missing or write replayed")
			}
			observed, err := base.Do(context.Background(), consulRuntimeRead(t, key))
			if err != nil || len(observed.Entries) != 1 || !bytes.Equal(observed.Entries[0].Value, []byte{0, 255}) {
				t.Fatal("actual original fixture state missing", err)
			}
			// The recovered unknown is independent of the successful later read.
			if !recovery.Unknown.WriteOutcomeUnknown || recovery.Unknown.HTTP.Acknowledged || !bytes.Equal(recovery.Unknown.HTTP.Body, original.HTTP.Body) {
				t.Fatal("later read promoted unknown to original ACK")
			}
		})
	}
}

func TestConsulNativeRuntimeLateConnectionClosesOnlyCapturedOriginalSession(t *testing.T) {
	cfg, secret := consulRuntimeConfig(t)
	m := dbruntime.New()
	defer m.Close()
	opened, release := make(chan *consul.Session, 1), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var calls atomic.Int32
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
		raw, err := consul.OpenNativeSession(ctx, p.ID, cfg, secret)
		if err == nil && calls.Add(1) == 1 {
			opened <- raw
			<-release
		}
		return raw, err
	}))
	out := make(chan error, 1)
	p := consulRuntimeProfile()
	go func() { _, err := m.ConnectSession(context.Background(), p, secret); out <- err }()
	var original *consul.Session
	select {
	case original = <-opened:
	case <-time.After(15 * time.Second):
		t.Fatal("original native connection did not open")
	}
	fresh, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	select {
	case err = <-out:
	case <-time.After(15 * time.Second):
		t.Fatal("late original connection did not settle")
	}
	if err == nil || original.Alive() || m.Generation(p.ID) != fresh.Generation() {
		t.Fatal("late captured original closed replacement")
	}
	consulRuntimeDo(t, consulRuntimeReserve(t, m, 1), consulRuntimeRead(t, "native/runtime-replacement"))
}

type consulRuntimeRecoveryCounter struct {
	*consul.Session
	recoveries *atomic.Int32
}

func (s *consulRuntimeRecoveryCounter) RecoverKV(old contract.KVRequestLease) (contract.KVRecovery, error) {
	s.recoveries.Add(1)
	return s.Session.RecoverKV(old)
}

func TestConsulNativeRuntimeForeignScopeRejectedBeforeOriginalRecovery(t *testing.T) {
	cfg, secret := consulRuntimeConfig(t)
	for _, change := range []string{"consistency", "prefix", "configuration"} {
		t.Run(change, func(t *testing.T) {
			m := dbruntime.New()
			defer m.Close()
			var opens, recoveries atomic.Int32
			m.Register(connections.SQLite, dbruntime.ProviderFunc(func(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
				current := cfg
				if opens.Add(1) > 1 {
					switch change {
					case "consistency":
						current.Consistency = "stale"
					case "prefix":
						current.Prefix = "native/foreign/"
					case "configuration":
						current.RequestTimeout = 31 * time.Second
					}
				}
				s, err := consul.OpenNativeSession(ctx, p.ID, current, secret)
				if err != nil {
					return nil, err
				}
				return &consulRuntimeRecoveryCounter{s, &recoveries}, nil
			}))
			p := consulRuntimeProfile()
			if err := m.Connect(context.Background(), p, secret); err != nil {
				t.Fatal(err)
			}
			l := consulRuntimeReserve(t, m, 1)
			original := consulRuntimeDo(t, l, consulRuntimeRead(t, "native/foreign-scope"))
			if err := m.Connect(context.Background(), p, secret); err != nil {
				t.Fatal(err)
			}
			provider, _ := m.KVRequests(p.ID)
			if _, err := provider.RecoverKV(l); !errors.Is(err, contract.ErrRevoked) || recoveries.Load() != 0 || !bytes.Equal(l.LastResult().HTTP.Body, original.HTTP.Body) {
				t.Fatal("foreign scope consumed original recovery or evidence", err)
			}
		})
	}
}
