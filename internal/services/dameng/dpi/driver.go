package dpi

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"
)

var (
	ErrSessionClosed  = errors.New("DM8 original physical session is closed")
	ErrOutcomeUnknown = errors.New("DM8 operation outcome is unknown; do not replay")
)

// Options configure an explicitly installed native client's session boundary.
// Remote endpoints require an explicit TLS policy. Workbench registration is
// separate; omitting TLS retains the original loopback-only verification mode.
type Options struct {
	LibraryFile             string
	Host                    string
	Port                    uint16
	Username                string
	Schema                  string
	ConnectTimeoutSeconds   int
	OperationTimeoutSeconds int
	TLS                     *TLSOptions
}

type connector struct {
	a        *api
	o        Options
	password string
	tls      *tls.Config
}

func normalizeOptions(o Options, password string) (Options, *tls.Config, error) {
	if o.Host == "" || o.Username == "" || strings.ContainsAny(o.Host, "@/\\?#[] \t\r\n\x00") || strings.Contains(o.Host, ":") && net.ParseIP(o.Host) == nil || strings.ContainsRune(o.Username+o.Schema+password, 0) {
		return Options{}, nil, errors.New("invalid explicit DM8 endpoint or identity")
	}
	if address := net.ParseIP(o.Host); o.TLS == nil && (address == nil || !address.IsLoopback()) {
		return Options{}, nil, errors.New("DM8 DPI verification currently requires an explicit loopback address")
	}
	if len(o.Username) > 128 || len(o.Schema) > 128 || len(password) > 512 {
		return Options{}, nil, errors.New("DM8 native identity exceeds its explicit bounds")
	}
	if o.Schema != "" && !validInitialSchema(o.Schema) {
		return Options{}, nil, errors.New("invalid bounded DM8 initial schema")
	}
	if o.Port == 0 {
		o.Port = 5236
	}
	if o.ConnectTimeoutSeconds == 0 {
		o.ConnectTimeoutSeconds = 10
	}
	if o.OperationTimeoutSeconds == 0 {
		o.OperationTimeoutSeconds = 10
	}
	if o.ConnectTimeoutSeconds < 1 || o.ConnectTimeoutSeconds > 120 || o.OperationTimeoutSeconds < 1 || o.OperationTimeoutSeconds > 120 {
		return Options{}, nil, errors.New("invalid bounded DM8 native timeout")
	}
	var secure *tls.Config
	if o.TLS != nil {
		var normalized TLSOptions
		var err error
		secure, normalized, err = remoteTLS(o.Host, *o.TLS)
		if err != nil {
			return Options{}, nil, err
		}
		o.TLS = &normalized
	}
	return o, secure, nil
}

// ValidateOptions performs all public configuration checks without dlopen.
func ValidateOptions(o Options, password string) (Options, error) {
	o, _, err := normalizeOptions(o, password)
	return o, err
}

func NewConnector(o Options, password string) (driver.Connector, error) {
	o, secure, err := normalizeOptions(o, password)
	if err != nil {
		return nil, err
	}
	a, err := load(o.LibraryFile)
	if err != nil {
		return nil, err
	}
	return &connector{a: a, o: o, password: password, tls: secure}, nil
}

func (c *connector) Driver() driver.Driver { return driverType{} }

type driverType struct{}

func (driverType) Open(string) (driver.Conn, error) {
	return nil, errors.New("DM8 requires an explicit native connector")
}

