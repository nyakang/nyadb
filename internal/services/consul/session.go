package consul

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const sessionReceiptBudget int64 = 1 << 30
const documentReceiptBudget int64 = 384 << 20
const pendingReceiptBudget int64 = 128 << 20

// Receipt accounting is independent of lifecycle locks, allowing atomic evidence
// transfer between two original sessions without lock-order inversions.
var receiptBudgetMu sync.Mutex

type Session struct {
	mu                         sync.Mutex
	id                         int64
	cfg                        Config
	secret                     string
	tls                        *tls.Config
	scope                      contract.KVScope
	evidence                   OpenEvidence
	root                       *Client
	ctx                        context.Context
	cancel                     context.CancelFunc
	closed, revoked            bool
	closeDone                  chan struct{}
	closeErr                   error
	work                       sync.WaitGroup
	leases                     map[*documentLease]bool
	dataLeases                 map[*dataLease]bool
	watchLeases                map[*httpWatchLease]bool
	renewalLeases              map[*httpSessionRenewalLease]bool
	opener                     func(context.Context) (*Client, OpenEvidence, error)
	retained, reserved, budget int64 // guarded by receiptBudgetMu
	rootBytes                  int64
}

// OpenNativeSession is an injectable private provider; it does not register a
// product, read stored profiles or publish a connection/URI/UI entry point.
func OpenNativeSession(ctx context.Context, profileID int64, cfg Config, token string) (*Session, error) {
	if profileID <= 0 {
		return nil, ErrInvalid
	}
	c, evidence, err := Open(ctx, cfg, token)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		c.Close()
		return nil, err
	}
	root, cancel := context.WithCancel(context.Background())
	s := &Session{id: profileID, cfg: c.Config(), secret: token, root: c, ctx: root, cancel: cancel,
		evidence: evidence, closeDone: make(chan struct{}), leases: map[*documentLease]bool{}, budget: sessionReceiptBudget}
	if tc := c.http.Transport.(*http.Transport).TLSClientConfig; tc != nil {
		s.tls = tc.Clone()
	}
	raw, _ := json.Marshal(s.cfg)
	hash := sha256.Sum256(raw)
	i := evidence.Identity
	s.scope = contract.KVScope{Namespace: contract.NamespaceRef{ProfileID: profileID, Catalog: "kv"}, Generation: 1, Prefix: []byte(s.cfg.Prefix), Version: i.Version,
		HTTP: contract.KVHTTPIdentity{Protocol: "consul-http", Endpoint: s.cfg.Endpoint, Datacenter: s.cfg.Datacenter,
			Consistency: s.cfg.Consistency, Auth: s.cfg.Auth, Configuration: hex.EncodeToString(hash[:]),
			AgentID: i.NodeID, AgentName: i.NodeName, AgentDatacenter: i.Datacenter, AgentAvailable: i.Available}}
	s.rootBytes = openEvidenceCost(evidence)
	if s.rootBytes > 256<<20 {
		c.Close()
		cancel()
		return nil, ErrBounds
	}
	s.retained = s.rootBytes
	c.mu.Lock()
	c.cache = false
	c.last = Response{}
	c.mu.Unlock()
	s.opener = s.openCaptured
	return s, nil
}

func openEvidenceCost(e OpenEvidence) int64 {
	n := int64(len(e.Identity.NodeName) + len(e.Identity.NodeID) + len(e.Identity.Version) + len(e.Identity.Revision) + len(e.Identity.Datacenter))
	for _, r := range []Response{e.Self, e.Leader, e.Keys} {
		n += resultCost(&contract.KVResult{HTTP: &contract.KVHTTPResult{Body: r.Body, Header: r.Header, Trailer: r.Trailer}})
		for _, v := range r.Entries {
			n += nativeEntryCost(v)
		}
		if r.Txn != nil {
			for _, v := range r.Txn.Results {
				n += nativeEntryCost(v)
			}
		}
	}
	return n
}

func (s *Session) ProfileID() int64   { return s.id }
func (s *Session) Generation() uint64 { return 1 } // public generations belong to dbruntime
func (s *Session) Capabilities() contract.Capabilities {
	return dataCapabilities()
}
func (s *Session) KVScope() contract.KVScope { return s.scope.Clone() }
func (s *Session) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && !s.revoked && s.root.Alive()
}
func (s *Session) OpeningEvidence() OpenEvidence {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evidence.Clone()
}

