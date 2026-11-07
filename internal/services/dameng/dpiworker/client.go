package dpiworker

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

type connector struct {
	o          dpi.Options
	password   string
	executable string
}

// NewConnector never loads the native client in the workbench. Every Connect
// starts one captured executable, private environment and original session.
func NewConnector(o dpi.Options, password string) (driver.Connector, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, errors.New("DM8 private worker executable is unavailable")
	}
	return newConnector(o, password, executable)
}

func newConnector(o dpi.Options, password, executable string) (driver.Connector, error) {
	texts := []string{password, o.LibraryFile, o.Host, o.Username, o.Schema}
	if o.TLS != nil {
		texts = append(texts, o.TLS.Mode, o.TLS.CAFile, o.TLS.CertificateFile, o.TLS.KeyFile, o.TLS.ServerName)
	}
	for _, v := range texts {
		if !utf8.ValidString(v) {
			return nil, errors.New("DM8 native configuration must be valid UTF-8")
		}
	}
	o, err := dpi.ValidateOptions(o, password)
	if err != nil {
		return nil, err
	}
	if _, err = dpi.VerifyLibrary(o.LibraryFile); err != nil {
		return nil, err
	}
	return &connector{o: o, password: password, executable: executable}, nil
}

type driverType struct{}

func (driverType) Open(string) (driver.Conn, error) {
	return nil, errors.New("DM8 requires an explicit isolated native connector")
}
func (c *connector) Driver() driver.Driver { return driverType{} }

func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := dpi.VerifyLibrary(c.o.LibraryFile); err != nil {
		return nil, err
	}
	return c.start(ctx)
}

