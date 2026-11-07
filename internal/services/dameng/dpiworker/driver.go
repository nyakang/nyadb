package dpiworker

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"

	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

func (c *conn) Prepare(sql string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), sql)
}
func (c *conn) PrepareContext(ctx context.Context, sql string) (driver.Stmt, error) {
	r, err := c.invoke(ctx, request{Method: "prepare", SQL: sql}, false)
	if err != nil {
		return nil, err
	}
	return &statement{c: c, handle: r.Handle, inputs: r.Inputs}, nil
}

func (c *conn) ExecContext(ctx context.Context, sql string, args []driver.NamedValue) (driver.Result, error) {
	return c.exec(ctx, request{Method: "exec", SQL: sql}, args)
}
func (c *conn) QueryContext(ctx context.Context, sql string, args []driver.NamedValue) (driver.Rows, error) {
	return c.query(ctx, request{Method: "query", SQL: sql}, args)
}
func (c *conn) exec(ctx context.Context, q request, args []driver.NamedValue) (driver.Result, error) {
	var err error
	q.Values, err = encodeValues(args)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r, err := c.callLocked(ctx, q, true)
	if err != nil {
		return nil, err
	}
	setters, err := stageOutputs(args, r.Outputs)
	if err != nil {
		c.terminateLocked()
		return nil, errors.Join(err, dpi.ErrSessionClosed, dpi.ErrOutcomeUnknown)
	}
	for _, set := range setters {
		set()
	}
	return result{affected: r.Affected, err: r.AffectedError.err()}, nil
}
func (c *conn) query(ctx context.Context, q request, args []driver.NamedValue) (driver.Rows, error) {
	for _, arg := range args {
		if _, ok := arg.Value.(sql.Out); ok {
			return nil, errors.New("DM8 output parameters require ExecContext")
		}
	}
	var err error
	q.Values, err = encodeValues(args)
	if err != nil {
		return nil, err
	}
	r, err := c.invoke(ctx, q, true)
	if err != nil {
		return nil, err
	}
	return &rows{c: c, handle: r.Handle, owner: r.ID, ctx: ctx, columns: r.Columns}, nil
}

type result struct {
	affected int64
	err      error
}

func (r result) LastInsertId() (int64, error) {
	return 0, errors.New("DM8 generated identity requires explicit RETURNING")
}
func (r result) RowsAffected() (int64, error) {
	if r.err != nil {
		return 0, r.err
	}
	if r.affected < 0 {
		return 0, errors.New("DM8 affected row count is unknown")
	}
	return r.affected, nil
}

type statement struct {
	c      *conn
	handle uint64
	inputs int
	closed bool
}

func (s *statement) NumInput() int { return s.inputs }
func (s *statement) Close() error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.c.closed {
		return nil
	}
	_, err := s.c.callLocked(context.Background(), request{Method: "stmt-close", Handle: s.handle}, false)
	return err
}
func named(values []driver.Value) []driver.NamedValue {
	out := make([]driver.NamedValue, len(values))
	for i, v := range values {
		out[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return out
}
func (s *statement) Exec(v []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), named(v))
}
func (s *statement) Query(v []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), named(v))
}
func (s *statement) ExecContext(ctx context.Context, v []driver.NamedValue) (driver.Result, error) {
	return s.c.exec(ctx, request{Method: "stmt-exec", Handle: s.handle}, v)
}
func (s *statement) QueryContext(ctx context.Context, v []driver.NamedValue) (driver.Rows, error) {
	return s.c.query(ctx, request{Method: "stmt-query", Handle: s.handle}, v)
}
func (s *statement) CheckNamedValue(v *driver.NamedValue) error { return s.c.CheckNamedValue(v) }

type rows struct {
	c             *conn
	handle, owner uint64
	ctx           context.Context
	columns       []column
	closed        bool
}

