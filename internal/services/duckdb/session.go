// Package duckdb implements embedded DuckDB sessions through the neutral
// workbench contracts. UI modules retain the sole shared data grid.
package duckdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/duckdb/native"
)

type Session struct {
	mu             sync.Mutex
	profile        connections.Profile
	db, sqlDB      *sql.DB
	connector      *native.Connector
	ctx            context.Context
	cancel         context.CancelFunc
	closed         bool
	catalog, scope string
	leases         map[*queryLease]bool
	transactions   map[*transaction]bool
	slots          chan struct{}
}

func OpenSession(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
	p, err := connections.NormalizeProfile(p)
	if err != nil {
		return nil, err
	}
	if p.Kind != connections.DuckDB || p.ID <= 0 || password != "" {
		return nil, connections.ErrInvalid
	}
	a := p.DuckDBOptions
	c, err := native.Open(p.Database, native.Options{LibraryFile: a.LibraryFile, ReadOnly: a.ReadOnly, RequireExisting: a.RequireExisting, MemoryMB: a.MemoryLimitMB, Threads: a.Threads})
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(a.MaxConnections)
	db.SetMaxIdleConns(min(2, a.MaxConnections))
	var catalog string
	if err = db.QueryRowContext(ctx, "SELECT current_database()").Scan(&catalog); err != nil {
		db.Close()
		c.Close()
		return nil, safeError(err)
	}
	if ctx.Err() != nil {
		db.Close()
		c.Close()
		return nil, ctx.Err()
	}
	sqlDB := sql.OpenDB(c)
	sqlDB.SetMaxOpenConns(a.SQLMaxConnections)
	sqlDB.SetMaxIdleConns(0)
	sctx, cancel := context.WithCancel(context.Background())
	return &Session{profile: p, db: db, sqlDB: sqlDB, connector: c, ctx: sctx, cancel: cancel, catalog: catalog, scope: rand.Text(), leases: map[*queryLease]bool{}, transactions: map[*transaction]bool{}, slots: make(chan struct{}, a.SQLMaxConnections)}, nil
}
func (s *Session) ProfileID() int64   { return s.profile.ID }
func (s *Session) Generation() uint64 { return 1 }
func (s *Session) Catalog() string    { return s.catalog }
func (s *Session) Capabilities() contract.Capabilities {
	d, _ := connections.Describe(connections.DuckDB)
	c := d.Capabilities
	if s.profile.DuckDBOptions.ReadOnly {
		c.EditableTables = false
		c.Import = false
		c.Transactions = false
	}
	return c
}
func (s *Session) Alive() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.closed }
func (s *Session) request(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if !s.Alive() {
		return nil, nil, contract.ErrRevoked
	}
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return child, func() { stop(); cancel() }, nil
}
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	var leases []*queryLease
	for l := range s.leases {
		leases = append(leases, l)
	}
	var txs []*transaction
	for t := range s.transactions {
		txs = append(txs, t)
	}
	s.mu.Unlock()
	for _, l := range leases {
		l.Release()
	}
	for _, t := range txs {
		t.Close()
	}
	err := errors.Join(s.sqlDB.Close(), s.db.Close())
	s.connector.Close()
	return err
}
func safeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, native.ErrResultType) {
		return native.ErrResultType
	}
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, contract.ErrRevoked, contract.ErrSQLSessionLost, contract.ErrSQLSessionLimit, contract.ErrDataTransactionLost, contract.ErrDataCommitUncertain, contract.ErrDataReadOnly, contract.ErrUnsupported, contract.ErrDataConflict, contract.ErrDataPage, contract.ErrDataEditLimit, connections.ErrInvalid, connections.ErrNotFound} {
		if errors.Is(err, known) {
			return known
		}
	}
	// DuckDB diagnostics can include SQL literals and bound values. Do not
	// publish them in status, import journals or logs.
	return errors.New("DuckDB operation failed; check the SQL, object types and file permissions")
}
