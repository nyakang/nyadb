package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestConsulNativeAgentLocalReadsOriginalMapsFiltersAndConfiguredScope(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	service, ids := nativeHealthFixture(t, writer)
	cfg.Datacenter, cfg.Prefix, cfg.Consistency = "foreign-datacenter", "foreign-prefix/", "consistent"
	reader, err := New(cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	for _, endpoint := range []string{"services", "checks"} {
		filter := "Service == " + strconv.Quote(service)
		build := AgentServicesRequest
		if endpoint == "checks" {
			filter, build = "ServiceName == "+strconv.Quote(service), AgentChecksRequest
		}
		for _, input := range []*string{nil, str(""), &filter} {
			request := mustAgentRequest(t)(build(input))
			response := nativeDo(t, reader, request)
			actual, err := url.Parse(response.URL)
			if err != nil || actual.Query().Has("dc") || actual.Query().Has("consistent") || actual.Query().Has("stale") || actual.Query().Has("index") || actual.Query().Has("wait") || actual.Query().Has("filter") != (input != nil) {
				t.Fatal("local agent read inherited catalog routing", err)
			}
			var rows map[string]json.RawMessage
			if json.Unmarshal(response.Body, &rows) != nil || rows == nil {
				t.Fatal("native agent response lost original map shape")
			}
			if input == &filter && len(rows) != 2 {
				t.Fatal("native agent filter did not retain the selected services", len(rows))
			}
			for _, id := range ids {
				key := id
				if endpoint == "checks" {
					key += "-check"
				}
				var fields map[string]json.RawMessage
				if json.Unmarshal(rows[key], &fields) != nil {
					t.Fatal("original owned agent entry absent", endpoint, key)
				}
				field := "ID"
				if endpoint == "checks" {
					field = "CheckID"
				}
				var originalID string
				if json.Unmarshal(fields[field], &originalID) != nil || originalID != key {
					t.Fatal("original agent map key and identity disagree", endpoint)
				}
			}
			before := bytes.Clone(response.Body)
			response.Body[0] ^= 1
			if !bytes.Equal(reader.LastResponse().Body, before) {
				t.Fatal("inspection mutated the complete original agent response")
			}
		}
	}
	self := nativeDo(t, reader, AgentSelfRequest())
	var identity struct {
		Config struct{ NodeID string }
	}
	if json.Unmarshal(self.Body, &identity) != nil || identity.Config.NodeID != os.Getenv("NYADB_CONSUL_NODE_ID") {
		t.Fatal("local reads changed the originally contacted agent")
	}
	if readToken := os.Getenv("NYADB_CONSUL_READ_TOKEN"); readToken != "" {
		cfg.Auth = "token"
		limited, err := New(cfg, readToken)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { limited.Close() })
		for _, request := range []Request{AgentSelfRequest(), mustAgentRequest(t)(AgentServicesRequest(nil)), mustAgentRequest(t)(AgentChecksRequest(nil))} {
			r, err := limited.Do(context.Background(), request)
			denied := r.Status == 403 && err != nil && !r.Succeeded
			var entries map[string]json.RawMessage
			filtered := request.Path != "/v1/agent/self" && r.Status == 200 && err == nil && r.Succeeded && r.Metadata.ACLFiltered != nil && *r.Metadata.ACLFiltered && json.Unmarshal(r.Body, &entries) == nil && entries != nil && len(entries) == 0
			if !r.Captured || !r.EOF || !r.Acknowledged || r.WriteOutcomeUnknown || !denied && !filtered {
				t.Fatal("restricted agent read invented permission or discarded its receipt", r.Status, err)
			}
		}
	}
}

// The test relay damages only a completed original local GET. Registration
// uses the fixture's captured transport, without enabling production writes.
func nativeAgentReadRelay(t *testing.T, cfg Config, token, mode string) (Config, *atomic.Int32, <-chan struct{}) {
	t.Helper()
	upstream, err := New(cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { upstream.Close() })
	calls := &atomic.Int32{}
	observed := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
		calls.Add(1)
		request, err := http.NewRequestWithContext(incoming.Context(), incoming.Method, cfg.Endpoint+incoming.URL.RequestURI(), nil)
		if err != nil {
			t.Error(err)
			return
		}
		request.Header = incoming.Header.Clone()
		request.GetBody, request.Close = nil, true
		response, err := upstream.http.Do(request)
		if err != nil {
			t.Error("original agent relay transport failed")
			return
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 {
			t.Error("original agent relay did not capture a complete response")
			return
		}
		observed <- struct{}{}
		switch mode {
		case "lost":
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				connection.Close()
			}
		case "partial":
			w.Header().Set("Content-Length", strconv.Itoa(len(body)+10))
			w.WriteHeader(200)
			w.Write(body[:min(20, len(body))])
		case "cancel":
			<-incoming.Context().Done()
		}
	}))
	t.Cleanup(server.Close)
	proxy := cfg
	proxy.Endpoint = server.URL
	proxy.CAFile, proxy.ServerName, proxy.ClientCertFile, proxy.ClientKeyFile = "", "", "", ""
	return proxy, calls, observed
}

