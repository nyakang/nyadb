package consul

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeAgentRegister(t *testing.T, raw []byte, replacement *bool) Request {
	t.Helper()
	r, err := AgentServiceRegisterRequest(raw, replacement)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func nativeAgentDeregister(t *testing.T, id string) Request {
	t.Helper()
	r, err := AgentServiceDeregisterRequest(id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestConsulNativeAgentServiceOriginalPlansDefinitionsReplacementAndKnownDenials(t *testing.T) {
	s, base, cfg, _ := nativeDocumentSession(t, 8301)
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var opens atomic.Int32
	s.opener = func(ctx context.Context) (*Client, OpenEvidence, error) { opens.Add(1); return s.openCaptured(ctx) }
	id := "owned-" + t.Name() + "-Unicode 雪%?#++"
	payload := map[string]any{"Name": "owned-" + t.Name(), "ID": id, "Tags": []string{"original", "中文"}, "Meta": map[string]string{"original": "原值"}, "Address": "127.0.0.1", "Port": 8301, "Weights": map[string]int{"Passing": 3, "Warning": 2}, "Connect": map[string]any{"SidecarService": map[string]any{"Port": 18301}}}
	for _, step := range []struct {
		query string
		check string
		kept  []string
	}{{"", "a", []string{"a"}}, {"", "b", []string{"a", "b"}}, {"false", "c", []string{"a", "b", "c"}}, {"true", "d", []string{"d"}}, {"empty", "e", []string{"e"}}} {
		payload["Check"] = map[string]any{"CheckID": id + "-" + step.check, "TTL": "10m", "Notes": "original notes", "Status": "critical"}
		raw := nativeAgentServiceEvidenceJSON(t, payload)
		var replacement *bool
		if step.query == "false" || step.query == "true" {
			v := step.query == "true"
			replacement = &v
		}
		r := nativeAgentRegister(t, raw, replacement)
		if step.query == "empty" {
			r.Query = url.Values{"replace-existing-checks": {""}}
		}
		before := opens.Load()
		plan := documentPlan(t, l, r)
		if opens.Load() != before || !plan.Writes() {
			t.Fatal("Agent document preparation opened or lost write identity")
		}
		r.Body[0], raw[0] = 'x', 'x'
		result, err := l.Execute(context.Background(), plan)
		if err != nil || !result.Completed || result.HTTP == nil || !result.HTTP.Captured || !result.HTTP.EOF || !result.HTTP.Acknowledged || !result.HTTP.Succeeded || result.WriteOutcomeUnknown || len(result.HTTP.Body) != 0 || result.Native != nil || result.NativeCaptured {
			t.Fatal("Agent original empty HTTP success lost", err)
		}
		if _, err := l.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
			t.Fatal("Agent registration authorization replayed", err)
		}
		u, err := url.Parse(result.HTTP.URL)
		if err != nil || u.Query().Has("dc") || u.Query().Has("consistent") || u.Query().Has("stale") || u.Query().Has("token") {
			t.Fatal("local Agent write inherited catalog routing")
		}
		services := nativeAgentServiceEvidenceMaps(t, base, false)
		if _, ok := services[id]; !ok {
			t.Fatal("original service identity changed")
		}
		if _, ok := services[id+"-sidecar-proxy"]; !ok {
			t.Fatal("original sidecar configuration was dropped")
		}
		checks := nativeAgentServiceEvidenceMaps(t, base, true)
		for _, suffix := range []string{"a", "b", "c", "d", "e"} {
			_, present := checks[id+"-"+suffix]
			wanted := false
			for _, keep := range step.kept {
				wanted = wanted || keep == suffix
			}
			if present != wanted {
				t.Fatal("complete raw definition changed native check replacement")
			}
		}
	}
	if token := os.Getenv("NYADB_CONSUL_READ_TOKEN"); token != "" {
		cfg.Auth = "token"
		limited, err := New(cfg, token)
		if err != nil {
			t.Fatal(err)
		}
		defer limited.Close()
		for _, r := range []Request{nativeAgentRegister(t, nativeAgentServiceEvidenceJSON(t, payload), nil), nativeAgentDeregister(t, id)} {
			denied, err := limited.Do(context.Background(), r)
			if err == nil || denied.Status != 403 || !denied.Acknowledged || denied.Succeeded || denied.WriteOutcomeUnknown || !denied.Captured || !denied.EOF || len(denied.Body) == 0 {
				t.Fatal("original ACL denial changed", denied.Status, err)
			}
			if _, err := limited.Prepare(r); err != nil {
				t.Fatal("proved ACL denial invented unknown write barrier", err)
			}
		}
		if _, present := nativeAgentServiceEvidenceMaps(t, base, false)[id]; !present {
			t.Fatal("denied original deregistration changed state")
		}
	}
	removed := executeDocument(t, l, nativeAgentDeregister(t, id))
	if !removed.Completed || !removed.HTTP.Succeeded || len(removed.HTTP.Body) != 0 {
		t.Fatal("original service removal ACK changed")
	}
	missing, err := l.Execute(context.Background(), documentPlan(t, l, nativeAgentDeregister(t, id)))
	if err == nil || missing.HTTP.Status != 404 || !missing.Completed || !missing.HTTP.Acknowledged || missing.WriteOutcomeUnknown {
		t.Fatal("original absent ID acknowledgement not proved", missing.HTTP.Status, err)
	}
	services := nativeAgentServiceEvidenceMaps(t, base, false)
	if _, ok := services[id]; ok {
		t.Fatal("original deregistration did not remove its service")
	}
	if _, ok := services[id+"-sidecar-proxy"]; ok {
		t.Fatal("native deregistration retained associated sidecar")
	}
}

// Only a completed original Agent PUT is damaged. Readiness/identity requests
// pass through, and the original transport cannot redirect or retry the PUT.
func nativeAgentServiceFault(t *testing.T, cfg Config, token, mode string) (Config, *atomic.Int32, *atomic.Int32, <-chan struct{}) {
	return nativeAgentMutationFault(t, cfg, token, mode, "/v1/agent/service/")
}

func nativeAgentMutationFault(t *testing.T, cfg Config, token, mode, prefix string) (Config, *atomic.Int32, *atomic.Int32, <-chan struct{}) {
	t.Helper()
	upstream, err := New(cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { upstream.Close() })
	calls, writes := &atomic.Int32{}, &atomic.Int32{}
	processed := make(chan struct{})
	var once atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
		calls.Add(1)
		raw, err := io.ReadAll(incoming.Body)
		if err != nil {
			return
		}
		r, err := http.NewRequestWithContext(incoming.Context(), incoming.Method, cfg.Endpoint+incoming.URL.RequestURI(), bytes.NewReader(raw))
		if err != nil {
			t.Error(err)
			return
		}
		r.Header, r.GetBody, r.Close = incoming.Header.Clone(), nil, true
		response, err := upstream.http.Do(r)
		if err != nil {
			return
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			return
		}
		if incoming.Method == http.MethodPut && strings.HasPrefix(incoming.URL.Path, prefix) {
			writes.Add(1)
			if response.StatusCode != 200 || len(body) != 0 || !once.CompareAndSwap(false, true) {
				t.Error("relay did not capture exactly one original successful Agent mutation")
				return
			}
			close(processed)
			switch mode {
			case "lost", "partial":
				conn, out, err := w.(http.Hijacker).Hijack()
				if err == nil {
					defer conn.Close()
					if mode == "partial" {
						out.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 1\r\nX-Original: retained\r\n\r\n")
						out.Flush()
					}
				}
				return
			case "http400", "http500", "http408":
				status := map[string]int{"http400": 400, "http500": 500, "http408": 408}[mode]
				w.WriteHeader(status)
				w.Write([]byte("original processed Agent reply withheld"))
				return
			case "invalid":
				w.Write([]byte("null"))
				return
			case "cancel", "close", "retire":
				<-incoming.Context().Done()
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
	t.Cleanup(server.Close)
	relay := cfg
	relay.Endpoint = server.URL
	relay.CAFile, relay.ClientCertFile, relay.ClientKeyFile, relay.ServerName = "", "", "", ""
	return relay, calls, writes, processed
}

func TestConsulNativeAgentServiceProcessedRepliesOriginalLeaseRecoveryAndNoReplay(t *testing.T) {
	base, cfg, token := nativeClient(t)
	for _, kind := range []string{"register", "deregister"} {
		for _, mode := range []string{"lost", "partial", "http400", "http500", "http408", "invalid", "cancel", "close", "retire"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				id := "owned-" + strings.ReplaceAll(t.Name(), "/", "-")
				raw := nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": id, "ID": id, "Tags": []string{"original", "中文"}})
				r := nativeAgentRegister(t, raw, nil)
				if kind == "deregister" {
					nativeDo(t, base, r)
					r = nativeAgentDeregister(t, id)
				}
				relay, calls, writes, processed := nativeAgentServiceFault(t, cfg, token, mode)
				s, err := OpenNativeSession(context.Background(), 8302, relay, token)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				l, err := s.ReserveKV(1)
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
				if err := l.Preconnect(context.Background()); err != nil {
					t.Fatal(err)
				}
				plan := documentPlan(t, l, r)
				later := documentPlan(t, l, putRequest("later/"+id, nil))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan struct{})
				var original contract.KVResult
				go func() { original, err = l.Execute(ctx, plan); close(done) }()
				wait(t, processed)
				if mode == "cancel" {
					cancel()
				} else if mode == "close" {
					s.Close()
				} else if mode == "retire" {
					l.(*documentLease).retire()
				}
				wait(t, done)
				if err == nil || original.HTTP == nil || original.Completed || original.HTTP.Acknowledged || !original.WriteOutcomeUnknown || !original.HTTP.Ended || writes.Load() != 1 {
					t.Fatal("processed original Agent mutation confirmed or replayed", err)
				}
				if mode == "partial" && (original.HTTP.Captured || original.HTTP.EOF || len(original.HTTP.Body) != 0 || http.Header(original.HTTP.Header).Get("X-Original") != "retained") {
					t.Fatal("empty partial body invented complete original ACK")
				}
				if mode == "http400" || mode == "http500" || mode == "http408" || mode == "invalid" {
					if _, err := l.Execute(context.Background(), later); !errors.Is(err, contract.ErrKVWriteUnknown) {
						t.Fatal("earlier write plan bypassed Agent unknown barrier", err)
					}
					executeDocument(t, l, AgentSelfRequest())
				}
				target := s
				if mode == "close" {
					target, err = OpenNativeSession(context.Background(), 8302, relay, token)
					if err != nil {
						t.Fatal(err)
					}
					defer target.Close()
				}
				before := calls.Load()
				recovery, err := target.RecoverKV(l)
				if err != nil {
					t.Fatal(err)
				}
				defer recovery.Lease.Close()
				if calls.Load() != before || recovery.Unknown == nil || !reflect.DeepEqual(*recovery.Unknown, original) || writes.Load() != 1 {
					t.Fatal("Agent evidence-only recovery changed original receipt or dispatched")
				}
				for _, request := range []Request{r, putRequest("later/"+id, nil)} {
					doc, err := DocumentRequest(request)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := recovery.Lease.Prepare(doc); !errors.Is(err, contract.ErrKVWriteUnknown) {
						t.Fatal("recovery removed cross-operation unknown barrier", err)
					}
				}
				if _, err := recovery.Lease.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
					t.Fatal("recovery granted old original authority", err)
				}
				executeDocument(t, recovery.Lease, AgentSelfRequest())
				if !reflect.DeepEqual(*recovery.Lease.(*documentLease).recoveryOwned().Unknown, original) || writes.Load() != 1 {
					t.Fatal("later explicit original read cleared Agent unknown receipt")
				}
				_, present := nativeAgentServiceEvidenceMaps(t, base, false)[id]
				if present != (kind == "register") {
					t.Fatal("relay fault did not follow actual original mutation")
				}
			})
		}
	}
}

