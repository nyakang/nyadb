package jdbcplugin

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

type Options struct {
	MaxFrameBytes int
	CancelWait    time.Duration
}

// Client uses exactly one default JDBC connection per original process. The
// original plugin registers cancel tokens only for default requests, not its
// logical-session branch. Separate owners must start separate clients.
type Client struct {
	mu                            sync.Mutex
	cmd                           *exec.Cmd
	owner                         *processOwner
	stdin, stdout                 *os.File
	options                       Options
	connection                    json.RawMessage
	protocol                      Receipt
	pending                       map[uint64]chan response
	nextID                        uint64
	terminal                      error
	gate, writer                  chan struct{}
	ctx                           context.Context
	cancel                        context.CancelFunc
	closing                       atomic.Bool
	done, processDone, readerDone chan struct{}
	stopOnce, closeOnce           sync.Once
	closeError                    error
}

func Start(ctx context.Context, spec agentbridge.LaunchSpec, connection any, options Options) (*Client, error) {
	c, _, err := StartWithReceipt(ctx, spec, connection, options)
	return c, err
}

// StartWithReceipt preserves the original protocol and connect acknowledgements
// even when startup fails. A failed start waits for its captured process to exit.
func StartWithReceipt(ctx context.Context, spec agentbridge.LaunchSpec, connection any, options Options) (*Client, StartupReceipt, error) {
	var startup StartupReceipt
	if ctx == nil {
		return nil, startup, ErrRequest
	}
	if options.MaxFrameBytes == 0 {
		options.MaxFrameBytes = 64 << 20
	}
	if options.CancelWait == 0 {
		options.CancelWait = 15 * time.Second
	}
	if options.MaxFrameBytes < 1024 || options.MaxFrameBytes > 64<<20 || options.CancelWait <= 0 || options.CancelWait > time.Minute {
		return nil, startup, ErrRequest
	}
	raw, err := json.Marshal(connection)
	if err != nil {
		return nil, startup, ErrRequest
	}
	if _, err = object(raw); err != nil {
		return nil, startup, ErrRequest
	}
	if !filepath.IsAbs(spec.Executable) || len(spec.Artifacts) == 0 {
		return nil, startup, ErrArtifact
	}
	executableVerified := false
	for _, a := range spec.Artifacts {
		if !filepath.IsAbs(a.Path) || len(a.SHA256) != 64 {
			return nil, startup, ErrArtifact
		}
		expected, e := hex.DecodeString(a.SHA256)
		if e != nil {
			return nil, startup, ErrArtifact
		}
		f, e := os.Open(a.Path)
		if e != nil {
			return nil, startup, ErrArtifact
		}
		h := sha256.New()
		_, e = io.Copy(h, f)
		_ = f.Close()
		if e != nil || !bytes.Equal(h.Sum(nil), expected) {
			return nil, startup, ErrArtifact
		}
		executableVerified = executableVerified || filepath.Clean(a.Path) == filepath.Clean(spec.Executable)
		if ctx.Err() != nil {
			return nil, startup, ctx.Err()
		}
	}
	if !executableVerified {
		return nil, startup, ErrArtifact
	}
	cmd := exec.Command(spec.Executable, append([]string(nil), spec.Args...)...)
	cmd.Dir = spec.Directory
	if spec.Environment != nil {
		cmd.Env = append(os.Environ(), spec.Environment...)
	}
	cmd.Stderr = io.Discard
	owner, err := prepareProcess(cmd)
	if err != nil {
		return nil, startup, ErrLaunch
	}
	inputRead, inputWrite, err := os.Pipe()
	if err != nil {
		owner.release(cmd)
		return nil, startup, ErrLaunch
	}
	outputRead, outputWrite, err := os.Pipe()
	if err != nil {
		inputRead.Close()
		inputWrite.Close()
		owner.release(cmd)
		return nil, startup, ErrLaunch
	}
	cmd.Stdin, cmd.Stdout = inputRead, outputWrite
	if err = cmd.Start(); err != nil {
		inputRead.Close()
		inputWrite.Close()
		outputRead.Close()
		outputWrite.Close()
		owner.release(cmd)
		return nil, startup, ErrLaunch
	}
	inputRead.Close()
	outputWrite.Close()
	c := &Client{cmd: cmd, owner: owner, stdin: inputWrite, stdout: outputRead, options: options, connection: append(json.RawMessage(nil), raw...), pending: map[uint64]chan response{}, gate: make(chan struct{}, 1), writer: make(chan struct{}, 1), done: make(chan struct{}), processDone: make(chan struct{}), readerDone: make(chan struct{})}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.gate <- struct{}{}
	c.writer <- struct{}{}
	go func() { _ = cmd.Wait(); owner.release(cmd); close(c.processDone) }()
	go c.read()
	if err = owner.attach(cmd); err == nil && spec.Started != nil {
		err = spec.Started(cmd.Process.Pid)
	}
	if err != nil {
		c.stop(ErrLaunch)
		_ = c.Close()
		startup.ProcessExitConfirmed = true
		return nil, startup, ErrLaunch
	}
	c.protocol, err = c.exchange(ctx, "jdbcSessionProtocol", json.RawMessage(`{}`), "")
	startup.Protocol = c.protocol.Clone()
	if err == nil {
		fields, e := object(c.protocol.Result)
		var version int
		if e != nil || json.Unmarshal(fields["version"], &version) != nil || version != 2 {
			err = ErrProtocol
		}
	}
	if err == nil {
		startup.Connect, err = c.call(ctx, "connect", nil, false)
		if err == nil && !okResult(startup.Connect.Result) {
			err = ErrProtocol
		}
	}
	if err != nil || ctx.Err() != nil {
		c.stop(ErrLost)
		_ = c.Close()
		startup.ProcessExitConfirmed = true
		return nil, startup, errors.Join(err, ctx.Err())
	}
	return c, startup, nil
}

