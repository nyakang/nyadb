package native

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"
)

type Options struct {
	LibraryFile               string
	ReadOnly, RequireExisting bool
	MemoryMB, Threads         int
}

type database struct {
	a       *api
	h       uintptr
	refs    int
	options Options
	key     string
}

var databases = struct {
	sync.Mutex
	values map[string]*database
}{values: map[string]*database{}}

// Connector owns a shared native database instance. Both SQL and background
// pools use this same instance, with separate physical connection handles.
type Connector struct {
	db     *database
	once   sync.Once
	closed bool
}

func Open(path string, options Options) (*Connector, error) {
	a, err := load(options.LibraryFile)
	if err != nil {
		return nil, err
	}
	// Resolve paths while holding the registry lock so another opener cannot
	// create the file between canonicalization and the shared-instance lookup.
	databases.Lock()
	defer databases.Unlock()
	if path != ":memory:" {
		path, err = filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		info, e := os.Stat(path)
		if e != nil && (options.ReadOnly || options.RequireExisting || !errors.Is(e, os.ErrNotExist)) {
			return nil, errors.New("DuckDB database file is unavailable")
		}
		if e == nil {
			if !info.Mode().IsRegular() {
				return nil, errors.New("DuckDB database must be a regular file")
			}
			path, err = filepath.EvalSymlinks(path)
			if err != nil {
				return nil, err
			}
		}
	}
	if path != ":memory:" {
		if d := databases.values[path]; d != nil {
			if d.a != a || d.options.ReadOnly != options.ReadOnly || d.options.MemoryMB != options.MemoryMB || d.options.Threads != options.Threads {
				return nil, errors.New("DuckDB file is already open with different runtime settings")
			}
			d.refs++
			return &Connector{db: d}, nil
		}
	}
	var config uintptr
	if a.createConfig(&config) != 0 {
		return nil, errors.New("DuckDB configuration could not be created")
	}
	defer a.destroyConfig(&config)
	mode := "READ_WRITE"
	if options.ReadOnly {
		mode = "READ_ONLY"
	}
	settings := [][2]string{{"access_mode", mode}, {"memory_limit", strconv.Itoa(options.MemoryMB) + "MiB"}, {"threads", strconv.Itoa(options.Threads)}, {"autoinstall_known_extensions", "false"}, {"autoload_known_extensions", "false"}}
	for _, setting := range settings {
		if a.setConfig(config, setting[0], setting[1]) != 0 {
			return nil, fmt.Errorf("DuckDB configuration %s is unavailable", setting[0])
		}
	}
	var h uintptr
	var message unsafe.Pointer
	if a.open(path, &h, config, &message) != 0 {
		a.free(message)
		if h != 0 {
			a.close(&h)
		}
		return nil, errors.New("DuckDB database could not be opened; check its format, permissions and file lock")
	}
	a.free(message)
	if path != ":memory:" {
		// A newly created file can now be resolved, including directory links
		// and Windows path casing/short names. Store the same key future opens use.
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			a.close(&h)
			return nil, err
		}
	}
	d := &database{a: a, h: h, refs: 1, options: options, key: path}
	if path != ":memory:" {
		databases.values[path] = d
	}
	return &Connector{db: d}, nil
}
func (c *Connector) Close() {
	c.once.Do(func() {
		databases.Lock()
		defer databases.Unlock()
		c.closed = true
		d := c.db
		releaseDatabase(d)
	})
}

// Call with databases locked. Active physical connections retain the instance
// even when database/sql closes a pool before its canceled jobs have settled.
func releaseDatabase(d *database) {
	d.refs--
	if d.refs == 0 {
		delete(databases.values, d.key)
		d.a.close(&d.h)
	}
}
func (c *Connector) Driver() driver.Driver { return driverType{} }
func (c *Connector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	databases.Lock()
	defer databases.Unlock()
	if c.closed || c.db.h == 0 {
		return nil, errors.New("DuckDB database is closed")
	}
	var h uintptr
	if c.db.a.connect(c.db.h, &h) != 0 {
		return nil, errors.New("DuckDB connection could not be created")
	}
	c.db.refs++
	return &conn{a: c.db.a, h: h, owner: c.db}, nil
}