func TestConsulNativeAgentServiceOriginalPartialSidecarFailureRetainsCompleteUnknown(t *testing.T) {
	s, base, _, _ := nativeDocumentSession(t, 8303)
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	id := "owned-" + t.Name()
	raw := nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": id, "ID": id, "Connect": map[string]any{"SidecarService": map[string]any{"Port": 18303, "Check": map[string]any{"Args": []string{"/bin/true"}, "Interval": "10s"}}}})
	r := nativeAgentRegister(t, raw, nil)
	plan := documentPlan(t, l, r)
	original, err := l.Execute(context.Background(), plan)
	if err == nil || original.HTTP.Status != 500 || !original.HTTP.Captured || !original.HTTP.EOF || original.Completed || !original.WriteOutcomeUnknown || len(original.HTTP.Body) == 0 || !bytes.Equal(original.HTTP.Request.Body, raw) {
		t.Fatal("native partial sidecar error lost original request/receipt", original.HTTP.Status, err)
	}
	services := nativeAgentServiceEvidenceMaps(t, base, false)
	if _, present := services[id]; !present {
		t.Fatal("native partial sidecar fixture never created its original main service")
	}
	read, err := AgentServicesRequest(nil)
	if err != nil {
		t.Fatal(err)
	}
	executeDocument(t, l, read)
	recovery, err := s.RecoverKV(l)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Lease.Close()
	if recovery.Unknown == nil || !reflect.DeepEqual(*recovery.Unknown, original) {
		t.Fatal("partial native registration was promoted by later map read/recovery")
	}
	doc, _ := DocumentRequest(r)
	if _, err := recovery.Lease.Prepare(doc); !errors.Is(err, contract.ErrKVWriteUnknown) {
		t.Fatal("partial sidecar registration could be replayed", err)
	}
}