func (c *Client) PID() int                 { return c.cmd.Process.Pid }
func (c *Client) ProtocolReceipt() Receipt { return c.protocol.Clone() }
func (c *Client) Alive() bool {
	select {
	case <-c.done:
		return false
	case <-c.processDone:
		return false
	default:
		return !c.closing.Load()
	}
}

// Call snapshots parameters before waiting for the original executor. Callers
// cannot replace the bound connection, inject a logical session or cancel a
// different request. Queued cancellation never reaches the current executor.
func (c *Client) Call(ctx context.Context, method string, params any) (Receipt, error) {
	if !allowed(method) {
		return Receipt{}, ErrRequest
	}
	return c.call(ctx, method, params, false)
}

func (c *Client) call(ctx context.Context, method string, params any, typed bool) (Receipt, error) {
	if ctx == nil {
		return Receipt{}, ErrRequest
	}
	raw := json.RawMessage(`{}`)
	var err error
	if params != nil {
		raw, err = json.Marshal(params)
		if err != nil {
			return Receipt{}, ErrRequest
		}
	}
	fields, err := object(raw)
	if err != nil {
		return Receipt{}, ErrRequest
	}
	for _, key := range []string{"connection", "jdbcSessionId", "cancelToken", "nativeDiagnostics"} {
		if _, ok := fields[key]; ok {
			return Receipt{}, ErrRequest
		}
	}
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	defer func() { stop(); cancel() }()
	select {
	case <-child.Done():
		return Receipt{}, child.Err()
	case <-c.gate:
	}
	defer func() { c.gate <- struct{}{} }()
	if !c.Alive() {
		return Receipt{}, ErrClosed
	}
	if child.Err() != nil {
		return Receipt{}, child.Err()
	}
	if typed {
		fields["nativeDiagnostics"] = json.RawMessage(`true`)
	}
	fields["connection"] = append(json.RawMessage(nil), c.connection...)
	token := rand.Text()
	fields["cancelToken"], _ = json.Marshal(token)
	raw, _ = json.Marshal(fields)
	r, err := c.exchange(child, method, raw, token)
	if typed {
		return c.settleQuery(r, err)
	}
	return r, err
}
func allowed(method string) bool {
	switch method {
	case "connectionInfo", "executeQuery", "executeQueryPage", "fetchQueryPage", "closeQuerySession", "listDatabases", "listSchemas", "listTables", "getColumns", "listObjects", "listIndexes", "getObjectSource", "listDataTypes", "read_large_value_chunk", "release_large_value":
		return true
	}
	return false
}
func (c *Client) stop(err error) {
	c.stopOnce.Do(func() {
		c.cancel()
		c.mu.Lock()
		c.terminal = err
		for id, ch := range c.pending {
			ch <- response{err: err}
			delete(c.pending, id)
		}
		close(c.done)
		c.mu.Unlock()
		_ = c.stdin.Close()
		c.owner.kill(c.cmd)
		_ = c.stdout.Close()
	})
}
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.closing.Store(true)
		c.cancel()
		<-c.gate
		defer func() { c.connection = nil; c.gate <- struct{}{} }()
		select {
		case <-c.done:
		default:
			ctx, cancel := context.WithTimeout(context.Background(), c.options.CancelWait)
			receipt, err := c.exchange(ctx, "close", json.RawMessage(`{}`), "")
			if err == nil && !okResult(receipt.Result) {
				err = ErrProtocol
			}
			if err == nil {
				select {
				case <-c.processDone:
				case <-ctx.Done():
					err = ErrLost
				}
			}
			cancel()
			c.closeError = err
		}
		c.stop(ErrClosed)
		<-c.processDone
		<-c.readerDone
	})
	return c.closeError
}
func (c *Client) read() {
	defer close(c.readerDone)
	reader := bufio.NewReaderSize(c.stdout, 32<<10)
	for {
		var frame []byte
		for {
			part, err := reader.ReadSlice('\n')
			if len(frame)+len(part) > c.options.MaxFrameBytes {
				c.stop(ErrLimit)
				return
			}
			frame = append(frame, part...)
			if err == nil {
				break
			}
			if !errors.Is(err, bufio.ErrBufferFull) {
				c.stop(ErrLost)
				return
			}
		}
		fields, err := object(frame)
		if err != nil {
			c.stop(ErrProtocol)
			return
		}
		var id uint64
		if json.Unmarshal(fields["id"], &id) != nil || id == 0 {
			c.stop(ErrProtocol)
			return
		}
		result, hasResult := fields["result"]
		remote, hasError := fields["error"]
		if hasResult == hasError {
			c.stop(ErrProtocol)
			return
		}
		if hasError {
			details, e := object(remote)
			var message string
			if e != nil || json.Unmarshal(details["message"], &message) != nil {
				c.stop(ErrProtocol)
				return
			}
		}
		receipt := Receipt{Result: append(json.RawMessage(nil), result...), RemoteFailure: hasError, ExecutorAcknowledged: true, original: append(json.RawMessage(nil), frame...)}
		c.mu.Lock()
		ch := c.pending[id]
		if ch == nil {
			c.mu.Unlock()
			c.stop(ErrProtocol)
			return
		}
		delete(c.pending, id)
		ch <- response{receipt: receipt}
		c.mu.Unlock()
	}
}
func (c *Client) submit(ctx context.Context, method string, params json.RawMessage) (chan response, bool, error) {
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case <-c.done:
		return nil, false, ErrLost
	case <-c.writer:
	}
	defer func() { c.writer <- struct{}{} }()
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	c.mu.Lock()
	if c.terminal != nil {
		err := c.terminal
		c.mu.Unlock()
		return nil, false, err
	}
	if c.nextID == math.MaxUint64 || len(c.pending) >= 3 {
		c.mu.Unlock()
		return nil, false, ErrLimit
	}
	c.nextID++
	id := c.nextID
	ch := make(chan response, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	raw, err := json.Marshal(struct {
		ID     uint64          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}{id, method, params})
	if err != nil || len(raw)+1 > c.options.MaxFrameBytes {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, false, ErrLimit
	}
	var writing atomic.Bool
	writing.Store(true)
	stop := context.AfterFunc(ctx, func() {
		if writing.CompareAndSwap(true, false) {
			c.stop(ErrLost)
		}
	})
	_, err = c.stdin.Write(append(raw, '\n'))
	writing.Store(false)
	stop()
	if err != nil {
		c.stop(ErrLost)
		<-c.processDone
		return nil, true, errors.Join(ErrLost, ctx.Err())
	}
	return ch, true, nil
}
func (c *Client) finish(v response, dispatched, canceled bool, cause error) (Receipt, error) {
	r := v.receipt
	r.Dispatched = dispatched
	r.CancellationRequested = canceled
	if v.err != nil {
		<-c.processDone
		r.ProcessExitConfirmed = true
		return r, errors.Join(v.err, cause)
	}
	select {
	case <-c.processDone:
		r.ProcessExitConfirmed = true
	default:
	}
	if r.RemoteFailure {
		return r, errors.Join(ErrRemote, cause)
	}
	return r, cause
}
func (c *Client) exchange(ctx context.Context, method string, params json.RawMessage, token string) (Receipt, error) {
	ch, dispatched, err := c.submit(ctx, method, params)
	if err != nil {
		r := Receipt{Dispatched: dispatched, CancellationRequested: dispatched && ctx.Err() != nil}
		select {
		case <-c.processDone:
			r.ProcessExitConfirmed = true
		default:
		}
		return r, err
	}
	select {
	case v := <-ch:
		return c.finish(v, true, false, nil)
	case <-ctx.Done():
		if token == "" {
			c.stop(ErrLost)
			return c.finish(<-ch, true, true, ctx.Err())
		}
		wait, cancel := context.WithTimeout(context.Background(), c.options.CancelWait)
		defer cancel()
		raw, _ := json.Marshal(map[string]string{"cancelToken": token})
		cancelDone := make(chan response, 1)
		go func() {
			r, e := c.exchange(wait, "cancelStatement", raw, "")
			if e == nil {
				f, bad := object(r.Result)
				var matched bool
				if bad != nil || json.Unmarshal(f["cancelled"], &matched) != nil {
					e = ErrProtocol
				}
			}
			cancelDone <- response{receipt: r, err: e}
		}()
		var original response
		var cancellation response
		gotOriginal, gotCancel := false, false
		for !gotOriginal || !gotCancel {
			select {
			case v := <-ch:
				original = v
				gotOriginal = true
			case value := <-cancelDone:
				cancellation = value
				gotCancel = true
				if value.err != nil {
					c.stop(ErrLost)
					<-c.processDone
				}
			case <-wait.Done():
				c.stop(ErrLost)
				if !gotOriginal {
					original = <-ch
					gotOriginal = true
				}
				<-c.processDone
				if !gotCancel {
					cancellation = <-cancelDone
					gotCancel = true
				}
			}
		}
		original.receipt.cancellation = cancellation.receipt.OriginalResponse()
		return c.finish(original, true, true, errors.Join(ctx.Err(), cancellation.err))
	}
}
