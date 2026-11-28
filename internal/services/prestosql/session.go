package prestosql

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
)

// Provider is private until profile, driver, workspace and UI acceptance.
// Observe receives copies on worker threads and must be concurrency-safe.
type Provider struct {
	Store   *driverstore.Store
	Started func(int) error
	Observe func(string, jdbcplugin.Receipt)
}

type Session struct {
	mu           sync.Mutex
	ctx          context.Context
	cancel       context.CancelFunc
	closed, lost bool
	work         sync.WaitGroup
	profileID    int64
	config       connections.PrestoSQLManagedConfig
	provider     Provider
	connection   map[string]any
	metadata     *process
	metadataOps  sync.Mutex
	queries      map[uint64]*queryLease
	dataLeases   map[*dataLease]bool
	nextOwner    uint64
	slots        chan struct{}
	dataSlots    chan struct{}
	auxSlots     chan struct{}
	namespaces   map[string]contract.NamespaceRef
	once         sync.Once
	closeErr     error
}

func (p Provider) OpenManaged(ctx context.Context, profileID int64, config connections.PrestoSQLManagedConfig, credentials Credentials) (*Session, error) {
	if ctx == nil || profileID <= 0 || p.Store == nil {
		return nil, connections.ErrInvalid
	}
	c, err := connections.NormalizePrestoSQLManagedConfig(config)
	if err != nil {
		return nil, err
	}
	connection, err := nativeConnection(c, credentials)
	if err != nil {
		return nil, err
	}
	original, err := p.role("metadata").start(ctx, c, connection)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		_ = original.runtime.Close()
		return nil, ctx.Err()
	}
	s := &Session{profileID: profileID, config: c, provider: p, connection: connection,
		metadata: original, queries: map[uint64]*queryLease{}, slots: make(chan struct{}, c.MaxQueryProcesses),
		dataLeases: map[*dataLease]bool{}, dataSlots: make(chan struct{}, c.MaxQueryProcesses), auxSlots: make(chan struct{}, 2),
		namespaces: map[string]contract.NamespaceRef{}}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	if c.Catalog != "" && c.Schema != "" {
		n := contract.NamespaceRef{ProfileID: profileID, Catalog: c.Catalog, Schema: c.Schema}
		s.namespaces[namespaceLabel(n)] = n
	}
	return s, nil
}

func (p Provider) role(role string) Provider {
	observe := p.Observe
	if observe != nil {
		p.Observe = func(method string, r jdbcplugin.Receipt) { observe(role+"/"+method, r) }
	}
	return p
}

func (s *Session) ProfileID() int64   { return s.profileID }
func (s *Session) Generation() uint64 { return 1 }
func (s *Session) Capabilities() contract.Capabilities {
	return contract.Capabilities{Query: true, Metadata: true, Schemas: true, EditableTables: true, Count: true, FieldChunks: true, Export: true, JSONL: true}
}
func (s *Session) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && !s.lost && s.metadata.runtime.Client.Alive()
}

func (s *Session) request(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, connections.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.lost || !s.metadata.runtime.Client.Alive() {
		return nil, nil, contract.ErrRevoked
	}
	s.work.Add(1)
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return child, func() { stop(); cancel(); s.work.Done() }, nil
}

func (s *Session) revoke() {
	s.mu.Lock()
	if s.lost || s.closed {
		s.mu.Unlock()
		return
	}
	s.lost = true
	s.cancel()
	s.mu.Unlock()
	// The current metadata request still owns work. Closing outside that
	// request waits for it and every captured query executor to settle.
	go func() { _ = s.Close() }()
}

func (s *Session) Close() error {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel()
		leases := make([]*queryLease, 0, len(s.queries))
		for _, q := range s.queries {
			leases = append(leases, q)
		}
		data := make([]*dataLease, 0, len(s.dataLeases))
		for d := range s.dataLeases {
			data = append(data, d)
		}
		s.connection = nil
		s.namespaces = nil
		s.mu.Unlock()
		for _, q := range leases {
			q.Release()
		}
		for _, d := range data {
			_ = d.Close()
		}
		s.work.Wait()
		s.closeErr = s.metadata.runtime.Close()
	})
	return s.closeErr
}

func validName(name string) bool {
	return name != "" && len(name) <= 256 && utf8.ValidString(name) && strings.IndexFunc(name, unicode.IsControl) < 0
}
func quoteName(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
func namespaceLabel(n contract.NamespaceRef) string {
	return quoteName(n.Catalog) + "." + quoteName(n.Schema)
}

func (s *Session) Namespace(label string) contract.NamespaceRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	if label == "" {
		return contract.NamespaceRef{ProfileID: s.profileID, Catalog: s.config.Catalog, Schema: s.config.Schema}
	}
	if n, ok := s.namespaces[label]; ok {
		return n
	}
	return contract.NamespaceRef{ProfileID: s.profileID}
}

func (s *Session) checkNamespace(n contract.NamespaceRef) error {
	if n.ProfileID != s.profileID || !validName(n.Catalog) || !validName(n.Schema) {
		return connections.ErrInvalid
	}
	if !s.Alive() {
		return contract.ErrRevoked
	}
	return nil
}

func (s *Session) ReserveQuery(document uint64) (contract.QueryLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.lost || !s.metadata.runtime.Client.Alive() {
		return nil, contract.ErrRevoked
	}
	if document == 0 || len(s.queries) >= 64 || s.queries[document] != nil || s.nextOwner == ^uint64(0) {
		return nil, connections.ErrInvalid
	}
	s.nextOwner++
	ctx, cancel := context.WithCancel(s.ctx)
	q := &queryLease{session: s, document: document, role: "query-" + strconv.FormatUint(s.nextOwner, 10),
		ctx: ctx, cancel: cancel}
	s.queries[document] = q
	return q, nil
}

func (s *Session) withMetadata(ctx context.Context, fn func(context.Context, *process) error) error {
	child, done, err := s.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	s.metadataOps.Lock()
	defer s.metadataOps.Unlock()
	if child.Err() != nil {
		return child.Err()
	}
	err = fn(child, s.metadata)
	if errors.Is(err, contract.ErrSQLSessionLost) || !s.metadata.runtime.Client.Alive() {
		s.revoke()
	}
	return err
}

var _ contract.Session = (*Session)(nil)
var _ contract.QueryProvider = (*Session)(nil)
