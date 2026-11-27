package jdbcplugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

type prestoFixture struct{ Endpoint, Java, Plugin, Driver, Directory string }

func nativeFixture(t *testing.T) prestoFixture {
	t.Helper()
	if os.Getenv("NYADB_PRESTOSQL_PLUGIN_IT") != "1" {
		t.Skip("explicit original PrestoSQL plugin fixture required")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_PRESTOSQL_PLUGIN_FIXTURE"))
	if err != nil {
		t.Fatal(err)
	}
	var f prestoFixture
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("invalid original fixture")
	}
	u, err := url.Parse(f.Endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		t.Fatal("fixture must use owned loopback endpoint")
	}
	return f
}
func nativeClient(t *testing.T, f prestoFixture) *Client {
	t.Helper()
	var artifacts []agentbridge.Artifact
	for _, p := range []string{f.Java, f.Plugin, f.Driver} {
		file, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.New()
		_, err = io.Copy(h, file)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, agentbridge.Artifact{Path: p, SHA256: hex.EncodeToString(h.Sum(nil))})
	}
	endpoint, _ := url.Parse(f.Endpoint)
	spec := agentbridge.LaunchSpec{Executable: f.Java, Args: []string{"-jar", f.Plugin}, Artifacts: artifacts, Started: func(pid int) error {
		file, err := os.OpenFile(filepath.Join(f.Directory, "go-processes.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		return json.NewEncoder(file).Encode(map[string]any{"test": t.Name(), "pid": pid, "executable": f.Java})
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Start(ctx, spec, map[string]any{"connection_string": "jdbc:presto://" + endpoint.Host + "/memory/default", "jdbc_driver_class": "io.prestosql.jdbc.PrestoDriver", "jdbc_driver_paths": []string{f.Driver}, "username": "nyadb_native_go"}, Options{CancelWait: 10 * time.Second})
	if err != nil {
		t.Fatal("original plugin connect", err)
	}
	t.Cleanup(func() {
		_ = c.Close()
		select {
		case <-c.processDone:
		default:
			t.Error("original JVM not settled")
		}
	})
	return c
}
func nativeCall(t *testing.T, c *Client, method string, params any) Receipt {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := c.Call(ctx, method, params)
	if err != nil || !r.Dispatched || !r.ExecutorAcknowledged || len(r.OriginalResponse()) == 0 {
		t.Fatal("original executor receipt", method, err, r)
	}
	return r
}
func nativeSQL(t *testing.T, c *Client, sql string) Receipt {
	t.Helper()
	return nativeCall(t, c, "executeQuery", map[string]any{"sql": sql, "database": "memory", "schema": "default", "maxRows": 100})
}
func nativeEvidence(t *testing.T, f prestoFixture, name string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.Directory, name), raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func nativeRows(t *testing.T, r Receipt) [][]json.RawMessage {
	t.Helper()
	var result struct {
		Rows [][]json.RawMessage `json:"rows"`
	}
	if json.Unmarshal(r.Result, &result) != nil {
		t.Fatal("native rows malformed")
	}
	return result.Rows
}
func TestPrestoSQLOriginalPluginTypedReceiptsAndOwnership(t *testing.T) {
	f := nativeFixture(t)
	c := nativeClient(t, f)
	info := nativeCall(t, c, "connectionInfo", nil)
	if !bytes.Contains(info.Result, []byte(`"productVersion":"350"`)) {
		t.Fatal("not actual PrestoSQL350", string(info.Result))
	}
	table := `memory.default."go_%猫"`
	nativeSQL(t, c, "CREATE TABLE "+table+" (id BIGINT, amount DECIMAL(38,20), payload VARBINARY, note VARCHAR)")
	inserted := nativeSQL(t, c, "INSERT INTO "+table+" VALUES (9223372036854775807,DECIMAL '9007199254740993.12345678901234567890',X'00ff80',NULL)")
	value := nativeSQL(t, c, "SELECT id,amount,payload,note FROM "+table)
	rows := nativeRows(t, value)
	if len(rows) != 1 || string(rows[0][0]) != "9223372036854775807" || string(rows[0][1]) != "9007199254740993.1234567890123456789" || string(rows[0][2]) != `"0x00ff80"` || string(rows[0][3]) != "null" {
		t.Fatal("native numeric lexemes changed", string(value.Result))
	}
	cast := nativeSQL(t, c, "SELECT CAST(id AS VARCHAR),CAST(amount AS VARCHAR),to_hex(payload) FROM "+table)
	if !bytes.Contains(cast.Result, []byte(`9007199254740993.12345678901234567890`)) {
		t.Fatal("server decimal cross-check differs")
	}
	for _, params := range []map[string]any{{"connection": map[string]string{"username": "other"}}, {"jdbcSessionId": "other"}, {"cancelToken": "other"}} {
		if r, err := c.Call(context.Background(), "executeQuery", params); !errors.Is(err, ErrRequest) || r.Dispatched {
			t.Fatal("caller replaced original ownership")
		}
	}
	// A queued cancellation must not dispatch or disturb the original connection.
	<-c.gate
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, err := c.Call(ctx, "executeQuery", map[string]string{"sql": "DROP TABLE " + table}); !errors.Is(err, context.Canceled) || r.Dispatched {
		t.Fatal("pre-cancel dispatched", r, err)
	}
	c.gate <- struct{}{}
	retained := value.Clone()
	raw := value.OriginalResponse()
	raw[0] = 'x'
	value.Result[0] = 'x'
	if retained.OriginalResponse()[0] != '{' || retained.Result[0] != '{' {
		t.Fatal("native receipt aliases caller bytes")
	}
	refused, err := c.Call(context.Background(), "executeQuery", map[string]any{"sql": "UPDATE " + table + " SET note='secret_value_for_error'", "database": "memory", "schema": "default"})
	if !errors.Is(err, ErrRemote) || !refused.ExecutorAcknowledged || strings.Contains(err.Error(), "secret_value") {
		t.Fatal("native refusal was exposed or lost", err)
	}
	current := nativeSQL(t, c, "SELECT id,amount,payload,note FROM "+table)
	if !reflect.DeepEqual(rows, nativeRows(t, current)) {
		t.Fatal("refused UPDATE changed original row")
	}
	nativeSQL(t, c, "DROP TABLE "+table)
	if err = c.Close(); err != nil {
		t.Fatal("original graceful close", err)
	}
	if r, e := c.Call(context.Background(), "executeQuery", map[string]string{"sql": "SELECT 1"}); e == nil || r.Dispatched {
		t.Fatal("closed original process recovered implicitly")
	}
	nativeEvidence(t, f, "go-typed.json", map[string]any{"protocol": c.ProtocolReceipt().OriginalResponse(), "info": info.OriginalResponse(), "insert": inserted.OriginalResponse(), "typed": retained.OriginalResponse(), "cast": cast.OriginalResponse(), "refusal": refused.OriginalResponse(), "owned_process_exited": true, "pre_canceled_unsent": true, "foreign_identity_rejected": true})
}

// Independent native HTTP observations only establish when the original query
// is executing. They never substitute for its JDBC completion acknowledgement.
func nativeHTTP(t *testing.T, f prestoFixture, sql string) [][]json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.Endpoint+"/v1/statement", strings.NewReader(sql))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Presto-User", "nyadb_independent")
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var rows [][]json.RawMessage
	for range 100 {
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		response.Body.Close()
		if err != nil || response.StatusCode != 200 {
			t.Fatal("native HTTP response", response.StatusCode, err)
		}
		var result struct {
			Data    [][]json.RawMessage `json:"data"`
			NextURI string              `json:"nextUri"`
			Error   json.RawMessage     `json:"error"`
		}
		if json.Unmarshal(raw, &result) != nil || len(result.Error) > 0 {
			t.Fatal("native HTTP query failed", string(raw))
		}
		rows = append(rows, result.Data...)
		if result.NextURI == "" {
			return rows
		}
		next, e := url.Parse(result.NextURI)
		origin, _ := url.Parse(f.Endpoint)
		if e != nil || next.Scheme != origin.Scheme || next.Host != origin.Host {
			t.Fatal("native page escaped owned server")
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, result.NextURI, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Presto-User", "nyadb_independent")
	}
	t.Fatal("native HTTP pagination did not finish")
	return nil
}
func nativeRunning(t *testing.T, f prestoFixture, sql string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		rows := nativeHTTP(t, f, "SELECT state FROM system.runtime.queries WHERE query='"+strings.ReplaceAll(sql, "'", "''")+"'")
		for _, row := range rows {
			if len(row) == 1 && string(row[0]) == `"RUNNING"` {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("original SQL never reached native RUNNING")
}
func TestPrestoSQLOriginalPluginCancellationCloseAndExplicitRecovery(t *testing.T) {
	f := nativeFixture(t)
	for _, mode := range []string{"cancel", "close", "lost"} {
		t.Run(mode, func(t *testing.T) {
			c := nativeClient(t, f)
			sql := "SELECT sum(a.orderkey * b.orderkey) FROM tpch.sf100.lineitem a CROSS JOIN tpch.sf100.lineitem b /* " + mode + " */"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan response, 1)
			go func() {
				r, e := c.Call(ctx, "executeQuery", map[string]any{"sql": sql, "maxRows": 10})
				done <- response{receipt: r, err: e}
			}()
			nativeRunning(t, f, sql)
			closed := make(chan error, 1)
			switch mode {
			case "cancel":
				cancel()
			case "close":
				go func() { closed <- c.Close() }()
			case "lost":
				c.owner.kill(c.cmd)
			}
			var completed response
			select {
			case completed = <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("original executor never settled")
			}
			r := completed.receipt
			if !r.Dispatched || completed.err == nil {
				t.Fatal("original execution not retained", r, completed.err)
			}
			if mode == "lost" {
				if r.ExecutorAcknowledged || !r.ProcessExitConfirmed || len(r.OriginalResponse()) != 0 {
					t.Fatal("process loss fabricated acknowledgement", r)
				}
				if next, e := c.Call(context.Background(), "executeQuery", map[string]string{"sql": "SELECT 1"}); e == nil || next.Dispatched {
					t.Fatal("dead process silently recovered")
				}
			} else {
				if !r.CancellationRequested || !r.ExecutorAcknowledged || r.ProcessExitConfirmed || len(r.CancellationResponse()) == 0 {
					t.Fatal("cancellation skipped original acknowledgement", r, completed.err)
				}
				if mode == "close" {
					select {
					case e := <-closed:
						if e != nil {
							t.Fatal(e)
						}
					case <-time.After(15 * time.Second):
						t.Fatal("close did not settle")
					}
				} else {
					nativeSQL(t, c, "SELECT 17")
				}
			}
			_ = c.Close()
			replacement := nativeClient(t, f)
			healthy := nativeSQL(t, replacement, "SELECT 23")
			if err := replacement.Close(); err != nil {
				t.Fatal(err)
			}
			nativeEvidence(t, f, fmt.Sprintf("go-%s.json", mode), map[string]any{"receipt": r, "original": r.OriginalResponse(), "cancel": r.CancellationResponse(), "replacement": healthy.OriginalResponse(), "old_pid": c.PID(), "new_pid": replacement.PID(), "explicit_recovery": true, "original_query_was_running": true})
		})
	}
}
