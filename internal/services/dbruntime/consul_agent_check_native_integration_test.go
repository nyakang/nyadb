package dbruntime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
)

func consulRuntimeCheckRegister(t *testing.T, id string) consul.Request {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"Name": id, "ID": id, "TTL": "10m", "Notes": "original 中文"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := consul.AgentCheckRegisterRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func consulRuntimeCheckDeregister(t *testing.T, id string) consul.Request {
	t.Helper()
	r, err := consul.AgentCheckDeregisterRequest(id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Separate counters prove both zero-I/O recovery and one original mutation.
func consulRuntimeCheckFault(t *testing.T, cfg consul.Config, tokenMode string) (consul.Config, *atomic.Int32, *atomic.Int32, <-chan struct{}) {
	t.Helper()
	_, token := consulRuntimeConfig(t)
	upstream, err := consul.New(cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { upstream.Close() })
	calls, writes := &atomic.Int32{}, &atomic.Int32{}
	processed := make(chan struct{})
	var once atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(incoming.Body)
		if err != nil {
			return
		}
		r := consul.Request{Method: incoming.Method, Path: incoming.URL.EscapedPath(), Query: incoming.URL.Query(), Body: body}
		result, err := upstream.Do(incoming.Context(), r)
		if err != nil && !result.Captured {
			t.Error("original native runtime relay capture failed")
			return
		}
		if incoming.Method == http.MethodPut && strings.HasPrefix(incoming.URL.Path, "/v1/agent/check/") {
			writes.Add(1)
			if result.Status != 200 || len(result.Body) != 0 {
				t.Error("runtime relay did not capture original Agent success")
				return
			}
			if once.CompareAndSwap(false, true) {
				close(processed)
				switch tokenMode {
				case "lost", "partial":
					conn, out, err := w.(http.Hijacker).Hijack()
					if err == nil {
						defer conn.Close()
						if tokenMode == "partial" {
							out.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 1\r\n\r\n")
							out.Flush()
						}
					}
					return
				case "http400", "http500", "http408":
					status := map[string]int{"http400": 400, "http500": 500, "http408": 408}[tokenMode]
					w.WriteHeader(status)
					w.Write([]byte("original processed Agent reply withheld"))
					return
				case "cancel", "replace", "close":
					<-incoming.Context().Done()
					return
				}
			}
		}
		for key, values := range result.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(result.Status)
		w.Write(result.Body)
	}))
	t.Cleanup(server.Close)
	relay := cfg
	relay.Endpoint = server.URL
	relay.CAFile, relay.ClientCertFile, relay.ClientKeyFile, relay.ServerName = "", "", "", ""
	return relay, calls, writes, processed
}

func TestConsulNativeRuntimeAgentCheckOriginalPublishedGenerationsAndRecovery(t *testing.T) {
	cfg, token := consulRuntimeConfig(t)
	relay, calls, writes, processed := consulRuntimeCheckFault(t, cfg, "observe")
	m := consulRuntimeManager(t, relay)
	profile := consulRuntimeProfile()
	if err := m.Connect(context.Background(), profile, token); err != nil {
		t.Fatal(err)
	}
	second, err := m.ConnectSession(context.Background(), profile, token)
	if err != nil || second.Generation() <= 1 {
		t.Fatal("public generation did not differ from the native context generation", err)
	}
	l := consulRuntimeReserve(t, m, 8311)
	id := "owned-" + t.Name()
	r := consulRuntimeCheckRegister(t, id)
	before := calls.Load()
	plan := consulRuntimePlan(t, l, r)
	if calls.Load() != before || !plan.Writes() || l.Scope().Generation != second.Generation() {
		t.Fatal("Agent preparation dispatched or lost its original publication")
	}
	original, err := l.Execute(context.Background(), plan)
	if err != nil || !original.Completed || original.HTTP == nil || !original.HTTP.Succeeded || len(original.HTTP.Body) != 0 || !bytes.Equal(original.HTTP.Request.Body, r.Body) || original.Native != nil || original.Header != (contract.KVHeader{}) {
		t.Fatal("runtime changed original empty Agent acknowledgement", err)
	}
	consulRuntimeAwait(t, processed)
	third, err := m.ConnectSession(context.Background(), profile, token)
	if err != nil || third.Generation() <= second.Generation() {
		t.Fatal("replacement publication did not advance", err)
	}
	if _, err := l.Execute(context.Background(), plan); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("original Agent plan acquired replacement authority", err)
	}
	fresh, err := m.KVRequests(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	before = calls.Load()
	recovered, err := fresh.RecoverKV(l)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Lease.Close()
	if calls.Load() != before || recovered.Lease.Scope().Generation != third.Generation() || recovered.Unknown != nil || !reflect.DeepEqual(recovered.Last, original) {
		t.Fatal("runtime recovery dispatched or changed original Agent receipt")
	}
	if _, err := recovered.Lease.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("recovery granted foreign original Agent authority", err)
	}
	if err := recovered.Lease.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	before = calls.Load()
	consulRuntimeDo(t, recovered.Lease, consulRuntimeCheckDeregister(t, id))
	if calls.Load() != before+1 || writes.Load() != 2 {
		t.Fatal("explicit original deregistration did not dispatch exactly once")
	}
}

