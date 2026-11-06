// Package mongodb implements native commands and BSON document workspaces.
package mongodb

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
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

type Session struct {
	mu      sync.Mutex
	profile connections.Profile
	client  *mongo.Client
	ctx     context.Context
	cancel  context.CancelFunc
	closed  bool
	key     [32]byte
	leases  map[*queryLease]bool
}

func clientOptions(p connections.Profile, password string) (*options.ClientOptions, error) {
	a := p.MongoDBOptions
	hosts := []string{net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))}
	if a.Seeds != "" {
		hosts = append(hosts, strings.Split(a.Seeds, ",")...)
	}
	o := options.Client()
	if a.SRV {
		o.ApplyURI("mongodb+srv://" + p.Host)
	} else {
		o.SetHosts(hosts)
	}
	o.SetConnectTimeout(time.Duration(a.ConnectTimeoutSeconds) * time.Second).SetServerSelectionTimeout(time.Duration(a.ConnectTimeoutSeconds) * time.Second).SetMaxPoolSize(uint64(a.MaxConnections)).SetAppName(a.ApplicationName).SetRetryReads(false).SetRetryWrites(false)
	if !a.SRV {
		o.SetDirect(a.DirectConnection)
	}
	if a.ReplicaSet != "" {
		o.SetReplicaSet(a.ReplicaSet)
	}
	if p.Username != "" {
		o.SetAuth(options.Credential{Username: p.Username, Password: password, PasswordSet: true, AuthSource: a.AuthSource, AuthMechanism: a.AuthMechanism})
	} else if password != "" {
		return nil, connections.ErrInvalid
	}
	if p.TLSMode == "verify-full" {
		config := &tls.Config{MinVersion: tls.VersionTLS12}
		if a.CAFile != "" {
			roots, err := x509.SystemCertPool()
			if err != nil {
				roots = x509.NewCertPool()
			}
			raw, err := os.ReadFile(a.CAFile)
			if err != nil || len(raw) > 1<<20 || !roots.AppendCertsFromPEM(raw) {
				return nil, errors.New("MongoDB CA certificate could not be loaded")
			}
			config.RootCAs = roots
		}
		o.SetTLSConfig(config)
	}
	return o, nil
}

func OpenSession(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
	p, err := connections.NormalizeProfile(p)
	if err != nil {
		return nil, err
	}
	if p.Kind != connections.MongoDB || p.ID <= 0 {
		return nil, connections.ErrInvalid
	}
	o, err := clientOptions(p, password)
	if err != nil {
		return nil, err
	}
	c, err := mongo.Connect(o)
	if err != nil {
		return nil, safeError(err)
	}
	open, stop := context.WithTimeout(ctx, time.Duration(p.MongoDBOptions.ConnectTimeoutSeconds)*time.Second)
	err = c.Ping(open, readpref.Primary())
	stop()
	if err != nil || ctx.Err() != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.Disconnect(closeCtx)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, safeError(err)
	}
	sctx, cancel := context.WithCancel(context.Background())
	s := &Session{profile: p, client: c, ctx: sctx, cancel: cancel, leases: map[*queryLease]bool{}}
	if _, err = rand.Read(s.key[:]); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
func (s *Session) ProfileID() int64   { return s.profile.ID }
func (s *Session) Generation() uint64 { return 1 }
func (s *Session) Capabilities() contract.Capabilities {
	d, _ := connections.Describe(connections.MongoDB)
	return d.Capabilities
}
func (s *Session) Alive() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.closed }
func (s *Session) Namespace(name string) contract.NamespaceRef {
	return contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: name}
}
func (s *Session) request(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if !s.Alive() {
		return nil, nil, contract.ErrRevoked
	}
	c, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return c, func() { stop(); cancel() }, nil
}
func (s *Session) object(o contract.ObjectRef) error {
	if o.ProfileID != s.ProfileID() || o.Schema != "" || o.Catalog == "" || o.Name == "" || o.Kind != "collection" && o.Kind != "view" || strings.ContainsRune(o.Catalog, 0) || strings.ContainsRune(o.Name, 0) {
		return connections.ErrInvalid
	}
	return nil
}
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	leas := make([]*queryLease, 0, len(s.leases))
	for l := range s.leases {
		leas = append(leas, l)
	}
	s.mu.Unlock()
	for _, l := range leas {
		l.Release()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return safeError(s.client.Disconnect(ctx))
}
func safeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, contract.ErrRevoked) || errors.Is(err, contract.ErrUnsupported) {
		return err
	}
	if errors.Is(err, mongo.ErrNoDocuments) {
		return contract.ErrDataConflict
	}
	var command mongo.CommandError
	if errors.As(err, &command) {
		return fmt.Errorf("MongoDB command failed (code %d)", command.Code)
	}
	var writes mongo.WriteException
	if errors.As(err, &writes) && len(writes.WriteErrors) > 0 {
		return fmt.Errorf("MongoDB write failed (code %d)", writes.WriteErrors[0].Code)
	}
	return errors.New("MongoDB operation failed; check connectivity, authentication and server permissions")
}

var _ contract.Session = (*Session)(nil)
var _ contract.MetadataProvider = (*Session)(nil)
var _ contract.QueryProvider = (*Session)(nil)