func TestConsulNativeAgentServiceOriginalIDsScopeBoundsAndForeignPreparation(t *testing.T) {
	s, base, _, _ := nativeDocumentSession(t, 8304)
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	other, err := s.ReserveKV(2)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var opens atomic.Int32
	s.opener = func(ctx context.Context) (*Client, OpenEvidence, error) { opens.Add(1); return s.openCaptured(ctx) }
	for _, suffix := range []string{"qualified/id", "percent%2F", "Unicode 雪?#++"} {
		id := "owned-" + t.Name() + "-" + suffix
		r := nativeAgentRegister(t, nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": "owned-" + t.Name(), "ID": id}), nil)
		plan := documentPlan(t, l, r)
		before := opens.Load()
		if _, err := other.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) || opens.Load() != before {
			t.Fatal("foreign Agent preparation dispatched", err)
		}
		executeDocument(t, l, r)
		if _, present := nativeAgentServiceEvidenceMaps(t, base, false)[id]; !present {
			t.Fatal("native original ID rewritten")
		}
		executeDocument(t, l, nativeAgentDeregister(t, id))
		if _, present := nativeAgentServiceEvidenceMaps(t, base, false)[id]; present {
			t.Fatal("native escaped original ID not removed")
		}
	}
	before := opens.Load()
	for _, id := range []string{"leading/../target", "repeated//target", "/leading", "trailing/"} {
		if _, err := AgentServiceDeregisterRequest(id); !errors.Is(err, ErrUnsupported) {
			t.Fatal("noncanonical original ID did not reject before I/O", err)
		}
	}
	if _, err := AgentServiceRegisterRequest(bytes.Repeat([]byte{'x'}, requestLimit+1), nil); err == nil || opens.Load() != before {
		t.Fatal("oversized native definition dispatched or split")
	}
	for _, raw := range []string{`{"Name":"x","Namespace":"foreign"}`, `{"Name":"x","Connect":{"SidecarService":{"Token":"foreign"}}}`} {
		if _, err := AgentServiceRegisterRequest([]byte(raw), nil); !errors.Is(err, ErrUnsupported) || opens.Load() != before {
			t.Fatal("foreign Agent routing/authentication accepted", err)
		}
	}
}

