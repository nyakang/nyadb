package dameng

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeDB(t *testing.T, c connections.DamengConfig, password string) *sql.DB {
	t.Helper()
	connector, err := NewSessionConnector(c, password)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(c.Options.MaxConnections)
	t.Cleanup(func() { db.Close() })
	return db
}

func nativeQuote(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

// Independently callable lookalikes must not influence the built-in identity
// functions or source-free expressions used by the connector. This server
// separately reserves the native V$ names against user tables/views.
func nativeIdentityLookalikes(t *testing.T, ctx context.Context, db *sql.DB, schema string) {
	t.Helper()
	prefix := nativeQuote(schema) + "."
	for _, query := range []string{
		"CREATE VIEW " + prefix + `"V$VERSION" AS SELECT 'Oracle Database 23ai' AS BANNER FROM DUAL`,
		"CREATE VIEW " + prefix + `"V$DATABASE" AS SELECT 'OTHER_CATALOG' AS NAME FROM DUAL`,
	} {
		if _, err := db.ExecContext(ctx, query); err == nil || !strings.Contains(err.Error(), "code -2829") {
			t.Fatal("native system-view name reservation changed", err)
		}
	}
	for _, query := range []string{
		"CREATE VIEW " + prefix + `"DUAL" AS SELECT 99 AS DUMMY`,
		"CREATE FUNCTION " + prefix + "SF_GET_UNICODE_FLAG RETURN INTEGER AS BEGIN RETURN 7; END;",
		"CREATE FUNCTION " + prefix + "SF_GET_CASE_SENSITIVE_FLAG RETURN INTEGER AS BEGIN RETURN 7; END;",
		"CREATE FUNCTION " + prefix + "SF_GET_SCHEMA_NAME_BY_ID(V INTEGER) RETURN VARCHAR AS BEGIN RETURN 'OTHER_SCHEMA'; END;",
		"CREATE FUNCTION " + prefix + "SF_GET_PARA_VALUE(V INTEGER, N VARCHAR) RETURN INTEGER AS BEGIN RETURN 7; END;",
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal("owned identity lookalike fixture failed", err)
		}
	}
	var function int
	var dummy int
	if err := db.QueryRowContext(ctx, "SELECT "+prefix+"SF_GET_UNICODE_FLAG(),(SELECT DUMMY FROM "+prefix+`"DUAL")`).Scan(&function, &dummy); err != nil || function != 7 || dummy != 99 {
		t.Fatal("lookalikes were not independently callable", err)
	}
}

func TestNativeDamengQualifiedProductGateAndInitialSchema(t *testing.T) {
	c, password := nativeFixture(t)
	db := nativeDB(t, c, password)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*connections.DamengConfig){
		func(c *connections.DamengConfig) { c.Database = "OTHER_CATALOG" },
		func(c *connections.DamengConfig) { c.Options.DefaultSchema = "SYS" },
	} {
		candidate := c
		change(&candidate)
		other := nativeDB(t, candidate, password)
		err := other.PingContext(ctx)
		other.Close()
		if candidate.Database != c.Database && !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal("another native catalog passed product gate", err)
		}
		if candidate.Database == c.Database && err != nil {
			t.Fatal("explicit initial schema was not accepted", err)
		}
	}
	name := "NYADB_DM_GATE_" + nativeSuffix(t) + ` 租户 "'; --`
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+nativeQuote(name)+" AUTHORIZATION SYSDBA"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.ExecContext(context.Background(), "DROP SCHEMA "+nativeQuote(name)+" CASCADE") })
	nativeIdentityLookalikes(t, ctx, db, name)
	c.Options.DefaultSchema = name
	other := nativeDB(t, c, password)
	first, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for _, physical := range []*sql.Conn{first, second} {
		var actual string
		if err := physical.QueryRowContext(ctx, "SELECT SF_GET_SCHEMA_NAME_BY_ID(CURRENT_SCHID)").Scan(&actual); err != nil || actual != name {
			t.Fatal("physical connection lost the quoted initial schema", err, actual)
		}
	}
	var original string
	if err := db.QueryRowContext(ctx, "SELECT SF_GET_SCHEMA_NAME_BY_ID(CURRENT_SCHID)").Scan(&original); err != nil || original != "SYSDBA" {
		t.Fatal("another schema changed the original profile's connection", err)
	}
	c.Options.DefaultSchema = "NYADB_MISSING_SCHEMA_" + nativeSuffix(t)
	missing := nativeDB(t, c, password)
	if err := missing.PingContext(ctx); err == nil {
		t.Fatal("unavailable schema was silently ignored")
	}
	if err := db.QueryRowContext(ctx, "SELECT SF_GET_SCHEMA_NAME_BY_ID(CURRENT_SCHID)").Scan(&original); err != nil || original != "SYSDBA" {
		t.Fatal("failed schema setup closed another original session", err)
	}
}

