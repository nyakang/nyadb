package connections

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"
)

// Opt-in isolated MySQL fixture: verifies that disconnect rolls back an
// unfinished transaction and a fresh tab cannot inherit its SQL state.
// This test never commits fixture changes.
func TestMySQLDisconnectDropsTransactionStateIntegration(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_TAB_IT") != "1" {
		t.Skip("requires disposable MySQL loopback fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_MYSQL_TAB_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("invalid fixture port")
	}
	m := NewMySQLSessionManager()
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	const profile int64 = 2026102802
	settings := ActiveSettings{
		ProfileID: profile, Host: "127.0.0.1", Port: uint16(port),
		Username: "nyadb_tab", MySQLAdvanced: DefaultMySQLAdvanced(),
	}
	if err := m.Connect(ctx, settings, ""); err != nil {
		t.Fatal(err)
	}
	tokens := make(map[uint64]MySQLSQLTabToken)
	run := func(tab uint64, statement string) MySQLSQLResult {
		t.Helper()
		token, ok := tokens[tab]
		if !ok {
			var err error
			token, err = m.ReserveSQLTabToken(profile, tab)
			if err != nil {
				t.Fatal(err)
			}
			tokens[tab] = token
		}
		result, err := m.ExecuteSQLTabToken(ctx, token, "nyadb_tab_it", statement)
		if err != nil {
			t.Fatalf("tab %d %q: %v", tab, statement, err)
		}
		return result
	}
	before := run(1, "SELECT CONNECTION_ID()")
	run(1, "START TRANSACTION")
	run(1, "SET @nyadb_tx_marker = 91")
	run(1, "UPDATE nyadb_tab_it.tx_probe SET note='pending_disconnect' WHERE id=1")
	a := run(1, "SELECT note FROM nyadb_tab_it.tx_probe WHERE id=1")
	b := run(2, "SELECT note FROM nyadb_tab_it.tx_probe WHERE id=1")
	if a.Rows[0][0] != "pending_disconnect" || b.Rows[0][0] != "before" {
		t.Fatalf("fixture isolation was not established: A=%v B=%v", a.Rows, b.Rows)
	}
	if err := m.Disconnect(profile); err != nil {
		t.Fatal(err)
	}
	if err := m.Connect(ctx, settings, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ExecuteSQLTabToken(ctx, tokens[1], "nyadb_tab_it", "SELECT 1"); !errors.Is(err, ErrMySQLSQLSessionLost) {
		t.Fatalf("old token revived after profile reconnect: %v", err)
	}
	fresh := run(3, "SELECT CONNECTION_ID(), @nyadb_tx_marker")
	row := run(3, "SELECT note FROM nyadb_tab_it.tx_probe WHERE id=1")
	if fresh.Rows[0][0] == before.Rows[0][0] || fresh.Rows[0][1] != "NULL" {
		t.Fatalf("new session silently inherited disconnected state: old=%v new=%v", before.Rows, fresh.Rows)
	}
	if row.Rows[0][0] != "before" {
		t.Fatalf("uncommitted write survived disconnect: %+v", row.Rows)
	}
}