func TestConsulNativeAgentServiceOriginalKVUnknownBlocksAgentWritesWithoutReplay(t *testing.T) {
	_, cfg, token := nativeClient(t)
	relay, calls, processed := nativeFault(t, cfg, token, "http500")
	s, err := OpenNativeSession(context.Background(), 8305, relay, token)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	original, err := l.Execute(context.Background(), documentPlan(t, l, putRequest(keyFor(t, "original"), []byte("original KV\x00"))))
	if err == nil || !original.WriteOutcomeUnknown {
		t.Fatal("original KV reply loss not retained", err)
	}
	wait(t, processed)
	id := "owned-" + t.Name()
	before := calls.Load()
	for _, r := range []Request{nativeAgentRegister(t, []byte(fmt.Sprintf(`{"Name":%q,"ID":%q}`, id, id)), nil), nativeAgentDeregister(t, id)} {
		doc, err := DocumentRequest(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := l.Prepare(doc); !errors.Is(err, contract.ErrKVWriteUnknown) || calls.Load() != before {
			t.Fatal("Agent write bypassed original KV unknown", err)
		}
	}
	executeDocument(t, l, AgentSelfRequest())
	recovered, err := s.RecoverKV(l)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Lease.Close()
	if recovered.Unknown == nil || !reflect.DeepEqual(*recovered.Unknown, original) {
		t.Fatal("Agent read/recovery replaced original KV unknown")
	}
}
