package agentbridge

import (
	"bufio"
	"bytes"
	"context"
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
)

type Artifact struct{ Path, SHA256 string }

// LaunchSpec is public runtime configuration. Credentials belong only in the
// original session's stdin request, never arguments, environment or logs.
// The driver installer supplies fixed artifact digests; Start never installs,
// selects, upgrades or restarts a driver automatically.
type LaunchSpec struct {
	Executable        string
	Args, Environment []string
	Directory         string
	Artifacts         []Artifact
	// Started records this captured process before readiness/connect can wait.
	// It is a synchronous lifecycle observer, not a session publication callback.
	Started func(int) error
}

type Options struct {
	MaxFrameBytes int
	MaxInFlight   int
	CancelWait    time.Duration
	// RetainOriginalResponses keeps complete v2 envelopes for explicit native
	// inspection. Ordinary receipt serialization and errors remain sanitized.
	RetainOriginalResponses bool
	// ErrorCodes selects the original structured error code representation.
	// Default SQLStateCodes retains strict JDBC/SQLSTATE validation.
	ErrorCodes ErrorCodeFormat
}

func normalizeOptions(o Options) (Options, error) {
	if o.MaxFrameBytes == 0 {
		o.MaxFrameBytes = 64 << 20
	}
	if o.MaxInFlight == 0 {
		o.MaxInFlight = 16
	}
	if o.CancelWait == 0 {
		o.CancelWait = 15 * time.Second
	}
	if o.MaxFrameBytes < 1024 || o.MaxFrameBytes > 512<<20 || o.MaxInFlight < 1 || o.MaxInFlight > 32 || o.CancelWait <= 0 || o.CancelWait > time.Minute || o.ErrorCodes > CQLCodes {
		return o, ErrRequest
	}
	return o, nil
}

type pendingCall struct {
	result     chan response
	sessionID  string
	structured bool
}

// Client owns one captured process. Every terminal path closes and waits for
// that process; callers must construct a new Client to recover explicitly.
type Client struct {
	mu               sync.Mutex
	cmd              *exec.Cmd
	owner            *processOwner
	stdin, stdout    *os.File
	options          Options
	protocolVersion  int
	metadata         Handshake
	handshakeReceipt Receipt
	readyResponse    json.RawMessage
	nextID           uint64
	pending          map[uint64]*pendingCall
	sessions         map[*Session]bool
	terminal         error
	ready            chan struct{}
	done             chan struct{}
	processDone      chan struct{}
	readerDone       chan struct{}
	writer           chan struct{}
	slots            chan struct{}
	stopOnce         sync.Once
	closeOnce        sync.Once
	closing          atomic.Bool
	closeError       error
}

func Start(ctx context.Context, spec LaunchSpec, options Options) (*Client, error) {
	return start(ctx, spec, options, 2)
}

