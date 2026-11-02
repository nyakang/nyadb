// Package clickhouse implements the native ClickHouse protocol behind neutral
// workbench features. It deliberately provides no relational editing contract.
package clickhouse

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type Session struct {
	mu       sync.Mutex
	profile  connections.Profile
	password string
	conn     driver.Conn
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	leases   map[*queryLease]bool
}

func OpenSession(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
	var err error
	p, err = connections.NormalizeProfile(p)
	if err != nil {
		return nil, err
	}
	if p.Kind != connections.ClickHouse || p.ID <= 0 {
		return nil, connections.ErrInvalid
	}
	c, err := openNative(p, password, p.Database, false)
	if err != nil {
		return nil, safeError(err)
	}
	if err = c.Ping(ctx); err != nil {
		_ = c.Close()
		return nil, safeError(err)
	}
	if ctx.Err() != nil {
		_ = c.Close()
		return nil, ctx.Err()
	}
	sctx, cancel := context.WithCancel(context.Background())
	return &Session{profile: p, password: password, conn: c, ctx: sctx, cancel: cancel, leases: map[*queryLease]bool{}}, nil
}

// Dedicated query clients forbid a second physical dial. A native pool would
// otherwise silently replace a connection and lose session settings/temp data.
// Namespace changes are explicit new clients before executing any user SQL.
func openNative(p connections.Profile, password, database string, dedicated bool) (driver.Conn, error) {
	options := &ch.Options{Addr: []string{net.JoinHostPort(p.Host, fmt.Sprint(p.Port))}, Auth: ch.Auth{Database: database, Username: p.Username, Password: password}, DialTimeout: 10 * time.Second, ReadTimeout: time.Hour, MaxOpenConns: 8, MaxIdleConns: 2, Settings: ch.Settings{"max_block_size": 512}, ClientInfo: ch.ClientInfo{Products: []struct{ Name, Version string }{{Name: "NyaDB", Version: "1"}}}}
	if p.TLSMode == "verify-full" {
		options.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: p.Host}
	}
	if dedicated {
		options.MaxOpenConns, options.MaxIdleConns = 1, 1
		options.ConnMaxLifetime = 100 * 365 * 24 * time.Hour
		var dialed atomic.Bool
		options.DialContext = func(ctx context.Context, addr string) (net.Conn, error) {
			if !dialed.CompareAndSwap(false, true) {
				return nil, contract.ErrSQLSessionLost
			}
			d := &net.Dialer{Timeout: 10 * time.Second}
			if options.TLS != nil {
				return (&tls.Dialer{NetDialer: d, Config: options.TLS}).DialContext(ctx, "tcp", addr)
			}
			return d.DialContext(ctx, "tcp", addr)
		}
	}
	return ch.Open(options)
}

func (s *Session) ProfileID() int64   { return s.profile.ID }
func (s *Session) Generation() uint64 { return 1 } // dbruntime owns public generations.
func (s *Session) Capabilities() contract.Capabilities {
	d, _ := connections.Describe(connections.ClickHouse)
	return d.Capabilities
}
func (s *Session) Alive() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.closed }
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	all := make([]*queryLease, 0, len(s.leases))
	for l := range s.leases {
		all = append(all, l)
	}
	s.password = ""
	s.mu.Unlock()
	for _, l := range all {
		l.Release()
	}
	return s.conn.Close()
}
func (s *Session) request(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if !s.Alive() {
		return nil, nil, contract.ErrRevoked
	}
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return child, func() { stop(); cancel() }, nil
}

// Do not return ClickHouse exception messages: they can contain SQL, literal
// values, remote URLs or credentials. Numeric codes remain useful diagnostics.
func safeError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, contract.ErrRevoked, contract.ErrSQLSessionLost, contract.ErrUnsupported, connections.ErrInvalid} {
		if errors.Is(err, known) {
			return known
		}
	}
	var exception *ch.Exception
	if errors.As(err, &exception) {
		return fmt.Errorf("ClickHouse server error (code %d)", exception.Code)
	}
	return errors.New("ClickHouse connection or operation failed")
}

var _ contract.MetadataProvider = (*Session)(nil)
var _ contract.QueryProvider = (*Session)(nil)
var _ contract.DataReadsProvider = (*Session)(nil)
var _ contract.DataExportsProvider = (*Session)(nil)