func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.o.ConnectTimeoutSeconds)*time.Second)
	defer cancel()
	d := &conn{a: c.a, options: c.o, statements: make(map[uintptr]*statement)}
	if !succeeded(c.a.allocEnv(&d.env)) {
		return nil, errors.New("DM8 native environment allocation failed")
	}
	complete := false
	defer func() {
		if !complete {
			d.retireLocked()
		}
	}()
	if !succeeded(c.a.setEnv(d.env, 12345, 1, 0)) {
		return nil, c.a.error(1, d.env, "UTF-8 environment")
	}
	if !succeeded(c.a.allocCon(d.env, &d.h)) {
		return nil, c.a.error(1, d.env, "connection allocation")
	}
	var local *tls.Config
	if c.tls != nil {
		var err error
		d.localTLS, err = newLocalTLS(c.o.Username)
		if err != nil {
			return nil, errors.New("DM8 private native TLS material is unavailable")
		}
		local = d.localTLS.server
	}
	owned, err := openConfiguredTransport(ctx, net.JoinHostPort(c.o.Host, strconv.Itoa(int(c.o.Port))), time.Duration(c.o.ConnectTimeoutSeconds)*time.Second, c.o.TLS != nil, c.tls, local)
	if err != nil {
		return nil, err
	}
	d.transport = owned
	port := d.transport.listener.Addr().(*net.TCPAddr).Port
	seconds := c.o.ConnectTimeoutSeconds
	if deadline, ok := ctx.Deadline(); ok {
		seconds = min(seconds, max(1, int(math.Ceil(time.Until(deadline).Seconds()))))
	}
	for _, attr := range []struct {
		id    int32
		value uintptr
	}{
		// Login/connection I/O attributes use milliseconds. In the 2026 client,
		// passing 10 produces a 10 ms SO_RCVTIMEO and can abort before verified
		// remote TLS finishes. The statement query timeout uses seconds.
		{103, uintptr(seconds * 1000)}, {113, uintptr(c.o.OperationTimeoutSeconds * 1000)}, {12350, uintptr(port)},
		{12345, 1}, {12412, 0}, {12408, 0}, {12424, 0}, {12359, 0}, {102, 1},
		{12358, 0}, {12403, 1}, {12413, 0},
	} {
		if !succeeded(c.a.setCon(d.h, attr.id, attr.value, 0)) {
			return nil, c.a.error(2, d.h, fmt.Sprintf("connection attribute %d", attr.id))
		}
	}
	var pin runtime.Pinner
	defer pin.Unpin()
	if d.localTLS != nil {
		path := append([]byte(d.localTLS.directory), 0)
		pin.Pin(&path[0])
		if !succeeded(c.a.setCon(d.h, 12401, uintptr(unsafe.Pointer(&path[0])), int32(len(d.localTLS.directory)))) {
			return nil, c.a.error(2, d.h, "private native TLS configuration")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Join the original login after closing its captured I/O on cancellation.
	join := d.transport.watch(ctx)
	rc := c.a.login(d.h, "127.0.0.1", c.o.Username, c.password)
	join()
	if !succeeded(rc) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := d.transport.startupFailure(); err != nil {
			return nil, errors.Join(err, c.a.error(2, d.h, "authentication"))
		}
		return nil, c.a.error(2, d.h, "authentication")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.o.Schema != "" {
		if err := InitializeSchema(ctx, d, c.o.Schema); err != nil {
			return nil, err
		}
	}
	complete = true
	return d, nil
}

func (c *conn) requestLocked(ctx context.Context, query string, values []driver.NamedValue) (*statement, error) {
	if len(values) != 0 {
		return c.prepareLocked(ctx, query)
	}
	if strings.ContainsRune(query, 0) || len(query) > 1<<20 {
		return nil, errors.New("invalid bounded DM8 SQL")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, err := c.allocateLocked()
	if err == nil {
		s.direct = query
	}
	return s, err
}

type conn struct {
	mu            sync.Mutex
	a             *api
	env, h        uintptr
	options       Options
	transport     *transport
	localTLS      *localTLS
	statements    map[uintptr]*statement
	inTransaction bool
}

func (c *conn) retireLocked() {
	c.transport.Close()
	for _, s := range c.statements {
		if s.h != 0 {
			c.a.freeStmt(s.h)
			s.h = 0
		}
	}
	clear(c.statements)
	if c.h != 0 {
		c.a.logout(c.h)
		c.a.freeCon(c.h)
		c.h = 0
	}
	if c.env != 0 {
		c.a.freeEnv(c.env)
		c.env = 0
	}
	if c.localTLS != nil {
		os.RemoveAll(c.localTLS.directory)
		c.localTLS = nil
	}
}

func (c *conn) Close() error  { c.mu.Lock(); defer c.mu.Unlock(); c.retireLocked(); return nil }
func (c *conn) IsValid() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.h != 0 }
func (c *conn) ResetSession(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.h == 0 {
		return ErrSessionClosed
	}
	if c.inTransaction {
		c.retireLocked()
		return ErrSessionClosed
	}
	return ctx.Err()
}

func (c *conn) CheckNamedValue(v *driver.NamedValue) error {
	if v.Name != "" {
		return errors.New("DM8 native parameters must be positional")
	}
	if _, ok := v.Value.(Decimal); ok {
		return nil
	}
	if out, ok := v.Value.(sql.Out); ok {
		_, err := OutputKind(out)
		return err
	}
	return driver.ErrSkip
}

// The outer operation scope also covers allocation, attributes and metadata:
// those native calls can perform network I/O before/after statement execution.
// Rows retain their caller context, not this temporary operation deadline.
func (c *conn) operationLocked(ctx context.Context, write bool) (context.Context, func(*error)) {
	if c.h == 0 || ctx.Err() != nil {
		return ctx, func(*error) {}
	}
	bounded, cancel := context.WithTimeout(ctx, time.Duration(c.options.OperationTimeoutSeconds)*time.Second)
	join := c.transport.watch(bounded)
	return bounded, func(result *error) {
		join()
		if err := bounded.Err(); err != nil {
			c.retireLocked()
			*result = errors.Join(*result, err)
			if write {
				*result = errors.Join(*result, ErrOutcomeUnknown)
			}
		}
		cancel()
	}
}

// runLocked never returns driver.ErrBadConn. Cancellation closes only this
// physical session's I/O and joins the native call before releasing handles.
func (c *conn) runLocked(ctx context.Context, s *statement, write bool, op string, call func() int16) error {
	if c.h == 0 || s.h == 0 {
		return ErrSessionClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.options.OperationTimeoutSeconds)*time.Second)
	defer cancel()
	join := c.transport.watch(ctx)
	rc := call()
	join()
	if err := ctx.Err(); err != nil {
		c.retireLocked()
		if write {
			return errors.Join(err, ErrOutcomeUnknown)
		}
		return err
	}
	if succeeded(rc) {
		return nil
	}
	err := c.a.error(3, s.h, op)
	var dead uint32
	var length int32
	if !succeeded(c.a.getCon(c.h, 1209, unsafe.Pointer(&dead), 4, &length)) || dead != 0 {
		c.retireLocked()
		if write {
			return errors.Join(err, ErrOutcomeUnknown)
		}
		return errors.Join(err, ErrSessionClosed)
	}
	return err
}