type driverType struct{}

func (driverType) Open(string) (driver.Conn, error) {
	return nil, errors.New("DuckDB connections require a database owner")
}

type conn struct {
	mu           sync.Mutex
	a            *api
	h            uintptr
	owner        *database
	lastAffected int64
}

func (c *conn) AffectedRows() int64 { c.mu.Lock(); defer c.mu.Unlock(); return c.lastAffected }
func AffectedRows(connection *sql.Conn) (int64, error) {
	var n int64
	err := connection.Raw(func(v any) error {
		c, ok := v.(interface{ AffectedRows() int64 })
		if !ok {
			return errors.New("not a DuckDB connection")
		}
		n = c.AffectedRows()
		return nil
	})
	return n, err
}

func (c *conn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("use DuckDB context-aware queries")
}
func (c *conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.h != 0 {
		c.a.disconnect(&c.h)
		databases.Lock()
		releaseDatabase(c.owner)
		databases.Unlock()
	}
	return nil
}
func (c *conn) Begin() (driver.Tx, error) { return c.BeginTx(context.Background(), driver.TxOptions{}) }
func (c *conn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	if o.Isolation != driver.IsolationLevel(0) || o.ReadOnly {
		return nil, errors.New("DuckDB uses snapshot transactions; explicit isolation or read-only transaction options are unsupported")
	}
	if _, err := c.ExecContext(ctx, "BEGIN TRANSACTION", nil); err != nil {
		return nil, err
	}
	return &tx{c: c}, nil
}
func (c *conn) Ping(ctx context.Context) error { _, e := c.ExecContext(ctx, "SELECT 1", nil); return e }
func (c *conn) CheckNamedValue(v *driver.NamedValue) error {
	if v.Name != "" {
		return errors.New("DuckDB requires positional parameters")
	}
	if t, ok := v.Value.(time.Time); ok {
		v.Value = t.Format(time.RFC3339Nano)
		return nil
	}
	return driver.ErrSkip
}

type tx struct{ c *conn }

func (t *tx) Commit() error { _, e := t.c.ExecContext(context.Background(), "COMMIT", nil); return e }
func (t *tx) Rollback() error {
	_, e := t.c.ExecContext(context.Background(), "ROLLBACK", nil)
	return e
}

func (c *conn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	r, err := c.QueryContext(ctx, q, args)
	if err != nil {
		return nil, err
	}
	v := r.(*rows)
	for {
		if err = ctx.Err(); err != nil {
			v.Close()
			return nil, err
		}
		chunk := v.a.fetchChunk(v.result)
		if chunk == 0 {
			break
		}
		v.row += v.a.chunkSize(chunk)
		v.a.destroyChunk(&chunk)
	}
	if message := v.a.resultError(&v.result); message != "" {
		v.Close()
		return nil, errors.New(message)
	}
	n := v.affectedRows()
	if err = v.Close(); err != nil {
		return nil, err
	}
	return execution(n), nil
}

type execution int64

func (e execution) LastInsertId() (int64, error) {
	return 0, errors.New("DuckDB generated values require RETURNING")
}
func (e execution) RowsAffected() (int64, error) { return int64(e), nil }

