package mysql

import (
	"context"
	"database/sql"
	"encoding/pem"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/transport"
)

func TestMySQLGatewayDisposableConnectorCapabilitiesAndCloseIsolation(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_CORE_IT") != "1" {
		t.Skip("requires explicitly enabled disposable MySQL core fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_MYSQL_CORE_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("invalid loopback fixture port")
	}
	gateway, err := transport.NewGateway(transport.GatewayConfig{Targets: map[string]string{"fixture": net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}, Token: "ephemeral-fixture-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	server := httptest.NewTLSServer(gateway)
	defer server.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	a := connections.DefaultMySQLAdvanced()
	a.HTTP.Enabled, a.HTTP.URL, a.HTTP.TargetID, a.HTTP.CACertificate = true, server.URL, "fixture", ca
	settings := connections.ActiveSettings{ProfileID: 906, Host: "logical.invalid", Port: 3306, Username: "nyadb_core", Database: "nyadb_core_it", MySQLAdvanced: a}
	scope, err := transport.New(settings, transport.Options{Secrets: transport.Secrets{HTTPToken: "ephemeral-fixture-token"}})
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	metadata, pool, _, err := openMySQLPools(ctx, settings, "", &scheduledGatewayQuitDialer{Dialer: scope}, "SET @owner_init=40")
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	defer pool.Close()
	// Force concurrent connector.Connect calls. Every pool lease must capture
	// only its own physical socket and run initial SQL on that same session.
	type opened struct {
		conn *sql.Conn
		err  error
	}
	ready := make(chan opened, 2)
	for i := 0; i < 2; i++ {
		go func() { c, e := pool.Conn(ctx); ready <- opened{c, e} }()
	}
	var leases []*sql.Conn
	var openError error
	for i := 0; i < 2; i++ {
		got := <-ready
		if got.err != nil {
			openError = got.err
			continue
		}
		leases = append(leases, got.conn)
		defer got.conn.Close()
	}
	if openError != nil {
		t.Fatal(openError)
	}
	var first, second int64
	var init int
	if err = leases[0].QueryRowContext(ctx, "SELECT CONNECTION_ID(),@owner_init").Scan(&first, &init); err != nil || init != 40 {
		t.Fatal("initial SQL lost", err, init)
	}
	if err = leases[1].QueryRowContext(ctx, "SELECT CONNECTION_ID(),@owner_init").Scan(&second, &init); err != nil || init != 40 || first == second {
		t.Fatal("concurrent connections collapsed", err, first, second)
	}
	var high string
	if err = leases[1].QueryRowContext(ctx, "SELECT CAST(? AS UNSIGNED)", uint64(1<<64-1)).Scan(&high); err != nil || high != "18446744073709551615" {
		t.Fatal("named value/query context changed", err, high)
	}
	if _, err = leases[1].ExecContext(ctx, "CREATE TEMPORARY TABLE original_tmp(id bigint unsigned) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	prepared, err := leases[1].PrepareContext(ctx, "INSERT INTO original_tmp VALUES (?)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = prepared.ExecContext(ctx, uint64(1<<64-1)); err != nil {
		t.Fatal(err)
	}
	if err = prepared.Close(); err != nil {
		t.Fatal(err)
	}
	tx, err := leases[1].BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO original_tmp VALUES (?)", 2); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = leases[1].QueryRowContext(ctx, "SELECT COUNT(*) FROM original_tmp").Scan(&count); err != nil || count != 1 {
		t.Fatal("rollback lost", err, count)
	}
	if err = leases[0].Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-scope.(interface{ Done() <-chan struct{} }).Done():
		t.Fatal("normal disposal revoked sibling")
	default:
	}
	var retained int64
	if err = leases[1].QueryRowContext(ctx, "SELECT CONNECTION_ID(),@owner_init").Scan(&retained, &init); err != nil || retained != second || init != 40 {
		t.Fatal("original sibling replaced", err, retained, second)
	}
	// Metadata reuse still runs validation/reset; active SQL state stays private.
	for i := 0; i < 4; i++ {
		if err = metadata.PingContext(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err = leases[1].QueryRowContext(ctx, "SELECT COUNT(*) FROM original_tmp").Scan(&count); err != nil || count != 1 {
		t.Fatal("metadata reuse affected sibling", err, count)
	}
	t.Logf("distinct physical IDs %d/%d; uint64, prepare, rollback, initial SQL, metadata reuse and sibling disposal passed", first, second)
}
