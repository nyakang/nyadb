package connections

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"
)

// Opt-in against an isolated mysql:8.4 container with account nyadb_tab,
// an empty fixture-only password, and schema nyadb_tab_it. Never run on a
// shared database: the test changes only its own session's wait_timeout.
func TestMySQLIdleTimeoutAndKeepAliveIntegration(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_TAB_IT") != "1" {
		t.Skip("requires explicit loopback disposable MySQL fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_MYSQL_TAB_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("missing valid loopback fixture port")
	}
	for _, tc := range []struct {
		name    string
		enabled bool
		wait    int
		hold    time.Duration
	}{
		{"keepalive-disabled-expiry", false, 2, 3500 * time.Millisecond},
		{"keepalive-enabled-preserves-session", true, 3, 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMySQLSessionManager()
			t.Cleanup(func() { _ = m.Close() })
			adv := DefaultMySQLAdvanced()
			adv.KeepAlive = tc.enabled
			adv.KeepAliveSeconds = 1
			const profile int64 = 2026102801
			ctx, stop := context.WithTimeout(context.Background(), 25*time.Second)
			defer stop()
			if err := m.Connect(ctx, ActiveSettings{
				ProfileID: profile, Host: "127.0.0.1", Port: uint16(port),
				Username: "nyadb_tab", MySQLAdvanced: adv,
			}, ""); err != nil {
				t.Fatal(err)
			}
			run := func(tab uint64, stmt string) MySQLSQLResult {
				t.Helper()
				result, err := m.ExecuteSQLTab(ctx, profile, tab, "nyadb_tab_it", stmt)
				if err != nil {
					t.Fatalf("%s: %v", stmt, err)
				}
				return result
			}
			a := run(1, "SELECT CONNECTION_ID()")
			run(1, "SET SESSION wait_timeout = "+strconv.Itoa(tc.wait))
			run(1, "SET @nyadb_ka_marker = 71")
			time.Sleep(tc.hold)
			got, err := m.ExecuteSQLTab(ctx, profile, 1, "nyadb_tab_it", "SELECT CONNECTION_ID(), @nyadb_ka_marker")
			if tc.enabled {
				if err != nil || len(got.Rows) != 1 || got.Rows[0][0] != a.Rows[0][0] || got.Rows[0][1] != "71" {
					t.Fatalf("heartbeat did not retain physical session and variable: result=%+v err=%v", got.Rows, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("server wait_timeout expired but SQL tab silently executed: %+v", got.Rows)
			}
			if _, nextErr := m.ExecuteSQLTab(ctx, profile, 1, "", "SELECT 1"); !errors.Is(nextErr, ErrMySQLSQLSessionLost) {
				t.Fatalf("expired tab session not marked lost: first=%T %v, second=%T %v", err, err, nextErr, nextErr)
			}
			b := run(2, "SELECT CONNECTION_ID(), @nyadb_ka_marker")
			if b.Rows[0][0] == a.Rows[0][0] || b.Rows[0][1] != "NULL" {
				t.Fatalf("new tab inherited expired session state: old=%v new=%v", a.Rows, b.Rows)
			}
		})
	}
}