func (c *conn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(q) > 8<<20 || strings.ContainsRune(q, 0) {
		return nil, errors.New("invalid or oversized DuckDB SQL")
	}
	c.mu.Lock()
	if c.h == 0 {
		c.mu.Unlock()
		return nil, errors.New("DuckDB connection is closed")
	}
	ack := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(ack); c.a.interrupt(c.h) })
	finish := func() {
		if !stop() {
			<-ack
		}
		c.mu.Unlock()
	}
	var prepared uintptr
	if c.a.prepare(c.h, q, &prepared) != 0 {
		err := errors.New(c.a.prepareError(prepared))
		c.a.destroyPrepared(&prepared)
		finish()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	cleanup := func() { c.a.destroyPrepared(&prepared); finish() }
	if c.a.nparams(prepared) != uint64(len(args)) {
		cleanup()
		return nil, errors.New("DuckDB parameter count mismatch")
	}
	for i, v := range args {
		n, rc := uint64(i+1), uint32(1)
		switch x := v.Value.(type) {
		case nil:
			rc = c.a.bindNull(prepared, n)
		case bool:
			rc = c.a.bindBool(prepared, n, x)
		case int64:
			rc = c.a.bindInt(prepared, n, x)
		case float64:
			rc = c.a.bindDouble(prepared, n, x)
		case string:
			b := []byte(x)
			rc = c.a.bindString(prepared, n, bytePointer(b), uint64(len(b)))
			runtime.KeepAlive(b)
		case []byte:
			rc = c.a.bindBlob(prepared, n, bytePointer(x), uint64(len(x)))
			runtime.KeepAlive(x)
		}
		if rc != 0 {
			cleanup()
			return nil, errors.New("DuckDB parameter could not be bound")
		}
	}
	r := &rows{a: c.a, ctx: ctx, finish: cleanup, owner: c, statementType: c.a.statementType(prepared)}
	var pending uintptr
	state := c.a.pendingStreaming(prepared, &pending)
	if state == 0 {
		state = c.a.executePending(pending, &r.result)
	}
	c.a.destroyPending(&pending)
	if state != 0 {
		err := errors.New(c.a.resultError(&r.result))
		c.a.destroyResult(&r.result)
		cleanup()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	n := c.a.columnCount(&r.result)
	if n > 4096 {
		r.Close()
		return nil, errors.New("DuckDB result has too many columns")
	}
	for i := uint64(0); i < n; i++ {
		r.names = append(r.names, c.a.columnName(&r.result, i))
		r.types = append(r.types, typeName(c.a, &r.result, i))
	}
	if ctx.Err() != nil {
		r.Close()
		return nil, ctx.Err()
	}
	return r, nil
}
func bytePointer(b []byte) unsafe.Pointer {
	if len(b) == 0 {
		return nil
	}
	return unsafe.Pointer(&b[0])
}

type rows struct {
	owner               *conn
	statementType       uint32
	chunk               uintptr
	chunkRow, chunkRows uint64
	a                   *api
	result              result
	ctx                 context.Context
	finish              func()
	names, types        []string
	row                 uint64
	closed              bool
}

func (r *rows) Columns() []string                       { return r.names }
func (r *rows) ColumnTypeDatabaseTypeName(i int) string { return r.types[i] }
func (r *rows) ColumnTypeNullable(int) (bool, bool)     { return true, false }
func (r *rows) Close() error {
	if !r.closed {
		r.closed = true
		r.owner.lastAffected = r.affectedRows()
		if r.chunk != 0 {
			r.a.destroyChunk(&r.chunk)
		}
		r.a.destroyResult(&r.result)
		r.finish()
	}
	return nil
}
func (r *rows) affectedRows() int64 {
	if r.statementType != 2 && r.statementType != 3 && r.statementType != 5 && r.statementType != 11 {
		return 0
	}
	if r.a.resultType(r.result) == 3 {
		return int64(r.row)
	}
	return int64(r.a.rowsChanged(&r.result))
}
func (r *rows) Next(dest []driver.Value) error {
	if r.closed {
		return io.EOF
	}
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if r.chunk == 0 || r.chunkRow >= r.chunkRows {
		if r.chunk != 0 {
			r.a.destroyChunk(&r.chunk)
		}
		r.chunk = r.a.fetchChunk(r.result)
		r.chunkRow = 0
		if r.chunk == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
			if message := r.a.resultError(&r.result); message != "" {
				return errors.New(message)
			}
			return io.EOF
		}
		r.chunkRows = r.a.chunkSize(r.chunk)
	}
	for i := range dest {
		v, err := r.a.vectorValue(r.a.chunkVector(r.chunk, uint64(i)), r.chunkRow, 0)
		if err != nil {
			return err
		}
		dest[i] = v
	}
	r.row++
	r.chunkRow++
	return nil
}

var _ driver.Connector = (*Connector)(nil)
var _ driver.QueryerContext = (*conn)(nil)
var _ driver.ExecerContext = (*conn)(nil)
var _ driver.ConnBeginTx = (*conn)(nil)
