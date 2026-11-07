package dpiworker

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == entryFlag {
		err := serve(os.Stdin, os.Stdout, os.Getenv("DM_SVC_PATH"), func(o dpi.Options, secret string) (driver.Connector, error) {
			if o.LibraryFile != "test-native-session" {
				return dpi.NewConnector(o, secret)
			}
			return fakeConnector{}, nil
		})
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type fakeConnector struct{}

func (fakeConnector) Driver() driver.Driver { return driverType{} }
func (fakeConnector) Connect(context.Context) (driver.Conn, error) {
	return &fakeConn{valid: true}, nil
}

type fakeConn struct{ valid bool }

func (c *fakeConn) Close() error                           { c.valid = false; return nil }
func (c *fakeConn) IsValid() bool                          { return c.valid }
func (c *fakeConn) ResetSession(ctx context.Context) error { return ctx.Err() }
func (c *fakeConn) Ping(ctx context.Context) error         { return ctx.Err() }
func (c *fakeConn) Prepare(q string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), q)
}
func (c *fakeConn) PrepareContext(_ context.Context, q string) (driver.Stmt, error) {
	return &fakeStmt{c: c, sql: q}, nil
}
func (c *fakeConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *fakeConn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	return &fakeTx{ctx}, nil
}
func (c *fakeConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	switch query {
	case "out-success":
		for _, arg := range args {
			out, ok := arg.Value.(sql.Out)
			if !ok {
				continue
			}
			var value any
			switch out.Dest.(type) {
			case *int64:
				value = int64(9007199254740993)
			case *string:
				value = "数据库 🌸"
			}
			set, err := dpi.StageOutput(out, value)
			if err != nil {
				return nil, err
			}
			set()
		}
	case "wait":
		markEntered()
		<-ctx.Done()
		time.Sleep(120 * time.Millisecond)
		c.valid = false
		return nil, errors.Join(ctx.Err(), dpi.ErrOutcomeUnknown)
	case "lose":
		os.Exit(0)
	case "hang":
		select {}
	case "ddl":
		return result{err: errors.New("unknown affected rows")}, nil
	case "native-error":
		return nil, &dpi.Error{Code: -2829, Operation: "execution"}
	}
	return driver.RowsAffected(1), nil
}
func (c *fakeConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	v := []driver.Value{int64(9007199254740993)}
	if q == "echo" {
		v = make([]driver.Value, len(args))
		for i, a := range args {
			v[i] = a.Value
		}
	}
	return &fakeRows{ctx: ctx, wait: q == "wait-rows", values: v}, nil
}

type fakeStmt struct {
	c   *fakeConn
	sql string
}

func (s *fakeStmt) Close() error  { return nil }
func (s *fakeStmt) NumInput() int { return -1 }
func (s *fakeStmt) Exec(v []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), named(v))
}
func (s *fakeStmt) Query(v []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), named(v))
}
func (s *fakeStmt) ExecContext(ctx context.Context, v []driver.NamedValue) (driver.Result, error) {
	return s.c.ExecContext(ctx, s.sql, v)
}
func (s *fakeStmt) QueryContext(ctx context.Context, v []driver.NamedValue) (driver.Rows, error) {
	return s.c.QueryContext(ctx, s.sql, v)
}

type fakeRows struct {
	ctx        context.Context
	wait, read bool
	values     []driver.Value
}

func (r *fakeRows) Columns() []string { return make([]string, len(r.values)) }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(v []driver.Value) error {
	if r.wait {
		markEntered()
		<-r.ctx.Done()
		time.Sleep(120 * time.Millisecond)
		return r.ctx.Err()
	}
	if r.read {
		return io.EOF
	}
	r.read = true
	copy(v, r.values)
	return nil
}
func (*fakeRows) ColumnTypeDatabaseTypeName(int) string             { return "DECIMAL" }
func (*fakeRows) ColumnTypeNullable(int) (bool, bool)               { return true, true }
func (*fakeRows) ColumnTypeLength(int) (int64, bool)                { return 128, true }
func (*fakeRows) ColumnTypePrecisionScale(int) (int64, int64, bool) { return 38, 20, true }
func (*fakeRows) ColumnTypeScanType(int) reflect.Type               { return reflect.TypeFor[string]() }

type fakeTx struct{ ctx context.Context }

func (t *fakeTx) Commit() error {
	markEntered()
	<-t.ctx.Done()
	time.Sleep(120 * time.Millisecond)
	return errors.Join(t.ctx.Err(), dpi.ErrOutcomeUnknown)
}
func (t *fakeTx) Rollback() error { return nil }

func testConn(t *testing.T) *conn {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("native worker requires independently verified Unix file permissions")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := &connector{o: dpi.Options{LibraryFile: "test-native-session", ConnectTimeoutSeconds: 5, OperationTimeoutSeconds: 5}, executable: exe}
	raw, err := c.start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	d := raw.(*conn)
	t.Cleanup(func() { d.Close() })
	return d
}

