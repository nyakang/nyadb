// Package redis implements a standalone Redis 7+ workbench with native commands.
package redis

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	redisdriver "github.com/redis/go-redis/v9"
)

type Session struct {
	mu      sync.Mutex
	profile connections.Profile
	options redisdriver.Options
	clients map[int]*redisdriver.Client
	leases  map[*queryLease]bool
	ctx     context.Context
	cancel  context.CancelFunc
	closed  bool
	key     [32]byte
}

func OpenSession(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
	p, err := connections.NormalizeProfile(p)
	if err != nil {
		return nil, err
	}
	if p.Kind != connections.Redis || p.ID <= 0 {
		return nil, connections.ErrInvalid
	}
	db, _ := connections.RedisDatabase(p.Database)
	a := p.RedisOptions
	o := redisdriver.Options{Addr: net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), Username: p.Username, Password: password, DB: db, Protocol: 2, MaxRetries: -1, PoolSize: a.MaxConnections, DialTimeout: time.Duration(a.ConnectTimeoutSeconds) * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, ContextTimeoutEnabled: true, DisableIdentity: true}
	if p.TLSMode == "verify-full" {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: a.ServerName}
		if cfg.ServerName == "" {
			cfg.ServerName = p.Host
		}
		if a.CAFile != "" {
			raw, e := os.ReadFile(a.CAFile)
			roots, e2 := x509.SystemCertPool()
			if e2 != nil {
				roots = x509.NewCertPool()
			}
			if e != nil || len(raw) > 1<<20 || !roots.AppendCertsFromPEM(raw) {
				return nil, errors.New("Redis CA certificate could not be loaded")
			}
			cfg.RootCAs = roots
		}
		o.TLSConfig = cfg
	}
	c := redisdriver.NewClient(&o)
	open, stop := context.WithTimeout(ctx, o.DialTimeout)
	defer stop()
	if err = c.Ping(open).Err(); err != nil {
		c.Close()
		return nil, safeError(err)
	}
	info, err := c.Info(open, "server", "cluster").Result()
	if err != nil {
		c.Close()
		return nil, safeError(err)
	}
	if strings.Contains(info, "cluster_enabled:1") || !redis7(info) {
		c.Close()
		return nil, errors.New("Redis requires a standalone Redis 7 or later server")
	}
	if err = ctx.Err(); err != nil {
		c.Close()
		return nil, err
	}
	sctx, cancel := context.WithCancel(context.Background())
	s := &Session{profile: p, options: o, clients: map[int]*redisdriver.Client{db: c}, leases: map[*queryLease]bool{}, ctx: sctx, cancel: cancel}
	if _, err = rand.Read(s.key[:]); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
func redis7(info string) bool {
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, "redis_version:") {
			n, _ := strconv.Atoi(strings.Split(strings.TrimPrefix(line, "redis_version:"), ".")[0])
			return n >= 7
		}
	}
	return false
}
func (s *Session) ProfileID() int64   { return s.profile.ID }
func (s *Session) Generation() uint64 { return 1 }
func (s *Session) Capabilities() contract.Capabilities {
	d, _ := connections.Describe(connections.Redis)
	return d.Capabilities
}
func (s *Session) Alive() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.closed }
func (s *Session) Namespace(db string) contract.NamespaceRef {
	return contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: db}
}
func (s *Session) request(ctx context.Context, db string) (context.Context, context.CancelFunc, *redisdriver.Client, error) {
	n, err := connections.RedisDatabase(db)
	if err != nil {
		return nil, nil, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, nil, contract.ErrRevoked
	}
	c := s.clients[n]
	if c == nil {
		if len(s.clients) >= 64 {
			return nil, nil, nil, errors.New("Redis active database limit reached")
		}
		opts := s.options
		opts.DB = n
		c = redisdriver.NewClient(&opts)
		s.clients[n] = c
	}
	r, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return r, func() { stop(); cancel() }, c, nil
}
func (s *Session) object(o contract.ObjectRef) error {
	if o.ProfileID != s.ProfileID() || o.Schema != "" || o.Kind != "keyspace" || o.Name != "keys" {
		return connections.ErrInvalid
	}
	_, err := connections.RedisDatabase(o.Catalog)
	return err
}
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	clients := s.clients
	s.clients = nil
	leases := make([]*queryLease, 0, len(s.leases))
	for l := range s.leases {
		leases = append(leases, l)
	}
	s.options.Password = ""
	s.mu.Unlock()
	for _, l := range leases {
		l.Release()
	}
	var err error
	for _, c := range clients {
		err = errors.Join(err, c.Close())
	}
	return safeError(err)
}
func safeError(err error) error {
	if err == nil {
		return nil
	}
	for _, e := range []error{context.Canceled, context.DeadlineExceeded, contract.ErrRevoked, contract.ErrUnsupported, contract.ErrDataConflict, contract.ErrSQLSessionLost} {
		if errors.Is(err, e) {
			return e
		}
	}
	if errors.Is(err, redisdriver.Nil) {
		return contract.ErrDataConflict
	}
	var server redisdriver.Error
	if errors.As(err, &server) {
		code := strings.Fields(err.Error())
		if len(code) > 0 {
			switch code[0] {
			case "NOAUTH", "WRONGPASS", "NOPERM", "OOM", "WRONGTYPE", "READONLY", "EXECABORT", "BUSY", "MOVED", "ASK":
				return fmt.Errorf("Redis operation failed (%s)", code[0])
			}
		}
	}
	return errors.New("Redis operation failed; check connectivity, authentication and permissions")
}
