// Package transport provides one cancellable TCP boundary shared by database
// pools and stateful leases. A failed tunnel is terminal; recovery constructs
// a new transport and never replays operations on a replacement channel.
package transport

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/nyakang/nyadb/internal/services/connections"
)

var ErrRevoked = errors.New("database tunnel revoked; explicitly reconnect")

type Dialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
	Close() error
}
type Secrets struct{ SSHPassword, SSHPassphrase, HTTPToken, ProxyPassword, HTTPKeyPassphrase string }
type HostIdentity struct {
	Host        string
	Fingerprint string
}
type ConfirmHost func(context.Context, HostIdentity) (bool, error)
type HostStore interface {
	Lookup(string) (string, bool, error)
	Remember(string, string) error
}
type Options struct {
	Secrets     Secrets
	Hosts       HostStore
	ConfirmHost ConfirmHost
}

// New consumes only non-secret configuration plus a separately injected secret
// snapshot. The returned scope owns every physical channel it dials.
func New(s connections.ActiveSettings, o Options) (Dialer, error) {
	a, err := connections.NormalizeMySQLAdvanced(s.MySQLAdvanced)
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	scope := &scope{ctx: lifetime, cancel: cancel, done: make(chan struct{}), conns: make(map[*trackedConn]struct{}), tunnel: a.SSH.Enabled || a.HTTP.Enabled}
	switch {
	case a.SSH.Enabled:
		scope.dial, scope.finish, err = newSSHDial(a.SSH, o, func() { _ = scope.Close() })
	case a.HTTP.Enabled:
		scope.dial, err = newHTTPDial(a.HTTP, o.Secrets)
	default:
		scope.dial = (&net.Dialer{}).DialContext
	}
	if err != nil {
		cancel()
		return nil, err
	}
	return scope, nil
}

type scope struct {
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	conns   map[*trackedConn]struct{}
	revoked bool
	tunnel  bool
	dial    func(context.Context, string, string) (net.Conn, error)
	finish  func() error
}

func (s *scope) DialContext(ctx context.Context, n, a string) (net.Conn, error) {
	s.mu.Lock()
	revoked := s.revoked
	s.mu.Unlock()
	if revoked {
		return nil, ErrRevoked
	}
	dialCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	c, err := s.dial(dialCtx, n, a)
	if err != nil {
		return nil, err
	}
	t := &trackedConn{Conn: c, owner: s}
	s.mu.Lock()
	if s.revoked {
		s.mu.Unlock()
		_ = c.Close()
		return nil, ErrRevoked
	}
	s.conns[t] = struct{}{}
	s.mu.Unlock()
	if notifier, ok := c.(interface{ Done() <-chan struct{} }); ok {
		go func() { <-notifier.Done(); t.failed(net.ErrClosed) }()
	}
	return t, nil
}
func (s *scope) Close() error {
	s.mu.Lock()
	if s.revoked {
		s.mu.Unlock()
		return nil
	}
	s.revoked = true
	close(s.done)
	all := s.conns
	s.conns = make(map[*trackedConn]struct{})
	s.mu.Unlock()
	s.cancel()
	var err error
	for c := range all {
		err = errors.Join(err, c.closePhysical())
	}
	if s.finish != nil {
		err = errors.Join(err, s.finish())
	}
	return err
}

type trackedConn struct {
	net.Conn
	owner         *scope
	mu            sync.Mutex
	locallyClosed bool
}

func (c *trackedConn) failed(err error) {
	if err == nil || !c.owner.tunnel {
		return
	}
	c.mu.Lock()
	local := c.locallyClosed
	c.mu.Unlock()
	if !local {
		_ = c.owner.Close()
	}
}
func (c *trackedConn) Read(p []byte) (int, error)  { n, e := c.Conn.Read(p); c.failed(e); return n, e }
func (c *trackedConn) Write(p []byte) (int, error) { n, e := c.Conn.Write(p); c.failed(e); return n, e }
func (c *trackedConn) closePhysical() error {
	c.mu.Lock()
	c.locallyClosed = true
	c.mu.Unlock()
	return c.Conn.Close()
}
func (c *trackedConn) Close() error {
	c.mu.Lock()
	c.locallyClosed = true
	c.mu.Unlock()
	c.owner.mu.Lock()
	delete(c.owner.conns, c)
	c.owner.mu.Unlock()
	return c.closePhysical()
}

// Done closes only when the physical transport is terminally revoked.
func (s *scope) Done() <-chan struct{} { return s.done }
