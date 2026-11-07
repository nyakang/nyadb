package gaussdb

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HuaweiCloudDeveloper/gaussdb-go/gaussdbconn"
)

// Preserve actual authentication and catalog messages, replacing just the
// product marker in one later physical connection's version response.
type foreignVersionConn struct {
	net.Conn
	pending []byte
	closed  atomic.Bool
}

func (c *foreignVersionConn) Read(p []byte) (int, error) {
	if len(c.pending) == 0 {
		var header [5]byte
		if _, err := io.ReadFull(c.Conn, header[:]); err != nil {
			return 0, err
		}
		length := int(binary.BigEndian.Uint32(header[1:])) - 4
		if length < 0 || length > 1<<20 {
			return 0, errors.New("invalid fixture wire frame")
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(c.Conn, payload); err != nil {
			return 0, err
		}
		if header[0] == 'D' {
			payload = bytes.ReplaceAll(payload, []byte("gaussdb (GaussDB Kernel"), []byte("foreign (GaussDB Kernel"))
		}
		c.pending = append(header[:], payload...)
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}
func (c *foreignVersionConn) Close() error { c.closed.Store(true); return c.Conn.Close() }

func TestNativeGaussDBRejectsAndClosesLaterForeignPhysicalConnectionFixture(t *testing.T) {
	c, password := nativeFixture(t)
	c.TLSMode, c.Options.CAFile = "disable", ""
	ctx, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	var count atomic.Int32
	var foreign *foreignVersionConn
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		if count.Add(1) == 2 {
			foreign = &foreignVersionConn{Conn: conn}
			return foreign, nil
		}
		return conn, nil
	}
	connector, err := NewSessionConnector(c, password, dial)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	first, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if later, err := db.Conn(ctx); err == nil {
		later.Close()
		t.Fatal("later physical connection bypassed native product check")
	}
	if foreign == nil || !foreign.closed.Load() {
		t.Fatal("rejected physical handshake leaked its socket")
	}
	if _, err = first.ExecContext(ctx, "SELECT 1"); err != nil {
		t.Fatal("rejected later handshake closed earlier original connection", err)
	}
}

// Gate only the captured query socket's cleanup; independent cancellation
// connections and the observer still talk to the actual native server.
type cleanupGateConn struct {
	net.Conn
	started chan struct{}
	allow   chan struct{}
	once    sync.Once
}

// Hold the real target-selection DataRow until the handshake is canceled.
// Authentication and earlier server frames still pass through unchanged.
type targetSelectionGate struct {
	net.Conn
	ctx     context.Context
	pending []byte
}

func (c *targetSelectionGate) Read(p []byte) (int, error) {
	if len(c.pending) == 0 {
		var header [5]byte
		if _, err := io.ReadFull(c.Conn, header[:]); err != nil {
			return 0, err
		}
		length := int(binary.BigEndian.Uint32(header[1:])) - 4
		if length < 0 || length > 1<<20 {
			return 0, errors.New("invalid native target frame")
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(c.Conn, payload); err != nil {
			return 0, err
		}
		if header[0] == 'D' {
			<-c.ctx.Done()
			return 0, c.ctx.Err()
		}
		c.pending = append(header[:], payload...)
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func TestNativeGaussDBCanceledTargetSelectionWaitsForCleanupFixture(t *testing.T) {
	c, password := nativeFixture(t)
	c.TLSMode, c.Options.CAFile, c.TargetSessionAttrs = "disable", "", "read-write"
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	gate := &cleanupGateConn{started: make(chan struct{}), allow: make(chan struct{})}
	var release, once sync.Once
	defer release.Do(func() { close(gate.allow) })
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		wrapped := conn
		once.Do(func() { gate.Conn = &targetSelectionGate{Conn: conn, ctx: ctx}; wrapped = gate })
		return wrapped, nil
	}
	connector, err := NewSessionConnector(c, password, dial)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { release.Do(func() { close(gate.allow) }); db.Close() }()
	connect, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- db.PingContext(connect) }()
	select {
	case <-gate.started:
	case <-ctx.Done():
		t.Fatal("target selection did not reach original cleanup")
	}
	select {
	case err := <-done:
		t.Fatal("canceled target published before original cleanup", err)
	default:
	}
	release.Do(func() { close(gate.allow) })
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("canceled target selection published a successful handshake")
		}
	case <-ctx.Done():
		t.Fatal("target cleanup did not acknowledge completion")
	}
}

func (c *cleanupGateConn) Close() error {
	c.once.Do(func() { close(c.started) })
	<-c.allow
	return c.Conn.Close()
}

func TestNativeGaussDBSessionWaitsForOriginalCleanupFixture(t *testing.T) {
	c, password := nativeFixture(t)
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	gate := &cleanupGateConn{started: make(chan struct{}), allow: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(gate.allow) })
	var once sync.Once
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		wrapped := conn
		once.Do(func() { gate.Conn = conn; wrapped = gate })
		return wrapped, nil
	}
	connector, err := NewSessionConnector(c, password, dial)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	defer release.Do(func() { close(gate.allow) })
	var native *gaussdbconn.GaussdbConn
	if err = conn.Raw(func(v any) error { native = v.(*sessionConn).Conn.Conn().GaussdbConn(); return nil }); err != nil {
		t.Fatal(err)
	}
	query, cancel := context.WithTimeout(ctx, 75*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := conn.ExecContext(query, "SELECT pg_sleep(30)"); done <- err }()
	select {
	case <-gate.started:
	case <-ctx.Done():
		t.Fatal("captured original socket did not reach cleanup")
	}
	select {
	case err := <-done:
		t.Fatal("cancellation completed before original cleanup", err)
	default:
	}
	select {
	case <-native.CleanupDone():
		t.Fatal("native cleanup completed before closing original socket")
	default:
	}
	release.Do(func() { close(gate.allow) })
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("canceled query reported success")
		}
	case <-ctx.Done():
		t.Fatal("cleanup acknowledgement did not settle")
	}
	select {
	case <-native.CleanupDone():
	default:
		t.Fatal("wrapper published before native cleanup acknowledgement")
	}
	if _, err = conn.ExecContext(ctx, "SELECT 1"); err == nil {
		t.Fatal("canceled original handle silently replaced")
	}
}

func TestNativeGaussDBPreparedRowsTransactionsAndStreamingCancellationFixture(t *testing.T) {
	c, password := nativeFixture(t)
	connector, err := NewSessionConnector(c, password, nil)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	ctx, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stmt, err := conn.PrepareContext(ctx, "SELECT $1::numeric(40,20),$2::bytea")
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	rows, err := stmt.QueryContext(ctx, "9007199254740993.12345678901234567890", []byte{0, 255})
	if err != nil {
		t.Fatal(err)
	}
	types, err := rows.ColumnTypes()
	if err != nil || len(types) != 2 || types[0].DatabaseTypeName() != "NUMERIC" || types[1].DatabaseTypeName() != "BYTEA" {
		t.Fatal("native prepared rows lost type metadata", err, types)
	}
	rows.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "CREATE TEMP TABLE native_tx(id bigint)"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(ctx, "SELECT * FROM native_tx"); err == nil {
		t.Fatal("native rollback failed")
	}
	stream, cancel := context.WithCancel(ctx)
	rows, err = conn.QueryContext(stream, "SELECT i,repeat('x',2048) FROM generate_series(1,100000) i")
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatal("native streaming returned no row", rows.Err())
	}
	cancel()
	rows.Close()
	if _, err = conn.ExecContext(ctx, "SELECT 1"); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("canceled streaming session not retired", err)
	}
}
