package dpi

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// transport owns exactly one upstream socket and one native client's socket.
// DPI does not expose its socket. The private loopback bridge lets cancellation
// interrupt original I/O without freeing handles underneath a native call.
// Explicit TLS parses only the bounded initial greeting. After authentication,
// it forwards bytes without reconnecting, retrying or recording them.
type transport struct {
	listener     net.Listener
	upstream     net.Conn
	mu           sync.Mutex
	downstream   net.Conn
	closed       bool
	done         chan struct{}
	startup      context.Context
	negotiateTLS bool
	remoteTLS    *tls.Config
	localTLS     *tls.Config
	failure      error
}

func openTransport(ctx context.Context, endpoint string, timeout time.Duration) (*transport, error) {
	return openConfiguredTransport(ctx, endpoint, timeout, false, nil, nil)
}

func openConfiguredTransport(ctx context.Context, endpoint string, timeout time.Duration, negotiate bool, remote, local *tls.Config) (*transport, error) {
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
	t := &transport{listener: listener, upstream: upstream, done: make(chan struct{}), startup: ctx, negotiateTLS: negotiate, remoteTLS: remote, localTLS: local}
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
	reader, writer := downstream, t.upstream
	if t.negotiateTLS {
		var err error
		reader, writer, err = t.negotiate(downstream)
		if err != nil {
			t.mu.Lock()
			t.failure = err
			t.mu.Unlock()
			t.closeSockets()
			return
		}
	}
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		io.Copy(writer, reader)
		t.closeSockets()
	}()
	io.Copy(reader, writer)
	t.closeSockets()
	<-joined
}

func (t *transport) startupFailure() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.failure
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
