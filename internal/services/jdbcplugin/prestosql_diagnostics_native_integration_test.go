package jdbcplugin

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
	"strings"
	"sync"
	"testing"
	"time"
)

// Faults replace only a captured completed server response, never a database
// executor. The independent connection verifies the original write actually ran.
func TestPrestoSQLOriginalPluginNativeDiagnosticsAndLostWrite(t *testing.T) {
	f := nativeFixture(t)
	target, _ := url.Parse(f.Endpoint)
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalDirector(request)
		request.Header.Set("Accept-Encoding", "identity")
	}
	var mu sync.Mutex
	var origin string
	var originalWrite json.RawMessage
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
			return fmt.Errorf("owned proxy JSON decode failed: encoding=%s", response.Header.Get("Content-Encoding"))
		}
		var next, update string
		_ = json.Unmarshal(fields["nextUri"], &next)
		_ = json.Unmarshal(fields["updateType"], &update)
		mu.Lock()
		if next == "" && update == "INSERT" && originalWrite == nil {
			originalWrite = append(json.RawMessage(nil), raw...)
			response.StatusCode, response.Status = http.StatusInternalServerError, "500 Internal Server Error"
			raw = []byte("owned fault: original completed response withheld")
		} else if next != "" {
			nextURL, parseErr := url.Parse(next)
			proxyURL, _ := url.Parse(origin)
			if parseErr != nil || nextURL.Scheme != target.Scheme || nextURL.Host != target.Host && nextURL.Host != proxyURL.Host {
				mu.Unlock()
				return fmt.Errorf("owned proxy next endpoint differs: %s", nextURL)
			}
			nextURL.Scheme, nextURL.Host = proxyURL.Scheme, proxyURL.Host
			fields["nextUri"], _ = json.Marshal(nextURL.String())
			raw, _ = json.Marshal(fields)
		}
		mu.Unlock()
		response.Body = io.NopCloser(bytes.NewReader(raw))
		response.ContentLength = int64(len(raw))
		response.Header.Del("Content-Length")
		return nil
	}
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	mu.Lock()
	origin = server.URL
	mu.Unlock()
	proxied := f
	proxied.Endpoint = server.URL
	c := nativeClient(t, proxied)
	nativeSQL(t, c, "CREATE TABLE memory.default.diag_lost63 (id BIGINT)")
	// Use the production typed path; query failures retain their original
	// diagnostics and ambiguous outcomes quarantine the original process.
	typed := func(sql string, uncertain bool) Receipt {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		r, err := c.Execute(ctx, Query{SQL: sql, Catalog: "memory", Schema: "default", MaxRows: 10})
		if !errors.Is(err, ErrRemote) || !r.ExecutorAcknowledged || r.Diagnostic == nil || errors.Is(err, ErrLost) != uncertain || r.ProcessExitConfirmed != uncertain || c.Alive() == uncertain {
			t.Fatal("original typed diagnostic/ownership differs", err, r.Diagnostic)
		}
		return r
	}
	syntax := typed("SELECT FROM secret_diagnostic_value63", false)
	unsupported := typed("UPDATE memory.default.diag_lost63 SET id=2", false)
	lost := typed("INSERT INTO memory.default.diag_lost63 VALUES (63)", true)
	mu.Lock()
	captured := append(json.RawMessage(nil), originalWrite...)
	mu.Unlock()
	if len(captured) == 0 || !bytes.Contains(captured, []byte(`"updateCount":1`)) {
		t.Fatal("fault did not capture original completed native write", string(captured))
	}
	rows := nativeHTTP(t, f, "SELECT id FROM memory.default.diag_lost63")
	if len(rows) != 1 || len(rows[0]) != 1 || string(rows[0][0]) != "63" {
		t.Fatal("native original write not independently observed", rows)
	}
	for _, r := range []Receipt{syntax, unsupported, lost} {
		if !bytes.Contains(r.Result, []byte(`"execution_error":true`)) || strings.Contains(string(r.Result), "secret_diagnostic_value63") {
			t.Fatal("typed diagnostic missing or exposing original SQL", string(r.Result))
		}
	}
	retained := lost.OriginalResponse()
	if r, err := c.Execute(context.Background(), Query{SQL: "INSERT INTO memory.default.diag_lost63 VALUES (99)", MaxRows: 10}); err == nil || r.Dispatched {
		t.Fatal("quarantined native write was replayed")
	}
	replacement := nativeClient(t, proxied)
	recovered := nativeSQL(t, replacement, "SELECT id FROM memory.default.diag_lost63")
	if !bytes.Equal(retained, lost.OriginalResponse()) || !bytes.Equal(nativeRows(t, recovered)[0][0], []byte("63")) {
		t.Fatal("recovery replaced the original uncertain receipt")
	}
	nativeEvidence(t, f, "go-diagnostics.json", map[string]any{"syntax": syntax.OriginalResponse(), "unsupported": unsupported.OriginalResponse(), "lost_original_response": lost.OriginalResponse(), "captured_native_completion": captured, "independent_rows": rows, "quarantined_receipt": lost, "recovery_read": recovered.OriginalResponse(), "old_pid": c.PID(), "replacement_pid": replacement.PID(), "replay_blocked": true})
	nativeHTTP(t, f, "DROP TABLE memory.default.diag_lost63")
}