func (s *Session) request(parent context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked || !s.root.Alive() {
		return nil, nil, contract.ErrRevoked
	}
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	s.work.Add(1)
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(s.ctx, cancel)
	return ctx, func() { stop(); cancel(); s.work.Done() }, nil
}
func (s *Session) openCaptured(ctx context.Context) (*Client, OpenEvidence, error) {
	s.mu.Lock()
	cfg, token, tc, dead := s.cfg, s.secret, s.tls, s.closed || s.revoked
	s.mu.Unlock()
	if dead {
		return nil, OpenEvidence{}, contract.ErrRevoked
	}
	if tc != nil {
		tc = tc.Clone()
	}
	c := newCapturedClient(cfg, tc, token)
	c.cache = false
	e, err := c.Observe(ctx)
	if err != nil {
		c.Close()
		return nil, e, err
	}
	return c, e, nil
}
func (s *Session) open(ctx context.Context) (*Client, error) {
	s.mu.Lock()
	opener, dead := s.opener, s.closed || s.revoked
	s.mu.Unlock()
	if dead {
		return nil, contract.ErrRevoked
	}
	c, e, err := opener(ctx)
	if err != nil {
		if c != nil {
			c.Close()
		}
		return nil, err
	}
	if c == nil {
		return nil, ErrProtocol
	}
	i := e.Identity
	expected := s.scope.HTTP
	if c.Config() != s.cfg || i.Available != expected.AgentAvailable || i.Version != s.scope.Version || i.NodeID != expected.AgentID || i.NodeName != expected.AgentName || i.Datacenter != expected.AgentDatacenter {
		c.Close()
		s.mu.Lock()
		s.revoked = true
		s.secret = ""
		s.cancel()
		s.mu.Unlock()
		return nil, contract.ErrRevoked
	}
	if !s.Alive() || ctx.Err() != nil {
		c.Close()
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		return nil, contract.ErrRevoked
	}
	return c, nil
}
func (s *Session) ReserveKV(document uint64) (contract.KVRequestLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked || !s.root.Alive() {
		return nil, contract.ErrRevoked
	}
	if document == 0 {
		return nil, ErrInvalid
	}
	if len(s.leases)+len(s.dataLeases)+len(s.watchLeases)+len(s.renewalLeases) >= 32 {
		return nil, contract.ErrKVLeaseLimit
	}
	for l := range s.leases {
		if l.document == document {
			return nil, ErrInvalid
		}
	}
	ctx, cancel := context.WithCancel(s.ctx)
	l := &documentLease{s: s, document: document, ctx: ctx, cancel: cancel}
	s.leases[l] = true
	return l, nil
}
func compatibleScopes(a, b contract.KVScope) bool {
	return a.Namespace == b.Namespace && bytes.Equal(a.Prefix, b.Prefix) && a.HTTP == b.HTTP && a.Version == b.Version && a.Header == b.Header
}

func (s *Session) RecoverKV(previous contract.KVRequestLease) (contract.KVRecovery, error) {
	old, ok := previous.(*documentLease)
	if !ok || old == nil {
		return contract.KVRecovery{}, contract.ErrKVPlanUsed
	}
	if !compatibleScopes(old.Scope(), s.KVScope()) || !s.Alive() {
		return contract.KVRecovery{}, contract.ErrRevoked
	}
	old.mu.Lock()
	if old.closed || old.recovered {
		old.mu.Unlock()
		return contract.KVRecovery{}, contract.ErrKVPlanUsed
	}
	old.retired = true
	old.cancel()
	old.mu.Unlock()
	old.ops.Lock()
	defer old.ops.Unlock()
	old.mu.Lock()
	invalid := old.closed || old.recovered
	old.mu.Unlock()
	if invalid {
		return contract.KVRecovery{}, contract.ErrKVPlanUsed
	}
	old.closePhysical()
	result := old.recoveryOwned()
	old.s.mu.Lock()
	delete(old.s.leases, old)
	old.s.mu.Unlock()
	fresh, err := s.ReserveKV(old.document)
	if err != nil {
		return result, err
	}
	next := fresh.(*documentLease)
	if err = transferOwnedReceipts(old, next); err != nil {
		next.Close()
		return result, err
	}
	old.mu.Lock()
	old.recovered = true
	old.mu.Unlock()
	result.Lease = next
	return result, nil
}

// The caller holds old.ops. Serialize against the destination's Close/retire so
// receipts cannot be installed after its cache has already been released.
func transferOwnedReceipts(old, next *documentLease) error {
	next.ops.Lock()
	defer next.ops.Unlock()
	if err := next.CheckKVLease(); err != nil {
		return err
	}
	s := next.s
	receiptBudgetMu.Lock()
	credit := int64(0)
	if old.s == s {
		credit = old.cacheBytes
	}
	if s.retained+s.reserved+old.cacheBytes-credit > s.budget {
		receiptBudgetMu.Unlock()
		return ErrBounds
	}
	old.s.retained -= old.cacheBytes
	s.retained += old.cacheBytes
	next.cacheBytes = old.cacheBytes
	old.cacheBytes = 0
	receiptBudgetMu.Unlock()
	old.receiptMu.Lock()
	next.receiptMu.Lock()
	next.last, next.unknown = old.last, old.unknown
	next.writeUnknown.Store(old.writeUnknown.Load())
	old.last, old.unknown = nil, nil
	next.receiptMu.Unlock()
	old.receiptMu.Unlock()
	return nil
}

func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.closeDone
		return s.closeErr
	}
	s.closed = true
	s.secret = ""
	s.cancel()
	leases := make([]*documentLease, 0, len(s.leases))
	for l := range s.leases {
		leases = append(leases, l)
	}
	dataLeases := make([]*dataLease, 0, len(s.dataLeases))
	for l := range s.dataLeases {
		dataLeases = append(dataLeases, l)
	}
	watchLeases := make([]*httpWatchLease, 0, len(s.watchLeases))
	for l := range s.watchLeases {
		watchLeases = append(watchLeases, l)
	}
	renewalLeases := make([]*httpSessionRenewalLease, 0, len(s.renewalLeases))
	for l := range s.renewalLeases {
		renewalLeases = append(renewalLeases, l)
	}
	s.mu.Unlock()
	err := s.root.Close()
	for _, l := range dataLeases {
		err = errors.Join(err, l.Close())
	}
	for _, l := range leases {
		err = errors.Join(err, l.retire())
	}
	for _, l := range watchLeases {
		err = errors.Join(err, l.retire())
	}
	for _, l := range renewalLeases {
		err = errors.Join(err, l.retire())
	}
	s.work.Wait()
	s.mu.Lock()
	s.evidence = OpenEvidence{}
	s.tls = nil
	s.closeErr = err
	s.mu.Unlock()
	receiptBudgetMu.Lock()
	s.retained -= s.rootBytes
	s.rootBytes = 0
	receiptBudgetMu.Unlock()
	close(s.closeDone)
	return err
}

var _ contract.Session = (*Session)(nil)
var _ contract.KVRequestsProvider = (*Session)(nil)
