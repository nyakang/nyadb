package kingbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"testing"
	"time"
)

type liveGateConnector struct {
	driver.Connector
	connections []*liveGateConn
}

func (c *liveGateConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	wrapped := &liveGateConn{Conn: conn, reject: len(c.connections) == 1}
	c.connections = append(c.connections, wrapped)
	return wrapped, nil
}

type liveGateConn struct {
	driver.Conn
	reject, closed bool
}

func (c *liveGateConn) Close() error { c.closed = true; return c.Conn.Close() }
func (c *liveGateConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if c.reject {
		return &gateRows{version: "PostgreSQL 17.6"}, nil
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}

func TestNativeKingbaseGateRetiresOnlyRejectedPhysicalSession(t *testing.T) {
	cfg, password := nativeFixture(t)
	base, err := NewConnector(cfg, password, nil)
	if err != nil {
		t.Fatal(err)
	}
	observed := &liveGateConnector{Connector: base}
	gate := sessionConnector{base: observed, timeout: 10 * time.Second}
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	first, err := gate.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := gate.Connect(ctx); !errors.Is(err, contract.ErrUnsupported) || second != nil {
		t.Fatal("new physical session bypassed native gate", err)
	}
	if observed.connections[0].closed || !observed.connections[1].closed {
		t.Fatal("native gate closed the wrong physical handle")
	}
	// The gate consumed real native identity on the first physical socket; a
	// later product mismatch uses the same gate and must preserve that socket.
	rows, err := first.(driver.QueryerContext).QueryContext(ctx, "SELECT 42::bigint", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	v := make([]driver.Value, 1)
	if err = rows.Next(v); err != nil || v[0] != int64(42) {
		t.Fatal("original native socket was revoked", err, v)
	}
}
