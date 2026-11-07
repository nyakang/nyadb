package gaussdb

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/HuaweiCloudDeveloper/gaussdb-go/gaussdbconn"
	"github.com/HuaweiCloudDeveloper/gaussdb-go/stdlib"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeFixture(t *testing.T) (connections.GaussDBConfig, string) {
	t.Helper()
	if os.Getenv("NYADB_GAUSSDB_IT") != "1" {
		t.Skip("requires an explicit disposable native GaussDB fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_GAUSSDB_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	c := connections.GaussDBConfig{Endpoints: []connections.GaussDBEndpoint{{Host: "127.0.0.1", Port: uint16(port)}}, Database: "nyadb_gaussdb_it", Username: "nyadb_it", TLSMode: "disable", Options: connections.DefaultPostgreSQLOptions()}
	c.Options.CAFile = os.Getenv("NYADB_GAUSSDB_CA_FILE")
	if c.Options.CAFile != "" {
		c.TLSMode = "verify-full"
	}
	password := os.Getenv("NYADB_GAUSSDB_PASSWORD")
	if password == "" {
		t.Fatal("explicit fixture password required")
	}
	return c, password
}

func TestNativeGaussDBAuthenticationTypesTransactionsAndPhysicalIsolation(t *testing.T) {
	c, password := nativeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := OpenDB(c, password, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	var version, compatibility, username, database string
	if err = first.QueryRowContext(ctx, "SELECT version(), current_user, current_database()").Scan(&version, &username, &database); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(version, "GaussDB Kernel") || strings.Contains(version, "openGauss") || username != c.Username || database != c.Database {
		t.Fatal("native product or explicit account identity not verified")
	}
	if err = first.QueryRowContext(ctx, "SHOW sql_compatibility").Scan(&compatibility); err != nil || compatibility != "PG" {
		t.Fatal("native fixture compatibility", err, compatibility)
	}
	t.Log("native server:", version)
	if _, err = first.ExecContext(ctx, "CREATE TEMP TABLE nyadb_transport(id BIGINT PRIMARY KEY, amount NUMERIC(40,20), payload BYTEA, note TEXT, flag BOOLEAN, day DATE) ON COMMIT PRESERVE ROWS"); err != nil {
		t.Fatal(err)
	}
	amount, note := "9007199254740993.12345678901234567890", "长 ' \\ ; SELECT 'literal'"
	if _, err = first.ExecContext(ctx, "INSERT INTO nyadb_transport VALUES($1,$2::numeric,$3,$4,$5,$6::date)", int64(9007199254740993), amount, []byte{0, 255, 1}, note, true, "2026-11-07"); err != nil {
		t.Fatal(err)
	}
	var id int64
	var gotAmount, gotNote string
	var payload []byte
	var flag bool
	var day time.Time
	if err = first.QueryRowContext(ctx, "SELECT id,amount,payload,note,flag,day FROM nyadb_transport").Scan(&id, &gotAmount, &payload, &gotNote, &flag, &day); err != nil {
		t.Fatal(err)
	}
	if id != 9007199254740993 || gotAmount != amount || !bytes.Equal(payload, []byte{0, 255, 1}) || gotNote != note || !flag || day.Format("2006-01-02") != "2026-11-07" {
		t.Fatal("native bound values or exact type decoding changed")
	}
	if _, err = first.ExecContext(ctx, "INSERT INTO nyadb_transport(id,payload,note) VALUES(2,$1,$2),(3,NULL,NULL)", []byte{}, ""); err != nil {
		t.Fatal(err)
	}
	var emptyPayload []byte
	var emptyNote sql.NullString
	if err = first.QueryRowContext(ctx, "SELECT payload,note FROM nyadb_transport WHERE id=2").Scan(&emptyPayload, &emptyNote); err != nil || emptyPayload == nil || len(emptyPayload) != 0 || !emptyNote.Valid || emptyNote.String != "" {
		t.Fatal("empty bytea/text became NULL", err)
	}
	if err = first.QueryRowContext(ctx, "SELECT payload,note FROM nyadb_transport WHERE id=3").Scan(&emptyPayload, &emptyNote); err != nil || emptyPayload != nil || emptyNote.Valid {
		t.Fatal("NULL bytea/text became an empty value", err)
	}
	second, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err = second.ExecContext(ctx, "SELECT * FROM nyadb_transport"); err == nil {
		t.Fatal("temporary table leaked into another physical session")
	}
	tx, err := first.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE nyadb_transport SET note=$1 WHERE id=2", "rolled back"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = first.QueryRowContext(ctx, "SELECT note FROM nyadb_transport WHERE id=2").Scan(&gotNote); err != nil || gotNote != "" {
		t.Fatal("native transaction rollback failed", err)
	}
	tx, err = first.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE nyadb_transport SET note=$1 WHERE id=2", "committed"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = first.QueryRowContext(ctx, "SELECT note FROM nyadb_transport WHERE id=2").Scan(&gotNote); err != nil || gotNote != "committed" {
		t.Fatal("native transaction commit failed", err)
	}
	for _, wrong := range []string{"", "incorrect-fixture-password"} {
		bad, err := OpenDB(c, wrong, nil)
		if err != nil {
			t.Fatal(err)
		}
		probe, stop := context.WithTimeout(ctx, 3*time.Second)
		err = bad.PingContext(probe)
		stop()
		bad.Close()
		if err == nil || strings.Contains(err.Error(), password) || wrong != "" && strings.Contains(err.Error(), wrong) {
			t.Fatal("invalid authentication accepted or exposed credentials")
		}
	}
}

func TestNativeGaussDBEndpointFallbackAndTLSVerification(t *testing.T) {
	c, password := nativeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unreachable := uint16(listener.Addr().(*net.TCPAddr).Port)
	listener.Close()
	c.Endpoints = append([]connections.GaussDBEndpoint{{Host: "127.0.0.1", Port: unreachable}}, c.Endpoints...)
	c.TargetSessionAttrs = "read-write"
	db, err := OpenDB(c, password, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.PingContext(ctx); err != nil {
		t.Fatal("native fallback to independent live endpoint failed", err)
	}
	if c.Options.CAFile == "" {
		c.Endpoints = c.Endpoints[1:]
		c.TLSMode = "require"
		required, err := OpenDB(c, password, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = required.PingContext(ctx)
		required.Close()
		if err == nil {
			t.Fatal("mandatory TLS silently downgraded on a plaintext server")
		}
		c.TLSMode = "prefer"
		preferred, err := OpenDB(c, password, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = preferred.PingContext(ctx)
		preferred.Close()
		if err != nil {
			t.Fatal("explicit prefer did not permit plaintext fallback", err)
		}
		return
	}
	// Route a deliberately wrong DNS name to the same fixture. Certificate
	// verification must still use the original configured endpoint name.
	actual := net.JoinHostPort(c.Endpoints[1].Host, strconv.Itoa(int(c.Endpoints[1].Port)))
	c.Endpoints = []connections.GaussDBEndpoint{{Host: "wrong.invalid", Port: c.Endpoints[1].Port}}
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, actual)
	}
	cfg, err := nativeConfig(c, password, dial)
	if err != nil {
		t.Fatal(err)
	}
	cfg.LookupFunc = func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }
	bad := sql.OpenDB(stdlib.GetConnector(*cfg))
	err = bad.PingContext(ctx)
	bad.Close()
	if err == nil {
		t.Fatal("verify-full accepted a certificate for a different endpoint")
	}
	c.TLSMode = "verify-ca"
	cfg, err = nativeConfig(c, password, dial)
	if err != nil {
		t.Fatal(err)
	}
	cfg.LookupFunc = func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }
	chainOnly := sql.OpenDB(stdlib.GetConnector(*cfg))
	err = chainOnly.PingContext(ctx)
	chainOnly.Close()
	if err != nil {
		t.Fatal("verify-ca should verify the CA without requiring the endpoint name", err)
	}
}

func TestNativeGaussDBCancellationRequiresOriginalPhysicalCleanup(t *testing.T) {
	c, password := nativeFixture(t)
	db, err := OpenDB(c, password, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var native *gaussdbconn.GaussdbConn
	if err = conn.Raw(func(value any) error {
		driver, ok := value.(*stdlib.Conn)
		if !ok {
			return errors.New("unexpected native driver")
		}
		native = driver.Conn().GaussdbConn()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	query, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	_, err = conn.ExecContext(query, "SELECT pg_sleep(30)")
	cancel()
	if err == nil {
		t.Fatal("cancellation accepted an incomplete query as successful")
	}
	select {
	case <-native.CleanupDone():
	case <-ctx.Done():
		t.Fatal("original native cleanup did not finish")
	}
	if !native.IsClosed() {
		t.Fatal("canceled physical session remains usable")
	}
	if _, err = conn.ExecContext(ctx, "SELECT 1"); err == nil {
		t.Fatal("canceled native session was silently replaced")
	}
}