func (c *connector) start(ctx context.Context) (driver.Conn, error) {
	dir, err := prepareDirectory()
	if err != nil {
		return nil, errors.New("DM8 private native configuration is unavailable")
	}
	cmd := exec.Command(c.executable, entryFlag)
	cmd.Dir = dir
	cmd.Env = workerEnvironment(os.Environ(), dir)
	// Own the pipe endpoints independently of Cmd.Wait. Wait must not close a
	// final native acknowledgement before the response reader has consumed it.
	childIn, parentIn, err := os.Pipe()
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	parentOut, childOut, err := os.Pipe()
	if err != nil {
		childIn.Close()
		parentIn.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = childIn, childOut, io.Discard
	if err = cmd.Start(); err != nil {
		childIn.Close()
		parentIn.Close()
		parentOut.Close()
		childOut.Close()
		os.RemoveAll(dir)
		return nil, errors.New("DM8 original private native worker could not start")
	}
	childIn.Close()
	childOut.Close()
	d := &conn{cmd: cmd, dir: dir, in: parentIn, out: parentOut, replies: make(chan reply, 1), exited: make(chan struct{}), readerDone: make(chan struct{}), valid: true, timeout: time.Duration(max(c.o.ConnectTimeoutSeconds, c.o.OperationTimeoutSeconds)+10) * time.Second}
	go func() { cmd.Wait(); close(d.exited) }()
	go func() {
		defer close(d.readerDone)
		for {
			var r response
			err := readFrame(d.out, &r)
			select {
			case d.replies <- reply{r, err}:
			case <-d.exited:
				// A final ACK may already be buffered; normal native exit follows
				// the close response. No call relies on the process-exit ordering.
				select {
				case d.replies <- reply{r, err}:
				default:
				}
				return
			}
			if err != nil {
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.o.ConnectTimeoutSeconds)*time.Second)
	defer cancel()
	d.mu.Lock()
	_, err = d.callLocked(ctx, request{Method: "init", Options: &c.o, Password: c.password}, false)
	if err != nil {
		d.terminateLocked()
	}
	d.timeout = time.Duration(c.o.OperationTimeoutSeconds+10) * time.Second
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return d, nil
}

type reply struct {
	response response
	err      error
}
type conn struct {
	mu                 sync.Mutex
	writeMu            sync.Mutex
	cmd                *exec.Cmd
	dir                string
	in, out            *os.File
	replies            chan reply
	exited, readerDone chan struct{}
	next               uint64
	valid, closed      bool
	timeout            time.Duration
}

// terminateLocked acknowledges OS exit before disposing the captured session
// resources. It never looks up another process by PID/name or restarts it.
func (c *conn) terminateLocked() {
	if c.closed {
		return
	}
	c.valid, c.closed = false, true
	c.cmd.Process.Kill()
	<-c.exited
	c.in.Close()
	c.out.Close()
	<-c.readerDone
	os.RemoveAll(c.dir)
}

func (c *conn) send(q request) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeFrame(c.in, q)
}

func (c *conn) callLocked(ctx context.Context, q request, write bool) (response, error) {
	if !utf8.ValidString(q.SQL) || len(q.SQL) > 1<<20 || strings.ContainsRune(q.SQL, 0) {
		return response{}, errors.New("invalid bounded DM8 SQL")
	}
	if c.closed {
		return response{}, dpi.ErrSessionClosed
	}
	if err := ctx.Err(); err != nil {
		return response{}, err
	}
	if err := validateDirectory(c.dir); err != nil {
		c.terminateLocked()
		return response{}, errors.Join(err, dpi.ErrSessionClosed)
	}
	c.next++
	q.ID = c.next
	if deadline, ok := ctx.Deadline(); ok {
		q.Deadline = deadline.UnixNano()
	}
	frame, err := marshalFrame(q)
	if err != nil {
		return response{}, err
	}
	// Submission and cancel writers run independently of the response/native
	// wait. A pipe fault after any submission is conservatively uncertain.
	sent := make(chan error, 1)
	go func() { c.writeMu.Lock(); err := writeBytes(c.in, frame); c.writeMu.Unlock(); sent <- err }()
	timer := time.NewTimer(c.timeout)
	defer timer.Stop()
	done := ctx.Done()
	for {
		select {
		case err := <-sent:
			sent = nil
			if err != nil {
				return response{}, c.failedLocked(ctx, write)
			}
		case p := <-c.replies:
			if p.err != nil || p.response.ID != q.ID {
				return response{}, c.failedLocked(ctx, write)
			}
			// Submission must finish before resources may be released. The
			// response necessarily follows the complete original request frame.
			if sent != nil {
				if err := <-sent; err != nil {
					return response{}, c.failedLocked(ctx, write)
				}
			}
			c.valid = p.response.Valid
			err := p.response.Error.err()
			if err != nil {
				// A cancel message can reach the child before its deadline timer
				// runs. Preserve the original caller's deadline/cancellation cause
				// together with the native acknowledged failure and outcome.
				err = errors.Join(err, ctx.Err())
			}
			return p.response, err
		case <-done:
			done = nil
			go func() { c.send(request{Method: "cancel", ID: q.ID, Owner: q.Owner}) }()
			// Keep waiting for the original operation's native ACK; context
			// cancellation alone does not establish a write outcome.
		case <-timer.C:
			return response{}, c.failedLocked(ctx, write)
		}
	}
}

func (c *conn) failedLocked(ctx context.Context, write bool) error {
	c.terminateLocked()
	err := errors.Join(errors.New("DM8 original private worker acknowledgement was lost"), dpi.ErrSessionClosed, ctx.Err())
	if write {
		err = errors.Join(err, dpi.ErrOutcomeUnknown)
	}
	return err
}

func (c *conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	_, err := c.callLocked(context.Background(), request{Method: "close"}, false)
	if !c.closed {
		select {
		case <-c.exited:
		case <-time.After(c.timeout):
		}
		c.terminateLocked()
	}
	return err
}

func (c *conn) IsValid() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.valid && !c.closed }

func (c *conn) invoke(ctx context.Context, q request, write bool) (response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.callLocked(ctx, q, write)
}

func (c *conn) Ping(ctx context.Context) error {
	_, err := c.invoke(ctx, request{Method: "ping"}, false)
	return err
}
func (c *conn) ResetSession(ctx context.Context) error {
	_, err := c.invoke(ctx, request{Method: "reset"}, false)
	return err
}

func (c *conn) CheckNamedValue(v *driver.NamedValue) error {
	if v.Name != "" {
		return errors.New("DM8 native parameters must be positional")
	}
	if _, ok := v.Value.(dpi.Decimal); ok {
		return nil
	}
	return driver.ErrSkip
}

func encodeValues(values []driver.NamedValue) ([]value, error) {
	if len(values) > 4096 {
		return nil, errProtocol
	}
	out := make([]value, len(values))
	for i, v := range values {
		if v.Name != "" || v.Ordinal != i+1 {
			return nil, errors.New("DM8 parameters must be consecutive positional values")
		}
		b, err := encodeValue(v.Value)
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}
