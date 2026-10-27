package connections

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Opt-in: provision a disposable MySQL 8.4 fixture, bound exclusively to
// 127.0.0.1, with schema nyadb_tab_it, row (1,'before') in tx_probe,
// and a least-privilege nyadb_tab account. Never contact production.
func TestMySQLSQLTabLeasesIntegration(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_TAB_IT") != "1" {
		t.Skip("requires explicit disposable MySQL 8.4 loopback fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_MYSQL_TAB_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("missing valid disposable loopback port")
	}
	m := NewMySQLSessionManager()
	t.Cleanup(func() { _ = m.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	const id int64 = 20261027
	if err := m.Connect(ctx, ActiveSettings{ProfileID: id, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_tab", MySQLAdvanced: DefaultMySQLAdvanced()}, ""); err != nil {
		t.Fatal(err)
	}
	if m.sessions[id].db == m.sessions[id].sqlDB {
		t.Fatal("production SQL tabs must not share the metadata pool")
	}
	query := func(tab uint64, statement string) MySQLSQLResult {
		t.Helper()
		got, err := m.ExecuteSQLTab(ctx, id, tab, "nyadb_tab_it", statement)
		if err != nil {
			t.Fatalf("tab %d SQL %q: %v", tab, statement, err)
		}
		return got
	}
	a := query(1, "SELECT CONNECTION_ID()")
	b := query(2, "SELECT CONNECTION_ID()")
	if a.Rows[0][0] == b.Rows[0][0] {
		t.Fatalf("tabs unexpectedly share physical connection: %s", a.Rows[0][0])
	}
	query(1, "SET @nyadb_tab_marker = 73")
	if got := query(1, "SELECT @nyadb_tab_marker, CONNECTION_ID()"); got.Rows[0][0] != "73" || got.Rows[0][1] != a.Rows[0][0] {
		t.Fatalf("tab A lost its session state: %+v", got.Rows)
	}
	if got := query(2, "SELECT @nyadb_tab_marker, CONNECTION_ID()"); got.Rows[0][0] != "NULL" || got.Rows[0][1] != b.Rows[0][0] {
		t.Fatalf("tab B inherited A variables: %+v", got.Rows)
	}
	query(1, "START TRANSACTION")
	query(1, "UPDATE nyadb_tab_it.tx_probe SET note='pending_a' WHERE id=1")
	if got := query(1, "SELECT note FROM nyadb_tab_it.tx_probe WHERE id=1"); got.Rows[0][0] != "pending_a" {
		t.Fatalf("transaction not retained in A: %+v", got.Rows)
	}
	if got := query(2, "SELECT note FROM nyadb_tab_it.tx_probe WHERE id=1"); got.Rows[0][0] != "before" {
		t.Fatalf("B read A's uncommitted update: %+v", got.Rows)
	}
	query(1, "ROLLBACK")
	if got := query(2, "SELECT note FROM nyadb_tab_it.tx_probe WHERE id=1"); got.Rows[0][0] != "before" {
		t.Fatalf("transaction not rolled back: %+v", got.Rows)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	start := time.Now()
	for _, tab := range []uint64{1, 2} {
		wg.Add(1)
		go func(tab uint64) {
			defer wg.Done()
			_, err := m.ExecuteSQLTab(ctx, id, tab, "nyadb_tab_it", "SELECT SLEEP(2)")
			errs <- err
		}(tab)
	}
	time.Sleep(150 * time.Millisecond)
	metaStart := time.Now()
	schemas, metadataErr := m.Databases(ctx, id)
	if metadataErr != nil || len(schemas) == 0 {
		t.Fatalf("metadata read alongside active SQL tabs: %v %v", schemas, metadataErr)
	}
	if elapsed := time.Since(metaStart); elapsed > 1700*time.Millisecond {
		t.Fatalf("metadata blocked behind interactive SQL tabs: %s", elapsed)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed > 3500*time.Millisecond {
		t.Fatalf("independent tabs unexpectedly serialized: %s", elapsed)
	}
	// Cancellation invalidates the stateful lease; never silently reconnect.
	cancelCtx, stop := context.WithTimeout(ctx, 150*time.Millisecond)
	defer stop()
	_, err = m.ExecuteSQLTab(cancelCtx, id, 1, "nyadb_tab_it", "SELECT SLEEP(8)")
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not propagated: %v", err)
	}
	if _, err := m.ExecuteSQLTab(ctx, id, 1, "", "SELECT 1"); !errors.Is(err, ErrMySQLSQLSessionLost) {
		t.Fatalf("canceled lease silently replaced: %v", err)
	}
	if got := query(2, "SELECT CONNECTION_ID()"); got.Rows[0][0] != b.Rows[0][0] {
		t.Fatalf("other tab was disturbed by cancellation: %+v", got.Rows)
	}
	m.ReleaseSQLTab(id, 1)
	m.ReleaseSQLTab(id, 2)
	deadline := time.Now().Add(3 * time.Second)
	for m.sessions[id].sqlDB.Stats().InUse != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := m.sessions[id].sqlDB.Stats().InUse; n != 0 {
		t.Fatalf("tab connections not returned after closing: %d", n)
	}
}
