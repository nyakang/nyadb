package native

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeDuckDBFixture(t *testing.T) {
	if os.Getenv("NYADB_DUCKDB_IT") != "1" {
		t.Skip("set NYADB_DUCKDB_IT=1 with the DuckDB runtime installed")
	}
	c, err := Open(filepath.Join(t.TempDir(), "fixture.duckdb"), Options{MemoryMB: 256, Threads: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	db := sql.OpenDB(c)
	defer db.Close()
	ctx := context.Background()
	if _, err = db.ExecContext(ctx, "CREATE TABLE records(id INTEGER PRIMARY KEY,n DECIMAL(38,20),b BLOB,s VARCHAR)"); err != nil {
		t.Fatal(err)
	}
	result, err := db.ExecContext(ctx, "INSERT INTO records VALUES(?,?,?,?)", 1, "9007199254740993.12345678901234567890", []byte{0, 255}, "a\x00b")
	if err != nil {
		t.Fatal(err)
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		t.Fatalf("affected %d: %v", n, e)
	}
	var n, s string
	var b []byte
	if err = db.QueryRowContext(ctx, "SELECT n,b,s FROM records").Scan(&n, &b, &s); err != nil {
		t.Fatal(err)
	}
	if n != "9007199254740993.12345678901234567890" || string(b) != string([]byte{0, 255}) || s != "a\x00b" {
		t.Fatalf("native values were not preserved: %q %x %q", n, b, s)
	}
	var nested string
	if err = db.QueryRowContext(ctx, "SELECT {'x': [1,2,NULL]}").Scan(&nested); err != nil || nested == "" {
		t.Fatalf("nested value: %s %v", nested, err)
	}
	for _, expression := range []string{
		"'-170141183460469231731687303715884105728'::HUGEINT",
		"'340282366920938463463374607431768211455'::UHUGEINT",
		"'18446744073709551615'::UBIGINT",
		"'2026-11-06 12:34:56.123456789'::TIMESTAMP_NS",
		"'2026-11-06'::DATE",
		"'12:34:56.123456'::TIME",
		"'2026-11-06 12:34:56.123456'::TIMESTAMP",
		"'2026-11-06 12:34:56.123'::TIMESTAMP_MS",
		"'2026-11-06 12:34:56'::TIMESTAMP_S",
		"INTERVAL '1 month 2 days 3 seconds'",
		"'{\"large\":9007199254740993}'::JSON",
	} {
		var actual, expected string
		if err = db.QueryRowContext(ctx, "SELECT v,CAST(v AS VARCHAR) FROM (SELECT "+expression+" AS v)").Scan(&actual, &expected); err != nil || actual != expected {
			t.Fatalf("exact scalar %s: %q != %q: %v", expression, actual, expected, err)
		}
	}
	if err = db.QueryRowContext(ctx, "SELECT {'s': ['a,b','NULL',NULL], 'n': [1,2]}").Scan(&nested); err != nil || nested != "{'s': ['a,b', 'NULL', NULL], 'n': [1, 2]}" {
		t.Fatalf("nested strings lost their boundaries: %q %v", nested, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE records SET s='rollback'"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, "SELECT s FROM records").Scan(&s); err != nil || s != "a\x00b" {
		t.Fatalf("rollback: %q %v", s, err)
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	_, err = db.ExecContext(cctx, "SELECT sum(a.i*b.i) FROM range(10000000) a(i),range(10000000) b(i)")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation: %v", err)
	}
	if err = db.PingContext(ctx); err != nil {
		t.Fatalf("late interrupt affected next query: %v", err)
	}
}
