package agentbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This subprocess implements an independent sequential v1 server. Its external
// barriers observe actual dispatch without inventing a concurrent probe RPC.
func TestSingleSessionProcess(t *testing.T) {
	if os.Getenv("NYADB_SINGLE_SESSION_HELPER") != "1" {
		return
	}
	dir, scenario := os.Getenv("NYADB_SINGLE_SESSION_DIRECTORY"), os.Getenv("NYADB_SINGLE_SESSION_SCENARIO")
	encoder := json.NewEncoder(os.Stdout)
	_ = encoder.Encode(map[string]bool{"ready": true})
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024), 2<<20)
	for scanner.Scan() {
		var request struct {
			ID     uint64                     `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.Params["agentSessionId"] != nil {
			os.Exit(8)
		}
		journal, err := os.OpenFile(filepath.Join(dir, "requests.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			os.Exit(9)
		}
		_, _ = journal.Write(append(append([]byte(nil), scanner.Bytes()...), '\n'))
		_ = journal.Close()
		result := any(map[string]bool{"ok": true})
		switch request.Method {
		case "handshake":
			h := Handshake{1, 1, []string{"connect", "kv", "test_connection"}}
			switch scenario {
			case "v2":
				h = Handshake{2, 2, []string{"connect", "multi_session"}}
			case "multi":
				h.Capabilities = append(h.Capabilities, "multi_session")
			case "missing_connect":
				h.Capabilities = []string{"kv"}
			case "duplicate":
				h.Capabilities = append(h.Capabilities, "kv")
			}
			result = h
		case "connect":
			if scenario == "invalid_connect" {
				result = map[string]bool{"ok": false}
			}
			if scenario == "failed_connect" {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -1, "message": "original connect failed"}})
				continue
			}
		case "hold":
			if os.WriteFile(filepath.Join(dir, "started"), []byte("original executor"), 0600) != nil {
				os.Exit(10)
			}
			for {
				if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
					break
				}
				time.Sleep(time.Millisecond)
			}
			result = map[string]any{"completed": "original", "integer": json.Number("9007199254740993")}
		case "test_connection":
			if scenario == "invalid_test" {
				result = map[string]bool{"ok": false}
			}
		case "echo", "ack_exit":
			result = map[string]any{"integer": json.Number("9007199254740993"), "decimal": json.Number("123456789.123456789123456789"), "params": request.Params}
		case "native_error":
			message, _ := rawString(request.Params["secret"])
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -1, "message": message}})
			continue
		case "oversized":
			fmt.Fprintln(os.Stdout, strings.Repeat("x", 4096))
			continue
		case "bad_id":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID + 1000, "result": result})
			continue
		case "exit":
			os.Exit(3)
		case "shutdown":
			_ = os.WriteFile(filepath.Join(dir, "shutdown"), []byte("original shutdown"), 0600)
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		if request.Method == "shutdown" || request.Method == "ack_exit" {
			os.Exit(0)
		}
	}
	os.Exit(0)
}

func singleSpec(t *testing.T, scenario string) (LaunchSpec, string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	return LaunchSpec{Executable: executable, Args: []string{"-test.run=^TestSingleSessionProcess$"}, Environment: []string{"NYADB_SINGLE_SESSION_HELPER=1", "NYADB_SINGLE_SESSION_DIRECTORY=" + dir, "NYADB_SINGLE_SESSION_SCENARIO=" + scenario}}, dir
}

func helperSingle(t *testing.T, scenario string, options Options) (*SingleSession, string) {
	t.Helper()
	spec, dir := singleSpec(t, scenario)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := StartSingleSession(ctx, spec, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

func bindSingle(t *testing.T, s *SingleSession) {
	t.Helper()
	r, err := s.Connect(context.Background(), map[string]any{"connection": map[string]any{"password": "original stdin only", "max_retries": 0}})
	if err != nil || !r.ExecutorAcknowledged {
		t.Fatal("original connect", err)
	}
}

func awaitSingleFile(t *testing.T, dir, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("original subprocess barrier not observed", name)
		}
		time.Sleep(time.Millisecond)
	}
}

func singleMethods(t *testing.T, dir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var methods []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var r struct {
			Method string `json:"method"`
		}
		if json.Unmarshal([]byte(line), &r) != nil {
			t.Fatal("invalid original request journal")
		}
		methods = append(methods, r.Method)
	}
	return methods
}

func TestSingleSessionExplicitAdmissionAndCapturedLaunchFailure(t *testing.T) {
	for _, scenario := range []string{"v2", "multi", "missing_connect", "duplicate"} {
		t.Run(scenario, func(t *testing.T) {
			spec, _ := singleSpec(t, scenario)
			_, err := StartSingleSession(context.Background(), spec, Options{})
			want := ErrSingleVersion
			if scenario == "duplicate" {
				want = ErrProtocol
			}
			if !errors.Is(err, want) {
				t.Fatal("invalid single-session contract admitted", err)
			}
		})
	}
	spec, _ := singleSpec(t, "")
	observed := 0
	spec.Started = func(pid int) error { observed = pid; return errors.New("observer refuses original process") }
	if _, err := StartSingleSession(context.Background(), spec, Options{}); !errors.Is(err, ErrLaunch) || observed == 0 {
		t.Fatal("launch observer not enforced", err)
	}
}

func TestSingleSessionOriginalNumbersErrorsOneBindingAndGracefulClose(t *testing.T) {
	s, dir := helperSingle(t, "", Options{})
	if r, err := s.Call(context.Background(), "echo", nil); !errors.Is(err, ErrClosed) || r.Dispatched {
		t.Fatal("unbound request dispatched")
	}
	if r, err := s.TestConnection(context.Background(), nil); err != nil || !r.ExecutorAcknowledged {
		t.Fatal("native test failed", err)
	}
	bindSingle(t, s)
	if r, err := s.Connect(context.Background(), nil); !errors.Is(err, ErrClosed) || r.Dispatched {
		t.Fatal("process rebound")
	}
	if r, err := s.TestConnection(context.Background(), nil); !errors.Is(err, ErrClosed) || r.Dispatched {
		t.Fatal("bound process tested another connection")
	}
	for _, method := range []string{"connect", "disconnect", "open_session", "close_session", "cancel_session", "handshake", "shutdown"} {
		if r, err := s.Call(context.Background(), method, nil); !errors.Is(err, ErrRequest) || r.Dispatched {
			t.Fatal("lifecycle control dispatched", method)
		}
	}
	for _, params := range []any{json.RawMessage(`{"x":1,"x":2}`), json.RawMessage(`{"agentSessionId":"foreign"}`)} {
		if r, err := s.Call(context.Background(), "echo", params); !errors.Is(err, ErrRequest) || r.Dispatched {
			t.Fatal("invalid ownership dispatched")
		}
	}
	r, err := s.Call(context.Background(), "echo", json.RawMessage(`{"bytes":"AP+A","integer":9007199254740993}`))
	if err != nil || !strings.Contains(string(r.Result), "123456789.123456789123456789") || !strings.Contains(string(r.Result), "9007199254740993") || !json.Valid(r.OriginalResponse()) {
		t.Fatal("original exact values lost", err)
	}
	secret := "native-error-private-secret"
	r, err = s.Call(context.Background(), "native_error", map[string]string{"secret": secret})
	public, _ := json.Marshal(r)
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(string(public), secret) || !r.ExecutorAcknowledged || r.RemoteError == nil || r.RemoteError.OperationOutcome != "" || !strings.Contains(string(r.OriginalResponse()), secret) {
		t.Fatal("generic error evidence or sanitization lost")
	}
	copy := r.OriginalResponse()
	copy[0] = '!'
	if !json.Valid(r.OriginalResponse()) {
		t.Fatal("original receipt mutated through accessor")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	awaitSingleFile(t, dir, "shutdown")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Exited():
	default:
		t.Fatal("close returned before original process exit")
	}
	if r, err := s.Call(context.Background(), "echo", nil); !errors.Is(err, ErrClosed) || r.Dispatched {
		t.Fatal("closed process dispatched")
	}
	if got := strings.Join(singleMethods(t, dir), ","); got != "handshake,test_connection,connect,echo,native_error,shutdown" {
		t.Fatal("unexpected original requests", got)
	}
}

type singleOutcome struct {
	receipt Receipt
	err     error
}

func startSingleHold(t *testing.T, s *SingleSession, dir string, ctx context.Context) <-chan singleOutcome {
	t.Helper()
	done := make(chan singleOutcome, 1)
	go func() { r, err := s.Call(ctx, "hold", nil); done <- singleOutcome{r, err} }()
	awaitSingleFile(t, dir, "started")
	return done
}

func TestSingleSessionQueuedCancellationNeverStopsActiveExecutor(t *testing.T) {
	s, dir := helperSingle(t, "", Options{})
	bindSingle(t, s)
	done := startSingleHold(t, s, dir, context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r, err := s.Call(ctx, "echo", map[string]string{"queued": "must not dispatch"})
	if !errors.Is(err, context.DeadlineExceeded) || r.Dispatched || !s.Alive() {
		t.Fatal("queued cancel reached active process", err)
	}
	select {
	case <-done:
		t.Fatal("active executor settled before its original response")
	default:
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), []byte("finish"), 0600); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if out.err != nil || !out.receipt.ExecutorAcknowledged || !strings.Contains(string(out.receipt.Result), "original") {
		t.Fatal("active receipt lost", out.err)
	}
	if got := strings.Join(singleMethods(t, dir), ","); got != "handshake,connect,hold" {
		t.Fatal("queued operation dispatched", got)
	}
}

func TestSingleSessionCancellationWaitsOriginalProcessAndNeverReplays(t *testing.T) {
	s, dir := helperSingle(t, "", Options{})
	bindSingle(t, s)
	other, _ := helperSingle(t, "", Options{})
	bindSingle(t, other)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := startSingleHold(t, s, dir, ctx)
	cancel()
	out := <-done
	r := out.receipt
	if !errors.Is(out.err, context.Canceled) || !r.Dispatched || !r.CancellationRequested || r.ExecutorAcknowledged || !r.ProcessExitConfirmed || len(r.OriginalResponse()) != 0 {
		t.Fatal("unacknowledged operation became a known write", out.err)
	}
	select {
	case <-s.Exited():
	default:
		t.Fatal("cancellation returned before original exit")
	}
	if _, err := other.Call(context.Background(), "echo", nil); err != nil {
		t.Fatal("original cancellation reached independent process", err)
	}
	if r, err := s.Call(context.Background(), "echo", nil); !errors.Is(err, ErrClosed) || r.Dispatched {
		t.Fatal("canceled process restarted")
	}
	if got := strings.Join(singleMethods(t, dir), ","); got != "handshake,connect,hold" {
		t.Fatal("original operation replayed or native cancel invented", got)
	}
}

func TestSingleSessionCloseActiveAndFailedConnectKeepOriginalOwnership(t *testing.T) {
	s, dir := helperSingle(t, "", Options{})
	bindSingle(t, s)
	done := startSingleHold(t, s, dir, context.Background())
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if !out.receipt.Dispatched || out.receipt.ExecutorAcknowledged || !out.receipt.ProcessExitConfirmed || out.err == nil {
		t.Fatal("active close invented an acknowledgment")
	}
	failed, failedDir := helperSingle(t, "failed_connect", Options{})
	r, err := failed.Connect(context.Background(), nil)
	if err == nil || !r.ExecutorAcknowledged || !r.Dispatched {
		t.Fatal("original failed connect receipt lost")
	}
	if r, err := failed.Connect(context.Background(), nil); !errors.Is(err, ErrClosed) || r.Dispatched {
		t.Fatal("failed connect rebound original process")
	}
	if got := strings.Join(singleMethods(t, failedDir), ","); got != "handshake,connect" {
		t.Fatal("failed connect retried", got)
	}
}

func TestSingleSessionRejectsInvalidNativeConnectionAcknowledgments(t *testing.T) {
	for _, scenario := range []string{"invalid_connect", "invalid_test"} {
		t.Run(scenario, func(t *testing.T) {
			s, _ := helperSingle(t, scenario, Options{})
			var r Receipt
			var err error
			if scenario == "invalid_connect" {
				r, err = s.Connect(context.Background(), nil)
			} else {
				r, err = s.TestConnection(context.Background(), nil)
			}
			if !errors.Is(err, ErrProtocol) || !r.ExecutorAcknowledged || !r.ProcessExitConfirmed || s.Alive() {
				t.Fatal("invalid native acknowledgment published as connected", err)
			}
		})
	}
}

func TestSingleSessionTerminalFaultsAndAcknowledgedExitKeepOriginalReceipt(t *testing.T) {
	for _, method := range []string{"bad_id", "oversized", "exit", "ack_exit"} {
		t.Run(method, func(t *testing.T) {
			s, _ := helperSingle(t, "", Options{MaxFrameBytes: 2048})
			bindSingle(t, s)
			r, err := s.Call(context.Background(), method, nil)
			if method == "ack_exit" {
				if err != nil || !r.ExecutorAcknowledged || !strings.Contains(string(r.OriginalResponse()), "9007199254740993") {
					t.Fatal("delivered original acknowledgment lost to process exit", err)
				}
			} else if err == nil || !r.Dispatched || r.ExecutorAcknowledged || !r.ProcessExitConfirmed {
				t.Fatal("terminal operation outcome invented", err)
			}
		})
	}
}