func (r *rows) Columns() []string {
	out := make([]string, len(r.columns))
	for i, c := range r.columns {
		out[i] = c.Name
	}
	return out
}
func (r *rows) Close() error {
	r.c.mu.Lock()
	defer r.c.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if r.c.closed {
		return nil
	}
	_, err := r.c.callLocked(context.Background(), request{Method: "rows-close", Handle: r.handle}, false)
	return err
}
func (r *rows) Next(values []driver.Value) error {
	r.c.mu.Lock()
	defer r.c.mu.Unlock()
	if r.closed {
		return io.EOF
	}
	if len(values) != len(r.columns) {
		return errProtocol
	}
	v, err := r.c.callLocked(r.ctx, request{Method: "next", Handle: r.handle, Owner: r.owner}, false)
	if err != nil {
		return err
	}
	if v.EOF {
		return io.EOF
	}
	if len(v.Values) != len(values) {
		r.c.terminateLocked()
		return errors.Join(errProtocol, dpi.ErrSessionClosed)
	}
	for i, b := range v.Values {
		x, err := decodeValue(b)
		if err != nil {
			r.c.terminateLocked()
			return errors.Join(err, dpi.ErrSessionClosed)
		}
		values[i] = x
	}
	return nil
}
func (r *rows) ColumnTypeDatabaseTypeName(i int) string { return r.columns[i].Type }
func (r *rows) ColumnTypeNullable(i int) (bool, bool) {
	c := r.columns[i]
	return c.Nullable, c.NullableKnown
}
func (r *rows) ColumnTypeLength(i int) (int64, bool) {
	c := r.columns[i]
	return c.Length, c.LengthKnown
}
func (r *rows) ColumnTypePrecisionScale(i int) (int64, int64, bool) {
	c := r.columns[i]
	return c.Precision, c.Scale, c.DecimalKnown
}
func (r *rows) ColumnTypeScanType(i int) reflect.Type {
	switch r.columns[i].Scan {
	case "int64":
		return reflect.TypeFor[int64]()
	case "float64":
		return reflect.TypeFor[float64]()
	case "bytes":
		return reflect.TypeFor[[]byte]()
	case "text":
		return reflect.TypeFor[string]()
	}
	return nil
}

func (c *conn) Begin() (driver.Tx, error) { return c.BeginTx(context.Background(), driver.TxOptions{}) }
func (c *conn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	r, err := c.invoke(ctx, request{Method: "begin", Tx: o}, false)
	if err != nil {
		return nil, err
	}
	return &transaction{c: c, handle: r.Handle, owner: r.ID, ctx: ctx}, nil
}

type transaction struct {
	c             *conn
	handle, owner uint64
	ctx           context.Context
	ended         bool
}

func (t *transaction) Commit() error                             { return t.end(t.ctx, "commit") }
func (t *transaction) Rollback() error                           { return t.end(context.Background(), "rollback") }
func (t *transaction) CommitContext(ctx context.Context) error   { return t.end(ctx, "commit") }
func (t *transaction) RollbackContext(ctx context.Context) error { return t.end(ctx, "rollback") }
func (t *transaction) end(ctx context.Context, method string) error {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	if t.ended {
		return dpi.ErrSessionClosed
	}
	// A pre-canceled commit was never submitted. Keep the captured transaction
	// available for its independent rollback, never reconnect/replay it.
	if err := ctx.Err(); err != nil {
		return err
	}
	t.ended = true
	r, err := t.c.callLocked(ctx, request{Method: method, Handle: t.handle, Owner: t.owner}, true)
	if err != nil {
		if r.ID == 0 && !errors.Is(err, dpi.ErrOutcomeUnknown) && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			// Cancellation can race between end's check and callLocked's
			// check. No frame was sent; retain this original rollback handle.
			t.ended = false
		} else if method == "commit" {
			// A received response ends the child's transaction ownership even
			// when it reports cancellation before native COMMIT. Never treat a
			// submitted terminal request as a reusable transaction.
			err = errors.Join(err, dpi.ErrOutcomeUnknown)
		}
	}
	return err
}

var _ driver.Connector = (*connector)(nil)
var _ driver.Conn = (*conn)(nil)
var _ driver.QueryerContext = (*conn)(nil)
var _ driver.ExecerContext = (*conn)(nil)
var _ driver.ConnBeginTx = (*conn)(nil)
var _ driver.SessionResetter = (*conn)(nil)
var _ driver.Validator = (*conn)(nil)
var _ driver.StmtExecContext = (*statement)(nil)
var _ driver.StmtQueryContext = (*statement)(nil)
var _ driver.RowsColumnTypeScanType = (*rows)(nil)
