package prestosql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
)

// providerRelay forwards to the actual owned server; it never executes or
// fabricates a database result. A write fault withholds one completed response.
type providerRelay struct {
	server         *httptest.Server
	mu             sync.Mutex
	origin         string
	completed      json.RawMessage
	blocked, armed atomic.Bool
	writes         atomic.Int64
}

func newProviderRelay(t *testing.T, endpoint string) *providerRelay {
	t.Helper()
	target, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	r := &providerRelay{}
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(request *http.Request) { director(request); request.Header.Set("Accept-Encoding", "identity") }
	proxy.ModifyResponse = func(response *http.Response) error {
		if response.StatusCode != http.StatusOK {
			return nil
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		response.Body.Close()
		if err != nil {
			return err
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return errors.New("owned relay received invalid original JSON")
		}
		var next, update string
		_ = json.Unmarshal(fields["nextUri"], &next)
		_ = json.Unmarshal(fields["updateType"], &update)
		r.mu.Lock()
		if r.armed.Load() && next == "" && update == "INSERT" && r.completed == nil {
			r.completed = append(json.RawMessage(nil), raw...)
			response.StatusCode, response.Status = http.StatusInternalServerError, "500 Internal Server Error"
			raw = []byte("owned relay withheld original completed write response")
		} else if next != "" {
			nextURL, err := url.Parse(next)
			origin, _ := url.Parse(r.origin)
			if err != nil || nextURL.Scheme != target.Scheme || nextURL.Host != target.Host && nextURL.Host != origin.Host {
				r.mu.Unlock()
				return errors.New("native page escaped owned endpoints")
			}
			nextURL.Scheme, nextURL.Host = origin.Scheme, origin.Host
			fields["nextUri"], _ = json.Marshal(nextURL.String())
			raw, _ = json.Marshal(fields)
		}
		r.mu.Unlock()
		response.Body = io.NopCloser(bytes.NewReader(raw))
		response.ContentLength = int64(len(raw))
		response.Header.Del("Content-Length")
		return nil
	}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if r.blocked.Load() {
			http.Error(w, "owned original network unavailable", http.StatusInternalServerError)
			return
		}
		if request.Method == http.MethodPost && request.URL.Path == "/v1/statement" {
			raw, err := io.ReadAll(io.LimitReader(request.Body, (1<<20)+1))
			request.Body.Close()
			if err != nil || len(raw) > 1<<20 {
				http.Error(w, "owned request bound", 400)
				return
			}
			request.Body = io.NopCloser(bytes.NewReader(raw))
			if strings.HasPrefix(strings.TrimSpace(string(raw)), "INSERT INTO memory.default.uncertain_provider64") {
				r.writes.Add(1)
			}
		}
		proxy.ServeHTTP(w, request)
	}))
	r.mu.Lock()
	r.origin = r.server.URL
	r.mu.Unlock()
	t.Cleanup(r.server.Close)
	return r
}

func (e *providerEvidence) originalFailure(role string) jdbcplugin.Receipt {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := len(e.receipts) - 1; i >= 0; i-- {
		row := e.receipts[i]
		if row["method"] == role+"/executeQuery" {
			r := row["receipt"].(jdbcplugin.Receipt)
			if r.RemoteFailure || r.ProcessExitConfirmed {
				return r.Clone()
			}
		}
	}
	return jdbcplugin.Receipt{}
}

