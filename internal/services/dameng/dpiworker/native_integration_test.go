package dpiworker

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func nativeFixture(t *testing.T) (dpi.Options, string) {
	t.Helper()
	if os.Getenv("NYADB_DAMENG_DPI_IT") != "1" {
		t.Skip("requires an explicitly enabled owned DM8 fixture")
	}
	data, err := os.ReadFile(os.Getenv("NYADB_DAMENG_DPI_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Endpoint           string `json:"endpoint"`
		PasswordFile       string `json:"password_file"`
		NativeSSLDirectory string `json:"native_ssl_directory"`
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(cfg.Endpoint)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("requires a loopback fixture")
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	password, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	o := dpi.Options{LibraryFile: os.Getenv("NYADB_DAMENG_DPI_LIBRARY"), Host: host, Port: uint16(n), Username: "SYSDBA", ConnectTimeoutSeconds: 3, OperationTimeoutSeconds: 5}
	if cfg.NativeSSLDirectory != "" {
		o.ConnectTimeoutSeconds = 10
		o.OperationTimeoutSeconds = 10
		o.TLS = &dpi.TLSOptions{Mode: "verify-full", CAFile: filepath.Join(cfg.NativeSSLDirectory, "ca-cert.pem"), CertificateFile: filepath.Join(cfg.NativeSSLDirectory, "client-cert.pem"), KeyFile: filepath.Join(cfg.NativeSSLDirectory, "client-key.pem")}
	}
	return o, string(password)
}

func nativeDB(t *testing.T, o dpi.Options, password string) *sql.DB {
	t.Helper()
	configured, err := NewConnector(o, password)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(configured)
	db.SetMaxOpenConns(2)
	t.Cleanup(func() { db.Close() })
	return db
}
func suffix(t *testing.T) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return strings.ToUpper(hex.EncodeToString(b[:]))
}

func TestNativeWorkerOwnsEnvironmentAndNeverLoadsClientInParent(t *testing.T) {
	o, password := nativeFixture(t)
	foreign := t.TempDir()
	if err := os.WriteFile(filepath.Join(foreign, "dm_svc.conf"), []byte("foreign configuration must not be read\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DM_SVC_PATH", foreign)
	t.Setenv("OPENSSL_CONF", filepath.Join(foreign, "missing-openssl.cnf"))
	c, err := NewConnector(o, password)
	if err != nil {
		t.Fatal(err)
	}
	first, err := c.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := c.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	a, b := first.(*conn), second.(*conn)
	if a.dir == b.dir || a.cmd.Process.Pid == b.cmd.Process.Pid {
		t.Fatal("physical connections share native process/configuration")
	}
	for _, d := range []*conn{a, b} {
		if err := validateDirectory(d.dir); err != nil {
			t.Fatal(err)
		}
		if err := d.Ping(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv("DM_SVC_PATH") != foreign || os.Getenv("OPENSSL_CONF") != filepath.Join(foreign, "missing-openssl.cnf") {
		t.Fatal("native worker mutated workbench environment")
	}
	maps, err := os.ReadFile("/proc/self/maps")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(maps, []byte("libdmdpi")) {
		t.Fatal("native DPI client was loaded in the parent process")
	}
	if err := os.Remove(filepath.Join(a.dir, "dm_svc.conf")); err != nil {
		t.Fatal(err)
	}
	// Bypass the parent's filesystem check to exercise the child's own gate.
	a.next++
	if err := a.send(request{ID: a.next, Method: "ping"}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-a.replies:
		if r.err != nil || r.response.ID != a.next || !errors.Is(r.response.Error.err(), dpi.ErrSessionClosed) {
			t.Fatal("child allowed native system fallback", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not retire changed configuration")
	}
	a.mu.Lock()
	a.terminateLocked()
	a.mu.Unlock()
	if err := b.Ping(context.Background()); err != nil {
		t.Fatal("configuration removal retired another physical process", err)
	}
}

func TestNativeWorkerExactValuesAndTransactions(t *testing.T) {
	o, password := nativeFixture(t)
	db := nativeDB(t, o, password)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connection, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err = connection.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	name := "NYADB_DPI_" + suffix(t)
	if _, err = connection.ExecContext(ctx, "CREATE TABLE "+name+" (ID BIGINT, N DECIMAL(38,20), T VARCHAR(200), B VARBINARY(200), Z VARCHAR(20), E VARCHAR(20))"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.ExecContext(context.Background(), "DROP TABLE "+name) })
	const number = "123456789012345678.12345678901234567891"
	text := "数据库 ' \\ 🌸"
	binary := []byte{0, 1, 128, 255}
	result, err := connection.ExecContext(ctx, "INSERT INTO "+name+" VALUES(?,?,?,?,?,?)", int64(9007199254740993), dpi.Decimal(number), text, binary, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("affected rows=%d err=%v", n, err)
	}
	rows, err := connection.QueryContext(ctx, "SELECT ID,N,T,B,Z,E FROM "+name)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := rows.ColumnTypes()
	if err != nil {
		t.Fatal(err)
	}
	if p, s, ok := columns[1].DecimalSize(); !ok || p != 38 || s != 20 {
		t.Fatalf("decimal metadata=%d,%d,%t", p, s, ok)
	}
	var id int64
	var decimal, actualText, empty string
	var actualBinary []byte
	var null sql.NullString
	if !rows.Next() {
		t.Fatal(rows.Err())
	}
	if err = rows.Scan(&id, &decimal, &actualText, &actualBinary, &null, &empty); err != nil {
		t.Fatal(err)
	}
	if id != 9007199254740993 || decimal != number || actualText != text || !bytes.Equal(actualBinary, binary) || null.Valid || empty != "" {
		t.Fatalf("owned exact fixture mismatch: integer=%d decimal=%s text=%q binary=%v null=%t empty=%q", id, decimal, actualText, actualBinary, null.Valid, empty)
	}
	if rows.Next() {
		t.Fatal("unexpected extra row")
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, commit := range []bool{false, true} {
		tx, err := connection.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO "+name+" (ID) VALUES(?)", int64(42)); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			t.Fatal(err)
		}
		var count int
		if err = connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if want := 1; commit {
			want = 2
			if count != want {
				t.Fatalf("commit count=%d", count)
			}
		} else if count != want {
			t.Fatalf("rollback count=%d", count)
		}
	}
}

func TestNativeWorkerCancellationRetiresOriginalSession(t *testing.T) {
	o, password := nativeFixture(t)
	db := nativeDB(t, o, password)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	connection, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err = connection.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	run, cancelRun := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancelRun()
	started := time.Now()
	_, err = connection.ExecContext(run, "SELECT COUNT(*) FROM SYS.SYSOBJECTS A CROSS JOIN SYS.SYSOBJECTS B CROSS JOIN SYS.SYSOBJECTS C WHERE MOD(A.ID,17)+MOD(B.ID,17)+MOD(C.ID,17)=0")
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, dpi.ErrOutcomeUnknown) || errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("running cancel=%v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("native cancellation did not settle within the bound")
	}
	t.Log("original query joined and native session retired after:", time.Since(started))
	if err = connection.PingContext(ctx); err == nil {
		t.Fatal("canceled physical session was reused")
	}
	recovery, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Close()
	if err = recovery.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestNativeWorkerPhysicalSessionIsolationAndReusablePreparedRows(t *testing.T) {
	o, password := nativeFixture(t)
	db := nativeDB(t, o, password)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	first, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	name := "NYADB_DPI_TEMP_" + suffix(t)
	if _, err = first.ExecContext(ctx, "CREATE GLOBAL TEMPORARY TABLE "+name+" (ID BIGINT) ON COMMIT PRESERVE ROWS"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.ExecContext(context.Background(), "DROP TABLE "+name) })
	insert, err := first.PrepareContext(ctx, "INSERT INTO "+name+" VALUES (?)")
	if err != nil {
		t.Fatal(err)
	}
	defer insert.Close()
	for _, value := range []int64{9007199254740993, 42} {
		if _, err = insert.ExecContext(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	read, err := first.PrepareContext(ctx, "SELECT ID FROM "+name+" WHERE ID=?")
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	for _, value := range []int64{9007199254740993, 42} {
		var actual int64
		if err = read.QueryRowContext(ctx, value).Scan(&actual); err != nil || actual != value {
			t.Fatalf("prepared original row=%d expected=%d err=%v", actual, value, err)
		}
	}
	for _, connection := range []*sql.Conn{first, second} {
		var count int
		if err = connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 0
		if connection == first {
			want = 2
		}
		if count != want {
			t.Fatalf("physical session temporary rows=%d expected=%d", count, want)
		}
	}
}
