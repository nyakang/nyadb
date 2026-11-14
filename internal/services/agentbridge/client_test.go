package agentbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestAgentBridgeProcess supplies independent protocol behavior in an owned
// subprocess. In particular, cancel acknowledgment and executor completion are
// controlled separately, and requests from two original runtimes can coexist.
func TestAgentBridgeProcess(t *testing.T) {
	if os.Getenv("NYADB_AGENT_BRIDGE_HELPER") != "1" {
		return
	}
	scenario := os.Getenv("NYADB_AGENT_BRIDGE_SCENARIO")
	var output sync.Mutex
	write := func(value any) { output.Lock(); defer output.Unlock(); _ = json.NewEncoder(os.Stdout).Encode(value) }
	write(map[string]any{"ready": true})
	var mu sync.Mutex
	started, canceled := map[string]int{}, map[string]int{}
	finished := map[string]chan struct{}{}
	opens := 0
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024), 2<<20)
	for scanner.Scan() {
		var request struct {
			ID     uint64                     `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(8)
		}
		go func() {
			result := any(map[string]bool{"ok": true})
			session, _ := rawString(request.Params["agentSessionId"])
			switch request.Method {
			case "execute_query_page", "fetch_query_page":
				if strings.HasPrefix(scenario, "query_cursor") {
					id := session + "-cursor"
					more := request.Method == "execute_query_page"
					row := 1
					if !more {
						if string(request.Params["sessionId"]) != fmt.Sprintf("%q", id) {
							os.Exit(10)
						}
						row = 3
					}
					if !more && scenario == "query_cursor_wrong_id" {
						id = "unrelated-original-runtime-cursor"
					}
					result = map[string]any{"columns": []string{"exact"}, "column_types": []string{"BIGINT"}, "rows": [][]any{{row}}, "affected_rows": 0, "execution_time_ms": 1, "truncated": false, "session_id": id, "has_more": more}
				}
			case "close_query_session":
				result = true
			case "open_session":
				mu.Lock()
				opens++
				mu.Unlock()
				if scenario == "query_pool" && string(request.Params["password"]) != `"original"` && request.Params["password"] != nil {
					os.Exit(9)
				}
			case "pool_probe":
				mu.Lock()
				result = map[string]int{"opens": opens}
				mu.Unlock()
			case "execute_query":
				if scenario == "query_pool" {
					statement, _ := rawString(request.Params["sql"])
					result = map[string]any{"columns": []string{"exact"}, "column_types": []string{"BIGINT"}, "rows": [][]any{{json.Number("9007199254740993")}}, "affected_rows": 0, "execution_time_ms": 1, "truncated": false}
					if statement == "SELECT binary" {
						result = map[string]any{"columns": []string{"binary"}, "column_types": []string{"BLOB"}, "rows": [][]any{{"0x" + strings.Repeat("00", 70000)}, {"0xff"}}, "affected_rows": 0, "execution_time_ms": 1, "truncated": false}
					}
				}
			case "handshake":
				if scenario == "legacy" {
					result = Handshake{1, 1, []string{"connect"}}
				} else {
					result = Handshake{2, 2, []string{"connect", "query", "multi_session", "structured_error_v1"}}
				}
			case "hold":
				mu.Lock()
				started[session]++
				ch := make(chan struct{})
				finished[session] = ch
				mu.Unlock()
				<-ch
				result = map[string]any{"affected_rows": 1, "completed": "original"}
			case "probe":
				var target string
				_ = json.Unmarshal(request.Params["target"], &target)
				mu.Lock()
				result = map[string]int{"started": started[target], "canceled": canceled[target]}
				mu.Unlock()
			case "finish":
				var target string
				_ = json.Unmarshal(request.Params["target"], &target)
				mu.Lock()
				ch := finished[target]
				delete(finished, target)
				mu.Unlock()
				if ch != nil {
					close(ch)
				}
			case "cancel_session":
				mu.Lock()
				canceled[session]++
				mu.Unlock()
				if scenario == "stalled_cancel" {
					select {}
				}
			case "echo":
				result = request.Params
			case "bad_id":
				write(map[string]any{"jsonrpc": "2.0", "id": request.ID + 1000, "result": result})
				return
			case "duplicate_id":
				output.Lock()
				fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":%d,\"id\":%d,\"result\":{}}\n", request.ID, request.ID)
				output.Unlock()
				return
			case "oversized":
				output.Lock()
				fmt.Fprintln(os.Stdout, strings.Repeat("x", 4096))
				output.Unlock()
				return
			case "exit":
				os.Exit(3)
			case "shutdown":
				write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
				os.Exit(0)
			case "native_error":
				message, _ := rawString(request.Params["secret"])
				err := map[string]any{"code": -1, "message": message, "data": map[string]any{"contractVersion": 1, "category": "sql", "retryable": true, "sessionDisposition": "keep", "stage": "execute", "operationOutcome": "unknown", "sqlState": "42000", "vendorCode": 17, "agentSessionId": session}}
				write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": err})
				return
			}
			write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		}()
	}
	os.Exit(0)
}

func helperClient(t *testing.T, scenario string, options Options) *Client {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Start(ctx, LaunchSpec{Executable: path, Args: []string{"-test.run=^TestAgentBridgeProcess$"}, Environment: []string{"NYADB_AGENT_BRIDGE_HELPER=1", "NYADB_AGENT_BRIDGE_SCENARIO=" + scenario}}, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func helperSession(t *testing.T, client *Client) *Session {
	t.Helper()
	s, r, err := client.OpenSession(context.Background(), map[string]string{"database": "fixture"})
	if err != nil || !r.ExecutorAcknowledged {
		t.Fatalf("session open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func waitProbe(t *testing.T, s *Session, target, field string, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		r, err := s.Call(ctx, "probe", map[string]string{"target": target})
		if err != nil {
			t.Fatal(err)
		}
		var probe map[string]int
		if json.Unmarshal(r.Result, &probe) != nil {
			t.Fatal("invalid probe")
		}
		if probe[field] == want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("native condition was not observed")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestCancellationRetainsOriginalExecutorReceiptAndOtherRuntime(t *testing.T) {
	c := helperClient(t, "", Options{})
	s, other := helperSession(t, c), helperSession(t, c)
	replacement := helperClient(t, "", Options{})
	newSession := helperSession(t, replacement)
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		receipt Receipt
		err     error
	}
	completed := make(chan outcome, 1)
	go func() { r, err := s.Call(ctx, "hold", nil); completed <- outcome{r, err} }()
	waitProbe(t, other, s.ID(), "started", 1)
	cancel()
	waitProbe(t, other, s.ID(), "canceled", 1)
	select {
	case <-completed:
		t.Fatal("cancel acknowledgment replaced the original executor receipt")
	default:
	}
	if _, err := newSession.Call(context.Background(), "echo", map[string]string{"value": "replacement"}); err != nil {
		t.Fatal("original cancel reached another runtime")
	}
	if _, err := other.Call(context.Background(), "finish", map[string]string{"target": s.ID()}); err != nil {
		t.Fatal(err)
	}
	result := <-completed
	if !errors.Is(result.err, context.Canceled) || !result.receipt.CancellationRequested || !result.receipt.ExecutorAcknowledged || !strings.Contains(string(result.receipt.Result), "original") {
		t.Fatalf("original completion facts lost: %v", result.err)
	}
	if !other.Alive() || !newSession.Alive() {
		t.Fatal("unrelated session revoked")
	}
}

func TestQueuedCancellationNeverCancelsActiveSessionRequest(t *testing.T) {
	c := helperClient(t, "", Options{})
	s, probe := helperSession(t, c), helperSession(t, c)
	active := make(chan error, 1)
	go func() { _, err := s.Call(context.Background(), "hold", nil); active <- err }()
	waitProbe(t, probe, s.ID(), "started", 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := s.Call(ctx, "echo", map[string]int{"queued": 1})
	if !errors.Is(err, context.Canceled) || r.Dispatched {
		t.Fatal("queued cancellation dispatched")
	}
	waitProbe(t, probe, s.ID(), "canceled", 0)
	if _, err := probe.Call(context.Background(), "finish", map[string]string{"target": s.ID()}); err != nil {
		t.Fatal(err)
	}
	if err := <-active; err != nil {
		t.Fatal(err)
	}
}

func TestProtocolFaultsRetireOriginalProcessWithoutReplay(t *testing.T) {
	for _, method := range []string{"bad_id", "duplicate_id", "oversized", "exit"} {
		t.Run(method, func(t *testing.T) {
			c := helperClient(t, "", Options{MaxFrameBytes: 2048})
			s := helperSession(t, c)
			r, err := s.Call(context.Background(), method, nil)
			if err == nil || !r.Dispatched || r.ExecutorAcknowledged || !r.ProcessExitConfirmed || c.Alive() {
				t.Fatal("invalid terminal receipt")
			}
			if r, err := s.Call(context.Background(), "echo", nil); !errors.Is(err, ErrClosed) || r.Dispatched {
				t.Fatal("closed session replayed")
			}
			select {
			case <-c.Exited():
			default:
				t.Fatal("original process exit not confirmed")
			}
		})
	}
}

func TestCancelTimeoutKeepsUnknownOutcomeAndWaitsForProcess(t *testing.T) {
	c := helperClient(t, "stalled_cancel", Options{CancelWait: 100 * time.Millisecond})
	s, probe := helperSession(t, c), helperSession(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	completed := make(chan Receipt, 1)
	go func() { r, _ := s.Call(ctx, "hold", nil); completed <- r }()
	waitProbe(t, probe, s.ID(), "started", 1)
	cancel()
	r := <-completed
	if !r.Dispatched || !r.CancellationRequested || r.ExecutorAcknowledged || !r.ProcessExitConfirmed {
		t.Fatal("unknown write became a canceled/confirmed executor receipt")
	}
}

func TestSessionOwnershipSnapshotsSecretsAndIdempotentClose(t *testing.T) {
	c := helperClient(t, "", Options{})
	s := helperSession(t, c)
	for _, params := range []any{map[string]string{"agentSessionId": "foreign"}, json.RawMessage(`{"x":1,"x":2}`)} {
		if r, err := s.Call(context.Background(), "echo", params); !errors.Is(err, ErrRequest) || r.Dispatched {
			t.Fatal("foreign or repeated parameter admitted")
		}
	}
	if r, err := s.Call(context.Background(), "close_session", nil); !errors.Is(err, ErrRequest) || r.Dispatched {
		t.Fatal("caller supplied lifecycle control")
	}
	secret := "credential-must-not-be-an-error-message"
	r, err := s.Call(context.Background(), "native_error", map[string]string{"secret": secret})
	if err == nil || strings.Contains(err.Error(), secret) || r.RemoteError == nil || r.RemoteError.OperationOutcome != "unknown" || !r.RemoteError.Retryable || !s.Alive() {
		t.Fatal("native error classification or sanitization lost")
	}
	first, err := s.Close()
	if err != nil || !first.ExecutorAcknowledged {
		t.Fatal(err)
	}
	second, err := s.Close()
	if err != nil || string(first.Result) != string(second.Result) {
		t.Fatal("close receipt changed")
	}
	if r, err := s.Call(context.Background(), "echo", nil); !errors.Is(err, ErrClosed) || r.Dispatched {
		t.Fatal("closed session dispatched")
	}
	capabilities := c.Handshake()
	capabilities.Capabilities[0] = "mutated"
	if c.Handshake().Supports("mutated") {
		t.Fatal("handshake cache modified")
	}
}

func TestLaunchArtifactAndProtocolAdmission(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), LaunchSpec{Executable: path, Artifacts: []Artifact{{path, strings.Repeat("0", 64)}}}, Options{}); !errors.Is(err, ErrArtifact) {
		t.Fatal("changed artifact admitted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = Start(ctx, LaunchSpec{Executable: path, Args: []string{"-test.run=^TestAgentBridgeProcess$"}, Environment: []string{"NYADB_AGENT_BRIDGE_HELPER=1", "NYADB_AGENT_BRIDGE_SCENARIO=legacy"}}, Options{})
	if !errors.Is(err, ErrVersion) {
		t.Fatalf("unsupported shared protocol admitted: %v", err)
	}
}
