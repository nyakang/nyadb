package connections

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type keepAliveTestConnector struct {
	pings *atomic.Int64
	fail  *atomic.Bool
}

func (c keepAliveTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &keepAliveTestConn{
		sqlTestConn: sqlTestConn{log: make(chan string, 64)},
		pings:       c.pings,
		fail:        c.fail,
	}, nil
}

func (keepAliveTestConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }

type keepAliveTestConn struct {
	sqlTestConn
	pings *atomic.Int64
	fail  *atomic.Bool
}

func (c *keepAliveTestConn) Ping(ctx context.Context) error {
	c.pings.Add(1)
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.fail.Load() {
		return driver.ErrBadConn
	}
	return nil
}

func keepAliveTestManager(t *testing.T, interval int, enabled bool, pings *atomic.Int64, fail *atomic.Bool) (*MySQLSessionManager, *sql.DB, int64) {
	t.Helper()
	m := NewMySQLSessionManager()
	var db *sql.DB
	m.opener = func(context.Context, ActiveSettings, string) (*sql.DB, error) {
		db = sql.OpenDB(keepAliveTestConnector{pings: pings, fail: fail})
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(2)
		return db, nil
	}
	adv := DefaultMySQLAdvanced()
	adv.KeepAlive, adv.KeepAliveSeconds = enabled, interval
	const profile int64 = 2101
	if err := m.Connect(context.Background(), ActiveSettings{ProfileID: profile, MySQLAdvanced: adv}, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, db, profile
}

func TestMySQLKeepAlivePingsIdleTabWithoutReplacingSession(t *testing.T) {
	var pings atomic.Int64
	var fail atomic.Bool
	m, db, id := keepAliveTestManager(t, 240, true, &pings, &fail)
	if err := m.ReserveSQLTab(id, 11); err != nil {
		t.Fatal(err)
	}
	if err := m.PreconnectSQLTab(context.Background(), id, 11); err != nil {
		t.Fatal(err)
	}
	m.sessions[id].keepAliveOnce()
	if got := pings.Load(); got != 1 {
		t.Fatalf("idle SQL tab was not pinged once: %d", got)
	}
	if used := db.Stats().InUse; used != 1 {
		t.Fatalf("heartbeat replaced or released the reserved SQL connection: %d", used)
	}
	if _, err := m.ExecuteSQLTab(context.Background(), id, 11, "", "SELECT id,name FROM products"); err != nil {
		t.Fatalf("SQL tab lost its session on successful Ping: %v", err)
	}
	if pings.Load() != 1 {
		t.Fatal("SQL execution itself unexpectedly scheduled a heartbeat")
	}
}

func TestMySQLKeepAliveSkipsBusyAndUnopenedSQLTabs(t *testing.T) {
	var pings atomic.Int64
	var fail atomic.Bool
	m, _, id := keepAliveTestManager(t, 240, true, &pings, &fail)
	if err := m.ReserveSQLTab(id, 11); err != nil {
		t.Fatal(err)
	}
	m.sessions[id].keepAliveOnce()
	if pings.Load() != 0 {
		t.Fatal("unopened SQL tab caused a network connection")
	}
	if err := m.PreconnectSQLTab(context.Background(), id, 11); err != nil {
		t.Fatal(err)
	}
	lease := m.sessions[id].sqlTabs[11]
	lease.mu.Lock() // simulate a query owning the physical connection
	start := time.Now()
	m.sessions[id].keepAliveOnce()
	elapsed := time.Since(start)
	lease.mu.Unlock()
	if elapsed > 200*time.Millisecond || pings.Load() != 0 {
		t.Fatalf("keepalive blocked behind running SQL or pinged a busy tab: %s, %d", elapsed, pings.Load())
	}
	m.sessions[id].keepAliveOnce()
	if pings.Load() != 1 {
		t.Fatal("idle tab did not resume heartbeats")
	}
}

func TestMySQLKeepAliveBrokenTabIsLostAndNeverSilentlyReconnected(t *testing.T) {
	var pings atomic.Int64
	var fail atomic.Bool
	m, db, id := keepAliveTestManager(t, 240, true, &pings, &fail)
	if err := m.ReserveSQLTab(id, 11); err != nil {
		t.Fatal(err)
	}
	if err := m.PreconnectSQLTab(context.Background(), id, 11); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	m.sessions[id].keepAliveOnce()
	if got := pings.Load(); got != 1 {
		t.Fatalf("missing expected failure Ping: %d", got)
	}
	if _, err := m.ExecuteSQLTab(context.Background(), id, 11, "", "SELECT id,name FROM products"); !errors.Is(err, ErrMySQLSQLSessionLost) {
		t.Fatalf("broken SQL tab was silently reconnected: %v", err)
	}
	waitSQLTabConnectionCount(t, db, 0)
	m.sessions[id].keepAliveOnce()
	if pings.Load() != 1 {
		t.Fatal("lost session continued receiving Ping")
	}
}

func TestMySQLKeepAliveMetadataOnlyPingsExistingIdleConnection(t *testing.T) {
	var pings atomic.Int64
	var fail atomic.Bool
	m, db, id := keepAliveTestManager(t, 240, true, &pings, &fail)
	m.sessions[id].keepAliveOnce()
	if pings.Load() != 0 || db.Stats().OpenConnections != 0 {
		t.Fatal("empty metadata pool caused proactive dial")
	}
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.sessions[id].keepAliveOnce()
	if got := pings.Load(); got != 2 { // initial ping + heartbeat
		t.Fatalf("idle metadata connection was not pinged: %d", got)
	}
	if db.Stats().OpenConnections != 1 {
		t.Fatalf("metadata heartbeat opened a second connection: %+v", db.Stats())
	}
}

func TestMySQLKeepAliveTickerStopsOnDisconnect(t *testing.T) {
	var pings atomic.Int64
	var fail atomic.Bool
	m, _, id := keepAliveTestManager(t, 1, true, &pings, &fail)
	if err := m.ReserveSQLTab(id, 11); err != nil {
		t.Fatal(err)
	}
	if err := m.PreconnectSQLTab(context.Background(), id, 11); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2500 * time.Millisecond)
	for pings.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if pings.Load() == 0 {
		t.Fatal("enabled KeepAlive did not tick near configured one-second interval")
	}
	if err := m.Disconnect(id); err != nil {
		t.Fatal(err)
	}
	before := pings.Load()
	time.Sleep(1200 * time.Millisecond)
	if got := pings.Load(); got != before {
		t.Fatalf("KeepAlive continued after disconnect: %d -> %d", before, got)
	}
}

func TestMySQLKeepAliveDisabledDoesNotStartWorker(t *testing.T) {
	var pings atomic.Int64
	var fail atomic.Bool
	m, _, id := keepAliveTestManager(t, 1, false, &pings, &fail)
	if err := m.ReserveSQLTab(id, 11); err != nil {
		t.Fatal(err)
	}
	if err := m.PreconnectSQLTab(context.Background(), id, 11); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1150 * time.Millisecond)
	if got := pings.Load(); got != 0 {
		t.Fatalf("disabled KeepAlive unexpectedly pinged: %d", got)
	}
}