func (c *conn) allocateLocked() (*statement, error) {
	if c.h == 0 {
		return nil, ErrSessionClosed
	}
	s := &statement{c: c}
	if !succeeded(c.a.allocStmt(c.h, &s.h)) {
		return nil, c.a.error(2, c.h, "statement allocation")
	}
	c.statements[s.h] = s
	return s, nil
}

func (c *conn) prepareLocked(ctx context.Context, query string) (*statement, error) {
	if strings.ContainsRune(query, 0) || len(query) > 1<<20 {
		return nil, errors.New("invalid bounded DM8 SQL")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, err := c.allocateLocked()
	if err != nil {
		return nil, err
	}
	if err = c.runLocked(ctx, s, false, "prepare", func() int16 { return c.a.prepare(s.h, query) }); err != nil {
		s.freeLocked()
		return nil, err
	}
	var count int16
	if !succeeded(c.a.nParams(s.h, &count)) || count < 0 {
		s.freeLocked()
		return nil, errors.New("DM8 native parameter metadata is invalid")
	}
	s.count = int(count)
	return s, nil
}

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}
func (c *conn) PrepareContext(ctx context.Context, query string) (value driver.Stmt, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, finish := c.operationLocked(ctx, false)
	defer finish(&err)
	return c.prepareLocked(ctx, query)
}
func (c *conn) Ping(ctx context.Context) error {
	rows, err := c.QueryContext(ctx, "SELECT 1", nil)
	if err != nil {
		return err
	}
	return errors.Join(rows.Next(make([]driver.Value, 1)), rows.Close(), ctx.Err())
}

