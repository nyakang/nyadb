package dpi

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func nativeFixture(t *testing.T) (Options, string) {
	t.Helper()
	if os.Getenv("NYADB_DAMENG_DPI_IT") != "1" {
		t.Skip("requires an explicitly enabled owned DM8 fixture")
	}
	data, err := os.ReadFile(os.Getenv("NYADB_DAMENG_DPI_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Endpoint     string `json:"endpoint"`
		PasswordFile string `json:"password_file"`
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
	return Options{LibraryFile: os.Getenv("NYADB_DAMENG_DPI_LIBRARY"), Host: host, Port: uint16(n), Username: "SYSDBA", ConnectTimeoutSeconds: 3, OperationTimeoutSeconds: 5}, string(password)
}

func nativeDB(t *testing.T, o Options, password string) *sql.DB {
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

func TestNativeDPIExactValuesAndTransactions(t *testing.T) {
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
	version, err := ClientVersion(connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("actual native client version:", version)
	name := "NYADB_DPI_" + suffix(t)
	if _, err = connection.ExecContext(ctx, "CREATE TABLE "+name+" (ID BIGINT, N DECIMAL(38,20), T VARCHAR(200), B VARBINARY(200), Z VARCHAR(20), E VARCHAR(20))"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.ExecContext(context.Background(), "DROP TABLE "+name) })
	const number = "123456789012345678.12345678901234567891"
	text := "数据库 ' \\ 🌸"
	binary := []byte{0, 1, 128, 255}
	result, err := connection.ExecContext(ctx, "INSERT INTO "+name+" VALUES(?,?,?,?,?,?)", int64(9007199254740993), Decimal(number), text, binary, nil, "")
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

func TestNativeDPIOrdinaryAuthentication(t *testing.T) {
	o, password := nativeFixture(t)
	admin := nativeDB(t, o, password)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	username := "NYADB_DPI_USER_" + suffix(t)
	userPassword := "Aa9!" + suffix(t) + "@/?#:%'"
	if _, err := admin.ExecContext(ctx, "CREATE USER "+username+" IDENTIFIED BY \""+userPassword+"\""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.ExecContext(context.Background(), "DROP USER "+username+" CASCADE") })
	o.Username = username
	for _, value := range []string{userPassword, "", "incorrect-password"} {
		db := nativeDB(t, o, value)
		err := db.PingContext(ctx)
		if value == userPassword && err != nil {
			t.Fatal(err)
		}
		if value != userPassword && err == nil {
			t.Fatal("invalid password unexpectedly accepted")
		}
		if err != nil && strings.Contains(err.Error(), userPassword) {
			t.Fatal("credential leaked")
		}
		db.Close()
	}
}

func TestNativeDPICancellationRetiresOriginalSession(t *testing.T) {
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
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, driver.ErrBadConn) {
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
