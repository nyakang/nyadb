// Package redis implements a standalone Redis 7+ workbench with native commands.
package redis

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	redisdriver "github.com/redis/go-redis/v9"
)

type Session struct {
	topology *topology
	mu       sync.Mutex
	profile  connections.Profile
	options  redisdriver.Options
	clients  map[int]*redisdriver.Client
	leases   map[*queryLease]bool
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	key      [32]byte
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
	o.TLSConfig, err = redisTLS(p.TLSMode, a.CAFile, a.ServerName)
	if err != nil {
		return nil, err
	}
	sctx, cancel := context.WithCancel(context.Background())
	s := &Session{profile: p, options: o, clients: map[int]*redisdriver.Client{}, leases: map[*queryLease]bool{}, ctx: sctx, cancel: cancel}
	open, stop := context.WithTimeout(ctx, o.DialTimeout)
	defer stop()
	var c *redisdriver.Client
	if a.Topology == "standalone" {
		c = redisdriver.NewClient(&o)
		s.clients[db] = c
	} else {
		s.topology, err = newTopology(p, o, connections.RedisSentinelPassword(ctx))
		if err != nil {
			s.Close()
			return nil, err
		}
		addr := ""
		if a.Topology == "sentinel" {
			addr, err = s.topology.master(open)
		} else {
			var ranges []clusterRange
			ranges, err = s.topology.refresh(open)
			if err == nil {
				addr = ranges[0].Addr
			}
		}
		if err == nil {
			c, err = s.topology.client(addr, db)
		}
		if err != nil {
			s.Close()
			return nil, safeError(err)
		}
	}
	if err = c.Ping(open).Err(); err != nil {
		s.Close()
		return nil, safeError(err)
	}
	info, err := c.Info(open, "server", "cluster").Result()
	if err != nil {
		s.Close()
		return nil, safeError(err)
	}
	cluster := strings.Contains(info, "cluster_enabled:1")
	if !redis7(info) || cluster != (a.Topology == "cluster") {
		s.Close()
		return nil, errors.New("Redis version or server mode does not match the configured topology")
	}
	if err = ctx.Err(); err != nil {
		s.Close()
		return nil, err
	}
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
	if s.topology != nil && s.topology.mode == "cluster" && n != 0 {
		return nil, nil, nil, errors.New("Redis Cluster supports only database 0")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, nil, contract.ErrRevoked
	}
	r, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	done := func() { stop(); cancel() }
	if s.topology == nil {
		c := s.clients[n]
		if c == nil {
			if len(s.clients) >= 64 {
				s.mu.Unlock()
				done()
				return nil, nil, nil, errors.New("Redis active database limit reached")
			}
			o := s.options
			o.DB = n
			c = redisdriver.NewClient(&o)
			s.clients[n] = c
		}
		s.mu.Unlock()
		return r, done, c, nil
	}
	s.mu.Unlock()
	addr := ""
	if s.topology.mode == "sentinel" {
		addr, err = s.topology.master(r)
	} else {
		var nodes []scanNode
		nodes, err = s.topology.masters(r)
		if err == nil {
			addr = nodes[0].Addr
		}
	}
	if err != nil {
		done()
		return nil, nil, nil, safeError(err)
	}
	c, err := s.topology.client(addr, n)
	if err != nil {
		done()
		return nil, nil, nil, err
	}
	return r, done, c, nil
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
	if s.topology != nil {
		err = errors.Join(err, s.topology.Close())
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