func TestWorkerValueCodecPreservesPrecisionNullAndEmptyBytes(t *testing.T) {
	for _, v := range []any{nil, "", "数据库 🌸", []byte{}, []byte{0, 128, 255}, int64(math.MinInt64), int64(math.MaxInt64), float64(1.2345), true, dpi.Decimal("123456789012345678.12345678901234567891")} {
		b, err := encodeValue(v)
		if err != nil {
			t.Fatal(err)
		}
		x, err := decodeValue(b)
		if err != nil || !reflect.DeepEqual(v, x) {
			t.Fatal("value changed across native boundary", err, v, x)
		}
	}
	for _, v := range []any{math.Inf(1), math.NaN(), strings.Repeat("x", (1<<20)+1), string([]byte{255}), uint64(math.MaxUint64)} {
		if _, err := encodeValue(v); err == nil {
			t.Fatal("unrepresentable value accepted")
		}
	}
	for _, v := range []value{{Kind: "int64", Text: "01"}, {Kind: "float64", Text: "NaN"}, {Kind: "bool", Text: "yes"}, {Kind: "null", Text: "x"}, {Kind: "decimal", Text: strings.Repeat("1", 129)}} {
		if _, err := decodeValue(v); err == nil {
			t.Fatal("invalid value accepted")
		}
	}
}

type shortWriter struct{ bytes.Buffer }

func (w *shortWriter) Write(b []byte) (int, error) {
	if len(b) > 3 {
		b = b[:3]
	}
	return w.Buffer.Write(b)
}
func TestWorkerFramingRejectsTruncationUnknownFieldsAndTrailingJSON(t *testing.T) {
	w := new(shortWriter)
	original := request{ID: 42, Method: "query", SQL: "SELECT ?", Values: []value{{Kind: "text", Text: "数据库"}}}
	if err := writeFrame(w, original); err != nil {
		t.Fatal(err)
	}
	var got request
	if err := readFrame(bytes.NewReader(w.Bytes()), &got); err != nil || !reflect.DeepEqual(got, original) {
		t.Fatal(err)
	}
	for i := 0; i < len(w.Bytes()); i++ {
		if err := readFrame(bytes.NewReader(w.Bytes()[:i]), new(request)); err == nil {
			t.Fatal("truncated frame accepted")
		}
	}
	for _, body := range []any{map[string]any{"id": 1, "unexpected": true}, "bad"} {
		var b bytes.Buffer
		writeFrame(&b, body)
		if err := readFrame(&b, new(request)); err == nil {
			t.Fatal("invalid JSON shape accepted")
		}
	}
	if err := writeFrame(io.Discard, strings.Repeat("x", maxFrame)); err == nil {
		t.Fatal("oversize frame accepted")
	}
}

func TestWorkerEnvironmentOwnsExistingConfigAndRejectsChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native client has no admitted Windows ABI")
	}
	dir, err := prepareDirectory()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := validateDirectory(dir); err != nil {
		t.Fatal(err)
	}
	parent := []string{"PATH=/bin", "DM_SVC_PATH=/foreign", "OPENSSL_CONF=/foreign", "LD_PRELOAD=/foreign", "SECRET=hidden", "LANG=foreign"}
	env := workerEnvironment(parent, dir)
	if len(parent) != 6 || parent[1] != "DM_SVC_PATH=/foreign" {
		t.Fatal("parent environment changed")
	}
	for _, v := range env {
		if strings.Contains(v, "foreign") || strings.Contains(v, "SECRET") {
			t.Fatal("foreign process setting inherited")
		}
	}
	path := filepath.Join(dir, "dm_svc.conf")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := validateDirectory(dir); err == nil {
		t.Fatal("native /etc fallback was allowed")
	}
	if err := os.Symlink(filepath.Join(dir, "openssl.cnf"), path); err != nil {
		t.Fatal(err)
	}
	if err := validateDirectory(dir); err == nil {
		t.Fatal("configuration symlink was accepted")
	}
}

func TestWorkerExactIPCPreparedMetadataAndDDL(t *testing.T) {
	c := testConn(t)
	args := named([]driver.Value{int64(9007199254740993), "数据库 🌸", []byte{0, 128, 255}, nil, []byte{}, dpi.Decimal("123456789012345678.12345678901234567891")})
	st, err := c.PrepareContext(context.Background(), "echo")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r, err := st.(driver.StmtQueryContext).QueryContext(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]driver.Value, len(args))
	if err := r.Next(got); err != nil {
		t.Fatal(err)
	}
	for i, arg := range args {
		if !reflect.DeepEqual(arg.Value, got[i]) {
			t.Fatal("IPC changed exact values")
		}
	}
	p, s, known := r.(driver.RowsColumnTypePrecisionScale).ColumnTypePrecisionScale(0)
	if !known || p != 38 || s != 20 {
		t.Fatal("native metadata lost")
	}
	if err := r.Next(got); err != io.EOF {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := c.ExecContext(context.Background(), "ddl", nil)
	if err != nil {
		t.Fatal("successful DDL changed to failure", err)
	}
	if _, err := result.RowsAffected(); err == nil {
		t.Fatal("unknown row count reported as known")
	}
	_, err = c.ExecContext(context.Background(), "native-error", nil)
	var native *dpi.Error
	if !errors.As(err, &native) || native.Code != -2829 {
		t.Fatal("native error code lost", err)
	}
}

