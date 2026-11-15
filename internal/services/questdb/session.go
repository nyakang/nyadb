// Package questdb implements native QuestDB semantics over PostgreSQL transport.
package questdb

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type Session struct {
	mu                           sync.Mutex
	profile                      connections.Profile
	password, version, tableMode string
	ctx                          context.Context
	cancel                       context.CancelFunc
	closed                       bool
	leases                       map[*queryLease]bool
	metadataSlots, sqlSlots      chan struct{}
	active                       sync.WaitGroup
	closeOnce                    sync.Once
	closedDone                   chan struct{}
}

func OpenSession(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
	if ctx == nil {
		return nil, connections.ErrInvalid
	}
	var err error
	p, err = connections.NormalizeProfile(p)
	if err != nil || p.Kind != connections.QuestDB || p.ID <= 0 {
		return nil, connections.ErrInvalid
	}
	root, cancel := context.WithCancel(context.Background())
	s := &Session{profile: p, password: password, ctx: root, cancel: cancel, leases: map[*queryLease]bool{}, metadataSlots: make(chan struct{}, p.QuestDBOptions.MaxConnections), sqlSlots: make(chan struct{}, p.QuestDBOptions.SQLMaxConnections), closedDone: make(chan struct{})}
	err = s.withConnection(ctx, func(child context.Context, c *pgx.Conn) error {
		set, e := execute(child, c, "SELECT build()", nil, 0, nil, nil)
		if e != nil {
			return e
		}
		if len(set.Rows) != 1 || len(set.Rows[0]) != 1 || !strings.Contains(set.Rows[0][0].Text, "QuestDB ") {
			return contract.ErrUnsupported
		}
		s.version = set.Rows[0][0].Text
		for _, mode := range []string{"table_type", "matView", "basic"} {
			if _, e = execute(child, c, tablesSQL(mode), nil, 0, nil, nil); e == nil {
				s.tableMode = mode
				return nil
			}
			var server *pgconn.PgError
			if !errors.As(e, &server) {
				return e
			}
		}
		return contract.ErrUnsupported
	})
	if err != nil || ctx.Err() != nil {
		_ = s.Close()
		if err == nil {
			err = ctx.Err()
		}
		return nil, safeError(err, password)
	}
	return s, nil
}

func connectionConfig(p connections.Profile, password string) (*pgx.ConnConfig, error) {
	u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), Path: "/qdb", User: url.User(p.Username)}
	q := u.Query()
	q.Set("sslmode", p.TLSMode)
	q.Set("sslrootcert", p.QuestDBOptions.CAFile)
	q.Set("sslcert", "")
	q.Set("sslkey", "")
	q.Set("sslpassword", "")
	q.Set("passfile", "")
	u.RawQuery = q.Encode()
	cfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		return nil, connections.ErrInvalid
	}
	cfg.Password = password
	cfg.RuntimeParams = map[string]string{}
	cfg.Fallbacks = nil
	cfg.ConnectTimeout = time.Duration(p.QuestDBOptions.ConnectTimeoutSeconds) * time.Second
	cfg.DefaultQueryExecMode = pgx.QueryExecModeExec
	return cfg, nil
}

func (s *Session) connect(ctx context.Context) (*pgx.Conn, error) {
	s.mu.Lock()
	password, closed := s.password, s.closed
	s.mu.Unlock()
	if closed {
		return nil, contract.ErrRevoked
	}
	cfg, err := connectionConfig(s.profile, password)
	if err != nil {
		return nil, err
	}
	return pgx.ConnectConfig(ctx, cfg)
}

func (s *Session) request(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, connections.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, contract.ErrRevoked
	}
	s.active.Add(1)
	s.mu.Unlock()
	child, cancel := context.WithTimeout(ctx, time.Duration(s.profile.QuestDBOptions.QueryTimeoutSeconds)*time.Second)
	stop := context.AfterFunc(s.ctx, cancel)
	return child, func() { stop(); cancel(); s.active.Done() }, nil
}

func closeConnection(c *pgx.Conn) {
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = c.Close(ctx)
}

func (s *Session) withConnection(ctx context.Context, work func(context.Context, *pgx.Conn) error) error {
	child, done, err := s.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	select {
	case s.metadataSlots <- struct{}{}:
	case <-child.Done():
		return child.Err()
	}
	defer func() { <-s.metadataSlots }()
	c, err := s.connect(child)
	if err != nil {
		return safeError(err, s.profileSecret())
	}
	defer closeConnection(c)
	return safeError(work(child, c), s.profileSecret())
}

func (s *Session) profileSecret() string { s.mu.Lock(); defer s.mu.Unlock(); return s.password }
func (s *Session) ProfileID() int64      { return s.profile.ID }
func (s *Session) Generation() uint64    { return 1 }
func (s *Session) Catalog() string       { return "qdb" }
func (s *Session) Alive() bool           { s.mu.Lock(); defer s.mu.Unlock(); return !s.closed }
func (s *Session) Capabilities() contract.Capabilities {
	d, _ := connections.Describe(connections.QuestDB)
	return d.Capabilities
}
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel()
		all := make([]*queryLease, 0, len(s.leases))
		for l := range s.leases {
			all = append(all, l)
		}
		s.mu.Unlock()
		for _, l := range all {
			l.Release()
		}
		s.active.Wait()
		s.mu.Lock()
		s.password = ""
		s.mu.Unlock()
		close(s.closedDone)
	})
	<-s.closedDone
	return nil
}

func safeError(err error, password string) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{contract.ErrRevoked, contract.ErrSQLSessionLost, contract.ErrUnsupported, connections.ErrInvalid, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return known
		}
	}
	var server *pgconn.PgError
	if errors.As(err, &server) {
		message := server.Message
		if password != "" {
			message = strings.ReplaceAll(message, password, "[redacted]")
		}
		if len(message) > 512 {
			message = message[:512]
		}
		return fmt.Errorf("QuestDB server error [%s]: %s", server.Code, message)
	}
	return errors.New("QuestDB connection or operation failed")
}
