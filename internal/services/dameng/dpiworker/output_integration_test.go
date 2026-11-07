package dpiworker

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

func TestNativeWorkerOutputParametersReturnExactStoredValues(t *testing.T) {
	o, password := nativeFixture(t)
	db := nativeDB(t, o, password)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	name := `"SYSDBA"."NYADB_OUTPUT_` + suffix(t) + `"`
	if _, err = c.ExecContext(ctx, "CREATE TABLE "+name+"(ID BIGINT IDENTITY(9007199254740993,1) PRIMARY KEY,V VARCHAR(64) DEFAULT 'default',N DECIMAL(38,20),G BIGINT GENERATED ALWAYS AS(ID+1) VIRTUAL)"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(context.Background(), "DROP TABLE "+name); err != nil {
			t.Error(err)
		}
	}()
	trigger := name[:len(name)-1] + `_TR"`
	if _, err = c.ExecContext(ctx, "CREATE TRIGGER "+trigger+" BEFORE INSERT OR UPDATE ON "+name+" FOR EACH ROW BEGIN :NEW.V := UPPER(:NEW.V); END;"); err != nil {
		t.Fatal(err)
	}
	const decimal = "123456789012345678.12345678901234567891"
	var key int64
	var text, number string
	_, err = c.ExecContext(ctx, "BEGIN INSERT INTO "+name+"(V,N) VALUES(?,?) RETURNING ID,V,N INTO ?,?,?; END;", "数据库 🌸", dpi.Decimal(decimal), sql.Out{Dest: &key}, sql.Out{Dest: &text}, sql.Out{Dest: &number})
	if err != nil {
		t.Fatal(err)
	}
	if key != 9007199254740993 || text != "数据库 🌸" || number != decimal {
		t.Fatalf("outputs: %d %q %q", key, text, number)
	}
	var storedKey int64
	var storedText, storedNumber string
	if err = c.QueryRowContext(ctx, "SELECT ID,V,N FROM "+name+" WHERE ID=?", key).Scan(&storedKey, &storedText, &storedNumber); err != nil {
		t.Fatal(err)
	}
	if storedKey != key || storedText != text || storedNumber != number {
		t.Fatal("outputs do not match the stored row")
	}
	var generated int64
	_, err = c.ExecContext(ctx, "BEGIN INSERT INTO "+name+"(V) VALUES(DEFAULT) RETURNING ID,V,G INTO ?,?,?; END;", sql.Out{Dest: &key}, sql.Out{Dest: &text}, sql.Out{Dest: &generated})
	if err != nil || key != 9007199254740994 || text != "DEFAULT" || generated != key+1 {
		t.Fatal("default/generated/trigger outputs", err, key, text, generated)
	}
	stmt, err := c.PrepareContext(ctx, "BEGIN UPDATE "+name+" SET V=? WHERE ID=? RETURNING ID,V INTO ?,?; ? := SQL%ROWCOUNT; END;")
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	for _, input := range []string{"first", "second"} {
		var returnedKey, affected int64
		_, err = stmt.ExecContext(ctx, input, key, sql.Out{Dest: &returnedKey}, sql.Out{Dest: &text}, sql.Out{Dest: &affected})
		if err != nil || returnedKey != key || text != strings.ToUpper(input) || affected != 1 {
			t.Fatal("prepared returned values", err, returnedKey, text, affected)
		}
		if err = c.QueryRowContext(ctx, "SELECT V FROM "+name+" WHERE ID=?", key).Scan(&storedText); err != nil || storedText != text {
			t.Fatal("trigger output differs from stored row", err)
		}
	}
	var nullText = sql.NullString{String: "stale", Valid: true}
	var nullKey = sql.NullInt64{Int64: 42, Valid: true}
	var empty = "stale"
	_, err = c.ExecContext(ctx, "BEGIN ? := NULL; ? := NULL; ? := ''; END;", sql.Out{Dest: &nullText}, sql.Out{Dest: &nullKey}, sql.Out{Dest: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if nullText.Valid || nullKey.Valid || empty != "" {
		t.Fatalf("null/empty outputs: %+v %+v %q", nullText, nullKey, empty)
	}
	for _, want := range []int64{math.MinInt64, math.MaxInt64, 9007199254740993} {
		_, err = c.ExecContext(ctx, "BEGIN ? := ?; END;", sql.Out{Dest: &key}, want)
		if err != nil || key != want {
			t.Fatal("integer boundary changed", err, key, want)
		}
	}
	for _, want := range []string{strings.Repeat("x", dpi.MaxOutputTextBytes), strings.Repeat("数", 2730) + "ab"} {
		_, err = c.ExecContext(ctx, "BEGIN ? := ?; END;", sql.Out{Dest: &text}, want)
		if err != nil || text != want {
			t.Fatal("bounded multibyte output changed", err, len(text), len(want))
		}
	}
	key, text = 42, "original"
	_, err = c.ExecContext(ctx, "BEGIN INSERT INTO "+name+"(ID,V) VALUES(9007199254740993,'duplicate') RETURNING ID,V INTO ?,?; END;", sql.Out{Dest: &key}, sql.Out{Dest: &text})
	if err == nil || errors.Is(err, driver.ErrBadConn) || key != 42 || text != "original" {
		t.Fatal("native failure changed outputs", err, key, text)
	}
	if err = c.PingContext(ctx); err != nil {
		t.Fatal("ordinary native failure retired the session", err)
	}
	if rows, err := c.QueryContext(ctx, "BEGIN INSERT INTO "+name+"(V) VALUES('query must not execute') RETURNING ID INTO ?; END;", sql.Out{Dest: &key}); err == nil {
		rows.Close()
		t.Fatal("query output was executed")
	}
	var count int
	if err = c.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+name).Scan(&count); err != nil || count != 2 {
		t.Fatal("rejected statement mutated the table", err, count)
	}
	for _, commit := range []bool{false, true} {
		tx, err := c.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.ExecContext(ctx, "BEGIN INSERT INTO "+name+"(V) VALUES('transaction') RETURNING ID,V INTO ?,?; END;", sql.Out{Dest: &key}, sql.Out{Dest: &text})
		if err != nil || text != "TRANSACTION" {
			tx.Rollback()
			t.Fatal("transaction output", err, text)
		}
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = c.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+name+" WHERE ID=?", key).Scan(&count); err != nil || count != map[bool]int{false: 0, true: 1}[commit] {
			t.Fatal("output transaction applied incorrectly", err, count, commit)
		}
	}
}

