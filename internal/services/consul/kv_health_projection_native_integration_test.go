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
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// Fixture writes use only the original test client's captured transport. They
// do not extend the production request allowlist or publish Agent authority.
func nativeHealthFixturePUT(t *testing.T, c *Client, path string, value any) {
	t.Helper()
	var body []byte
	var err error
	if value != nil {
		body, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, "PUT", c.config.Endpoint+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	if c.config.Auth == "token" {
		r.Header.Set("X-Consul-Token", c.token)
	}
	response, err := c.http.Do(r)
	if err != nil {
		t.Fatal("original fixture registration transport failed", err)
	}
	defer response.Body.Close()
	reply, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil || response.StatusCode != 200 {
		message := string(reply)
		if c.token != "" {
			message = strings.ReplaceAll(message, c.token, "[omitted token]")
		}
		t.Fatal("original fixture registration failed", response.StatusCode, err, message)
	}
}

func nativeHealthFixture(t *testing.T, c *Client) (service string, ids []string) {
	t.Helper()
	service = "owned-" + strings.ReplaceAll(t.Name(), "/", "-")
	for _, status := range []string{"passing", "warning"} {
		id := service + "-" + status
		ids = append(ids, id)
		nativeHealthFixturePUT(t, c, "/v1/agent/service/register", map[string]any{
			"ID": id, "Name": service, "Address": "127.0.0.1", "Port": 8087,
			"Tags": []string{"original", "中文"}, "Meta": map[string]string{"original": "原值"},
			"TaggedAddresses": map[string]any{"lan": map[string]any{"Address": "127.0.0.1", "Port": 8087}},
			"Weights":         map[string]uint64{"Passing": 3, "Warning": 2}, "EnableTagOverride": true,
			"Check": map[string]any{"CheckID": id + "-check", "Name": "original check", "TTL": "10m", "Status": "critical", "Notes": "original notes"},
		})
		t.Cleanup(func() { nativeHealthFixturePUT(t, c, "/v1/agent/service/deregister/"+url.PathEscape(id), nil) })
		nativeHealthFixturePUT(t, c, "/v1/agent/check/update/"+url.PathEscape(id+"-check"), map[string]string{"Status": status, "Output": "original output 中文"})
	}
	// Agent check updates reach the catalog asynchronously. Await the owned
	// fixture's real output before assertions about the captured read snapshot.
	deadline := time.Now().Add(10 * time.Second)
	for {
		r := nativeDo(t, c, nativeHealthRequest(t)(HealthChecksRequest(service)))
		var checks []struct{ Output string }
		if json.Unmarshal(r.Body, &checks) == nil && len(checks) == 2 && checks[0].Output == "original output 中文" && checks[1].Output == "original output 中文" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned native check output did not reach the original catalog")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return service, ids
}

func nativeHealthRequest(t *testing.T) func(Request, error) Request {
	t.Helper()
	return func(request Request, err error) Request {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return request
	}
}

func nativeHealthPresence(t *testing.T, raw json.RawMessage, present, nulls []string) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil || len(fields) != len(present) {
		t.Fatal("original health fields lost")
	}
	for name, value := range fields {
		if !slices.Contains(present, name) || slices.Contains(nulls, name) != bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			t.Fatal("original health presence/null changed", name)
		}
	}
	return fields
}

