// Package manticore implements private native Manticore Search sessions.
// It uses the MySQL wire driver without a pool, reconnects or SQL retries.
package manticore

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type Session struct {
	mu                      sync.Mutex
	profileID               int64
	config                  connections.ManticoreConfig
	password, version       string
	ctx                     context.Context
	cancel                  context.CancelFunc
	closed                  bool
	leases                  map[*queryLease]bool
	dataLeases              map[*dataLease]bool
	schemaDocuments         map[string]*schemaDocument
	nextOwner               uint64
	dataSlots               chan struct{}
	metadataSlots, sqlSlots chan struct{}
	active                  sync.WaitGroup
	closeOnce               sync.Once
	closedDone              chan struct{}
}

// Open is intentionally not registered with dbruntime or the public catalog.
func Open(ctx context.Context, profileID int64, cfg connections.ManticoreConfig, password string) (*Session, error) {
	if ctx == nil || profileID <= 0 {
		return nil, connections.ErrInvalid
	}
	cfg, err := connections.NormalizeManticoreConfig(cfg)
	if err != nil {
		return nil, err
	}
	root, cancel := context.WithCancel(context.Background())
	s := &Session{profileID: profileID, config: cfg, password: password, ctx: root, cancel: cancel, leases: map[*queryLease]bool{}, dataLeases: map[*dataLease]bool{}, dataSlots: make(chan struct{}, cfg.MaxConnections), metadataSlots: make(chan struct{}, cfg.MaxConnections), sqlSlots: make(chan struct{}, cfg.SQLMaxConnections), closedDone: make(chan struct{})}
	err = s.withConnection(ctx, func(child context.Context, c driver.Conn) error {
		namespaces, e := read(child, c, "SHOW DATABASES", 0, nil, nil)
		if e != nil {
			return e
		}
		if !hasDataCatalog(namespaces) {
			return contract.ErrUnsupported
		}
		version, e := read(child, c, "SELECT VERSION()", 0, nil, nil)
		if e != nil {
			return e
		}
		if len(version.Rows) != 1 || len(version.Rows[0]) != 1 || version.Rows[0][0].Text == "" {
			return contract.ErrUnsupported
		}
		s.version = version.Rows[0][0].Text
		tables, e := read(child, c, "SHOW TABLES", 0, nil, nil)
		if e != nil {
			return e
		}
		_, e = tableObjects(s.Namespace("Manticore"), tables)
		return e
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

func connectionConfig(c connections.ManticoreConfig, password string) *mysqldriver.Config {
	cfg := mysqldriver.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(c.Host, strconv.Itoa(int(c.Port)))
	cfg.User = c.Username
	cfg.Passwd = password
	cfg.Timeout = time.Duration(c.ConnectTimeoutSeconds) * time.Second
	cfg.ReadTimeout = time.Duration(c.QueryTimeoutSeconds) * time.Second
	cfg.WriteTimeout = cfg.ReadTimeout
	cfg.Logger = log.New(io.Discard, "", 0)
	// No DSN options, initialization SQL, local infile, multi-statements or pool.
	return cfg
}

func (s *Session) connect(ctx context.Context) (driver.Conn, error) {
	s.mu.Lock()
	password, closed := s.password, s.closed
	s.mu.Unlock()
	if closed {
		return nil, contract.ErrRevoked
	}
	cfg := connectionConfig(s.config, password)
	connector, err := mysqldriver.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	return connector.Connect(ctx)
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
	child, cancel := context.WithTimeout(ctx, time.Duration(s.config.QueryTimeoutSeconds)*time.Second)
	stop := context.AfterFunc(s.ctx, cancel)
	return child, func() { stop(); cancel(); s.active.Done() }, nil
}

func closeConnection(c driver.Conn) {
	if c != nil {
		_ = c.Close()
	}
}

func (s *Session) withConnection(ctx context.Context, work func(context.Context, driver.Conn) error) error {
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
func (s *Session) ProfileID() int64      { return s.profileID }
func (s *Session) Generation() uint64    { return 1 }
func (s *Session) Catalog() string       { return "Manticore" }
func (s *Session) Alive() bool           { s.mu.Lock(); defer s.mu.Unlock(); return !s.closed }
func (s *Session) Capabilities() contract.Capabilities {
	return contract.Capabilities{Query: true, Metadata: true}
}
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel()
		documents := make([]*dataLease, 0, len(s.dataLeases))
		for d := range s.dataLeases {
			documents = append(documents, d)
		}
		schemas := make([]*schemaDocument, 0, len(s.schemaDocuments))
		for _, d := range s.schemaDocuments {
			schemas = append(schemas, d)
		}
		all := make([]*queryLease, 0, len(s.leases))
		for l := range s.leases {
			all = append(all, l)
		}
		s.mu.Unlock()
		for _, d := range documents {
			_ = d.Close()
		}
		for _, d := range schemas {
			_ = d.Close()
		}
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
	for _, known := range []error{connections.ErrNotFound, contract.ErrRevoked, contract.ErrSQLSessionLost, contract.ErrUnsupported, connections.ErrInvalid, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return known
		}
	}
	var server *mysqldriver.MySQLError
	if errors.As(err, &server) {
		message := server.Message
		if password != "" {
			message = strings.ReplaceAll(message, password, "[redacted]")
		}
		if len(message) > 512 {
			message = message[:512]
		}
		return fmt.Errorf("Manticore server error [%d]: %s", server.Number, message)
	}
	return errors.New("Manticore connection or operation failed")
}

func (s *Session) Version() string { return s.version }
