package dameng

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type gateRows struct {
	columns  int
	values   [][]driver.Value
	index    int
	closed   atomic.Bool
	next     func() error
	closeErr error
}

func (r *gateRows) Columns() []string { return make([]string, r.columns) }
func (r *gateRows) Close() error      { r.closed.Store(true); return r.closeErr }
func (r *gateRows) Next(values []driver.Value) error {
	if r.next != nil {
		return r.next()
	}
	if r.index == len(r.values) {
		return io.EOF
	}
	copy(values, r.values[r.index])
	r.index++
	return nil
}

func verifiedRows() (*gateRows, *gateRows) {
	return &gateRows{columns: 1, values: [][]driver.Value{{"DM Database Server 64 V8"}, {"DB Version: 0x7000c"}, {verifiedServerVersion}}},
		&gateRows{columns: 8, values: [][]driver.Value{{"DAMENG", "SYSDBA", "SYSDBA", int64(1), int64(1), int64(0), int64(1), int64(1)}}}
}

func gateConfig() connections.DamengConfig {
	return connections.DamengConfig{Host: "127.0.0.1", Port: 5236, Database: "DAMENG", Username: "SYSDBA", TLSMode: "verify-full", Options: connections.DefaultDamengOptions()}
}

type gateConn struct {
	driver.Conn
	version, identity *gateRows
	closed            atomic.Bool
}

func (c *gateConn) Close() error { c.closed.Store(true); return nil }
func (c *gateConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if len(args) != 0 {
		return nil, errors.New("identity gate unexpectedly submitted user values")
	}
	if query == versionSQL {
		c.version.index = 0
		return c.version, nil
	}
	if query == identitySQL {
		c.identity.index = 0
		return c.identity, nil
	}
	return nil, errors.New("identity gate submitted an unexpected statement")
}

type gateConnector struct {
	driver.Connector
	connections []driver.Conn
	index       int
}

func (c *gateConnector) Connect(context.Context) (driver.Conn, error) {
	v := c.connections[c.index]
	c.index++
	return v, nil
}

func TestDamengGateRejectsOtherVersionsModesAndQualifiedIdentity(t *testing.T) {
	for _, change := range []func(*gateRows, *gateRows){
		func(v, i *gateRows) { v.values[0][0] = "Oracle Database 23ai" },
		func(v, i *gateRows) { v.values[2][0] = "08134283904-20220804-166351-99999" },
		func(v, i *gateRows) { v.values = v.values[:2] },
		func(v, i *gateRows) { v.values = append(v.values, []driver.Value{"extra"}) },
		func(v, i *gateRows) { v.columns = 2 },
		func(v, i *gateRows) { i.columns = 7 },
		func(v, i *gateRows) { i.values[0][0] = "OTHER_CATALOG" },
		func(v, i *gateRows) { i.values[0][1] = "OTHER_ACCOUNT" },
		func(v, i *gateRows) { i.values[0][2] = "OTHER_SCHEMA" },
		func(v, i *gateRows) { i.values[0][3] = int64(0) },
		func(v, i *gateRows) { i.values[0][4] = int64(0) },
		func(v, i *gateRows) { i.values[0][5] = int64(1) },
		func(v, i *gateRows) { i.values[0][6] = int64(0) },
		func(v, i *gateRows) { i.values[0][7] = int64(0) },
		func(v, i *gateRows) { i.values[0][3] = float64(1) },
		func(v, i *gateRows) { i.values = append(i.values, append([]driver.Value(nil), i.values[0]...)) },
	} {
		version, identity := verifiedRows()
		change(version, identity)
		conn := &gateConn{version: version, identity: identity}
		gate := sessionConnector{base: &gateConnector{connections: []driver.Conn{conn}}, config: gateConfig()}
		value, err := gate.Connect(context.Background())
		if value != nil || !errors.Is(err, contract.ErrUnsupported) || !conn.closed.Load() || !version.closed.Load() {
			t.Fatal("unverified native identity was accepted or retained", err)
		}
	}
}

func TestDamengGateChecksEveryPhysicalHandleAndRetiresOnlyRejection(t *testing.T) {
	v1, i1 := verifiedRows()
	v2, i2 := verifiedRows()
	i2.values[0][0] = "OTHER_CATALOG"
	first, second := &gateConn{version: v1, identity: i1}, &gateConn{version: v2, identity: i2}
	gate := sessionConnector{base: &gateConnector{connections: []driver.Conn{first, second}}, config: gateConfig()}
	value, err := gate.Connect(context.Background())
	if err != nil || value != first {
		t.Fatal(err)
	}
	if value, err = gate.Connect(context.Background()); value != nil || !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal(err)
	}
	if first.closed.Load() || !second.closed.Load() || !i1.closed.Load() || !i2.closed.Load() {
		t.Fatal("gate closed another physical handle or leaked rows")
	}
	first.Close()
}