func (c *conn) ExecContext(ctx context.Context, query string, values []driver.NamedValue) (value driver.Result, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, finish := c.operationLocked(ctx, true)
	s, err := c.requestLocked(ctx, query, values)
	if err != nil {
		finish(&err)
		return nil, err
	}
	defer func() { finish(&err); s.finishOutputsLocked(err) }()
	defer s.freeLocked()
	return s.execLocked(ctx, values)
}
func (c *conn) QueryContext(ctx context.Context, query string, values []driver.NamedValue) (value driver.Rows, err error) {
	if err := rejectQueryOutputs(values); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	operation, finish := c.operationLocked(ctx, true)
	defer finish(&err)
	s, err := c.requestLocked(operation, query, values)
	if err != nil {
		return nil, err
	}
	r, err := s.queryLocked(operation, values)
	if err != nil {
		s.freeLocked()
		return nil, err
	}
	r.ownsStatement = true
	r.ctx = ctx
	return r, nil
}

func (c *conn) Begin() (driver.Tx, error) { return c.BeginTx(context.Background(), driver.TxOptions{}) }
func (c *conn) BeginTx(ctx context.Context, o driver.TxOptions) (value driver.Tx, err error) {
	if o.Isolation != 0 && o.Isolation != driver.IsolationLevel(sql.LevelReadCommitted) || o.ReadOnly {
		return nil, errors.New("DM8 native transaction options are not verified")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.h == 0 {
		return nil, ErrSessionClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.inTransaction {
		return nil, errors.New("DM8 transaction is already active")
	}
	_, finish := c.operationLocked(ctx, false)
	defer finish(&err)
	if !succeeded(c.a.setCon(c.h, 108, 1, 0)) || !succeeded(c.a.setCon(c.h, 102, 0, 0)) {
		c.retireLocked()
		return nil, ErrSessionClosed
	}
	c.inTransaction = true
	return &transaction{c: c, ctx: ctx}, nil
}

type transaction struct {
	c   *conn
	ctx context.Context
}

func (t *transaction) Commit() error { return t.end(t.ctx, "COMMIT") }
func (t *transaction) Rollback() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(t.c.options.OperationTimeoutSeconds)*time.Second)
	defer cancel()
	return t.end(ctx, "ROLLBACK")
}
func (t *transaction) end(ctx context.Context, query string) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if _, err := t.c.ExecContext(ctx, query, nil); err != nil {
		if query == "COMMIT" {
			return errors.Join(err, ErrOutcomeUnknown)
		}
		return err
	}
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	if query == "COMMIT" {
		defer func() {
			if err != nil {
				err = errors.Join(err, ErrOutcomeUnknown)
			}
		}()
	}
	if t.c.h == 0 {
		return ErrSessionClosed
	}
	if err = ctx.Err(); err != nil {
		t.c.retireLocked()
		return err
	}
	_, finish := t.c.operationLocked(ctx, query == "COMMIT")
	defer finish(&err)
	if !succeeded(t.c.a.setCon(t.c.h, 102, 1, 0)) {
		t.c.retireLocked()
		return ErrSessionClosed
	}
	t.c.inTransaction = false
	return nil
}

