package dpi

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// transport owns exactly one upstream socket and one native client's socket.
// DPI does not expose its socket. The private loopback bridge lets cancellation
// interrupt original I/O without freeing handles underneath a native call.
// It neither interprets messages nor reconnects, retries or records their bytes.
type transport struct {
	listener   net.Listener
	upstream   net.Conn
	mu         sync.Mutex
	downstream net.Conn
	closed     bool
	done       chan struct{}
}

func openTransport(ctx context.Context, endpoint string, timeout time.Duration) (*transport, error) {
	dialer := net.Dialer{Timeout: timeout}
	upstream, err := dialer.DialContext(ctx, "tcp", endpoint)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("DM8 explicit endpoint is unavailable")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		upstream.Close()
		return nil, errors.New("DM8 private native transport is unavailable")
	}
	t := &transport{listener: listener, upstream: upstream, done: make(chan struct{})}
	go t.serve()
	return t, nil
}

func (t *transport) serve() {
	defer close(t.done)
	downstream, err := t.listener.Accept()
	if err != nil {
		t.closeSockets()
		return
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		downstream.Close()
		return
	}
	t.downstream = downstream
	t.mu.Unlock()
	t.listener.Close()
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		io.Copy(t.upstream, downstream)
		t.closeSockets()
	}()
	io.Copy(downstream, t.upstream)
	t.closeSockets()
	<-joined
}

func (t *transport) closeSockets() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	t.listener.Close()
	t.upstream.Close()
	if t.downstream != nil {
		t.downstream.Close()
	}
}

func (t *transport) Close() {
	if t != nil {
		t.closeSockets()
		<-t.done
	}
}

// watch returns a join function. It cannot interrupt another request once the
// original call has completed, and callers join it before freeing native state.
func (t *transport) watch(ctx context.Context) func() {
	finished := make(chan struct{})
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-finished:
		case <-ctx.Done():
			select {
			case <-finished:
				return
			default:
				t.Close()
			}
		}
	}()
	return func() { close(finished); <-joined }
}
