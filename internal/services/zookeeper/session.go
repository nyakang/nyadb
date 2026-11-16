package zookeeper

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
)

const sessionBudget int64 = 512 << 20
const documentBudget int64 = 256 << 20
const responseReservation int64 = 128 << 20

// Receipt transfers use a lock independent of lifecycle locks so recovery can
// atomically move original evidence between two original profile sessions.
var receiptBudgetMu sync.Mutex

type Options struct {
	Driver       connections.ManagedDriver
	MaxDocuments int
	Started      func(int) error // original process observer, never session publication
}

type originalRuntime interface {
	Alive() bool
	Call(context.Context, string, any) (agentbridge.Receipt, error)
	Close() error
	OpeningReceipts() []contract.KVResult
}

type originalProcess struct {
	runtime *driverstore.SingleSessionRuntime
	opening []contract.KVResult
}

func (p *originalProcess) Alive() bool { return p.runtime.Session.Alive() }
func (p *originalProcess) Call(ctx context.Context, m string, params any) (agentbridge.Receipt, error) {
	return p.runtime.Session.Call(ctx, m, params)
}
func (p *originalProcess) Close() error { return p.runtime.Close() }
func (p *originalProcess) OpeningReceipts() []contract.KVResult {
	out := slices.Clone(p.opening)
	for i := range out {
		out[i] = out[i].Clone()
	}
	return out
}

func startOriginal(ctx context.Context, c Config, secret string, o Options) (originalRuntime, error) {
	store, err := driverstore.New(o.Driver.Directory)
	if err != nil {
		return nil, ErrInvalid
	}
	lease, err := store.Acquire(ctx, "zookeeper", o.Driver.Version, o.Driver.SHA256)
	if err != nil {
		return nil, err
	}
	manifest := lease.Manifest()
	if manifest.Runtime != "native" {
		lease.Close()
		return nil, ErrInvalid
	}
	runtime, err := lease.StartSingleSessionObserved(ctx, "zookeeper-v1", agentbridge.Options{MaxFrameBytes: frameLimit, CancelWait: 5 * time.Second}, o.Started)
	if err != nil {
		return nil, err
	}
	p := &originalProcess{runtime: runtime}
	// Credentials only enter the captured process's stdin. Application write
	// retries are explicitly zero for every original document connection.
	params := struct {
		Connection struct {
			Config
			Password   string `json:"password"`
			MaxRetries int    `json:"max_retries"`
			BaseSleep  int    `json:"base_sleep_time_ms"`
		} `json:"connection"`
	}{}
	params.Connection.Config = c.Clone()
	params.Connection.Password = secret
	receipt, err := runtime.Session.Connect(ctx, params)
	p.opening = append(p.opening, nativeResult(contract.KVRequest{Method: "connect", JSON: "{}"}, false, receipt))
	if err == nil {
		receipt, err = runtime.Session.Call(ctx, "connection_info", nil)
		p.opening = append(p.opening, nativeResult(contract.KVRequest{Method: "connection_info", JSON: "{}"}, false, receipt))
		if err == nil && (p.opening[1].ZNode.Reply == nil || p.opening[1].ProjectionError != "") {
			err = ErrProjection
		}
	}
	if err != nil || ctx.Err() != nil || !p.Alive() {
		if err == nil {
			err = ctx.Err()
			if err == nil {
				err = contract.ErrKVLeaseLost
			}
		}
		return nil, errors.Join(err, p.Close())
	}
	return p, nil
}

type Session struct {
	mu                 sync.Mutex
	id                 int64
	captured           capturedConfig
	secret             string
	authDigest         [32]byte // private comparison only; never published or serialized
	options            Options
	scope              contract.KVScope
	root               originalRuntime
	opener             func(context.Context) (originalRuntime, error)
	ctx                context.Context
	cancel             context.CancelFunc
	closed             bool
	closeDone          chan struct{}
	closeErr           error
	work               sync.WaitGroup
	leases             map[*documentLease]bool
	retained, reserved int64 // guarded by receiptBudgetMu
	opening            []contract.KVResult
}

