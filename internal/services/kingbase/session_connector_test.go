package kingbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"io"
	"testing"
	"time"
)

const verifiedKingbaseVersion = "KingbaseES V008R006C009B0014 on x86_64-pc-linux-gnu"

func TestKingbaseProductGateRejectsUnverifiedModesAndVersions(t *testing.T) {
	for _, v := range []struct{ version, mode string }{{verifiedKingbaseVersion, "oracle"}, {verifiedKingbaseVersion, "mysql"}, {"KingbaseES V008R002C001B0102 on x86_64", "pg"}, {"PostgreSQL 17.6", "pg"}, {"openGauss 6.0.0", "pg"}, {"gaussdb (GaussDB Kernel 503.1.0)", "pg"}, {verifiedKingbaseVersion, "PG"}} {
		if !errors.Is(validateProduct(v.version, v.mode), contract.ErrUnsupported) {
			t.Fatal("unverified product published", v)
		}
	}
	if err := validateProduct(verifiedKingbaseVersion, "pg"); err != nil {
		t.Fatal(err)
	}
}

type gateConnector struct {
	connects    int
	connections []*gateConn
}

func (c *gateConnector) Driver() driver.Driver { return nil }
func (c *gateConnector) Connect(context.Context) (driver.Conn, error) {
	c.connects++
	v := verifiedKingbaseVersion
	if c.connects == 2 {
		v = "PostgreSQL 17.6"
	}
	conn := &gateConn{version: v}
	c.connections = append(c.connections, conn)
	return conn, nil
}

type gateConn struct {
	driver.Conn
	version string
	closed  bool
}

func (c *gateConn) Close() error { c.closed = true; return nil }
func (c *gateConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &gateRows{version: c.version}, nil
}

type gateRows struct {
	version  string
	consumed bool
}

func (*gateRows) Columns() []string { return []string{"version", "mode"} }
func (*gateRows) Close() error      { return nil }
func (r *gateRows) Next(v []driver.Value) error {
	if r.consumed {
		return io.EOF
	}
	r.consumed = true
	v[0], v[1] = r.version, "pg"
	return nil
}

func TestKingbaseGateChecksEachPhysicalHandleAndClosesOnlyRejectedOne(t *testing.T) {
	base := &gateConnector{}
	gate := sessionConnector{base: base, timeout: time.Second}
	first, err := gate.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := gate.Connect(context.Background()); !errors.Is(err, contract.ErrUnsupported) || second != nil {
		t.Fatal("second physical connection bypassed product gate", err)
	}
	if base.connections[0].closed || !base.connections[1].closed {
		t.Fatal("gate closed another handle or leaked rejected handle")
	}
}
