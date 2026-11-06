// Package oracle implements Oracle sessions behind the neutral workbench
// contracts, keeping SQL leases and background data transactions separate.
package oracle

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/sijms/go-ora/v2/network"
)

type Session struct {
	mu             sync.Mutex
	profile        connections.Profile
	db, sqlDB      *sql.DB
	ctx            context.Context
	cancel         context.CancelFunc
	closed         bool
	catalog, scope string
	crypto         bool
	leases         map[*queryLease]bool
	transactions   map[*transaction]bool
	slots          chan struct{}
}

func connector(p connections.Profile, password string) (*wireConnector, error) {
	a := p.OracleOptions
	u := url.URL{Scheme: "oracle", Host: net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), User: url.UserPassword(p.Username, password)}
	options := url.Values{"CONNECT TIMEOUT": {strconv.Itoa(a.ConnectTimeoutSeconds)}, "TIMEOUT": {"0"}, "PREFETCH_ROWS": {"20"}, "LOB FETCH": {"STREAM"}, "PROGRAM": {a.ApplicationName}}
	if a.ConnectionType == "sid" {
		options.Set("SID", p.Database)
	} else {
		u.Path = "/" + p.Database
	}
	var config *tls.Config
	if p.TLSMode != "disable" {
		options.Set("SSL", "true")
		config = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}
		if p.TLSMode == "verify-full" {
			roots, err := x509.SystemCertPool()
			if err != nil {
				roots = x509.NewCertPool()
			}
			if a.CAFile != "" {
				raw, err := os.ReadFile(a.CAFile)
				if err != nil || len(raw) > 1<<20 || !roots.AppendCertsFromPEM(raw) {
					return nil, errors.New("Oracle CA certificate could not be loaded")
				}
			}
			host := p.Host
			if a.ServerName != "" {
				host = a.ServerName
			}
			// Upstream overwrites TLS ServerName with the transport host. Verify
			// the requested identity explicitly; the TLS config is per connection.
			config.VerifyConnection = func(state tls.ConnectionState) error {
				if len(state.PeerCertificates) == 0 {
					return errors.New("Oracle server certificate is missing")
				}
				intermediate := x509.NewCertPool()
				for _, cert := range state.PeerCertificates[1:] {
					intermediate.AddCert(cert)
				}
				_, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: intermediate})
				return err
			}
		}
	}
	u.RawQuery = options.Encode()
	return &wireConnector{dsn: u.String(), tls: config, init: []string{"ALTER SESSION SET NLS_NUMERIC_CHARACTERS='.,'", "ALTER SESSION SET TIME_ZONE='+00:00'"}}, nil
}

func OpenSession(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
	p, err := connections.NormalizeProfile(p)
	if err != nil {
		return nil, err
	}
	if p.Kind != connections.Oracle || p.ID <= 0 {
		return nil, connections.ErrInvalid
	}
	c, err := connector(p, password)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(p.OracleOptions.MaxConnections)
	db.SetMaxIdleConns(min(2, p.OracleOptions.MaxConnections))
	openCtx, stop := context.WithTimeout(ctx, time.Duration(p.OracleOptions.ConnectTimeoutSeconds)*time.Second)
	defer stop()
	var catalog string
	if err = db.QueryRowContext(openCtx, "SELECT SYS_CONTEXT('USERENV','CON_NAME') FROM dual").Scan(&catalog); err != nil {
		db.Close()
		return nil, safeError(err)
	}
	if ctx.Err() != nil {
		db.Close()
		return nil, ctx.Err()
	}
	var digest string
	crypto := db.QueryRowContext(openCtx, "SELECT RAWTOHEX(DBMS_CRYPTO.HASH(UTL_RAW.CAST_TO_RAW('nyadb'),4)) FROM dual").Scan(&digest) == nil
	sqlDB := sql.OpenDB(c)
	sqlDB.SetMaxOpenConns(p.OracleOptions.SQLMaxConnections)
	sqlDB.SetMaxIdleConns(0)
	sctx, cancel := context.WithCancel(context.Background())
	return &Session{profile: p, db: db, sqlDB: sqlDB, ctx: sctx, cancel: cancel, catalog: catalog, scope: rand.Text(), crypto: crypto, leases: map[*queryLease]bool{}, transactions: map[*transaction]bool{}, slots: make(chan struct{}, p.OracleOptions.SQLMaxConnections)}, nil
}
func (s *Session) ProfileID() int64   { return s.profile.ID }
func (s *Session) Generation() uint64 { return 1 }
func (s *Session) Catalog() string    { return s.catalog }
func (s *Session) Capabilities() contract.Capabilities {
	d, _ := connections.Describe(connections.Oracle)
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
	return errors.Join(s.sqlDB.Close(), s.db.Close())
}
func safeError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, contract.ErrRevoked, contract.ErrSQLSessionLost, contract.ErrSQLSessionLimit, contract.ErrDataTransactionLost, contract.ErrDataCommitUncertain, contract.ErrDataReadOnly, contract.ErrUnsupported, contract.ErrDataConflict, contract.ErrDataPage, contract.ErrDataEditLimit, connections.ErrInvalid, connections.ErrNotFound} {
		if errors.Is(err, known) {
			return known
		}
	}
	var ora *network.OracleError
	if errors.As(err, &ora) {
		return fmt.Errorf("Oracle error ORA-%05d", ora.ErrCode)
	}
	return errors.New("Oracle connection or operation failed")
}