func start(ctx context.Context, spec LaunchSpec, options Options, version int) (*Client, error) {
	if ctx == nil {
		return nil, ErrRequest
	}
	o, err := normalizeOptions(options)
	if err != nil {
		return nil, err
	}
	spec.Args = append([]string(nil), spec.Args...)
	spec.Environment = append([]string(nil), spec.Environment...)
	spec.Artifacts = append([]Artifact(nil), spec.Artifacts...)
	for _, artifact := range spec.Artifacts {
		if err = verifyArtifact(ctx, artifact); err != nil {
			return nil, err
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	path, err := exec.LookPath(spec.Executable)
	if err != nil {
		return nil, ErrLaunch
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, ErrLaunch
	}
	cmd := exec.Command(path, spec.Args...)
	cmd.Dir = spec.Directory
	if spec.Environment != nil {
		cmd.Env = append(os.Environ(), spec.Environment...)
	}
	// Native stderr can contain passwords and query text. It is never captured,
	// surfaced, or passed to workbench logging by this protocol boundary.
	cmd.Stderr = io.Discard
	owner, err := prepareProcess(cmd)
	if err != nil {
		return nil, ErrLaunch
	}
	inputRead, inputWrite, err := os.Pipe()
	if err != nil {
		owner.release(cmd)
		return nil, ErrLaunch
	}
	outputRead, outputWrite, err := os.Pipe()
	if err != nil {
		inputRead.Close()
		inputWrite.Close()
		owner.release(cmd)
		return nil, ErrLaunch
	}
	cmd.Stdin, cmd.Stdout = inputRead, outputWrite
	if err = cmd.Start(); err != nil {
		inputRead.Close()
		inputWrite.Close()
		outputRead.Close()
		outputWrite.Close()
		owner.release(cmd)
		return nil, ErrLaunch
	}
	inputRead.Close()
	outputWrite.Close()
	c := &Client{cmd: cmd, owner: owner, stdin: inputWrite, stdout: outputRead, options: o, protocolVersion: version,
		pending: map[uint64]*pendingCall{}, sessions: map[*Session]bool{}, ready: make(chan struct{}),
		done: make(chan struct{}), processDone: make(chan struct{}), readerDone: make(chan struct{}),
		writer: make(chan struct{}, 1), slots: make(chan struct{}, o.MaxInFlight)}
	c.writer <- struct{}{}
	go func() {
		_ = cmd.Wait()
		owner.release(cmd)
		close(c.processDone)
	}()
	if err = owner.attach(cmd); err != nil {
		c.stop(ErrLaunch)
		<-c.processDone
		outputRead.Close()
		return nil, ErrLaunch
	}
	if spec.Started != nil {
		if err = spec.Started(c.PID()); err != nil {
			c.stop(ErrLaunch)
			<-c.processDone
			outputRead.Close()
			return nil, ErrLaunch
		}
	}
	go c.readResponses()
	select {
	case <-c.ready:
	case <-ctx.Done():
		c.Close()
		return nil, ctx.Err()
	case <-c.done:
		c.Close()
		return nil, c.failure()
	case <-c.processDone:
		c.Close()
		return nil, ErrLost
	}
	params, _ := json.Marshal(map[string]any{"appVersion": "nyadb", "protocolVersion": version})
	r, err := c.call(ctx, "handshake", params, "", true)
	if err != nil {
		c.Close()
		return nil, err
	}
	h, err := decodeHandshakeVersion(r.Result, version)
	if err != nil {
		c.Close()
		return nil, err
	}
	c.mu.Lock()
	c.metadata = h
	c.handshakeReceipt = cloneReceipts([]Receipt{r})[0]
	c.mu.Unlock()
	if ctx.Err() != nil || !c.Alive() {
		c.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrLost
	}
	return c, nil
}

func verifyArtifact(ctx context.Context, artifact Artifact) error {
	if !filepath.IsAbs(artifact.Path) || len(artifact.SHA256) != 64 {
		return ErrArtifact
	}
	expected, err := hex.DecodeString(artifact.SHA256)
	if err != nil {
		return ErrArtifact
	}
	f, err := os.Open(artifact.Path)
	if err != nil {
		return ErrArtifact
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > 1<<30 {
		return ErrArtifact
	}
	h := sha256.New()
	buffer := make([]byte, 64<<10)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, err := f.Read(buffer)
		h.Write(buffer[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return ErrArtifact
		}
	}
	if !bytes.Equal(h.Sum(nil), expected) {
		return ErrArtifact
	}
	return nil
}

func (c *Client) PID() int                { return c.cmd.Process.Pid }
func (c *Client) Exited() <-chan struct{} { return c.processDone }
func (c *Client) Alive() bool {
	if c.closing.Load() {
		return false
	}
	select {
	case <-c.done:
		return false
	case <-c.processDone:
		return false
	default:
		return true
	}
}
func (c *Client) Handshake() Handshake {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.metadata
	h.Capabilities = append([]string(nil), h.Capabilities...)
	return h
}

// HandshakeReceipt and ReadyResponse return owned original protocol evidence.
// Original v2 bytes are retained only when requested at process construction.
func (c *Client) HandshakeReceipt() Receipt {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneReceipts([]Receipt{c.handshakeReceipt})[0]
}

func (c *Client) ReadyResponse() json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append(json.RawMessage(nil), c.readyResponse...)
}
func (c *Client) failure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.terminal != nil {
		return c.terminal
	}
	return ErrLost
}

func (c *Client) stop(cause error) {
	c.stopOnce.Do(func() {
		c.mu.Lock()
		c.terminal = cause
		pending := c.pending
		c.pending = map[uint64]*pendingCall{}
		all := make([]*Session, 0, len(c.sessions))
		for s := range c.sessions {
			all = append(all, s)
		}
		c.sessions = map[*Session]bool{}
		close(c.done)
		c.mu.Unlock()
		for _, s := range all {
			s.revoke()
		}
		c.owner.kill(c.cmd)
		c.stdin.Close()
		c.stdout.Close()
		for _, p := range pending {
			p.result <- response{err: cause}
		}
	})
}

func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.closing.Store(true)
		c.mu.Lock()
		graceful := c.terminal == nil && (c.metadata.ProtocolVersion == 2 || c.metadata.ProtocolVersion == 1 && len(c.pending) == 0)
		all := make([]*Session, 0, len(c.sessions))
		for s := range c.sessions {
			all = append(all, s)
		}
		c.mu.Unlock()
		if graceful {
			for _, s := range all {
				s.revoke()
			}
			for _, s := range all {
				_, _ = s.Close()
			}
			ctx, cancel := context.WithTimeout(context.Background(), c.options.CancelWait)
			r, err := c.call(ctx, "shutdown", json.RawMessage(`{}`), "", true)
			if err == nil && !receiptOK(r.Result) {
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
		c.stop(ErrLost)
		<-c.processDone
		<-c.readerDone
	})
	return c.closeError
}

