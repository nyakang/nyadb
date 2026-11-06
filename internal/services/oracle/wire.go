package oracle

import (
	"context"
	"crypto/tls"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"reflect"
	"sync"
	"sync/atomic"

	goora "github.com/sijms/go-ora/v2"
)

// The upstream cancellation callback can outlive QueryContext. Own cancellation
// at the transport boundary instead: close only the captured original socket,
// wait for the operation/rows to acknowledge it, and never return ErrBadConn
// from an executed operation (database/sql could otherwise replay its writes).
var errWireLost = errors.New("Oracle physical session was lost")

type wireConnector struct {
	dsn  string
	tls  *tls.Config
	init []string
}

func (c *wireConnector) Driver() driver.Driver { return wireDriver{c} }

type wireDriver struct{ connector *wireConnector }

func (d wireDriver) Open(string) (driver.Conn, error) {
	return d.connector.Connect(context.Background())
}

type wireDialer struct {
	mu      sync.Mutex
	sockets []net.Conn
	ctx     context.Context
	revoked bool
}

func (d *wireDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if d.ctx != nil {
		ctx = d.ctx
	}
	c, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err == nil {
		d.mu.Lock()
		if d.revoked || ctx.Err() != nil {
			d.mu.Unlock()
			_ = c.Close()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errWireLost
		}
		d.sockets = append(d.sockets, c)
		d.mu.Unlock()
	}
	return c, err
}
func (d *wireDialer) close() {
	d.mu.Lock()
	d.revoked = true
	sockets := append([]net.Conn(nil), d.sockets...)
	d.mu.Unlock()
	for _, s := range sockets {
		_ = s.Close()
	}
}

func (c *wireConnector) Connect(ctx context.Context) (driver.Conn, error) {
	dialer := &wireDialer{ctx: ctx}
	connector := goora.NewConnector(c.dsn).(*goora.OracleConnector)
	connector.Dialer(dialer)
	if c.tls != nil {
		connector.WithTLSConfig(c.tls.Clone())
	}
	ack := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(ack); dialer.close() })
	raw, err := connector.Connect(context.Background())
	if !stop() {
		<-ack
	}
	if ctx.Err() != nil {
		dialer.close()
		if raw != nil {
			_ = raw.Close()
		}
		return nil, ctx.Err()
	}
	if err != nil {
		dialer.close()
		return nil, err
	}
	w := &wireConn{raw: raw.(*goora.Connection), dialer: dialer}
	for _, sql := range c.init {
		if _, err = w.ExecContext(ctx, sql, nil); err != nil {
			w.Close()
			return nil, err
		}
	}
	return w, nil
}

type wireConn struct {
	mu     sync.Mutex
	raw    *goora.Connection
	dialer *wireDialer
	closed bool
	lost   atomic.Bool
}

func (c *wireConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("use Oracle context-aware execution")
}
func (c *wireConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	c.lost.Store(true)
	c.dialer.close()
	// The captured socket is already destroyed. Logoff cannot acknowledge on
	// it; the server drops the physical session and rolls back pending work.
	_ = c.raw.Close()
	return nil
}
func (c *wireConn) CheckNamedValue(v *driver.NamedValue) error {
	switch v.Value.(type) {
	case goora.TimeStamp, goora.TimeStampTZ, goora.NVarChar, goora.Clob, goora.NClob, goora.Blob, *goora.Number, goora.Out:
		return nil
	}
	return c.raw.CheckNamedValue(v)
}
func (c *wireConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *wireConn) begin(ctx context.Context) (func(), error) {
	c.mu.Lock()
	if c.closed || c.lost.Load() {
		c.mu.Unlock()
		return nil, errWireLost
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	ack := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(ack); c.lost.Store(true); c.dialer.close() })
	return func() {
		if !stop() {
			<-ack
		}
		c.mu.Unlock()
	}, nil
}
func wireError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, driver.ErrBadConn) {
		return errWireLost
	}
	return err
}
func (c *wireConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	done, err := c.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	r, err := c.raw.ExecContext(context.Background(), q, args)
	if connectionLost(err) {
		c.lost.Store(true)
	}
	return r, wireError(ctx, err)
}
func (c *wireConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	done, err := c.begin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := c.raw.QueryContext(context.Background(), q, args)
	if err != nil {
		if connectionLost(err) {
			c.lost.Store(true)
		}
		done()
		return nil, wireError(ctx, err)
	}
	return &wireRows{Rows: r, ctx: ctx, done: done}, nil
}
func (c *wireConn) Ping(ctx context.Context) error {
	r, err := c.QueryContext(ctx, "SELECT 1 FROM dual", nil)
	if err != nil {
		return err
	}
	defer r.Close()
	return r.Next(make([]driver.Value, 1))
}
func (c *wireConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	done, err := c.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	t, err := c.raw.BeginTx(context.Background(), o)
	if err != nil {
		return nil, wireError(ctx, err)
	}
	return &wireTx{owner: c, raw: t, ctx: ctx}, nil
}

type wireTx struct {
	owner *wireConn
	raw   driver.Tx
	ctx   context.Context
}

func (t *wireTx) Commit() error {
	done, err := t.owner.begin(t.ctx)
	if err != nil {
		return err
	}
	defer done()
	return wireError(t.ctx, t.raw.Commit())
}
func (t *wireTx) Rollback() error {
	done, err := t.owner.begin(t.ctx)
	if err != nil {
		t.owner.dialer.close()
		return err
	}
	defer done()
	return wireError(t.ctx, t.raw.Rollback())
}

type wireRows struct {
	driver.Rows
	ctx  context.Context
	done func()
	once sync.Once
}

func (r *wireRows) Close() (err error) {
	r.once.Do(func() { err = r.Rows.Close(); r.done() })
	return wireError(r.ctx, err)
}
func (r *wireRows) Next(v []driver.Value) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	return wireError(r.ctx, r.Rows.Next(v))
}
func (r *wireRows) HasNextResultSet() bool {
	v, ok := r.Rows.(driver.RowsNextResultSet)
	return ok && v.HasNextResultSet()
}
func (r *wireRows) NextResultSet() error {
	v, ok := r.Rows.(driver.RowsNextResultSet)
	if !ok {
		return io.EOF
	}
	return wireError(r.ctx, v.NextResultSet())
}
func (r *wireRows) ColumnTypeDatabaseTypeName(i int) string {
	if v, ok := r.Rows.(driver.RowsColumnTypeDatabaseTypeName); ok {
		return v.ColumnTypeDatabaseTypeName(i)
	}
	return ""
}
func (r *wireRows) ColumnTypeNullable(i int) (bool, bool) {
	if v, ok := r.Rows.(driver.RowsColumnTypeNullable); ok {
		return v.ColumnTypeNullable(i)
	}
	return true, false
}
func (r *wireRows) ColumnTypeLength(i int) (int64, bool) {
	if v, ok := r.Rows.(driver.RowsColumnTypeLength); ok {
		return v.ColumnTypeLength(i)
	}
	return 0, false
}
func (r *wireRows) ColumnTypePrecisionScale(i int) (int64, int64, bool) {
	if v, ok := r.Rows.(driver.RowsColumnTypePrecisionScale); ok {
		return v.ColumnTypePrecisionScale(i)
	}
	return 0, 0, false
}
func (r *wireRows) ColumnTypeScanType(i int) reflect.Type {
	if v, ok := r.Rows.(driver.RowsColumnTypeScanType); ok {
		return v.ColumnTypeScanType(i)
	}
	return reflect.TypeOf("")
}

func (c *wireConn) IsValid() bool { return !c.lost.Load() }