func TestConsulNativeRuntimeAgentCheckProcessedReplacementKeepsOriginalUnknown(t *testing.T) {
	cfg, token := consulRuntimeConfig(t)
	base, _, err := consul.Open(context.Background(), cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	for _, kind := range []string{"register", "deregister"} {
		for _, mode := range []string{"http400", "http500", "http408", "lost", "partial", "cancel", "replace", "close"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				id := "owned-" + strings.ReplaceAll(t.Name(), "/", "-")
				r := consulRuntimeCheckRegister(t, id)
				if kind == "deregister" {
					if _, err := base.Do(context.Background(), r); err != nil {
						t.Fatal(err)
					}
					r = consulRuntimeCheckDeregister(t, id)
				}
				relay, calls, writes, processed := consulRuntimeCheckFault(t, cfg, mode)
				m := consulRuntimeManager(t, relay)
				profile := consulRuntimeProfile()
				if err := m.Connect(context.Background(), profile, token); err != nil {
					t.Fatal(err)
				}
				second, err := m.ConnectSession(context.Background(), profile, token)
				if err != nil {
					t.Fatal(err)
				}
				l := consulRuntimeReserve(t, m, 8312)
				plan := consulRuntimePlan(t, l, r)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan struct{})
				var original contract.KVResult
				var executionErr error
				go func() { original, executionErr = l.Execute(ctx, plan); close(done) }()
				consulRuntimeAwait(t, processed)
				if mode == "cancel" {
					cancel()
				} else if mode == "replace" {
					if err := m.Connect(context.Background(), profile, token); err != nil {
						t.Fatal(err)
					}
				} else if mode == "close" {
					if err := m.Disconnect(profile.ID); err != nil {
						t.Fatal(err)
					}
				}
				consulRuntimeAwait(t, done)
				if executionErr == nil || !original.WriteOutcomeUnknown || original.Completed || original.HTTP == nil || original.HTTP.Acknowledged || !original.HTTP.Ended || writes.Load() != 1 {
					t.Fatal("runtime promoted or replayed processed Agent mutation", executionErr)
				}
				if err := m.Connect(context.Background(), profile, token); err != nil {
					t.Fatal(err)
				}
				fresh, err := m.KVRequests(profile.ID)
				if err != nil {
					t.Fatal(err)
				}
				before := calls.Load()
				recovered, err := fresh.RecoverKV(l)
				if err != nil {
					t.Fatal(err)
				}
				defer recovered.Lease.Close()
				if recovered.Lease.Scope().Generation <= second.Generation() || calls.Load() != before || recovered.Unknown == nil || !reflect.DeepEqual(*recovered.Unknown, original) {
					t.Fatal("runtime pure recovery changed original unknown/public generation")
				}
				if _, err := recovered.Lease.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) || calls.Load() != before {
					t.Fatal("runtime recovery granted original plan authority", err)
				}
				for _, request := range []consul.Request{r, consulRuntimePut(t, "later/"+id, nil)} {
					doc, err := consul.DocumentRequest(request)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := recovered.Lease.Prepare(doc); !errors.Is(err, contract.ErrKVWriteUnknown) || calls.Load() != before {
						t.Fatal("runtime original unknown barrier removed", err)
					}
				}
				consulRuntimeDo(t, recovered.Lease, consul.AgentSelfRequest())
				fresh2, err := fresh.RecoverKV(recovered.Lease)
				if err != nil {
					t.Fatal(err)
				}
				defer fresh2.Lease.Close()
				if fresh2.Unknown == nil || !reflect.DeepEqual(*fresh2.Unknown, original) || writes.Load() != 1 {
					t.Fatal("later Agent read replaced original processed-write evidence")
				}
				read, err := consul.AgentChecksRequest(nil)
				if err != nil {
					t.Fatal(err)
				}
				values, err := base.Do(context.Background(), read)
				var checks map[string]json.RawMessage
				if err != nil || json.Unmarshal(values.Body, &checks) != nil {
					t.Fatal("actual original check map unavailable", err)
				}
				_, present := checks[id]
				if present != (kind == "register") {
					t.Fatal("runtime fault did not follow actual original Agent mutation")
				}
			})
		}
	}
}