func (c *Client) readResponses() {
	defer close(c.readerDone)
	reader := bufio.NewReaderSize(c.stdout, 32<<10)
	seenReady := false
	for {
		line, err := readFrame(reader, c.options.MaxFrameBytes)
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = ErrLost
			}
			c.stop(err)
			return
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		fields, err := objectFields(line)
		if err != nil {
			c.stop(ErrProtocol)
			return
		}
		if raw, exists := fields["ready"]; exists {
			var ready bool
			if seenReady || len(fields) != 1 || json.Unmarshal(raw, &ready) != nil || !ready {
				c.stop(ErrProtocol)
				return
			}
			seenReady = true
			if c.options.RetainOriginalResponses {
				c.mu.Lock()
				c.readyResponse = append(json.RawMessage(nil), line...)
				c.mu.Unlock()
			}
			close(c.ready)
			continue
		}
		if !seenReady {
			c.stop(ErrProtocol)
			return
		}
		var id uint64
		if json.Unmarshal(fields["id"], &id) != nil || id == 0 {
			c.stop(ErrProtocol)
			return
		}
		version, err := rawString(fields["jsonrpc"])
		result, hasResult := fields["result"]
		remote, hasError := fields["error"]
		if err != nil || version != "2.0" || hasResult == hasError {
			c.stop(ErrProtocol)
			return
		}
		c.mu.Lock()
		p := c.pending[id]
		c.mu.Unlock()
		if p == nil {
			c.stop(ErrProtocol)
			return
		}
		r := response{result: append(json.RawMessage(nil), result...)}
		if c.protocolVersion == 1 || c.options.RetainOriginalResponses {
			r.original = append(json.RawMessage(nil), line...)
		}
		if hasError {
			r.remote, err = decodeRemote(remote, p.structured, p.sessionID, c.options.ErrorCodes)
			if err != nil {
				c.stop(ErrProtocol)
				return
			}
		}
		c.mu.Lock()
		if c.pending[id] != p {
			c.mu.Unlock()
			return
		}
		delete(c.pending, id)
		// Delivery and removal are atomic against terminal draining. The private
		// one-result channel cannot block and has one original consumer only.
		p.result <- r
		c.mu.Unlock()
	}
}

func readFrame(reader *bufio.Reader, limit int) ([]byte, error) {
	var frame []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(frame)+len(part) > limit {
			return nil, ErrLimit
		}
		frame = append(frame, part...)
		if err == nil {
			return frame, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) && len(frame) != 0 {
			return nil, ErrProtocol
		}
		return nil, err
	}
}

