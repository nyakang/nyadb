// Package rqlite implements original native HTTP requests behind neutral
// workbench contracts. A session owns its endpoint, credentials and receipts.
package rqlite

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type Session struct {
	mu                               sync.Mutex
	profile                          connections.Profile
	config                           connections.RQLiteConfig
	password, version, sqliteVersion string
	dataScope                        string
	client                           *http.Client
	transport                        *http.Transport
	closed                           bool
	closing, closedDone              chan struct{}
	requests                         chan struct{}
	active                           sync.WaitGroup
	leases                           map[*queryLease]bool
	documents                        map[*dataDocument]bool
}

var serverVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][a-zA-Z0-9.-]+)?$`)

func OpenSession(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
	var err error
	p, err = connections.NormalizeProfile(p)
	if err != nil {
		return nil, err
	}
	if p.ID <= 0 || p.Kind != connections.RQLite {
		return nil, connections.ErrInvalid
	}
	c, err := connections.RQLiteProfileConfig(p)
	if err != nil || connections.ValidateRQLiteCredential(c, password) != nil {
		return nil, connections.ErrInvalid
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: p.Host, InsecureSkipVerify: p.TLSMode == "require"} // Explicit insecure TLS setting.
	if c.Options.ServerName != "" {
		tlsConfig.ServerName = c.Options.ServerName
	}
	if c.Options.CAFile != "" {
		raw, e := os.ReadFile(c.Options.CAFile)
		if e != nil || len(raw) > 4<<20 {
			return nil, errors.New("RQLite TLS roots could not be loaded")
		}
		roots, e := x509.SystemCertPool()
		if e != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(raw) {
			return nil, errors.New("RQLite TLS roots are invalid")
		}
		tlsConfig.RootCAs = roots
	}
	if c.Options.ClientCertFile != "" {
		cert, e := tls.LoadX509KeyPair(c.Options.ClientCertFile, c.Options.ClientKeyFile)
		if e != nil {
			return nil, errors.New("RQLite TLS client identity could not be loaded")
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}
	d := &net.Dialer{Timeout: time.Duration(c.Options.ConnectTimeoutSeconds) * time.Second}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: d.DialContext, TLSClientConfig: tlsConfig, TLSHandshakeTimeout: d.Timeout, MaxConnsPerHost: c.Options.MaxRequests, MaxIdleConnsPerHost: c.Options.MaxRequests, IdleConnTimeout: 90 * time.Second, DisableCompression: true}
	client := &http.Client{Transport: transport, Timeout: time.Duration(c.Options.RequestTimeoutSeconds) * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var scope [32]byte
	if _, err := rand.Read(scope[:]); err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	s := &Session{profile: p, config: c, password: password, dataScope: hex.EncodeToString(scope[:]), client: client, transport: transport, closing: make(chan struct{}), closedDone: make(chan struct{}), requests: make(chan struct{}, c.Options.MaxRequests), leases: make(map[*queryLease]bool), documents: make(map[*dataDocument]bool)}
	// Query-only credentials need no /status permission. The native version
	// header and SQLite handshake identify the original serving product.
	r, err := s.request(ctx, "/db/query", []any{"SELECT 1,sqlite_version()"}, false, false)
	if err == nil && (len(r.Results) != 1 || len(r.Results[0].Values) != 1 || len(r.Results[0].Values[0]) != 2) {
		err = errIncomplete
	}
	if err == nil {
		cells, e := r.Results[0].cells(r.Results[0].Values[0])
		err = e
		if e == nil && (cells[0].Kind != contract.Integer || cells[0].Text != "1" || cells[1].Kind != contract.Text || !serverVersion.MatchString(cells[1].Text)) {
			err = errIncomplete
		}
		if err == nil {
			s.sqliteVersion = cells[1].Text
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Session) ProfileID() int64   { return s.profile.ID }
func (s *Session) Generation() uint64 { return 1 } // Public generation belongs to dbruntime.
func (s *Session) Capabilities() contract.Capabilities {
	return contract.Capabilities{Query: true, Metadata: true, Count: true, FieldChunks: true}
}
func (s *Session) Alive() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.closed }
func (s *Session) ServerVersions() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version, s.sqliteVersion
}

func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		done := s.closedDone
		s.mu.Unlock()
		<-done
		return nil
	}
	s.closed = true
	close(s.closing)
	all := make([]*queryLease, 0, len(s.leases))
	for l := range s.leases {
		all = append(all, l)
	}
	documents := make([]*dataDocument, 0, len(s.documents))
	for d := range s.documents {
		documents = append(documents, d)
	}
	s.mu.Unlock()
	for _, d := range documents {
		_ = d.Close()
	}
	for _, l := range all {
		l.Release()
	}
	s.active.Wait() // Original dispatched requests keep their own bounded contexts.
	s.transport.CloseIdleConnections()
	s.mu.Lock()
	s.password = ""
	close(s.closedDone)
	s.mu.Unlock()
	return nil
}

var _ contract.Session = (*Session)(nil)
var _ contract.QueryProvider = (*Session)(nil)
var _ contract.MetadataProvider = (*Session)(nil)
