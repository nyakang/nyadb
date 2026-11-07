package easysearch

import (
	"context"
	"errors"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// Session owns the original cluster identity, credentials and physical leases.
// Each new physical client must prove that same cluster before any operation.
type Session struct {
	mu              sync.Mutex
	profile         connections.Profile
	secret          string
	identity        Identity
	ctx             context.Context
	cancel          context.CancelFunc
	closed, revoked bool
	closeDone       chan struct{}
	closeErr        error
	work            sync.WaitGroup
	leases          map[*queryLease]bool
	datas           map[*dataLease]bool
}

func OpenSession(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
	p, err := connections.NormalizeProfile(p)
	if err != nil {
		return nil, err
	}
	if p.Kind != connections.Easysearch || p.ID <= 0 {
		return nil, connections.ErrInvalid
	}
	cfg, err := connections.EasysearchProfileConfig(p)
	if err != nil {
		return nil, err
	}
	c, err := Open(ctx, cfg, secret)
	if err != nil {
		return nil, err
	}
	identity := c.Identity()
	err = c.Close()
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	root, cancel := context.WithCancel(context.Background())
	return &Session{profile: p, secret: secret, identity: identity, ctx: root, cancel: cancel, leases: map[*queryLease]bool{}, closeDone: make(chan struct{})}, nil
}

func (s *Session) ProfileID() int64   { return s.profile.ID }
func (s *Session) Generation() uint64 { return 1 }
func (s *Session) Capabilities() contract.Capabilities {
	c := s.DataDocumentCapabilities()
	c.Query = true
	return c
}
func (*Session) DataDocumentCapabilities() contract.Capabilities {
	return contract.Capabilities{Documents: true, Metadata: true, Keyset: true, Count: true, FieldChunks: true, Export: true, JSONL: true}
}
func (s *Session) Alive() bool        { s.mu.Lock(); defer s.mu.Unlock(); return !s.closed && !s.revoked }
func (s *Session) Identity() Identity { return s.identity }
func (s *Session) Namespace(label string) contract.NamespaceRef {
	if label == "" || label == s.identity.ClusterName || label == s.identity.ClusterUUID {
		return contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: s.identity.ClusterUUID}
	}
	return contract.NamespaceRef{ProfileID: s.ProfileID()}
}
func (s *Session) checkNamespace(n contract.NamespaceRef) error {
	if !s.Alive() {
		return contract.ErrRevoked
	}
	if n.ProfileID != s.ProfileID() || n.Catalog != s.identity.ClusterUUID || n.Schema != "" {
		return connections.ErrInvalid
	}
	return nil
}
func (s *Session) checkObject(o contract.ObjectRef) error {
	if err := s.checkNamespace(o.Namespace()); err != nil {
		return err
	}
	if !validTargetName(o.Kind, o.Name) {
		return connections.ErrInvalid
	}
	if o.Kind != "index" && o.Kind != "alias" && o.Kind != "data_stream" {
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
	cfg, err := connections.EasysearchProfileConfig(p)
	if err != nil {
		return nil, err
	}
	c, err := Open(ctx, cfg, secret)
	if err != nil {
		return nil, err
	}
	if c.Identity().ClusterUUID != s.identity.ClusterUUID {
		_ = c.Close()
		s.revoke()
		return nil, contract.ErrRevoked
	}
	if err := ctx.Err(); err != nil {
		_ = c.Close()
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
		return s.closeErr
	}
	s.closed, s.secret = true, ""
	s.cancel()
	leases := make([]*queryLease, 0, len(s.leases))
	for l := range s.leases {
		leases = append(leases, l)
	}
	datas := make([]*dataLease, 0, len(s.datas))
	for l := range s.datas {
		datas = append(datas, l)
	}
	s.mu.Unlock()
	for _, l := range leases {
		l.Release()
	}
	var closeErr error
	for _, l := range datas {
		closeErr = errors.Join(closeErr, l.Close())
	}
	s.work.Wait()
	s.closeErr = closeErr
	close(s.closeDone)
	return s.closeErr
}
func operationError(err error) error {
	if errors.Is(err, ErrRetired) {
		return errors.Join(contract.ErrSQLSessionLost, err)
	}
	return err
}

var _ contract.Session = (*Session)(nil)
var _ contract.MetadataProvider = (*Session)(nil)
var _ contract.QueryProvider = (*Session)(nil)
