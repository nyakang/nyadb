package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestConsulNativeSessionProjectionOriginalListNodeInfoAndOptionalPresence(t *testing.T) {
	s, base, _, _ := nativeDocumentSession(t, 2101)
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	first := nativeSessionToolCreate(t, base, t.Name()+"-first", "10m", "release")
	second := nativeSessionToolCreate(t, base, t.Name()+"-second", "10m", "delete")
	defer nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, first))
	defer nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, second))
	for _, request := range []Request{{Method: "GET", Path: "/v1/session/list"}, {Method: "GET", Path: "/v1/session/node/" + url.PathEscape(s.KVScope().HTTP.AgentName)}, {Method: "GET", Path: "/v1/session/info/" + first}} {
		result := executeDocument(t, l, request)
		h := result.HTTP
		if h == nil || h.Sessions == nil || h.ProjectionError != "" || !h.Acknowledged || !h.Succeeded {
			t.Fatal("original Session read projection missing", result.ProjectionError)
		}
		var rows []map[string]json.RawMessage
		if json.Unmarshal(h.Body, &rows) != nil || len(rows) != len(h.Sessions.Rows) {
			t.Fatal("projection changed original number of Session rows")
		}
		found := false
		for i, row := range h.Sessions.Rows {
			var id string
			if json.Unmarshal(rows[i]["ID"], &id) != nil || id != row.ID {
				t.Fatal("projection changed original response order or UUID")
			}
			for name, raw := range rows[i] {
				if !slices.Contains(row.PresentFields, name) || slices.Contains(row.NullFields, name) != bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
					t.Fatal("native Session presence/null lost", name)
				}
			}
			if row.ID == first {
				found = true
				if row.Node != s.KVScope().HTTP.AgentName || row.Name != t.Name()+"-first" || row.LockDelay != 0 || row.TTL != "10m" && row.TTL != "10m0s" || row.Behavior != "release" {
					t.Fatal("typed native Session fields changed", row)
				}
			}
		}
		if !found {
			t.Fatal("original selected Session absent")
		}
		original := l.LastResult()
		clone := result.Clone()
		clone.HTTP.Sessions.Rows[0].ID = "changed"
		if original.HTTP.Sessions.Rows[0].ID == "changed" || l.LastResult().HTTP.Sessions.Rows[0].ID == "changed" {
			t.Fatal("Session receipt projection shares original cache")
		}
	}
	executeDocument(t, l, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, first))
	gone := executeDocument(t, l, nativeSessionToolID(t, contract.KVToolHTTPSessionInfo, first))
	if gone.HTTP.Sessions == nil || len(gone.HTTP.Sessions.Rows) != 0 {
		t.Fatal("missing original Session not represented by exact empty observation")
	}
}

func TestConsulNativeSessionProjectionACLAndOriginalUnknownRecovery(t *testing.T) {
	base, cfg, token := nativeClient(t)
	id := nativeSessionToolCreate(t, base, t.Name(), "10m", "release")
	defer nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, id))
	if cfg.Auth != "none" {
		for _, variable := range []string{"NYADB_CONSUL_READ_TOKEN", "NYADB_CONSUL_SESSION_READ_TOKEN"} {
			secret := os.Getenv(variable)
			if secret == "" {
				t.Fatal("restricted fixture token missing")
			}
			s, err := OpenNativeSession(context.Background(), 2102, cfg, secret)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			l, err := s.ReserveKV(1)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			result := executeDocument(t, l, nativeSessionToolID(t, contract.KVToolHTTPSessionInfo, id))
			if result.HTTP.Sessions == nil || !result.HTTP.Acknowledged {
				t.Fatal("ACL-filtered HTTP receipt lost")
			}
			want := 1
			if variable == "NYADB_CONSUL_READ_TOKEN" {
				want = 0
			}
			if len(result.HTTP.Sessions.Rows) != want {
				t.Fatal("original Session permissions bypassed")
			}
		}
	}
	relay, _, processed := nativeFault(t, cfg, token, "lost")
	s, err := OpenNativeSession(context.Background(), 2103, relay, token)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	request := nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, id)
	plan := documentPlan(t, l, request)
	unknown, err := l.Execute(context.Background(), plan)
	wait(t, processed)
	if err == nil || !unknown.WriteOutcomeUnknown || unknown.HTTP == nil || unknown.HTTP.Sessions != nil {
		t.Fatal("processed destroy received read projection or lost unknown")
	}
	recovery, err := s.RecoverKV(l)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Lease.Close()
	read := executeDocument(t, recovery.Lease, nativeSessionToolID(t, contract.KVToolHTTPSessionInfo, id))
	if read.HTTP.Sessions == nil || len(read.HTTP.Sessions.Rows) != 0 {
		t.Fatal("later original read projection missing")
	}
	evidence := recovery.Lease.(*documentLease).recoveryOwned()
	if evidence.Unknown == nil || !evidence.Unknown.WriteOutcomeUnknown || evidence.Unknown.HTTP.Acknowledged || !bytes.Equal(evidence.Unknown.HTTP.Body, unknown.HTTP.Body) {
		t.Fatal("Session read projection confirmed original unknown write")
	}
}