type statement struct {
	c       *conn
	h       uintptr
	count   int
	busy    bool
	direct  string
	outputs []func()
}

func (s *statement) NumInput() int { return s.count }
func (s *statement) freeLocked() {
	if s.h != 0 {
		s.c.a.freeStmt(s.h)
		delete(s.c.statements, s.h)
		s.h = 0
	}
}
func (s *statement) Close() (err error) {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	_, finish := s.c.operationLocked(context.Background(), false)
	defer finish(&err)
	s.freeLocked()
	return nil
}
func (s *statement) Exec(values []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), named(values))
}
func (s *statement) Query(values []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), named(values))
}
func named(values []driver.Value) []driver.NamedValue {
	n := make([]driver.NamedValue, len(values))
	for i, v := range values {
		n[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return n
}
func (s *statement) ExecContext(ctx context.Context, values []driver.NamedValue) (value driver.Result, err error) {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	ctx, finish := s.c.operationLocked(ctx, true)
	defer func() { finish(&err); s.finishOutputsLocked(err) }()
	return s.execLocked(ctx, values)
}
func (s *statement) QueryContext(ctx context.Context, values []driver.NamedValue) (value driver.Rows, err error) {
	if err := rejectQueryOutputs(values); err != nil {
		return nil, err
	}
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	operation, finish := s.c.operationLocked(ctx, true)
	defer finish(&err)
	r, err := s.queryLocked(operation, values)
	if r != nil {
		r.ctx = ctx
	}
	return r, err
}

func (s *statement) executeLocked(ctx context.Context, values []driver.NamedValue, write bool) error {
	s.outputs = nil
	if s.h == 0 || s.c.h == 0 {
		return ErrSessionClosed
	}
	if s.busy {
		return errors.New("DM8 statement still owns an open result")
	}
	if len(values) != s.count {
		return errors.New("DM8 parameter count mismatch")
	}
	outputCount := 0
	for _, v := range values {
		if out, ok := v.Value.(sql.Out); ok {
			if _, err := OutputKind(out); err != nil {
				return err
			}
			outputCount++
		}
	}
	if outputCount > MaxOutputParameters {
		return errors.New("DM8 output parameter count exceeds its bound")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	seconds := s.c.options.OperationTimeoutSeconds
	if deadline, ok := ctx.Deadline(); ok {
		seconds = min(seconds, max(1, int(math.Ceil(time.Until(deadline).Seconds()))))
	}
	if !succeeded(s.c.a.setStmt(s.h, 0, uintptr(seconds), 0)) {
		return s.c.a.error(3, s.h, "bounded statement timeout")
	}
	bindings := make([]*binding, 0, len(values))
	defer func() {
		if s.h != 0 && len(bindings) > 0 && !succeeded(s.c.a.unBind(s.h)) {
			s.freeLocked()
		}
		for _, b := range bindings {
			b.release()
		}
	}()
	for i, v := range values {
		if v.Name != "" || v.Ordinal != i+1 {
			return errors.New("DM8 parameters must be consecutive positional values")
		}
		b, err := newBinding(v.Value)
		if err != nil {
			return err
		}
		bindings = append(bindings, b)
		rc := s.c.a.bind(s.h, uint16(i+1), b.direction, b.ctype, b.dtype, b.precision, b.scale, b.pointer(), int64(len(b.bytes)), &b.length)
		if !succeeded(rc) || b.output != nil && rc != 0 {
			return s.c.a.error(3, s.h, "parameter binding")
		}
	}
	var executionRC int16
	err := s.c.runLocked(ctx, s, write, "execution", func() int16 {
		if s.direct != "" {
			executionRC = s.c.a.execDirect(s.h, s.direct)
		} else {
			executionRC = s.c.a.execute(s.h)
		}
		return executionRC
	})
	if err != nil {
		return err
	}
	if outputCount > 0 && executionRC != 0 {
		err := s.c.a.error(3, s.h, "output execution warning")
		s.c.retireLocked()
		return errors.Join(err, ErrSessionClosed, ErrOutcomeUnknown)
	}
	for _, b := range bindings {
		if b.output == nil {
			continue
		}
		set, err := b.stageOutput()
		if err != nil {
			s.outputs = nil
			s.c.retireLocked()
			return errors.Join(err, ErrSessionClosed, ErrOutcomeUnknown)
		}
		s.outputs = append(s.outputs, set)
	}
	return nil
}

func (s *statement) finishOutputsLocked(err error) {
	if err == nil {
		for _, set := range s.outputs {
			set()
		}
	}
	s.outputs = nil
}

func rejectQueryOutputs(values []driver.NamedValue) error {
	for _, v := range values {
		if _, ok := v.Value.(sql.Out); ok {
			return errors.New("DM8 output parameters require ExecContext")
		}
	}
	return nil
}

func (s *statement) execLocked(ctx context.Context, values []driver.NamedValue) (driver.Result, error) {
	if err := s.executeLocked(ctx, values, true); err != nil {
		return nil, err
	}
	var changed int64
	if !succeeded(s.c.a.rowCount(s.h, &changed)) {
		err := s.c.a.error(3, s.h, "affected rows")
		if len(s.outputs) > 0 {
			s.c.retireLocked()
			err = errors.Join(err, ErrSessionClosed, ErrOutcomeUnknown)
		}
		return nil, err
	}
	return result(changed), nil
}
func (s *statement) queryLocked(ctx context.Context, values []driver.NamedValue) (*rows, error) {
	// QueryContext can contain a statement with side effects. Lost execution
	// acknowledgements are conservative even when the caller expects rows.
	if err := s.executeLocked(ctx, values, true); err != nil {
		return nil, err
	}
	var count int16
	if !succeeded(s.c.a.nColumns(s.h, &count)) || count < 0 {
		return nil, errors.New("DM8 result column metadata is invalid")
	}
	r := &rows{s: s, ctx: ctx, columns: make([]column, count)}
	for i := range r.columns {
		name := make([]byte, 4096)
		var length int16
		col := &r.columns[i]
		if !succeeded(s.c.a.column(s.h, int16(i+1), unsafe.Pointer(&name[0]), int16(len(name)), &length, &col.kind, &col.size, &col.scale, &col.nullable)) || length < 0 || int(length) >= len(name) {
			return nil, errors.New("DM8 result column description is incomplete")
		}
		col.name = string(name[:length])
	}
	s.busy = true
	return r, nil
}

type result int64

func (r result) LastInsertId() (int64, error) {
	return 0, errors.New("DM8 generated identity requires explicit RETURNING")
}
func (r result) RowsAffected() (int64, error) {
	if r < 0 {
		return 0, errors.New("DM8 affected row count is unknown")
	}
	return int64(r), nil
}

// ClientVersion reads the original physical connection's native API version.
func ClientVersion(connection *sql.Conn) (string, error) {
	var version string
	err := connection.Raw(func(raw any) error {
		c, ok := raw.(*conn)
		if !ok {
			return errors.New("not a DM8 DPI physical connection")
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.h == 0 {
			return ErrSessionClosed
		}
		buffer := make([]byte, 1024)
		var length int32
		if !succeeded(c.a.getCon(c.h, 12407, unsafe.Pointer(&buffer[0]), int32(len(buffer)), &length)) || length < 0 || int(length) >= len(buffer) {
			return errors.New("DM8 client version is unavailable")
		}
		version = string(buffer[:length])
		return nil
	})
	return version, err
}

var _ driver.Connector = (*connector)(nil)
var _ driver.Conn = (*conn)(nil)
var _ driver.ExecerContext = (*conn)(nil)
var _ driver.QueryerContext = (*conn)(nil)
var _ driver.Validator = (*conn)(nil)
