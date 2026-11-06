package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMariaDBServerProductAndSupportedVersions(t *testing.T) {
	for _, version := range []string{"10.11.14-MariaDB-ubu2204", "11.4.8-MariaDB", "11.8.3-MariaDB-log", "5.5.5-10.11.14-MariaDB"} {
		if err := validateMariaDBVersion(version); err != nil {
			t.Fatalf("supported version %q: %v", version, err)
		}
	}
	for _, version := range []string{"8.4.6", "10.6.20-MariaDB", "5.5.5-MariaDB", "unknown-MariaDB", "11.x.3-MariaDB"} {
		if err := validateMariaDBVersion(version); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatalf("unsupported product/version %q accepted: %v", version, err)
		}
	}
}

type mariaProbeConnector struct{ version string }

func (c mariaProbeConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }
func (c mariaProbeConnector) Connect(context.Context) (driver.Conn, error) {
	return &mariaProbeConn{version: c.version}, nil
}

type mariaProbeConn struct {
	mysqlSessionMockConn
	version string
}

func (c *mariaProbeConn) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	if q != "SELECT VERSION()" {
		return nil, errors.New("unexpected MariaDB probe")
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	return &structureRows{cols: []string{"VERSION()"}, values: [][]driver.Value{{c.version}}}, nil
}

func TestMariaDBFailedProductProbeClosesPoolBeforePublication(t *testing.T) {
	m := NewMySQLSessionManager()
	defer m.Close()
	var opened *sql.DB
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		opened = sql.OpenDB(mariaProbeConnector{version: "8.4.6"})
		return opened, nil
	}
	settings := connections.ActiveSettings{Kind: connections.MariaDB, ProfileID: 42, Host: "localhost", Port: 3306}
	if e := m.Connect(context.Background(), settings, ""); !errors.Is(e, contract.ErrUnsupported) {
		t.Fatal("non-MariaDB server accepted", e)
	}
	if m.IsConnected(42) || m.SessionGeneration(42) != 0 || len(m.pending) != 0 {
		t.Fatal("failed probe published or retained handshake")
	}
	if e := opened.Ping(); e == nil {
		t.Fatal("failed probe leaked original pool")
	}
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(mariaProbeConnector{version: "11.4.12-MariaDB"}), nil
	}
	if e := m.Connect(context.Background(), settings, ""); e != nil || !m.IsConnected(42) {
		t.Fatal("explicit retry failed", e)
	}
}
