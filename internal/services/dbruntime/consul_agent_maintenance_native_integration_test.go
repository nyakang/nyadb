package dbruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
)

func consulRuntimeMaintenanceRequest(t *testing.T, operation, id string) consul.Request {
	t.Helper()
	reason := "original processed reason 中文\n\r\x00"
	enable := strings.HasSuffix(operation, "-enable")
	var r consul.Request
	var err error
	if strings.HasPrefix(operation, "node-") {
		r, err = consul.AgentNodeMaintenanceRequest(enable, &reason)
	} else if strings.HasPrefix(operation, "service-") {
		r, err = consul.AgentServiceMaintenanceRequest(id, enable, &reason)
	} else {
		t.Fatal("unknown maintenance operation", operation)
	}
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func consulRuntimeMaintenanceSetup(t *testing.T, base *consul.Client, id, operation string) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"Name": id, "ID": id})
	if err != nil {
		t.Fatal(err)
	}
	r, err := consul.AgentServiceRegisterRequest(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Do(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	opposite := strings.Split(operation, "-")[0] + "-enable"
	if strings.HasSuffix(operation, "-enable") {
		opposite = strings.Split(operation, "-")[0] + "-disable"
	}
	if _, err := base.Do(context.Background(), consulRuntimeMaintenanceRequest(t, opposite, id)); err != nil {
		t.Fatal(err)
	}
}

func consulRuntimeMaintenanceDisable(t *testing.T, base *consul.Client, id string) {
	t.Helper()
	for _, kind := range []string{"node", "service"} {
		if _, err := base.Do(context.Background(), consulRuntimeMaintenanceRequest(t, kind+"-disable", id)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConsulNativeRuntimeAgentMaintenanceOriginalPublishedGenerationsAndRecovery(t *testing.T) {
	cfg, token := consulRuntimeConfig(t)
	relay, calls, writes, processed := consulRuntimeMaintenanceFault(t, cfg, "observe")
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
	base, _, err := consul.Open(context.Background(), cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	consulRuntimeMaintenanceSetup(t, base, id, "node-enable")
	defer consulRuntimeMaintenanceDisable(t, base, id)
	r := consulRuntimeMaintenanceRequest(t, "node-enable", id)
	before := calls.Load()
	plan := consulRuntimePlan(t, l, r)
	if calls.Load() != before || !plan.Writes() || l.Scope().Generation != second.Generation() {
		t.Fatal("Agent preparation dispatched or lost its original publication")
	}
	original, err := l.Execute(context.Background(), plan)
	if err != nil || !original.Completed || original.HTTP == nil || !original.HTTP.Succeeded || len(original.HTTP.Body) != 0 || !reflect.DeepEqual(url.Values(original.HTTP.Request.Query), r.Query) || original.Native != nil || original.Header != (contract.KVHeader{}) {
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
	consulRuntimeDo(t, recovered.Lease, consulRuntimeMaintenanceRequest(t, "service-enable", id))
	if calls.Load() != before+1 || writes.Load() != 2 {
		t.Fatal("explicit original maintenance did not dispatch exactly once")
	}
}

func TestConsulNativeRuntimeAgentMaintenanceProcessedReplacementKeepsOriginalUnknown(t *testing.T) {
	cfg, token := consulRuntimeConfig(t)
	base, _, err := consul.Open(context.Background(), cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	for _, kind := range []string{"node-enable", "node-disable", "service-enable", "service-disable"} {
		for _, mode := range []string{"http400", "http500", "http408", "lost", "partial", "cancel", "replace", "close"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				id := "owned-" + strings.ReplaceAll(t.Name(), "/", "-")
				consulRuntimeMaintenanceSetup(t, base, id, kind)
				defer consulRuntimeMaintenanceDisable(t, base, id)
				r := consulRuntimeMaintenanceRequest(t, kind, id)
				relay, calls, writes, processed := consulRuntimeMaintenanceFault(t, cfg, mode)
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
				checkID := "_node_maintenance"
				if strings.HasPrefix(kind, "service-") {
					checkID = "_service_maintenance:" + id
				}
				if strings.HasSuffix(kind, "-enable") {
					var check struct{ Status, Notes, Type string }
					if json.Unmarshal(checks[checkID], &check) != nil || check.Status != "critical" || check.Type != "maintenance" || check.Notes != "original processed reason 中文\n\r\x00" {
						t.Fatal("runtime fault did not follow actual original maintenance enable")
					}
				} else if checks[checkID] != nil {
					t.Fatal("runtime fault did not follow actual original maintenance disable")
				}
				if !reflect.DeepEqual(url.Values(original.HTTP.Request.Query), r.Query) || original.HTTP.Request.Path != r.Path {
					t.Fatal("runtime unknown lost original maintenance target/enable/reason")
				}
			})
		}
	}
}

// Separate counters prove both zero-I/O recovery and one original mutation.
func consulRuntimeMaintenanceFault(t *testing.T, cfg consul.Config, tokenMode string) (consul.Config, *atomic.Int32, *atomic.Int32, <-chan struct{}) {
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
		if incoming.Method == http.MethodPut && (incoming.URL.Path == "/v1/agent/maintenance" || strings.HasPrefix(incoming.URL.Path, "/v1/agent/service/maintenance/")) {
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