func nativeHealthCheckValues(t *testing.T, raw json.RawMessage, check contract.KVHTTPHealthCheck) {
	t.Helper()
	fields := nativeHealthPresence(t, raw, check.PresentFields, check.NullFields)
	for name, actual := range map[string]string{"Node": check.Node, "CheckID": check.CheckID, "Name": check.Name, "Status": check.Status, "Notes": check.Notes, "Output": check.Output, "ServiceID": check.ServiceID, "ServiceName": check.ServiceName, "Type": check.Type, "Namespace": check.Namespace, "Partition": check.Partition} {
		if value, ok := fields[name]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			var original string
			if json.Unmarshal(value, &original) != nil || actual != original {
				t.Fatal("original health string changed", name)
			}
		}
	}
	for name, actual := range map[string]uint64{"CreateIndex": check.CreateIndex, "ModifyIndex": check.ModifyIndex, "ExposedPort": check.ExposedPort} {
		if value, ok := fields[name]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			var original uint64
			if json.Unmarshal(value, &original) != nil || actual != original {
				t.Fatal("original unsigned health field changed", name)
			}
		}
	}
	for name, actual := range map[string]contract.KVHTTPHealthDuration{"Interval": check.Interval, "Timeout": check.Timeout} {
		if value, ok := fields[name]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			value = bytes.TrimSpace(value)
			if value[0] == '"' {
				var original string
				if json.Unmarshal(value, &original) != nil || actual.Text != original || actual.Nanoseconds != nil {
					t.Fatal("original native check duration string changed", name)
				}
			} else {
				var original uint64
				if json.Unmarshal(value, &original) != nil || actual.Nanoseconds == nil || *actual.Nanoseconds != original || actual.Text != "" {
					t.Fatal("original native check duration nanoseconds changed", name)
				}
			}
		}
	}
	if value, ok := fields["Definition"]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		definition := nativeHealthPresence(t, value, check.Definition.PresentFields, check.Definition.NullFields)
		for name, actual := range map[string]contract.KVHTTPHealthDuration{"TTL": check.Definition.TTL, "Interval": check.Definition.Interval, "Timeout": check.Definition.Timeout} {
			if value, ok := definition[name]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				value = bytes.TrimSpace(value)
				if value[0] == '"' {
					var original string
					if json.Unmarshal(value, &original) != nil || actual.Text != original || actual.Nanoseconds != nil {
						t.Fatal("original native duration string changed", name)
					}
				} else {
					var original uint64
					if json.Unmarshal(value, &original) != nil || actual.Nanoseconds == nil || *actual.Nanoseconds != original || actual.Text != "" {
						t.Fatal("original native duration nanoseconds changed", name)
					}
				}
			}
		}
	}
}

func TestConsulNativeHealthProjectionOriginalEndpointsFieldsAndReadOptions(t *testing.T) {
	s, base, _, _ := nativeDocumentSession(t, 2501)
	service, ids := nativeHealthFixture(t, base)
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	requests := []Request{
		nativeHealthRequest(t)(HealthNodeRequest(s.KVScope().HTTP.AgentName)),
		nativeHealthRequest(t)(HealthChecksRequest(service)),
	}
	for _, state := range []string{"any", "passing", "warning", "critical"} {
		r := nativeHealthRequest(t)(HealthStateRequest(state))
		filter, near := "ServiceName == `"+service+"`", "_agent"
		requests = append(requests, nativeHealthRequest(t)(WithHealthReadOptions(r, HealthReadOptions{Filter: &filter, Near: &near})))
	}
	for _, request := range requests {
		result := executeDocument(t, l, request)
		h := result.HTTP
		if h == nil || h.Health == nil || !h.Health.Complete || !h.Health.ProjectionComplete || !h.Acknowledged || !h.Succeeded || h.Metadata.Index == nil || result.WriteOutcomeUnknown {
			t.Fatal("original health checks projection missing", result.ProjectionError)
		}
		var rows []json.RawMessage
		if json.Unmarshal(h.Body, &rows) != nil || len(rows) != len(h.Health.Checks) {
			t.Fatal("original health row order/count changed")
		}
		for i, check := range h.Health.Checks {
			nativeHealthCheckValues(t, rows[i], check)
		}
		if request.Query.Has("filter") {
			state := strings.TrimPrefix(request.Path, "/v1/health/state/")
			want := map[string]int{"any": 2, "passing": 1, "warning": 1, "critical": 0}[state]
			if len(rows) != want {
				t.Fatal("original state filter changed native check count", state, len(rows))
			}
		}
		if request.Path == "/v1/health/checks/"+url.PathEscape(service) && (len(rows) != 2 || h.Health.Checks[0].Output != "original output 中文" || h.Health.Checks[0].ModifyIndex == 0) {
			t.Fatal("owned native check fields absent", h.Health.Checks)
		}
		if request.Query.Has("filter") && !reflect.DeepEqual(url.Values(h.Request.Query), request.Query) {
			t.Fatal("original filter/near request changed")
		}
	}
	for _, passing := range []*bool{nil, new(false), new(true)} {
		request := nativeHealthRequest(t)(HealthServiceRequest(service, passing))
		result := executeDocument(t, l, request)
		h := result.HTTP
		want := 2
		if passing != nil && *passing {
			want = 1
		}
		if h == nil || h.Health == nil || !h.Health.Complete || len(h.Health.Instances) != want || h.Metadata.Index == nil {
			t.Fatal("native passing absent/false/true semantics changed", result.ProjectionError)
		}
		var rows []map[string]json.RawMessage
		if json.Unmarshal(h.Body, &rows) != nil || len(rows) != want {
			t.Fatal("original instance count changed")
		}
		for i, instance := range h.Health.Instances {
			nativeHealthPresence(t, rows[i]["Node"], instance.Node.PresentFields, instance.Node.NullFields)
			nativeHealthPresence(t, rows[i]["Service"], instance.Service.PresentFields, instance.Service.NullFields)
			if instance.Node.Node != s.KVScope().HTTP.AgentName || !slices.Contains(ids, instance.Service.ID) || instance.Service.Service != service || instance.Service.Meta["original"] != "原值" || instance.Service.Weights.Passing != 3 || instance.Service.Weights.Warning != 2 || instance.Service.Port != 8087 || instance.Service.TaggedAddresses["lan"].Port != 8087 || !slices.Equal(instance.Service.Tags, []string{"original", "中文"}) || !instance.Service.EnableTagOverride || instance.Node.CreateIndex == 0 || instance.Service.ModifyIndex == 0 {
				t.Fatal("original nested node/service fields changed")
			}
			var checks []json.RawMessage
			if json.Unmarshal(rows[i]["Checks"], &checks) != nil || len(checks) != len(instance.Checks) {
				t.Fatal("original nested check count changed")
			}
			for k, check := range instance.Checks {
				nativeHealthCheckValues(t, checks[k], check)
			}
		}
		index, waitDuration, near := *h.Metadata.Index, "1ms", "_agent"
		blocking := nativeHealthRequest(t)(WithHealthReadOptions(request, HealthReadOptions{Index: &index, Wait: &waitDuration, Near: &near}))
		read := executeDocument(t, l, blocking)
		if read.HTTP.Health == nil || len(read.HTTP.Health.Instances) != want || url.Values(read.HTTP.Request.Query).Get("index") != strconv.FormatUint(index, 10) || url.Values(read.HTTP.Request.Query).Get("wait") != waitDuration {
			t.Fatal("original bounded blocking options lost")
		}
		clone := result.Clone()
		clone.HTTP.Health.Instances[0].Service.Meta["original"] = "changed"
		clone.HTTP.Health.Instances[0].Checks[0].Status = "changed"
		if result.HTTP.Health.Instances[0].Service.Meta["original"] != "原值" || l.LastResult().HTTP.Health.Instances[0].Checks[0].Status == "changed" {
			t.Fatal("native health projection aliases original receipt")
		}
	}
	// Native maintenance remains a critical check; its local classification does
	// not overwrite Status or create authority to mutate the contacted Agent.
	nativeHealthFixturePUT(t, base, "/v1/agent/service/maintenance/"+url.PathEscape(ids[0])+"?enable=true&reason=original", nil)
	maintenance := executeDocument(t, l, nativeHealthRequest(t)(HealthChecksRequest(service)))
	if maintenance.HTTP.Health == nil {
		t.Fatal("original maintenance projection absent", maintenance.ProjectionError)
	}
	found := false
	for _, check := range maintenance.HTTP.Health.Checks {
		if check.CheckID == "_service_maintenance:"+ids[0] {
			found = check.Maintenance() && check.Status == "critical"
		}
	}
	if !found {
		t.Fatal("original maintenance check or critical status lost")
	}
}

