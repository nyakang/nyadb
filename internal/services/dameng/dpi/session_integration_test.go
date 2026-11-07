package dpi

import (
	"context"
	"database/sql"
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
