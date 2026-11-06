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

func TestOceanBaseServerProductModeAndVersions(t *testing.T) {
	for _, version := range []string{"5.7.25-OceanBase_CE-v4.3.5.6", "5.7.25-OceanBase-v4.4.2.1", "OceanBase_CE-v5.0.0"} {
		if err := validateOceanBaseVersion(version, "MYSQL"); err != nil {
			t.Fatalf("supported product: %v", err)
		}
		if err := validateOceanBaseVersion(version, "ORACLE"); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal("Oracle tenant accepted", err)
		}
	}
	for _, version := range []string{"8.4.6", "5.7.25-OceanBase_CE-v4.2.5.5", "5.7.25-OceanBase_CE-v4.3.4.1", "unknown-OceanBase", "5.7.25-TiDB-v8.5.3"} {
		if err := validateOceanBaseVersion(version, "MYSQL"); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal("unsupported product accepted", err)
		}
	}
}

func TestOceanBaseViewVerificationPreservesOwnerAndCompleteQuery(t *testing.T) {
	ref := ObjectIdentity{Database: "app", Name: "v", Kind: "view"}
	original := ObjectDefinition{ObjectIdentity: ref, Product: "oceanbase", Definer: "u@%", SQL: "CREATE VIEW `v` AS SELECT 'Case' AS marker"}
	plan := ObjectPlan{Original: original, Action: "alter", SQL: original.SQL}
	if err := ObjectDefinitionEquivalent(plan, original, ObjectVerificationContext{CurrentUser: "u@%"}); err != nil {
		t.Fatal(err)
	}
	changed := original
	changed.Definer = "other@%"
	if err := ObjectDefinitionEquivalent(plan, changed, ObjectVerificationContext{CurrentUser: "u@%"}); err == nil {
		t.Fatal("changed view owner accepted")
	}
	changed = original
	changed.SQL = "CREATE VIEW `v` AS SELECT 'case' AS marker"
	if err := ObjectDefinitionEquivalent(plan, changed, ObjectVerificationContext{CurrentUser: "u@%"}); err == nil {
		t.Fatal("changed query literal accepted")
	}
	changed = original
	changed.Definer = "other@%"
	if definitionFingerprint(original) == definitionFingerprint(changed) {
		t.Fatal("owner missing from optimistic fingerprint")
	}
}

type obProbeConnector struct{ version string }

func (c obProbeConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }
func (c obProbeConnector) Connect(context.Context) (driver.Conn, error) {
	return &obProbeConn{version: c.version}, nil
}

type obProbeConn struct {
	mysqlSessionMockConn
	version string
}

func (c *obProbeConn) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	if q != "SELECT VERSION(), @@ob_compatibility_mode" {
		return nil, errors.New("unexpected OceanBase probe")
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	return &structureRows{cols: []string{"VERSION()", "mode"}, values: [][]driver.Value{{c.version, "MYSQL"}}}, nil
}

func TestOceanBaseFailedProductProbeClosesPoolBeforePublication(t *testing.T) {
	m := NewMySQLSessionManager()
	defer m.Close()
	var opened *sql.DB
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		opened = sql.OpenDB(obProbeConnector{version: "8.4.6"})
		return opened, nil
	}
	settings := connections.ActiveSettings{Kind: connections.OceanBase, ProfileID: 42, Host: "localhost", Port: 2881}
	if e := m.Connect(context.Background(), settings, ""); !errors.Is(e, contract.ErrUnsupported) {
		t.Fatal("non-OceanBase server accepted", e)
	}
	if m.IsConnected(42) || m.SessionGeneration(42) != 0 || len(m.pending) != 0 {
		t.Fatal("failed probe published or retained handshake")
	}
	if e := opened.Ping(); e == nil {
		t.Fatal("failed probe leaked original pool")
	}
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(obProbeConnector{version: "5.7.25-OceanBase_CE-v4.3.5.6"}), nil
	}
	if e := m.Connect(context.Background(), settings, ""); e != nil || !m.IsConnected(42) {
		t.Fatal("explicit retry failed", e)
	}
}
