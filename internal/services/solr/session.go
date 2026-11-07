package solr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// Session binds one configured endpoint and observed native node epoch. The
// observation is revocable, not an immutable cluster UUID or remote lock.
type Session struct {
	mu              sync.Mutex
	profile         connections.Profile
	secret          string
	identity        Identity
	catalog         string
	ctx             context.Context
	cancel          context.CancelFunc
	closed, revoked bool
	closeDone       chan struct{}
	work            sync.WaitGroup
	leases          map[*queryLease]bool
}

func OpenSession(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
	p, err := connections.NormalizeProfile(p)
	if err != nil {
		return nil, err
	}
	if p.Kind != connections.Solr || p.ID <= 0 {
		return nil, connections.ErrInvalid
	}
	cfg, err := connections.SolrProfileConfig(p)
	if err != nil {
		return nil, err
	}
	c, err := Open(ctx, cfg, secret)
	if err != nil {
		return nil, err
	}
	identity := c.Identity()
	c.Close()
	if !validObservation(identity) {
		return nil, ErrProtocol
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	root, cancel := context.WithCancel(context.Background())
	return &Session{profile: p, secret: secret, identity: identity, catalog: observationCatalog(cfg.Endpoint, identity), ctx: root, cancel: cancel, leases: map[*queryLease]bool{}, closeDone: make(chan struct{})}, nil
}

func (s *Session) ProfileID() int64   { return s.profile.ID }
func (s *Session) Generation() uint64 { return 1 }
func (s *Session) Capabilities() contract.Capabilities {
	return contract.Capabilities{Objects: true, Metadata: true, Query: true}
}
func validObservation(i Identity) bool {
	started, err := time.Parse(time.RFC3339Nano, i.JVM.JMX.StartTime)
	return err == nil && started.UnixMilli() > 0 && validIdentity(i) && (i.Mode != "solrcloud" || i.ZKHost != "")
}
func observationCatalog(endpoint string, i Identity) string {
	raw, _ := json.Marshal(struct {
		Endpoint string
		Node     Identity
	}{endpoint, i})
	hash := sha256.Sum256(raw)
	return "solr-observation:" + hex.EncodeToString(hash[:])
}
func (s *Session) Alive() bool        { s.mu.Lock(); defer s.mu.Unlock(); return !s.closed && !s.revoked }
func (s *Session) Identity() Identity { return s.identity }
func (s *Session) Namespace(label string) contract.NamespaceRef {
	if label == "" || label == s.catalog || label == s.identity.Mode {
		return contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: s.catalog}
	}
	return contract.NamespaceRef{ProfileID: s.ProfileID()}
}
func (s *Session) checkNamespace(n contract.NamespaceRef) error {
	if !s.Alive() {
		return contract.ErrRevoked
	}
	if n.ProfileID != s.ProfileID() || n.Catalog != s.catalog || n.Schema != "" {
		return connections.ErrInvalid
	}
	return nil
}
func (s *Session) checkObject(o contract.ObjectRef) error {
	if err := s.checkNamespace(o.Namespace()); err != nil {
		return err
	}
	if !validTargetName(o.Name) {
		return connections.ErrInvalid
	}
	if (s.identity.Mode == "std" && o.Kind != "core") || (s.identity.Mode == "solrcloud" && o.Kind != "collection" && o.Kind != "alias") {
		return contract.ErrUnsupported
	}
	return nil
}
func (s *Session) request(ctx context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked {
		return nil, nil, contract.ErrRevoked
	}
	s.work.Add(1)
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return child, func() { stop(); cancel(); s.work.Done() }, nil
}
func (s *Session) open(ctx context.Context) (*Client, error) {
	s.mu.Lock()
	p, secret, dead := s.profile, s.secret, s.closed || s.revoked
	s.mu.Unlock()
	if dead {
		return nil, contract.ErrRevoked
	}
	cfg, err := connections.SolrProfileConfig(p)
	if err != nil {
		return nil, err
	}
	c, err := Open(ctx, cfg, secret)
	if err != nil {
		return nil, err
	}
	if !validObservation(c.Identity()) || c.Identity() != s.identity {
		c.Close()
		s.revoke()
		return nil, contract.ErrRevoked
	}
	if err := ctx.Err(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}
func (s *Session) revoke() {
	s.mu.Lock()
	s.revoked, s.secret = true, ""
	s.cancel()
	s.mu.Unlock()
}

func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.closeDone
		return nil
	}
	s.closed, s.secret = true, ""
	s.cancel()
	leases := make([]*queryLease, 0, len(s.leases))
	for l := range s.leases {
		leases = append(leases, l)
	}
	s.mu.Unlock()
	for _, l := range leases {
		l.Release()
	}
	s.work.Wait()
	close(s.closeDone)
	return nil
}

// validateNode re-observes the original node before dispatch or publication.
// The server does not make this check atomic with a subsequent REST request.
func (s *Session) validateNode(ctx context.Context, c *Client) error {
	r, err := c.Do(ctx, Request{Method: "GET", Path: "/admin/info/system?wt=json"})
	if err != nil {
		return err
	}
	var observed Identity
	if decodeJSON(r.Body, &observed) != nil || !validObservation(observed) || observed != s.identity {
		s.revoke()
		return contract.ErrRevoked
	}
	if !s.Alive() {
		return contract.ErrRevoked
	}
	return ctx.Err()
}

var _ contract.Session = (*Session)(nil)
var _ contract.MetadataProvider = (*Session)(nil)
var _ contract.QueryProvider = (*Session)(nil)
