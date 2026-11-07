package gaussdb

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	gaussdbgo "github.com/HuaweiCloudDeveloper/gaussdb-go"
	"github.com/HuaweiCloudDeveloper/gaussdb-go/gaussdbconn"
	"github.com/HuaweiCloudDeveloper/gaussdb-go/stdlib"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

var versionPattern = regexp.MustCompile(`^gaussdb \(GaussDB Kernel ([0-9]+)\.([0-9]+)\.([0-9]+) build [a-f0-9]+\)`)

func validateVersion(version string) error {
	m := versionPattern.FindStringSubmatch(version)
	if m == nil || strings.Contains(version, "openGauss") || strings.Contains(version, "DWS") {
		return fmt.Errorf("%w: native GaussDB Kernel server required", contract.ErrUnsupported)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 503 || major == 503 && minor < 1 {
		return fmt.Errorf("%w: GaussDB Kernel 503.1 or later required", contract.ErrUnsupported)
	}
	return nil
}

func validateSession(ctx context.Context, conn *gaussdbgo.Conn) (err error) {
	// This SDK's AfterConnect failure does not close the physical connection.
	// Rejected or canceled handshakes must settle their captured handle here.
	defer func() {
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = conn.Close(cleanup)
			<-conn.GaussdbConn().CleanupDone()
		}
	}()
	var version, compatibility string
	if err := conn.QueryRow(ctx, "SELECT version()").Scan(&version); err != nil {
		return err
	}
	if err := validateVersion(version); err != nil {
		return err
	}
	if err := conn.QueryRow(ctx, "SHOW sql_compatibility").Scan(&compatibility); err != nil {
		return err
	}
	if compatibility != "PG" {
		return fmt.Errorf("%w: GaussDB PG compatibility required", contract.ErrUnsupported)
	}
	return nil
}

// NewSessionConnector verifies every physical connection before publication.
// Its driver wrappers wait for original native cleanup after cancellation or
// connection loss; users must explicitly recover a retired SQL document lease.
func NewSessionConnector(c connections.GaussDBConfig, password string, dial func(context.Context, string, string) (net.Conn, error)) (driver.Connector, error) {
	cfg, err := nativeConfig(c, password, dial)
	if err != nil {
		return nil, err
	}
	validateTarget := cfg.ValidateConnect
	cfg.ValidateConnect = func(ctx context.Context, conn *gaussdbconn.GaussdbConn) error {
		if validateTarget == nil {
			return nil
		}
		err := validateTarget(ctx, conn)
		var preference *gaussdbconn.NotPreferredError
		if err != nil && (!errors.As(err, &preference) || ctx.Err() != nil || conn.IsClosed()) {
			// Target selection runs before stdlib's AfterConnect callback. Its
			// canceled native queries can also leave asynchronous cleanup.
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = conn.Close(cleanup)
			<-conn.CleanupDone()
		}
		// A healthy NotPreferredError is the SDK's primary fallback signal.
		// Its final attempt may return this same connection to the caller.
		return err
	}
	return sessionConnector{base: stdlib.GetConnector(*cfg, stdlib.OptionAfterConnect(validateSession))}, nil
}

type sessionConnector struct{ base driver.Connector }

func (c sessionConnector) Driver() driver.Driver { return c.base.Driver() }
func (c sessionConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	native, ok := conn.(*stdlib.Conn)
	if !ok {
		_ = conn.Close()
		return nil, errors.New("unexpected GaussDB native driver")
	}
	return &sessionConn{Conn: native}, nil
}

type sessionConn struct{ *stdlib.Conn }

func (c *sessionConn) settled(ctx context.Context, err error) error {
	if ctx.Err() != nil && !c.Conn.Conn().IsClosed() {
		_ = c.Conn.Close()
	}
	native := c.Conn.Conn().GaussdbConn()
	if native.IsClosed() {
		// The driver's cleanup has its own bounded network deadline. Waiting on
		// the canceled request context would publish completion before its ack.
		<-native.CleanupDone()
	}
	if err == nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (c *sessionConn) Close() error {
	err := c.Conn.Close()
	return c.settled(context.Background(), err)
}
func (c *sessionConn) Ping(ctx context.Context) error {
	return c.settled(ctx, c.Conn.Ping(ctx))
}
func (c *sessionConn) ResetSession(ctx context.Context) error {
	return c.settled(ctx, c.Conn.ResetSession(ctx))
}
func (c *sessionConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	result, err := c.Conn.ExecContext(ctx, query, args)
	return result, c.settled(ctx, err)
}
func (c *sessionConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Conn.QueryContext(ctx, query, args)
	err = c.settled(ctx, err)
	if err != nil {
		if rows != nil {
			_ = rows.Close()
		}
		return nil, err
	}
	return c.rows(ctx, rows)
}
func (c *sessionConn) rows(ctx context.Context, rows driver.Rows) (driver.Rows, error) {
	native, ok := rows.(*stdlib.Rows)
	if !ok {
		_ = rows.Close()
		return nil, errors.New("unexpected GaussDB native rows")
	}
	return &sessionRows{Rows: native, conn: c, ctx: ctx}, nil
}
func (c *sessionConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}
func (c *sessionConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	stmt, err := c.Conn.PrepareContext(ctx, query)
	err = c.settled(ctx, err)
	if err != nil {
		return nil, err
	}
	native, ok := stmt.(*stdlib.Stmt)
	if !ok {
		_ = stmt.Close()
		return nil, errors.New("unexpected GaussDB native statement")
	}
	return &sessionStmt{Stmt: native, conn: c}, nil
}
func (c *sessionConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *sessionConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.BeginTx(ctx, opts)
	err = c.settled(ctx, err)
	if err != nil {
		return nil, err
	}
	return sessionTx{tx: tx, conn: c, ctx: ctx}, nil
}

type sessionRows struct {
	*stdlib.Rows
	conn *sessionConn
	ctx  context.Context
}

func (r *sessionRows) Next(values []driver.Value) error {
	return r.conn.settled(r.ctx, r.Rows.Next(values))
}
func (r *sessionRows) Close() error { return r.conn.settled(r.ctx, r.Rows.Close()) }

type sessionStmt struct {
	*stdlib.Stmt
	conn *sessionConn
}

func (s *sessionStmt) Close() error {
	return s.conn.settled(context.Background(), s.Stmt.Close())
}
func (s *sessionStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	result, err := s.Stmt.ExecContext(ctx, args)
	return result, s.conn.settled(ctx, err)
}
func (s *sessionStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := s.Stmt.QueryContext(ctx, args)
	if err = s.conn.settled(ctx, err); err != nil {
		if rows != nil {
			_ = rows.Close()
		}
		return nil, err
	}
	return s.conn.rows(ctx, rows)
}
func (s *sessionStmt) Exec(args []driver.Value) (driver.Result, error) {
	result, err := s.Stmt.Exec(args)
	return result, s.conn.settled(context.Background(), err)
}
func (s *sessionStmt) Query(args []driver.Value) (driver.Rows, error) {
	rows, err := s.Stmt.Query(args)
	if err = s.conn.settled(context.Background(), err); err != nil {
		return nil, err
	}
	return s.conn.rows(context.Background(), rows)
}

type sessionTx struct {
	tx   driver.Tx
	conn *sessionConn
	ctx  context.Context
}

func (t sessionTx) Commit() error   { return t.conn.settled(t.ctx, t.tx.Commit()) }
func (t sessionTx) Rollback() error { return t.conn.settled(t.ctx, t.tx.Rollback()) }