func TestDamengGateWaitsForCanceledInspectionAcknowledgement(t *testing.T) {
	version, identity := verifiedRows()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	version.next = func() error { close(entered); <-ctx.Done(); <-release; return ctx.Err() }
	conn := &gateConn{version: version, identity: identity}
	gate := sessionConnector{base: &gateConnector{connections: []driver.Conn{conn}}, config: gateConfig()}
	result := make(chan error, 1)
	go func() { _, err := gate.Connect(ctx); result <- err }()
	<-entered
	cancel()
	if version.closed.Load() || conn.closed.Load() {
		t.Fatal("canceled inspection released original handles early")
	}
	select {
	case err := <-result:
		t.Fatal("gate returned without original inspection acknowledgement", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-result; !errors.Is(err, context.Canceled) || !version.closed.Load() || !conn.closed.Load() {
		t.Fatal("canceled original inspection was not settled", err)
	}
}

func TestDamengGatePreservesInspectionAndRowsCloseFailures(t *testing.T) {
	version, identity := verifiedRows()
	failure := errors.New("original metadata close failed")
	identity.closeErr = failure
	conn := &gateConn{version: version, identity: identity}
	gate := sessionConnector{base: &gateConnector{connections: []driver.Conn{conn}}, config: gateConfig()}
	if value, err := gate.Connect(context.Background()); value != nil || !errors.Is(err, failure) || !conn.closed.Load() {
		t.Fatal("close failure was published", err)
	}
}

type schemaGateConn struct {
	*gateConn
	schema      string
	events      []string
	execute     func(context.Context) error
	verifyValue string
	afterSet    func()
}

func (c *schemaGateConn) ExecContext(ctx context.Context, sql string, values []driver.NamedValue) (driver.Result, error) {
	c.events = append(c.events, "set")
	if sql != `SET SCHEMA "`+strings.ReplaceAll(c.schema, `"`, `""`)+`"` || len(values) != 0 {
		return nil, errors.New("initial schema was not a single quoted identifier")
	}
	if c.execute != nil {
		if err := c.execute(ctx); err != nil {
			return nil, err
		}
	}
	c.identity.values[0][2] = c.schema
	if c.afterSet != nil {
		c.afterSet()
	}
	return driver.RowsAffected(0), nil
}

func (c *schemaGateConn) QueryContext(ctx context.Context, query string, values []driver.NamedValue) (driver.Rows, error) {
	if query == "SELECT SF_GET_SCHEMA_NAME_BY_ID(CURRENT_SCHID)" {
		c.events = append(c.events, "schema")
		actual := c.schema
		if c.verifyValue != "" {
			actual = c.verifyValue
		}
		return &gateRows{columns: 1, values: [][]driver.Value{{actual}}}, nil
	}
	if query == versionSQL {
		c.events = append(c.events, "version")
	} else {
		c.events = append(c.events, "identity")
	}
	return c.gateConn.QueryContext(ctx, query, values)
}

func schemaGate(schema string) (sessionConnector, *schemaGateConn) {
	version, identity := verifiedRows()
	conn := &schemaGateConn{gateConn: &gateConn{version: version, identity: identity}, schema: schema}
	config := gateConfig()
	config.Options.DefaultSchema = schema
	return sessionConnector{base: &gateConnector{connections: []driver.Conn{conn}}, config: config}, conn
}

func TestDamengGateVerifiesBeforeAndAfterQuotedSchemaInitialization(t *testing.T) {
	gate, conn := schemaGate(`租户 "'; -- 数据`)
	value, err := gate.Connect(context.Background())
	if err != nil || value != conn || conn.closed.Load() {
		t.Fatal("qualified schema initialization failed", err)
	}
	defer value.Close()
	if !reflect.DeepEqual(conn.events, []string{"version", "identity", "set", "schema", "version", "identity"}) {
		t.Fatal("schema was initialized before checking the native product, or was published without reinspection", conn.events)
	}
}

func TestDamengGateRejectsSchemaFailureAndPostInitializationIdentityChange(t *testing.T) {
	failure := errors.New("owned schema setup failed")
	for _, change := range []func(*schemaGateConn){
		func(c *schemaGateConn) { c.execute = func(context.Context) error { return failure } },
		func(c *schemaGateConn) { c.verifyValue = "UNCHANGED_SCHEMA" },
		func(c *schemaGateConn) { c.afterSet = func() { c.identity.values[0][0] = "OTHER_CATALOG" } },
		func(c *schemaGateConn) { c.afterSet = func() { c.identity.values[0][5] = int64(1) } },
		func(c *schemaGateConn) { c.afterSet = func() { c.version.values[2][0] = "OTHER_VERSION" } },
	} {
		gate, conn := schemaGate("OTHER_SCHEMA")
		change(conn)
		value, err := gate.Connect(context.Background())
		if value != nil || err == nil || !conn.closed.Load() {
			t.Fatal("failed schema setup or changed product was published", err)
		}
	}
	gate, conn := schemaGate("OTHER_SCHEMA")
	conn.identity.values[0][1] = "OTHER_ACCOUNT"
	if value, err := gate.Connect(context.Background()); value != nil || !errors.Is(err, contract.ErrUnsupported) || !reflect.DeepEqual(conn.events, []string{"version", "identity"}) {
		t.Fatal("unverified native identity received schema initialization", err, conn.events)
	}
}

func TestDamengGateWaitsForSchemaCancellationAcknowledgement(t *testing.T) {
	gate, conn := schemaGate("OTHER_SCHEMA")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	conn.execute = func(ctx context.Context) error { close(entered); <-ctx.Done(); <-release; return ctx.Err() }
	result := make(chan error, 1)
	go func() { _, err := gate.Connect(ctx); result <- err }()
	<-entered
	cancel()
	if conn.closed.Load() {
		t.Fatal("native schema initialization was released before acknowledgement")
	}
	select {
	case err := <-result:
		t.Fatal("schema cancellation was reported before acknowledgement", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-result; !errors.Is(err, context.Canceled) || !conn.closed.Load() {
		t.Fatal("canceled schema initialization was published", err)
	}
}