// OpenNativeSession is an injected private KV provider. Public profile,
// descriptor, chooser and URI publication require separate product acceptance.
func OpenNativeSession(ctx context.Context, id int64, c Config, password string, o Options) (*Session, error) {
	if ctx == nil || id <= 0 || len(password) > 65536 || o.Driver.Key != "zookeeper" || o.Driver.JDBCClass != "" || !o.Driver.Supports("zookeeper-v1") {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if o.MaxDocuments == 0 {
		o.MaxDocuments = 8
	}
	if o.MaxDocuments < 1 || o.MaxDocuments > 32 {
		return nil, ErrBounds
	}
	o.Driver.Profiles = slices.Clone(o.Driver.Profiles)
	captured, err := captureConfig(c)
	if err != nil {
		return nil, err
	}
	if captured.config.AuthScheme == "sasl_digest" && (captured.config.Username == "" || password == "") {
		if captured.directory != "" {
			os.RemoveAll(captured.directory)
		}
		return nil, ErrInvalid
	}
	runtime, err := startOriginal(ctx, captured.config, password, o)
	if err != nil {
		if captured.directory != "" {
			os.RemoveAll(captured.directory)
		}
		return nil, err
	}
	root, cancel := context.WithCancel(context.Background())
	s := &Session{id: id, captured: captured, secret: password, authDigest: sha256.Sum256([]byte(password)), options: o, root: runtime, ctx: root, cancel: cancel, closeDone: make(chan struct{}), leases: map[*documentLease]bool{}, opening: runtime.OpeningReceipts()}
	version := ""
	for _, r := range s.opening {
		if r.ZNode != nil && r.ZNode.Reply != nil && r.ZNode.Reply.Kind == "connection_info" {
			version = r.ZNode.Reply.ProductVersion
		}
	}
	s.scope = contract.KVScope{Namespace: contract.NamespaceRef{ProfileID: id}, Generation: 1, Prefix: []byte(captured.config.Prefix), Version: version, ZNode: contract.KVZNodeIdentity{Protocol: nativeProtocol, ConnectString: captured.config.ConnectString, Root: captured.root, Username: captured.config.Username, AuthScheme: captured.config.AuthScheme, Configuration: captured.fingerprint, DriverVersion: o.Driver.Version, DriverSHA256: o.Driver.SHA256}}
	for i := range s.opening {
		s.retained += resultCost(s.opening[i])
	}
	s.opener = s.openCaptured
	if ctx.Err() != nil {
		err = ctx.Err()
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Session) ProfileID() int64   { return s.id }
func (s *Session) Generation() uint64 { return 1 } // public generations belong to dbruntime
func (s *Session) Capabilities() contract.Capabilities {
	return contract.Capabilities{KeyValues: true, Export: true}
}
func (s *Session) KVScope() contract.KVScope { return s.scope.Clone() }
func (s *Session) Alive() bool               { s.mu.Lock(); defer s.mu.Unlock(); return !s.closed && s.root.Alive() }
func (s *Session) OpeningReceipts() []contract.KVResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(s.opening)
	for i := range out {
		out[i] = out[i].Clone()
	}
	return out
}

func (s *Session) request(parent context.Context) (context.Context, func(), error) {
	if parent == nil {
		return nil, nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.root.Alive() {
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

func (s *Session) openCaptured(ctx context.Context) (originalRuntime, error) {
	s.mu.Lock()
	c, secret, o, dead := s.captured.config.Clone(), s.secret, s.options, s.closed
	s.mu.Unlock()
	if dead {
		return nil, contract.ErrRevoked
	}
	return startOriginal(ctx, c, secret, o)
}

func (s *Session) ReserveKV(id uint64) (contract.KVRequestLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reserveLocked(id, nil)
}
func (s *Session) reserveLocked(id uint64, replacing *documentLease) (*documentLease, error) {
	if s.closed || !s.root.Alive() {
		return nil, contract.ErrRevoked
	}
	if id == 0 {
		return nil, ErrInvalid
	}
	count := len(s.leases)
	if replacing != nil && s.leases[replacing] {
		count--
	}
	if count >= s.options.MaxDocuments {
		return nil, contract.ErrKVLeaseLimit
	}
	for l := range s.leases {
		if l.document == id && l != replacing {
			return nil, ErrInvalid
		}
	}
	ctx, cancel := context.WithCancel(s.ctx)
	l := &documentLease{s: s, document: id, ctx: ctx, cancel: cancel}
	s.leases[l] = true
	if replacing != nil {
		delete(s.leases, replacing)
	}
	return l, nil
}

func (s *Session) RecoverKV(previous contract.KVRequestLease) (contract.KVRecovery, error) {
	old, ok := previous.(*documentLease)
	if !ok || old == nil {
		return contract.KVRecovery{}, contract.ErrKVPlanUsed
	}
	a, b := old.Scope(), s.KVScope()
	if a.Namespace != b.Namespace || a.ZNode != b.ZNode || a.Version != b.Version || !slices.Equal(a.Prefix, b.Prefix) || old.s.authDigest != s.authDigest || !s.Alive() {
		return contract.KVRecovery{}, contract.ErrRevoked
	}
	old.mu.Lock()
	if old.closed {
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
	if err := old.closePhysical(); err != nil {
		return contract.KVRecovery{}, err
	}
	result := old.recovery()
	s.mu.Lock()
	next, err := s.reserveLocked(old.document, old)
	s.mu.Unlock()
	if err != nil {
		return result, err
	}
	next.ops.Lock()
	defer next.ops.Unlock()
	if err := next.CheckKVLease(); err != nil {
		next.mu.Lock()
		next.closed = true
		next.cancel()
		next.mu.Unlock()
		s.mu.Lock()
		delete(s.leases, next)
		s.mu.Unlock()
		return result, err
	}
	receiptBudgetMu.Lock()
	credit := int64(0)
	if old.s == s {
		credit = old.retained
	}
	if s.retained+s.reserved+old.retained-credit > sessionBudget {
		receiptBudgetMu.Unlock()
		next.mu.Lock()
		next.closed = true
		next.cancel()
		next.mu.Unlock()
		s.mu.Lock()
		delete(s.leases, next)
		if old.s == s {
			s.leases[old] = true
		}
		s.mu.Unlock()
		return result, ErrBounds
	}
	old.mu.Lock()
	next.mu.Lock()
	next.last, next.unknown = old.last, old.unknown
	next.writeUnknown = old.writeUnknown
	next.retained = old.retained
	s.retained += old.retained
	old.s.retained -= old.retained
	old.retained = 0
	old.last, old.unknown = nil, nil
	old.recovered = true
	next.mu.Unlock()
	old.mu.Unlock()
	receiptBudgetMu.Unlock()
	old.s.mu.Lock()
	delete(old.s.leases, old)
	old.s.mu.Unlock()
	result.Lease = next
	return result, nil
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
	all := make([]*documentLease, 0, len(s.leases))
	for l := range s.leases {
		all = append(all, l)
	}
	s.mu.Unlock()
	err := s.root.Close()
	for _, l := range all {
		err = errors.Join(err, l.retire())
	}
	s.work.Wait()
	s.mu.Lock()
	opening := s.opening
	s.opening = nil
	s.mu.Unlock()
	receiptBudgetMu.Lock()
	for _, r := range opening {
		s.retained -= resultCost(r)
	}
	receiptBudgetMu.Unlock()
	if s.captured.directory != "" {
		err = errors.Join(err, os.RemoveAll(s.captured.directory))
	}
	s.closeErr = err
	close(s.closeDone)
	return err
}

var _ contract.Session = (*Session)(nil)
var _ contract.KVRequestsProvider = (*Session)(nil)