func TestConsulNativeHealthProjectionACLAndOriginalUnknownWriteRecovery(t *testing.T) {
	base, cfg, token := nativeClient(t)
	service, _ := nativeHealthFixture(t, base)
	request := nativeHealthRequest(t)(HealthServiceRequest(service, nil))
	if cfg.Auth != "none" {
		secret := os.Getenv("NYADB_CONSUL_READ_TOKEN")
		if secret == "" {
			t.Fatal("original restricted fixture token missing")
		}
		s, err := OpenNativeSession(context.Background(), 2502, cfg, secret)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		l, err := s.ReserveKV(1)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		for _, r := range []Request{request, nativeHealthRequest(t)(HealthStateRequest("any"))} {
			result := executeDocument(t, l, r)
			if result.HTTP.Health == nil || !result.HTTP.Acknowledged || !result.HTTP.Health.ProjectionComplete || result.HTTP.Health.Complete || result.HTTP.Metadata.ACLFiltered == nil || !*result.HTTP.Metadata.ACLFiltered || len(result.HTTP.Health.Instances) != 0 || len(result.HTTP.Health.Checks) != 0 {
				t.Fatal("restricted native health subset became complete inventory", result.ProjectionError)
			}
		}
	}
	relay, calls, processed := nativeFault(t, cfg, token, "lost")
	s, err := OpenNativeSession(context.Background(), 2503, relay, token)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	key, value := keyFor(t, "unknown-original-write"), []byte{0, 255}
	unknown, err := l.Execute(context.Background(), documentPlan(t, l, putRequest(key, value)))
	wait(t, processed)
	if err == nil || !unknown.WriteOutcomeUnknown || unknown.HTTP == nil || unknown.HTTP.Health != nil || !bytes.Equal(nativeRead(t, base, key).Value, value) {
		t.Fatal("processed original write lost independent unknown state")
	}
	before := calls.Load()
	written := nativeRead(t, base, key)
	recovery, err := s.RecoverKV(l)
	if err != nil || recovery.Unknown == nil || !reflect.DeepEqual(*recovery.Unknown, unknown) || calls.Load() != before {
		t.Fatal("explicit recovery executed or rewrote original unknown", err)
	}
	defer recovery.Lease.Close()
	read := executeDocument(t, recovery.Lease, request)
	if read.HTTP.Health == nil || len(read.HTTP.Health.Instances) != 2 || !reflect.DeepEqual(*recovery.Lease.(*documentLease).recoveryOwned().Unknown, unknown) || calls.Load() <= before || read.HTTP.Request.Path != request.Path || nativeRead(t, base, key).ModifyIndex != written.ModifyIndex {
		t.Fatal("later health observation missing, original unknown changed or write replayed", read.ProjectionError)
	}
	write, err := DocumentRequest(putRequest(key, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recovery.Lease.Prepare(write); !errors.Is(err, contract.ErrKVWriteUnknown) {
		t.Fatal("health read enabled further writes", err)
	}
}

// Faults follow a captured successful read from the original official server.
func nativeHealthProjectionRelay(t *testing.T, cfg Config, token, service, mode string) (Config, *atomic.Int32, <-chan struct{}) {
	t.Helper()
	upstream, err := New(cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { upstream.Close() })
	calls, processed := &atomic.Int32{}, make(chan struct{})
	var once atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, err := http.NewRequestWithContext(r.Context(), r.Method, cfg.Endpoint+r.URL.RequestURI(), r.Body)
		if err != nil {
			t.Error("invalid original relay request")
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
			t.Error("original health response incomplete before relay fault")
			return
		}
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		if r.Method == "GET" && r.URL.Path == "/v1/health/checks/"+service {
			calls.Add(1)
			if once.CompareAndSwap(false, true) {
				if response.StatusCode != 200 || !bytes.Contains(body, []byte(service)) {
					t.Error("owned native health response absent before fault")
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
					w.Write([]byte("original health reply withheld"))
					return
				case "foreign":
					body = []byte(`[{"Node":"foreign","CheckID":"foreign","ServiceName":"foreign"}]`)
				case "null":
					body = []byte("null")
				case "overflow":
					body = []byte(`[{"Node":"original","CheckID":"original","ServiceName":"` + service + `","ModifyIndex":18446744073709551616}]`)
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

func TestConsulNativeHealthProjectionProcessedReadFaultsCancellationRetirementAndRecovery(t *testing.T) {
	base, cfg, token := nativeClient(t)
	service, _ := nativeHealthFixture(t, base)
	request := nativeHealthRequest(t)(HealthChecksRequest(service))
	for _, mode := range []string{"lost", "partial", "http500", "http408", "foreign", "null", "overflow", "cancel", "retire", "close"} {
		t.Run(mode, func(t *testing.T) {
			relay, calls, processed := nativeHealthProjectionRelay(t, cfg, token, service, mode)
			s, err := OpenNativeSession(context.Background(), 2504, relay, token)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			l, err := s.ReserveKV(1)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			plan := documentPlan(t, l, request)
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
				t.Fatal("original health executor did not acknowledge read end")
			}
			h := out.result.HTTP
			if h == nil || h.Health != nil || !h.Ended || h.WriteOutcomeUnknown || out.result.WriteOutcomeUnknown || calls.Load() != 1 {
				t.Fatal("incomplete original health read became complete or replayed")
			}
			switch mode {
			case "foreign", "null", "overflow":
				if out.err != nil || !h.Acknowledged || !h.Succeeded || h.ProjectionError == "" {
					t.Fatal("optional projection invalidated original successful HTTP receipt", out.err)
				}
			default:
				if out.err == nil || h.Acknowledged || h.Succeeded {
					t.Fatal("incomplete/error original health read was acknowledged", out.err)
				}
			}
			if mode == "close" {
				return
			}
			original := l.LastResult()
			owner := s
			if mode == "retire" {
				owner, err = OpenNativeSession(context.Background(), 2504, relay, token)
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
			if calls.Load() != 1 || recovery.Unknown != nil || !reflect.DeepEqual(recovery.Last, original) {
				t.Fatal("health recovery executed or changed original evidence")
			}
			observed := executeDocument(t, recovery.Lease, request)
			if calls.Load() != 2 || observed.HTTP.Health == nil || len(observed.HTTP.Health.Checks) != 2 {
				t.Fatal("explicit new original health observation failed")
			}
		})
	}
}
