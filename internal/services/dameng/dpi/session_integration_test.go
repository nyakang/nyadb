package dpi

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestNativeDPIPhysicalSessionIsolationAndReusablePreparedRows(t *testing.T) {
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

func TestNativeDPIInitialSchemaUsesActualServerStateAndQuotedIdentity(t *testing.T) {
	o, password := nativeFixture(t)
	original := nativeDB(t, o, password)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	name := "NYADB_DPI_SCHEMA_" + suffix(t) + ` 中文 "'; --`
	quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	if _, err := original.ExecContext(ctx, "CREATE SCHEMA "+quoted+" AUTHORIZATION SYSDBA"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { original.ExecContext(context.Background(), "DROP SCHEMA "+quoted+" CASCADE") })
	o.Schema = name
	configured := nativeDB(t, o, password)
	first, err := configured.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := configured.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for _, physical := range []*sql.Conn{first, second} {
		var actual string
		if err := physical.QueryRowContext(ctx, schemaSQL).Scan(&actual); err != nil || actual != name {
			t.Fatal("raw native schema setup was acknowledged without server state", err)
		}
	}
	var actual string
	if err := original.QueryRowContext(ctx, schemaSQL).Scan(&actual); err != nil || actual != "SYSDBA" {
		t.Fatal("raw schema setup affected another connection", err)
	}
	o.Schema = "NYADB_MISSING_" + suffix(t)
	missing := nativeDB(t, o, password)
	if err := missing.PingContext(ctx); err == nil {
		t.Fatal("missing raw initial schema was silently ignored")
	}
	if err := original.PingContext(ctx); err != nil {
		t.Fatal("rejected raw schema setup closed another connection", err)
	}
}