func TestNativeWorkerOutputCoercionFailuresPreserveOriginalVariables(t *testing.T) {
	o, password := nativeFixture(t)
	db := nativeDB(t, o, password)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	name := `"SYSDBA"."NYADB_OUTPUT_REJECT_` + suffix(t) + `"`
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+name+"(ID INT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(context.Background(), "DROP TABLE "+name); err != nil {
			t.Error(err)
		}
	}()
	for i, expr := range []string{"1.234", "9223372036854775808", "NULL"} {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		key, text := int64(42), "original"
		_, err = c.ExecContext(ctx, "BEGIN INSERT INTO "+name+" VALUES(?); ? := 'changed'; ? := "+expr+"; END;", int64(i), sql.Out{Dest: &text}, sql.Out{Dest: &key})
		if !errors.Is(err, dpi.ErrOutcomeUnknown) || errors.Is(err, driver.ErrBadConn) || key != 42 || text != "original" {
			c.Close()
			t.Fatal("lossy output was published", expr, err, key, text)
		}
		if err = c.PingContext(ctx); err == nil {
			c.Close()
			t.Fatal("lossy output session reused")
		}
		c.Close()
		var count int
		if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+name+" WHERE ID=?", int64(i)).Scan(&count); err != nil || count != 1 {
			t.Fatal("write was lost or replayed", expr, err, count)
		}
	}
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	text := "original"
	_, err = c.ExecContext(ctx, "BEGIN ? := RPAD('x',8193,'x'); END;", sql.Out{Dest: &text})
	if err == nil || text != "original" || errors.Is(err, driver.ErrBadConn) {
		t.Fatal("oversize output was accepted", err, len(text))
	}
	var native *dpi.Error
	if !errors.As(err, &native) || native.Code != -6108 {
		t.Fatal("unexpected native output length rejection", err)
	}
}