func TestNativeDamengOrdinaryAccountCanInspectOriginalProductIdentity(t *testing.T) {
	c, password := nativeFixture(t)
	admin := nativeDB(t, c, password)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	username := "NYADB_DM_USER_" + strings.ToUpper(nativeSuffix(t))
	userPassword := "Aa9!" + nativeSuffix(t)
	if _, err := admin.ExecContext(ctx, "CREATE USER "+username+" IDENTIFIED BY \""+userPassword+"\""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.ExecContext(context.Background(), "DROP USER "+username+" CASCADE") })
	nativeIdentityLookalikes(t, ctx, admin, username)
	c.Username = username
	if c.TLSMode != "disable" {
		folder := t.TempDir()
		c.Options.CertificateFile = filepath.Join(folder, "client-cert.pem")
		c.Options.KeyFile = filepath.Join(folder, "client-key.pem")
		csr, ext := filepath.Join(folder, "client.csr"), filepath.Join(folder, "extensions")
		if err := os.WriteFile(ext, []byte("basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=clientAuth\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{"req", "-new", "-newkey", "rsa:2048", "-nodes", "-keyout", c.Options.KeyFile, "-out", csr, "-subj", "/CN=" + username},
			{"x509", "-req", "-in", csr, "-CA", c.Options.CAFile, "-CAkey", filepath.Join(filepath.Dir(c.Options.CAFile), "..", "ca-key.pem"), "-CAcreateserial", "-out", c.Options.CertificateFile, "-days", "1", "-sha256", "-extfile", ext},
		} {
			if err := exec.Command("openssl", args...).Run(); err != nil {
				t.Fatal("owned ordinary-account certificate generation failed")
			}
		}
		if err := os.Chmod(c.Options.KeyFile, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	user := nativeDB(t, c, userPassword)
	if err := user.PingContext(ctx); err != nil {
		t.Fatal("ordinary-account physical gate failed:", err)
	}
	for _, secret := range []string{"", "incorrect-password"} {
		wrong := nativeDB(t, c, secret)
		err := wrong.PingContext(ctx)
		wrong.Close()
		if err == nil || strings.Contains(err.Error(), userPassword) {
			t.Fatal("ordinary authentication rejection or credential privacy failed")
		}
	}
}

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
func (c *liveGateConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.reject && query == versionSQL {
		version, _ := verifiedRows()
		version.values[0][0] = "Oracle Database 23ai"
		return version, nil
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func TestNativeDamengGateRetiresOnlyLaterRejectedPhysicalConnection(t *testing.T) {
	c, password := nativeFixture(t)
	base, err := NewConnector(c, password)
	if err != nil {
		t.Fatal(err)
	}
	observed := &liveGateConnector{Connector: base}
	gate := sessionConnector{base: observed, config: c}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	first, err := gate.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := gate.Connect(ctx); second != nil || !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("later physical connection bypassed product inspection", err)
	}
	if observed.connections[0].closed || !observed.connections[1].closed {
		t.Fatal("product mismatch closed another native socket")
	}
	rows, err := first.(driver.QueryerContext).QueryContext(ctx, "SELECT 42 FROM DUAL", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	values := make([]driver.Value, 1)
	if err = rows.Next(values); err != nil || values[0] != int64(42) {
		t.Fatal("original native connection was not preserved", err)
	}
}