func (c *Client) submit(ctx context.Context, method string, params json.RawMessage, sessionID string) (*pendingCall, bool, error) {
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case <-c.done:
		return nil, false, c.failure()
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
	if c.closing.Load() && method != "shutdown" && method != "close_session" && method != "cancel_session" {
		c.mu.Unlock()
		return nil, false, ErrClosed
	}
	select {
	case <-c.processDone:
		c.mu.Unlock()
		return nil, false, ErrLost
	default:
	}
	if c.nextID == math.MaxUint64 || len(c.pending) >= 2*c.options.MaxInFlight+8 {
		c.mu.Unlock()
		return nil, false, ErrLimit
	}
	c.nextID++
	id := c.nextID
	p := &pendingCall{result: make(chan response, 1), sessionID: sessionID, structured: c.metadata.Supports("structured_error_v1")}
	c.pending[id] = p
	c.mu.Unlock()
	raw, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      uint64          `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}{"2.0", id, method, params})
	if err != nil || len(raw)+1 > c.options.MaxFrameBytes {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, false, ErrLimit
	}
	// A blocked stdin write cannot prevent cancellation or Close from retiring
	// the original process. Once writing starts, partial dispatch is uncertain.
	var writing atomic.Bool
	writing.Store(true)
	stopWrite := context.AfterFunc(ctx, func() {
		if writing.CompareAndSwap(true, false) {
			c.stop(ErrLost)
		}
	})
	_, err = c.stdin.Write(append(raw, '\n'))
	writing.Store(false)
	stopWrite()
	if err != nil {
		c.stop(ErrLost)
		<-c.processDone
		return nil, true, errorWithCancellation(ErrLost, ctx.Err())
	}
	return p, true, nil
}

func (c *Client) call(ctx context.Context, method string, params json.RawMessage, sessionID string, control bool) (Receipt, error) {
	r := Receipt{}
	if !control {
		select {
		case c.slots <- struct{}{}:
			defer func() { <-c.slots }()
		case <-ctx.Done():
			return r, ctx.Err()
		case <-c.done:
			return r, c.failure()
		}
	}
	p, dispatched, err := c.submit(ctx, method, params, sessionID)
	r.Dispatched = dispatched
	if err != nil {
		r.CancellationRequested = dispatched && ctx.Err() != nil
		r.ProcessExitConfirmed = c.exited()
		return r, err
	}
	select {
	case response := <-p.result:
		return c.finish(r, response, nil)
	case <-ctx.Done():
		// Retain the original pending channel. Its executor must respond, or its
		// original process must be confirmed dead, before this call can settle.
		r.CancellationRequested = true
		if sessionID == "" || control {
			c.stop(ErrLost)
			<-c.processDone
			return c.finish(r, <-p.result, ctx.Err())
		}
		wait, cancel := context.WithTimeout(context.Background(), c.options.CancelWait)
		defer cancel()
		cancelResult := make(chan error, 1)
		go func() {
			params, _ := json.Marshal(map[string]string{"agentSessionId": sessionID})
			receipt, err := c.call(wait, "cancel_session", params, sessionID, true)
			if err == nil && !receiptOK(receipt.Result) {
				err = ErrProtocol
			}
			cancelResult <- err
		}()
		var original response
		gotOriginal, gotCancel := false, false
		for !gotOriginal || !gotCancel {
			select {
			case value := <-p.result:
				original, gotOriginal = value, true
			case err := <-cancelResult:
				gotCancel = true
				if err != nil {
					c.stop(ErrLost)
				}
			case <-wait.Done():
				c.stop(ErrLost)
				if !gotOriginal {
					original, gotOriginal = <-p.result, true
				}
				<-c.processDone
				// The cancel goroutine now observes the same captured process exit.
				if !gotCancel {
					<-cancelResult
					gotCancel = true
				}
			}
		}
		return c.finish(r, original, ctx.Err())
	}
}

func receiptOK(raw json.RawMessage) bool {
	fields, err := objectFields(raw)
	var ok bool
	return err == nil && json.Unmarshal(fields["ok"], &ok) == nil && ok
}

func (c *Client) finish(r Receipt, native response, canceled error) (Receipt, error) {
	r.Result, r.RemoteError = native.result, native.remote
	r.original = native.original
	if native.err != nil {
		<-c.processDone
		r.ProcessExitConfirmed = true
		return r, errorWithCancellation(native.err, canceled)
	}
	r.ExecutorAcknowledged = true
	if native.remote != nil && native.remote.SessionDisposition == "replace_runtime" {
		c.stop(ErrLost)
		<-c.processDone
	}
	r.ProcessExitConfirmed = c.exited()
	if native.remote != nil {
		return r, errorWithCancellation(native.remote, canceled)
	}
	return r, canceled
}

func (c *Client) exited() bool {
	select {
	case <-c.processDone:
		return true
	default:
		return false
	}
}
