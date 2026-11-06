// Package sqlserver implements TDS sessions behind the neutral workbench
// contracts. SQL documents pin physical connections; data jobs use a separate
// pool and never execute user SQL or reuse its transaction state.
package sqlserver

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"sync"

	mssql "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/msdsn"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type Session struct {
	mu           sync.Mutex
	profile      connections.Profile
	db, sqlDB    *sql.DB
	ctx          context.Context
	cancel       context.CancelFunc
	closed       bool
	scope        string
	leases       map[*queryLease]bool
	transactions map[*transaction]bool
	slots        chan struct{}
}

func connector(p connections.Profile, password string) (*mssql.Connector, error) {
	a := p.SQLServerOptions
	u := url.URL{Scheme: "sqlserver", Host: net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), User: url.UserPassword(p.Username, password)}
	v := url.Values{"database": {p.Database}, "app name": {a.ApplicationName}, "connection timeout": {strconv.Itoa(a.ConnectTimeoutSeconds)}, "tlsmin": {"1.2"}, "encrypt": {"true"}, "TrustServerCertificate": {"false"}, "disableRetry": {"true"}}
	if p.TLSMode == "disable" {
		v.Set("encrypt", "disable")
	}
	if p.TLSMode == "require" {
		v.Set("TrustServerCertificate", "true")
	}
	if a.ServerName != "" {
		v.Set("hostNameInCertificate", a.ServerName)
	}
	u.RawQuery = v.Encode()
	config, err := msdsn.Parse(u.String())
	if err != nil {
		return nil, safeError(err)
	}
	if a.CAFile != "" {
		raw, err := os.ReadFile(a.CAFile)
		if err != nil || len(raw) > 1<<20 || config.TLSConfig == nil {
			return nil, errors.New("SQL Server CA certificate could not be loaded")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(raw) {
			certificate, err := x509.ParseCertificate(raw)
			if err != nil {
				return nil, errors.New("invalid SQL Server CA certificate")
			}
			pool.AddCert(certificate)
		}
		config.TLSConfig.RootCAs = pool
	}
	c := mssql.NewConnectorConfig(config)
	c.SessionInitSQL = "SET QUOTED_IDENTIFIER ON; SET ANSI_NULLS ON; SET XACT_ABORT ON;"
	return c, nil
}

func OpenSession(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
	var err error
	p, err = connections.NormalizeProfile(p)
	if err != nil {
		return nil, err
	}
	if p.Kind != connections.SQLServer || p.ID <= 0 {
		return nil, connections.ErrInvalid
	}
	c, err := connector(p, password)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(p.SQLServerOptions.MaxConnections)
	db.SetMaxIdleConns(min(2, p.SQLServerOptions.MaxConnections))
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, safeError(err)
	}
	if ctx.Err() != nil {
		_ = db.Close()
		return nil, ctx.Err()
	}
	sqlDB := sql.OpenDB(c)
	sqlDB.SetMaxOpenConns(p.SQLServerOptions.SQLMaxConnections)
	sqlDB.SetMaxIdleConns(0)
	sctx, cancel := context.WithCancel(context.Background())
	return &Session{profile: p, scope: rand.Text(), db: db, sqlDB: sqlDB, ctx: sctx, cancel: cancel, leases: map[*queryLease]bool{}, transactions: map[*transaction]bool{}, slots: make(chan struct{}, p.SQLServerOptions.SQLMaxConnections)}, nil
}
func (s *Session) ProfileID() int64   { return s.profile.ID }
func (s *Session) Generation() uint64 { return 1 }
func (s *Session) Catalog() string    { return s.profile.Database }
func (s *Session) Capabilities() contract.Capabilities {
	d, _ := connections.Describe(connections.SQLServer)
	return d.Capabilities
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
	leases := make([]*queryLease, 0, len(s.leases))
	for l := range s.leases {
		leases = append(leases, l)
	}
	txs := make([]*transaction, 0, len(s.transactions))
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
	return errors.Join(s.sqlDB.Close(), s.db.Close())
}
func safeError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, contract.ErrRevoked, contract.ErrSQLSessionLost, contract.ErrSQLSessionLimit, contract.ErrDataTransactionLost, contract.ErrDataCommitUncertain, contract.ErrDataReadOnly, contract.ErrUnsupported, contract.ErrDataConflict, contract.ErrDataPage, contract.ErrDataEditLimit, connections.ErrInvalid} {
		if errors.Is(err, known) {
			return known
		}
	}
	var e mssql.Error
	if errors.As(err, &e) {
		return fmt.Errorf("SQL Server error %d (state %d, line %d)", e.Number, e.State, e.LineNo)
	}
	return errors.New("SQL Server connection or operation failed")
}