func TestWorkerCancellationWaitsForOriginalNativeAcknowledgement(t *testing.T) {
	for _, method := range []string{"exec", "deadline", "rows", "commit"} {
		t.Run(method, func(t *testing.T) {
			c := testConn(t)
			other := testConn(t)
			ctx, cancel := context.WithCancel(context.Background())
			if method == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
			}
			defer cancel()
			var run func() error
			switch method {
			case "exec", "deadline":
				run = func() error { _, err := c.ExecContext(ctx, "wait", nil); return err }
			case "rows":
				r, err := c.QueryContext(ctx, "wait-rows", nil)
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				run = func() error { return r.Next(make([]driver.Value, 1)) }
			case "commit":
				tx, err := c.BeginTx(ctx, driver.TxOptions{})
				if err != nil {
					t.Fatal(err)
				}
				run = tx.Commit
			}
			result := make(chan error, 1)
			go func() { result <- run() }()
			awaitEntered(t, c.dir)
			if method == "deadline" {
				<-ctx.Done()
			} else {
				cancel()
			}
			select {
			case err := <-result:
				t.Fatal("returned before native acknowledged cancellation", err)
			case <-time.After(40 * time.Millisecond):
			}
			select {
			case err := <-result:
				want := context.Canceled
				if method == "deadline" {
					want = context.DeadlineExceeded
				}
				if !errors.Is(err, want) {
					t.Fatal(err)
				}
				if method != "rows" && !errors.Is(err, dpi.ErrOutcomeUnknown) {
					t.Fatal("uncertain write became retryable", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("native cancellation was not delivered")
			}
			if err := other.Ping(context.Background()); err != nil {
				t.Fatal("cancellation affected another original process", err)
			}
		})
	}
}

func TestWorkerLostReplyHardStopAndConfigRemovalRetireOnlyOriginal(t *testing.T) {
	for _, method := range []string{"lose", "hang", "config"} {
		t.Run(method, func(t *testing.T) {
			c := testConn(t)
			other := testConn(t)
			dir := c.dir
			c.timeout = 200 * time.Millisecond
			if method == "config" {
				if err := os.Remove(filepath.Join(dir, "dm_svc.conf")); err != nil {
					t.Fatal(err)
				}
			}
			_, err := c.ExecContext(context.Background(), method, nil)
			if !errors.Is(err, dpi.ErrSessionClosed) || errors.Is(err, driver.ErrBadConn) {
				t.Fatal("lost original session is retryable", err)
			}
			if method != "config" && !errors.Is(err, dpi.ErrOutcomeUnknown) {
				t.Fatal("lost submitted write has a definite outcome", err)
			}
			select {
			case <-c.exited:
			default:
				t.Fatal("returned before captured OS process exited")
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("private session files leaked")
			}
			if err := other.Ping(context.Background()); err != nil {
				t.Fatal("another physical worker retired", err)
			}
			if _, err := c.ExecContext(context.Background(), "write", nil); !errors.Is(err, dpi.ErrSessionClosed) {
				t.Fatal("lost write replayed", err)
			}
		})
	}
}

func TestWorkerRejectsBeforeSubmissionAndRetainsRollback(t *testing.T) {
	c := testConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ExecContext(ctx, "write", nil); !errors.Is(err, context.Canceled) || errors.Is(err, dpi.ErrOutcomeUnknown) {
		t.Fatal("unsubmitted cancellation became uncertain", err)
	}
	if _, err := c.ExecContext(context.Background(), string([]byte{255}), nil); err == nil || errors.Is(err, dpi.ErrOutcomeUnknown) {
		t.Fatal("invalid SQL was submitted", err)
	}
	args := make([]driver.NamedValue, 17)
	for i := range args {
		args[i] = driver.NamedValue{Ordinal: i + 1, Value: strings.Repeat("x", 1<<20)}
	}
	if _, err := c.ExecContext(context.Background(), "write", args); !errors.Is(err, errProtocol) || errors.Is(err, dpi.ErrOutcomeUnknown) {
		t.Fatal("oversize request was submitted", err)
	}
	if !c.IsValid() {
		t.Fatal("rejected input retired original session")
	}
	txctx, cancelTx := context.WithCancel(context.Background())
	tx, err := c.BeginTx(txctx, driver.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cancelTx()
	if err := tx.Commit(); !errors.Is(err, context.Canceled) || errors.Is(err, dpi.ErrOutcomeUnknown) {
		t.Fatal("pre-canceled commit was submitted", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal("original canceled transaction could not rollback independently", err)
	}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func markEntered() {
	os.WriteFile(filepath.Join(os.Getenv("DM_SVC_PATH"), "test-entered"), []byte("entered"), 0600)
}
func awaitEntered(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(filepath.Join(dir, "test-entered")); err == nil && string(b) == "entered" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("original worker did not enter native operation")
}
