// Package doris implements Apache Doris features independently of MySQL OLTP
// services. Every physical connection verifies its native product identity.
package doris

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type Session struct {
	mu       sync.Mutex
	profile  connections.Profile
	password string
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	leases   map[*queryLease]bool
}

type physical struct {
	db   *sql.DB
	conn *sql.Conn
}

func (p *physical) Close() {
	if p != nil {
		_ = p.conn.Close()
		_ = p.db.Close()
	}
}

// database/sql may retry ErrBadConn. This connector admits exactly one dial:
// neither a failed command nor lost session settings can trigger reconnection.
type onceConnector struct {
	driver.Connector
	opened atomic.Bool
}

func (c *onceConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if !c.opened.CompareAndSwap(false, true) {
		return nil, contract.ErrSQLSessionLost
	}
	return c.Connector.Connect(ctx)
}

type silentLogger struct{}

func (silentLogger) Print(...any) {} // The driver's diagnostics can contain endpoint/SQL data.

func openPhysical(ctx context.Context, p connections.Profile, password string) (_ *physical, err error) {
	cfg := mysqldriver.NewConfig()
	cfg.User, cfg.Passwd, cfg.Net, cfg.Addr = p.Username, password, "tcp", net.JoinHostPort(p.Host, fmt.Sprint(p.Port))
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 10*time.Second, time.Hour, 30*time.Second
	cfg.CheckConnLiveness = false
	cfg.MultiStatements, cfg.InterpolateParams, cfg.ParseTime = false, false, false
	cfg.Logger = silentLogger{}
	if p.TLSMode == "verify-full" {
		name := p.DorisOptions.ServerName
		if name == "" {
			name = p.Host
		}
		cfg.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: name}
		if p.DorisOptions.CAFile != "" {
			raw, e := os.ReadFile(p.DorisOptions.CAFile)
			if e != nil || len(raw) > 1<<20 {
				return nil, errors.New("Doris CA certificate cannot be read")
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(raw) {
				return nil, errors.New("invalid Doris CA certificate")
			}
			cfg.TLS.RootCAs = pool
		}
	}
	connector, err := mysqldriver.NewConnector(cfg)
	if err != nil {
		return nil, safeError(err)
	}
	db := sql.OpenDB(&onceConnector{Connector: connector})
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, safeError(err)
	}
	f := &physical{db: db, conn: conn}
	defer func() {
		if err != nil {
			f.Close()
		}
	}()
	var comment, catalog, mode string
	if err = conn.QueryRowContext(ctx, "SELECT @@version_comment,CURRENT_CATALOG(),@@sql_mode").Scan(&comment, &catalog, &mode); err != nil {
		return nil, safeError(err)
	}
	// VERSION() is the compatibility string 5.7.99, not the Doris kernel.
	if !strings.HasPrefix(comment, "doris version doris-4.1.3-") || catalog != "internal" {
		return nil, errors.New("unsupported Doris product or kernel; requires native Doris 4.1.3")
	}
	for _, flag := range strings.Split(strings.ToUpper(mode), ",") {
		if flag = strings.TrimSpace(flag); flag == "NO_BACKSLASH_ESCAPES" || flag == "ANSI_QUOTES" {
			return nil, errors.New("Doris SQL mode changes the supported literal rules")
		}
	}
	if _, err = conn.ExecContext(ctx, "SET return_object_data_as_binary=true,enable_decimal256=true"); err != nil {
		return nil, safeError(err)
	}
	if p.Database != "" {
		if !validName(p.Database) {
			return nil, connections.ErrInvalid
		}
		if _, err = conn.ExecContext(ctx, "USE "+quote(p.Database)); err != nil {
			return nil, safeError(err)
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return f, nil
}

func OpenSession(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
	p, err := connections.NormalizeProfile(p)
	if err != nil {
		return nil, err
	}
	if p.Kind != connections.Doris || p.ID <= 0 {
		return nil, connections.ErrInvalid
	}
	f, err := openPhysical(ctx, p, password)
	if err != nil {
		return nil, err
	}
	f.Close()
	sctx, cancel := context.WithCancel(context.Background())
	return &Session{profile: p, password: password, ctx: sctx, cancel: cancel, leases: map[*queryLease]bool{}}, nil
}
func (s *Session) ProfileID() int64   { return s.profile.ID }
func (s *Session) Generation() uint64 { return 1 }
func (s *Session) Capabilities() contract.Capabilities {
	d, _ := connections.Describe(connections.Doris)
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
	s.password = ""
	all := make([]*queryLease, 0, len(s.leases))
	for l := range s.leases {
		all = append(all, l)
	}
	s.mu.Unlock()
	for _, l := range all {
		l.Release()
	}
	return nil
}
func (s *Session) request(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if !s.Alive() {
		return nil, nil, contract.ErrRevoked
	}
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return child, func() { stop(); cancel() }, nil
}
func (s *Session) open(ctx context.Context) (*physical, error) {
	s.mu.Lock()
	p, password, closed := s.profile, s.password, s.closed
	s.mu.Unlock()
	if closed {
		return nil, contract.ErrRevoked
	}
	return openPhysical(ctx, p, password)
}
func safeError(err error) error {
	if err == nil {
		return nil
	}
	for _, e := range []error{context.Canceled, context.DeadlineExceeded, contract.ErrRevoked, contract.ErrSQLSessionLost, contract.ErrUnsupported, connections.ErrInvalid} {
		if errors.Is(err, e) {
			return e
		}
	}
	var authority x509.UnknownAuthorityError
	if errors.As(err, &authority) {
		return errors.New("Doris TLS certificate authority is not trusted")
	}
	var hostname x509.HostnameError
	if errors.As(err, &hostname) {
		return errors.New("Doris TLS server name mismatch")
	}
	var server *mysqldriver.MySQLError
	if errors.As(err, &server) {
		return fmt.Errorf("Doris server error (code %d)", server.Number)
	}
	return errors.New("Doris connection or operation failed")
}

var _ contract.MetadataProvider = (*Session)(nil)
var _ contract.QueryProvider = (*Session)(nil)
var _ contract.QueryFileLease = (*queryLease)(nil)
var _ contract.DataReadsProvider = (*Session)(nil)
var _ contract.DataExportsProvider = (*Session)(nil)
