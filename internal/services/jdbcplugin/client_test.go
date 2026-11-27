package jdbcplugin

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

// Supplemental boundary tests exercise malformed peers and settlement timing.
// They do not establish native database or JDBC compatibility.
func TestExternalJDBCPluginPeer(t *testing.T) {
	if os.Getenv("NYADB_JDBCPLUGIN_PEER") != "1" {
		return
	}
	var mu sync.Mutex
	var pending uint64
	var mode string
	emit := func(v any) { mu.Lock(); defer mu.Unlock(); _ = json.NewEncoder(os.Stdout).Encode(v) }
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     uint64                     `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		result := any(map[string]bool{"ok": true})
		switch request.Method {
		case "jdbcSessionProtocol":
			result = map[string]int{"version": 2}
		case "connect":
			var connection map[string]string
			if json.Unmarshal(request.Params["connection"], &connection) != nil || connection["username"] != "bound" {
				emit(map[string]any{"id": request.ID, "error": map[string]string{"message": "TOPSECRET connection changed"}})
				continue
			}
		case "executeQuery":
			var sql string
			_ = json.Unmarshal(request.Params["sql"], &sql)
			switch sql {
			case "wait", "unsettled", "bad-cancel":
				pending, mode = request.ID, sql
				continue
			case "bad-id":
				emit(map[string]any{"id": request.ID + 100, "result": map[string]bool{"ok": true}})
				continue
			case "duplicate":
				fmt.Fprintf(os.Stdout, "{\"id\":%d,\"id\":%d,\"result\":{}}\n", request.ID, request.ID)
				continue
			case "typed-ok":
				result = json.RawMessage(`{"columns":["value"],"rows":[[63]],"large_value_cells":[],"affected_rows":0,"execution_time_ms":1,"truncated":false}`)
			case "typed-sql":
				result = json.RawMessage(`{"execution_error":true,"error":{"diagnostics":{"category":"sql","vendorCode":1}}}`)
			case "typed-ambiguous":
				result = json.RawMessage(`{"execution_error":true,"error":{"diagnostics":{"category":"sql"}}}`)
			case "typed-sensitive":
				result = json.RawMessage(`{"execution_error":true,"error":{"diagnostics":{"category":"sql","message":"TOPSECRET"}}}`)
			case "secret":
				emit(map[string]any{"id": request.ID, "error": map[string]string{"message": "TOPSECRET SQL value"}})
				continue
			default:
				result = json.RawMessage(`{"rows":[[9223372036854775807,9007199254740993.1234567890123456789]]}`)
			}
		case "cancelStatement":
			if mode == "bad-cancel" {
				emit(map[string]any{"id": pending, "result": map[string]int{"affected_rows": 1}})
				emit(map[string]any{"id": request.ID, "result": map[string]string{"cancelled": "invalid"}})
				continue
			}
			emit(map[string]any{"id": request.ID, "result": map[string]bool{"cancelled": true}})
			if mode == "wait" {
				id := pending
				go func() {
					time.Sleep(80 * time.Millisecond)
					emit(map[string]any{"id": id, "result": map[string]int{"affected_rows": 1}})
				}()
			}
			continue
		case "close":
			emit(map[string]any{"id": request.ID, "result": result, "_dbx_close": true})
			os.Exit(0)
		}
		emit(map[string]any{"id": request.ID, "result": result})
	}
	os.Exit(0)
}
func peerSpec(t *testing.T) agentbridge.LaunchSpec {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return agentbridge.LaunchSpec{Executable: executable, Args: []string{"-test.run=^TestExternalJDBCPluginPeer$"}, Environment: []string{"NYADB_JDBCPLUGIN_PEER=1", "GORACE=atexit_sleep_ms=0"}, Artifacts: []agentbridge.Artifact{{Path: executable, SHA256: hex.EncodeToString(sum[:])}}}
}
func peer(t *testing.T) *Client {
	return peerOptions(t, Options{CancelWait: 250 * time.Millisecond})
}
func peerOptions(t *testing.T, options Options) *Client {
	t.Helper()
	c, err := Start(context.Background(), peerSpec(t), map[string]string{"username": "bound"}, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.Close()
		select {
		case <-c.processDone:
		default:
			t.Error("captured peer survived close")
		}
	})
	return c
}
func TestOriginalPluginBoundaryPreservesValuesAndRedactsErrors(t *testing.T) {
	c := peer(t)
	r, err := c.Call(context.Background(), "executeQuery", map[string]string{"sql": "exact"})
	if err != nil || !strings.Contains(string(r.Result), "9007199254740993.1234567890123456789") || !r.ExecutorAcknowledged {
		t.Fatal(r, err)
	}
	retained := r.Clone()
	r.Result[0] = 'x'
	original := r.OriginalResponse()
	original[0] = 'x'
	if retained.Result[0] != '{' || retained.OriginalResponse()[0] != '{' {
		t.Fatal("caller mutated original response")
	}
	rejected, err := c.Call(context.Background(), "executeQuery", map[string]string{"sql": "secret"})
	plain, _ := json.Marshal(rejected)
	if !errors.Is(err, ErrRemote) || strings.Contains(err.Error(), "TOPSECRET") || strings.Contains(string(plain), "TOPSECRET") || !strings.Contains(string(rejected.OriginalResponse()), "TOPSECRET") {
		t.Fatal("original error privacy boundary", err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestOriginalPluginCancellationWaitsForExecutorOrProcess(t *testing.T) {
	for _, mode := range []string{"wait", "unsettled", "bad-cancel"} {
		t.Run(mode, func(t *testing.T) {
			c := peer(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan response, 1)
			go func() {
				r, e := c.Call(ctx, "executeQuery", map[string]string{"sql": mode})
				done <- response{receipt: r, err: e}
			}()
			deadline := time.Now().Add(time.Second)
			for {
				c.mu.Lock()
				pending := len(c.pending)
				c.mu.Unlock()
				if pending == 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("no original request")
				}
				time.Sleep(time.Millisecond)
			}
			started := time.Now()
			cancel()
			var settled response
			select {
			case settled = <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("executor not settled")
			}
			r := settled.receipt
			if !errors.Is(settled.err, context.Canceled) || !r.Dispatched || !r.CancellationRequested || len(r.CancellationResponse()) == 0 {
				t.Fatal("original cancellation lost", r, settled.err)
			}
			if mode == "wait" {
				if !r.ExecutorAcknowledged || r.ProcessExitConfirmed || string(r.Result) != `{"affected_rows":1}` || time.Since(started) < 60*time.Millisecond {
					t.Fatal("cancel acknowledgement replaced executor", r)
				}
			} else if mode == "bad-cancel" {
				if !errors.Is(settled.err, ErrProtocol) || !r.ExecutorAcknowledged || !r.ProcessExitConfirmed || string(r.Result) != `{"affected_rows":1}` {
					t.Fatal("invalid cancellation lost original ACK or skipped process exit", r, settled.err)
				}
			} else if r.ExecutorAcknowledged || !r.ProcessExitConfirmed || len(r.OriginalResponse()) != 0 {
				t.Fatal("unsettled peer fabricated native result", r)
			}
		})
	}
}
func TestOriginalPluginRejectsMalformedRoutingAndImplicitRecovery(t *testing.T) {
	for _, mode := range []string{"bad-id", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			c := peer(t)
			r, err := c.Call(context.Background(), "executeQuery", map[string]string{"sql": mode})
			if !errors.Is(err, ErrProtocol) || !r.Dispatched || r.ExecutorAcknowledged || !r.ProcessExitConfirmed {
				t.Fatal("malformed routing accepted", r, err)
			}
			if r, err = c.Call(context.Background(), "executeQuery", map[string]string{"sql": "SELECT 1"}); err == nil || r.Dispatched {
				t.Fatal("dead original peer was replayed")
			}
		})
	}
}
func TestOriginalPluginPreCanceledOversizedAndForeignRequestsStayUnsent(t *testing.T) {
	c := peerOptions(t, Options{MaxFrameBytes: 1024, CancelWait: 250 * time.Millisecond})
	for _, params := range []map[string]any{{"nativeDiagnostics": true}, {"jdbcSessionId": "foreign"}, {"cancelToken": "foreign"}, {"connection": map[string]string{"username": "foreign"}}} {
		if r, err := c.Call(context.Background(), "executeQuery", params); !errors.Is(err, ErrRequest) || r.Dispatched {
			t.Fatal("foreign ownership sent", r, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, err := c.Call(ctx, "executeQuery", map[string]string{"sql": "SELECT 1"}); !errors.Is(err, context.Canceled) || r.Dispatched {
		t.Fatal("pre-cancel dispatched", r, err)
	}
	if r, err := c.Call(context.Background(), "executeQuery", map[string]string{"sql": strings.Repeat("x", 2000)}); !errors.Is(err, ErrLimit) || r.Dispatched {
		t.Fatal("oversize dispatch", r, err)
	}
	if _, err := c.Call(context.Background(), "executeQuery", map[string]string{"sql": "exact"}); err != nil {
		t.Fatal("unsent errors revoked healthy owner", err)
	}
}
func TestOriginalPluginArtifactFailureDoesNotStartProcess(t *testing.T) {
	spec := peerSpec(t)
	spec.Artifacts[0].SHA256 = strings.Repeat("0", 64)
	started := false
	spec.Started = func(int) error { started = true; return nil }
	if c, err := Start(context.Background(), spec, map[string]string{"username": "bound"}, Options{}); c != nil || !errors.Is(err, ErrArtifact) || started {
		t.Fatal("changed original artifact launched", c, err, started)
	}
}

func TestOriginalPluginStartupRetainsRejectedConnectionAndExit(t *testing.T) {
	c, startup, err := StartWithReceipt(context.Background(), peerSpec(t), map[string]string{"username": "wrong"}, Options{CancelWait: 250 * time.Millisecond})
	plain, _ := json.Marshal(startup)
	if c != nil || !errors.Is(err, ErrRemote) || !startup.Protocol.ExecutorAcknowledged || !startup.Connect.ExecutorAcknowledged || !startup.Connect.RemoteFailure || !startup.ProcessExitConfirmed {
		t.Fatal("failed startup lost its original acknowledgement or process", startup, err)
	}
	if strings.Contains(string(plain), "TOPSECRET") || strings.Contains(err.Error(), "TOPSECRET") || !strings.Contains(string(startup.Connect.OriginalResponse()), "TOPSECRET") {
		t.Fatal("failed startup exposed or discarded native diagnostic")
	}
}

func TestOriginalPluginCannotReconnectThroughPublicCall(t *testing.T) {
	c := peer(t)
	for _, method := range []string{"connect", "testConnection", "validateConnection", "openJdbcSession", "closeJdbcSession", "connectionParameters"} {
		if r, err := c.Call(context.Background(), method, nil); !errors.Is(err, ErrRequest) || r.Dispatched {
			t.Fatal("public call replaced original connection", method, r, err)
		}
	}
}

func TestOriginalTypedQuerySeparatesServerErrorsAndAmbiguousReceipts(t *testing.T) {
	c := peer(t)
	known, err := c.Execute(context.Background(), Query{SQL: "typed-sql", MaxRows: 10})
	if !errors.Is(err, ErrRemote) || errors.Is(err, ErrLost) || !c.Alive() || known.Diagnostic == nil || known.Diagnostic.VendorCode != 1 {
		t.Fatal("known server error changed original owner", known.Diagnostic, err)
	}
	if _, err := c.Execute(context.Background(), Query{SQL: "typed-ok", MaxRows: 10}); err != nil {
		t.Fatal(err)
	}
	copy := known.Clone()
	copy.Diagnostic.Category = "changed"
	if known.Diagnostic.Category != "sql" {
		t.Fatal("diagnostic aliases retained receipt")
	}
	for _, sql := range []string{"typed-ambiguous", "typed-sensitive"} {
		t.Run(sql, func(t *testing.T) {
			original := peer(t)
			receipt, err := original.Execute(context.Background(), Query{SQL: sql, MaxRows: 10})
			if !errors.Is(err, ErrLost) || !receipt.RemoteFailure || !receipt.ExecutorAcknowledged || !receipt.ProcessExitConfirmed || original.Alive() {
				t.Fatal("ambiguous native receipt did not quarantine original process", receipt.Diagnostic, err)
			}
			public, _ := json.Marshal(receipt)
			if strings.Contains(string(public), "TOPSECRET") || strings.Contains(err.Error(), "TOPSECRET") {
				t.Fatal("unvalidated diagnostic exposed")
			}
			if sql == "typed-sensitive" && (!errors.Is(err, ErrProtocol) || !strings.Contains(string(receipt.OriginalResponse()), "TOPSECRET")) {
				t.Fatal("malformed diagnostic original lost")
			}
			if next, e := original.Execute(context.Background(), Query{SQL: "typed-ok", MaxRows: 10}); e == nil || next.Dispatched {
				t.Fatal("ambiguous operation replayed")
			}
		})
	}
}
