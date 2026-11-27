package driverstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
)

func TestManagedPrestoSQLOriginalPluginLeaseAndFailureReceipts(t *testing.T) {
	if os.Getenv("NYADB_PRESTOSQL_PLUGIN_IT") != "1" {
		t.Skip("explicit original PrestoSQL plugin fixture required")
	}
	var fixture struct{ Endpoint, Archive, SHA256, Directory string }
	raw, err := os.ReadFile(os.Getenv("NYADB_PRESTOSQL_PLUGIN_FIXTURE"))
	if err != nil || json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("original managed fixture unavailable")
	}
	endpoint, err := url.Parse(fixture.Endpoint)
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" {
		t.Fatal("owned loopback fixture required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	store, err := New(filepath.Join(fixture.Directory, "managed-store"))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.Open(fixture.Archive)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := store.Install(ctx, archive, fixture.SHA256)
	archive.Close()
	if err != nil {
		t.Fatal("immutable plugin installation", err)
	}
	observe := func(pid int) error {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return err
		}
		fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		if len(fields) < 20 {
			return errors.New("original JVM identity unavailable")
		}
		file, err := os.OpenFile(filepath.Join(fixture.Directory, "go-managed-processes.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		return json.NewEncoder(file).Encode(map[string]any{"pid": pid, "start_ticks": fields[19]})
	}
	lease, err := store.Acquire(ctx, manifest.Key, manifest.Version, fixture.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	connection := map[string]any{"connection_string": "jdbc:presto://" + endpoint.Host + "/memory/default", "username": "nyadb_managed_probe"}
	foreign := map[string]any{"connection_string": connection["connection_string"], "jdbc_driver_paths": []string{"/foreign.jar"}}
	if r, _, err := lease.StartJDBCPlugin(ctx, "prestosql", foreign, jdbcplugin.Options{}, observe); r != nil || !errors.Is(err, ErrPackage) || lease.active || lease.consumed {
		t.Fatal("foreign graph consumed or launched original lease", err)
	}
	runtime, startup, err := lease.StartJDBCPlugin(ctx, "prestosql", connection, jdbcplugin.Options{CancelWait: 5 * time.Second}, observe)
	if err != nil || !startup.Connect.ExecutorAcknowledged || startup.Connect.RemoteFailure {
		t.Fatal("original managed startup", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if !errors.Is(lease.Close(), ErrBusy) || !errors.Is(store.Remove(ctx, manifest.Key, manifest.Version), ErrBusy) {
		t.Fatal("active original process lost package protection")
	}
	connection["connection_string"] = "jdbc:presto://127.0.0.1:1/foreign/default"
	r, err := runtime.Client.Call(ctx, "executeQuery", map[string]any{"sql": "SELECT BIGINT '9223372036854775807', DECIMAL '9007199254740993.12345678901234567890', X'00ff80', CAST(NULL AS VARCHAR)", "maxRows": 10})
	var result struct {
		Rows [][]json.RawMessage `json:"rows"`
	}
	if err != nil || !r.ExecutorAcknowledged || json.Unmarshal(r.Result, &result) != nil || len(result.Rows) != 1 || len(result.Rows[0]) != 4 {
		t.Fatal("bound managed original query", err)
	}
	want := []string{"9223372036854775807", "9007199254740993.1234567890123456789", `"0x00ff80"`, "null"}
	for i, value := range want {
		if string(result.Rows[0][i]) != value {
			t.Fatal("managed original value changed", i, string(result.Rows[0][i]))
		}
	}
	queryReceipt := r.OriginalResponse()
	if err = runtime.Close(); err != nil || runtime.Client.Alive() {
		t.Fatal("managed original JVM not closed", err)
	}
	if r, err = runtime.Client.Call(ctx, "executeQuery", map[string]string{"sql": "SELECT 1"}); err == nil || r.Dispatched {
		t.Fatal("closed managed process recovered implicitly")
	}
	failedLease, err := store.Acquire(ctx, manifest.Key, manifest.Version, fixture.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = failedLease.Close() })
	failed, rejected, err := failedLease.StartJDBCPlugin(ctx, "prestosql", map[string]any{"connection_string": "jdbc:presto://" + endpoint.Host + "/memory/default"}, jdbcplugin.Options{CancelWait: 5 * time.Second}, observe)
	if failed != nil || !errors.Is(err, jdbcplugin.ErrRemote) || !rejected.Connect.RemoteFailure || !rejected.Connect.ExecutorAcknowledged || !rejected.ProcessExitConfirmed || len(rejected.Connect.OriginalResponse()) == 0 {
		t.Fatal("failed original JDBC connect lost acknowledgement or exit", err)
	}
	if err = store.Remove(ctx, manifest.Key, manifest.Version); err != nil {
		t.Fatal("settled original processes retained package markers", err)
	}
	evidence, err := json.MarshalIndent(map[string]any{"startup": startup, "query": queryReceipt, "failed_startup": rejected, "failed_connect_original": rejected.Connect.OriginalResponse(), "package_removed_after_original_exits": true}, "", "  ")
	if err != nil || os.WriteFile(filepath.Join(fixture.Directory, "go-managed.json"), evidence, 0600) != nil {
		t.Fatal("managed original evidence unavailable")
	}
}