// The relay captures a real successful original Session read before changing
// its response. It never synthesizes a successful observation for the client.
func nativeSessionProjectionRelay(t *testing.T, cfg Config, token, id, mode string) (Config, *atomic.Int32, <-chan struct{}) {
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
			t.Error("invalid relay request")
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
			t.Error("original server response incomplete")
			return
		}
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		if r.Method == "GET" && r.URL.Path == "/v1/session/info/"+id {
			calls.Add(1)
			if once.CompareAndSwap(false, true) {
				if response.StatusCode != 200 || !bytes.Contains(body, []byte(id)) {
					t.Error("original Session read absent before fault")
					return
				}
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
					w.WriteHeader(200)
					w.Write(body)
					return
				case "cancel", "retire", "close":
					<-r.Context().Done()
					return
				case "http500", "http408":
					status := 500
					if mode == "http408" {
						status = 408
					}
					w.Header().Del("Content-Length")
					w.WriteHeader(status)
					w.Write([]byte("original read reply withheld"))
					return
				case "wrong-id":
					body = []byte(`[{"ID":"00000000-0000-0000-0000-000000000000"}]`)
				case "null":
					body = []byte("null")
				case "overflow":
					body = []byte(`[{"ID":"` + id + `","ModifyIndex":18446744073709551616}]`)
				}
			}
		}
		w.Header().Del("Content-Length")
		w.WriteHeader(response.StatusCode)
		w.Write(body)
	}))
	t.Cleanup(server.Close)
	proxy := cfg
	proxy.Endpoint, proxy.CAFile, proxy.ServerName, proxy.ClientCertFile, proxy.ClientKeyFile = server.URL, "", "", "", ""
	return proxy, calls, processed
}

func TestConsulNativeSessionProjectionProcessedReadFaultsCancellationRetirementAndRecovery(t *testing.T) {
	base, cfg, token := nativeClient(t)
	id := nativeSessionToolCreate(t, base, t.Name(), "10m", "release")
	defer nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, id))
	for _, mode := range []string{"lost", "partial", "http500", "http408", "wrong-id", "null", "overflow", "cancel", "retire", "close"} {
		t.Run(mode, func(t *testing.T) {
			relay, calls, processed := nativeSessionProjectionRelay(t, cfg, token, id, mode)
			s, err := OpenNativeSession(context.Background(), 2104, relay, token)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			l, err := s.ReserveKV(1)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			plan := documentPlan(t, l, nativeSessionToolID(t, contract.KVToolHTTPSessionInfo, id))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				result contract.KVResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() { result, err := l.Execute(ctx, plan); done <- outcome{result, err} }()
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
				t.Fatal("original executor did not acknowledge read end")
			}
			h := out.result.HTTP
			if h == nil || h.Sessions != nil || !h.Ended || h.WriteOutcomeUnknown || out.result.WriteOutcomeUnknown || calls.Load() != 1 {
				t.Fatal("incomplete original Session read became complete or replayed")
			}
			switch mode {
			case "wrong-id", "null", "overflow":
				if out.err != nil || !h.Acknowledged || !h.Succeeded || h.ProjectionError == "" {
					t.Fatal("optional projection invalidated original successful HTTP response", out.err)
				}
			case "http500", "http408":
				if out.err == nil || h.Acknowledged || h.Succeeded {
					t.Fatal("original HTTP error receipt changed", out.err)
				}
			default:
				if out.err == nil || h.Acknowledged {
					t.Fatal("incomplete original read was acknowledged", out.err)
				}
			}
			if mode == "close" {
				return
			}
			original := l.LastResult()
			owner := s
			if mode == "retire" {
				owner, err = OpenNativeSession(context.Background(), 2104, relay, token)
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
			if calls.Load() != 1 || recovery.Unknown != nil || recovery.Last.HTTP.Sessions != nil || !bytes.Equal(recovery.Last.HTTP.Body, original.HTTP.Body) {
				t.Fatal("recovery changed evidence or executed a read")
			}
			observed := executeDocument(t, recovery.Lease, nativeSessionToolID(t, contract.KVToolHTTPSessionInfo, id))
			if calls.Load() != 2 || observed.HTTP.Sessions == nil || len(observed.HTTP.Sessions.Rows) != 1 || observed.HTTP.Sessions.Rows[0].ID != id {
				t.Fatal("explicit new observation failed")
			}
		})
	}
}