func TestPrestoSQLPrivateProviderNativeUncertainWriteAndExplicitRecovery(t *testing.T) {
	f, provider, config, evidence := nativeProvider(t)
	relay := newProviderRelay(t, f.Endpoint)
	config.Endpoint = relay.server.URL
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	s, err := provider.OpenManaged(ctx, 64, config, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	q, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	queryNative(t, q, "", "CREATE TABLE memory.default.uncertain_provider64 (id BIGINT)")
	relay.armed.Store(true)
	out, err := q.Execute(ctx, "", "INSERT INTO memory.default.uncertain_provider64 VALUES (64)")
	if !errors.Is(err, contract.ErrSQLSessionLost) || len(out.Steps) != 1 || out.Steps[0].State != "unknown" || out.AffectedRows != 0 {
		t.Fatal("uncertain write was accepted or described as absent", err, out.Steps)
	}
	original := evidence.originalFailure("query-1")
	if !original.Dispatched || !original.ExecutorAcknowledged || !original.ProcessExitConfirmed || original.Diagnostic == nil || original.Diagnostic.HasVendorCode {
		t.Fatal("original uncertain receipt missing", original)
	}
	retained := original.OriginalResponse()
	relay.mu.Lock()
	completed := append(json.RawMessage(nil), relay.completed...)
	relay.mu.Unlock()
	var server struct {
		UpdateCount int `json:"updateCount"`
		Stats       struct {
			State string `json:"state"`
		} `json:"stats"`
	}
	if json.Unmarshal(completed, &server) != nil || server.UpdateCount != 1 || server.Stats.State != "FINISHED" {
		t.Fatal("fault did not capture an actual completed write")
	}
	if _, err = q.Execute(ctx, "", "INSERT INTO memory.default.uncertain_provider64 VALUES (99)"); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("original lost document accepted another request", err)
	}
	if err = q.Preconnect(ctx); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("original lease recovered implicitly", err)
	}
	read, err := s.ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	observed := queryNative(t, read, "", "SELECT id FROM memory.default.uncertain_provider64")
	if len(observed.Sets[0].Rows) != 1 || observed.Sets[0].Rows[0][0].Text != "64" {
		t.Fatal("original write not observed independently")
	}
	q, err = q.Replace()
	if err != nil {
		t.Fatal(err)
	}
	recovered := queryNative(t, q, "", "SELECT id FROM memory.default.uncertain_provider64")
	if relay.writes.Load() != 1 || len(recovered.Sets[0].Rows) != 1 || !bytes.Equal(retained, original.OriginalResponse()) {
		t.Fatal("recovery replayed SQL or replaced original receipt")
	}
	queryNative(t, q, "", "DROP TABLE memory.default.uncertain_provider64")
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = provider.Store.Remove(ctx, "prestosql", config.DriverVersion); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(map[string]any{"outcome": out, "original_receipt": original, "original_native_bytes": []byte(retained), "captured_native_completion": completed, "independent_read": observed, "explicit_recovery_read": recovered, "insert_dispatches": relay.writes.Load(), "closed_package_removed": true}, "", "  ")
	if err != nil || os.WriteFile(filepath.Join(f.Directory, "provider-uncertain-write.json"), raw, 0600) != nil {
		t.Fatal("write receipt evidence", err)
	}
}

func TestPrestoSQLPrivateProviderNativeNetworkValidationAndMetadataRevocation(t *testing.T) {
	f, provider, config, evidence := nativeProvider(t)
	relay := newProviderRelay(t, f.Endpoint)
	config.Endpoint = relay.server.URL
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	relay.blocked.Store(true)
	failed, err := provider.OpenManaged(ctx, 64, config, Credentials{})
	if failed != nil || err == nil {
		t.Fatal("local JDBC connection was published as network-valid")
	}
	failedHealth := evidence.originalFailure("metadata")
	if !failedHealth.Dispatched || !failedHealth.ProcessExitConfirmed {
		t.Fatal("failed original health request/process not retained", failedHealth)
	}
	relay.blocked.Store(false)
	s, err := provider.OpenManaged(ctx, 64, config, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	q, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	queryNative(t, q, "", "SELECT 64")
	relay.blocked.Store(true)
	_, err = s.Namespaces(ctx)
	if !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("untyped metadata network failure did not revoke original session", err)
	}
	if err = s.Close(); err != nil && !errors.Is(err, jdbcplugin.ErrLost) {
		t.Fatal(err)
	}
	relay.blocked.Store(false)
	if _, err = s.ReserveQuery(2); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("revoked metadata session recovered without explicit open", err)
	}
	if _, err = q.Execute(ctx, "", "SELECT 99"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("query from revoked session replayed", err)
	}
	replacement, err := provider.OpenManaged(ctx, 64, config, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = replacement.Close() })
	newQuery, err := replacement.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	value := queryNative(t, newQuery, "", "SELECT 65")
	if value.Sets[0].Rows[0][0].Text != "65" {
		t.Fatal("explicit original-profile recovery unavailable")
	}
	if err = replacement.Close(); err != nil {
		t.Fatal(err)
	}
	if err = provider.Store.Remove(ctx, "prestosql", config.DriverVersion); err != nil {
		t.Fatal(err)
	}
	evidence.mu.Lock()
	count := len(evidence.processes)
	evidence.mu.Unlock()
	if count != 5 {
		t.Fatal(fmt.Sprintf("unexpected original process count: %d", count))
	}
	raw, err := json.MarshalIndent(map[string]any{"failed_health_receipt": failedHealth, "failed_health_native_bytes": []byte(failedHealth.OriginalResponse()), "untyped_metadata_revoked_session": true, "old_query_refused_after_network_restored": true, "explicit_recovery": value, "processes": count, "closed_package_removed": true}, "", "  ")
	if err != nil || os.WriteFile(filepath.Join(f.Directory, "provider-network-recovery.json"), raw, 0600) != nil {
		t.Fatal("network recovery evidence", err)
	}
}