func TestNativeWorkerOutputCancellationPreservesVariablesAndOriginalOwnership(t *testing.T) {
	o, password := nativeFixture(t)
	db := nativeDB(t, o, password)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = c.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	key := int64(42)
	query := "BEGIN SELECT COUNT(*) INTO ? FROM SYS.SYSOBJECTS A CROSS JOIN SYS.SYSOBJECTS B CROSS JOIN SYS.SYSOBJECTS C WHERE MOD(A.ID,17)+MOD(B.ID,17)+MOD(C.ID,17)=0; END;"
	before, cancelBefore := context.WithCancel(ctx)
	cancelBefore()
	_, err = c.ExecContext(before, query, sql.Out{Dest: &key})
	if !errors.Is(err, context.Canceled) || errors.Is(err, dpi.ErrOutcomeUnknown) || key != 42 {
		t.Fatal("unsubmitted cancellation changed output", err, key)
	}
	if err = c.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	run, cancelRun := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancelRun()
	_, err = c.ExecContext(run, query, sql.Out{Dest: &key})
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, dpi.ErrOutcomeUnknown) || errors.Is(err, driver.ErrBadConn) || key != 42 {
		t.Fatal("running cancellation published output", err, key)
	}
	if err = c.PingContext(ctx); err == nil {
		t.Fatal("canceled original reused")
	}
	if err = db.PingContext(ctx); err != nil {
		t.Fatal("independent recovery failed", err)
	}
}

func TestNativeWorkerOutputLostReplyNeverPublishesOrReplays(t *testing.T) {
	o, password := nativeFixture(t)
	observer := nativeDB(t, o, password)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	name := `"SYSDBA"."NYADB_OUTPUT_LOST_` + suffix(t) + `"`
	if _, err := observer.ExecContext(ctx, "CREATE TABLE "+name+"(ID BIGINT IDENTITY(9007199254740993,1) PRIMARY KEY,V VARCHAR(10) DEFAULT 'default')"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := observer.ExecContext(context.Background(), "DROP TABLE "+name); err != nil {
			t.Error(err)
		}
	}()
	query := "BEGIN INSERT INTO " + name + "(V) VALUES(DEFAULT) RETURNING ID INTO ?; END;"
	var settings []dpi.TLSOptions
	if o.TLS != nil {
		settings = append(settings, *o.TLS)
	}
	proxy := dropNativeReply(t, net.JoinHostPort(o.Host, strconv.Itoa(int(o.Port))), query, settings...)
	options := o
	options.Port = uint16(proxy.listener.Addr().(*net.TCPAddr).Port)
	db := nativeDB(t, options, password)
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Prepare before arming so only the actual execution response is discarded.
	stmt, err := c.PrepareContext(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	proxy.requested.Store(true)
	proxy.armed.Store(true)
	key := int64(42)
	_, err = stmt.ExecContext(ctx, sql.Out{Dest: &key})
	if !errors.Is(err, dpi.ErrOutcomeUnknown) || errors.Is(err, driver.ErrBadConn) || key != 42 {
		t.Fatal("lost reply published outputs", err, key)
	}
	select {
	case <-proxy.dropped:
	default:
		t.Fatal("actual native execution reply was not dropped")
	}
	if err = c.PingContext(ctx); err == nil {
		t.Fatal("uncertain original reused")
	}
	var count int
	var stored int64
	if err = observer.QueryRowContext(ctx, "SELECT COUNT(*),MIN(ID) FROM "+name).Scan(&count, &stored); err != nil || count != 1 || stored != 9007199254740993 {
		t.Fatal("submitted original write was lost or replayed", err, count, stored)
	}
}