func TestConsulNativeAgentLocalReadReceiptFaultsAndCancellationNeverRetry(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	nativeHealthFixture(t, writer)
	for _, mode := range []string{"lost", "partial", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			proxy, calls, observed := nativeAgentReadRelay(t, cfg, token, mode)
			reader, err := New(proxy, token)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { reader.Close() })
			request := mustAgentRequest(t)(AgentChecksRequest(nil))
			plan, err := reader.Prepare(request)
			if err != nil || calls.Load() != 0 {
				t.Fatal("pure original agent preparation executed a request", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				response Response
				err      error
			}
			done := make(chan completion, 1)
			go func() {
				r, err := reader.Execute(ctx, plan)
				done <- completion{r, err}
			}()
			select {
			case <-observed:
			case <-time.After(30 * time.Second):
				t.Fatal("original upstream agent read did not complete")
			}
			if mode == "cancel" {
				cancel()
			}
			select {
			case result := <-done:
				if result.err == nil || !result.response.Ended || result.response.Acknowledged || result.response.WriteOutcomeUnknown || calls.Load() != 1 {
					t.Fatal("damaged original agent response was guessed or retried", result.err)
				}
				if mode == "partial" && len(result.response.Body) == 0 || mode == "cancel" && !result.response.Canceled {
					t.Fatal("original partial or cancelled read evidence discarded")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("original agent executor did not acknowledge cancellation")
			}
			if _, err := reader.Execute(context.Background(), plan); !errors.Is(err, ErrRetired) || reader.Alive() || calls.Load() != 1 {
				t.Fatal("retirement did not revoke the original agent executor", err)
			}
		})
	}
}

func TestConsulNativeAgentLocalReadsKeepOriginalUnknownWriteAndEvidenceOnlyRecovery(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	proxy, calls, _ := nativeFault(t, cfg, token, "http500")
	session, err := OpenNativeSession(context.Background(), 91, proxy, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	lease, err := session.ReserveKV(73)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lease.Close() })
	key, value := keyFor(t, "original-unknown"), []byte{0, 255, 128}
	request, err := DocumentRequest(putRequest(key, value))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := lease.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := lease.Execute(context.Background(), plan)
	if err == nil || !result.WriteOutcomeUnknown || result.HTTP == nil || result.HTTP.Status != 500 || !bytes.Equal(nativeRead(t, writer, key).Value, value) {
		t.Fatal("original processed write was confirmed or not observed", err)
	}
	unknown := lease.(*documentLease).recoveryOwned().Unknown
	if unknown == nil {
		t.Fatal("original unknown write receipt absent")
	}
	for _, native := range []Request{AgentSelfRequest(), mustAgentRequest(t)(AgentServicesRequest(nil)), mustAgentRequest(t)(AgentChecksRequest(nil))} {
		request, err := DocumentRequest(native)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := lease.Prepare(request)
		if err != nil {
			t.Fatal(err)
		}
		result, err := lease.Execute(context.Background(), plan)
		if err != nil || !result.Completed || result.HTTP == nil || !result.HTTP.Acknowledged || result.WriteOutcomeUnknown || !reflect.DeepEqual(unknown, lease.(*documentLease).recoveryOwned().Unknown) {
			t.Fatal("independent original agent read changed unknown write evidence", err)
		}
	}
	before := calls.Load()
	recovered, err := session.RecoverKV(lease)
	if err != nil || recovered.Lease == nil || calls.Load() != before || !reflect.DeepEqual(unknown, recovered.Unknown) {
		t.Fatal("evidence-only agent recovery dispatched or changed original evidence", err)
	}
	t.Cleanup(func() { recovered.Lease.Close() })
	if _, err := recovered.Lease.Prepare(request); !errors.Is(err, contract.ErrKVWriteUnknown) || calls.Load() != before {
		t.Fatal("agent reads or recovery removed the original write barrier", err)
	}
}
