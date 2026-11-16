package databend

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
)

var ErrManagedDriver = errors.New("Databend original managed driver must be installed and explicitly selected")

const originalJDBCDigest = "274597a199f205b347e66db63b384e1846e5982b759c4d0f31abd5e74e82e2d3"

// ManagedProcess binds one verified package lease, original Java process and
// fixed TLS-verifying/no-replay relay. It never publishes session generations,
// chooses a profile or automatically starts a replacement process.
type ManagedProcess struct {
	config             connections.DatabendManagedConfig
	runtime            *driverstore.Runtime
	relay              *Relay
	params             json.RawMessage
	ctx                context.Context
	cancel             context.CancelFunc
	mu                 sync.Mutex
	closed             bool
	sessions           map[*ManagedSession]bool
	workload, metadata chan struct{}
	opening            sync.WaitGroup
	once               sync.Once
	err                error
}

func StartManaged(ctx context.Context, store *driverstore.Store, config connections.DatabendManagedConfig, secret string, started func(int) error) (*ManagedProcess, error) {
	if ctx == nil || store == nil {
		return nil, connections.ErrInvalid
	}
	c, err := connections.NormalizeDatabendManagedConfig(config)
	if err != nil {
		return nil, err
	}
	ctx, cancelStart := context.WithTimeout(ctx, time.Duration(c.Transport.ConnectTimeoutSeconds)*time.Second)
	defer cancelStart()
	lease, err := store.Acquire(ctx, "databend", c.DriverVersion, c.DriverSHA256)
	if err != nil {
		return nil, errors.Join(ErrManagedDriver, err)
	}
	m := lease.Manifest()
	fixed := false
	for _, file := range m.Files {
		if file.Path == "databend-jdbc-0.4.8.jar" && file.SHA256 == originalJDBCDigest && slices.Contains(m.Classpath, file.Path) {
			fixed = true
		}
	}
	if m.Runtime != "java" || m.MainClass != "com.dbx.agent.databend.DatabendAgent" || m.JDBC != nil || !fixed {
		_ = lease.Close()
		return nil, ErrManagedDriver
	}
	relay, err := StartRelay(ctx, c.Transport, secret)
	if err != nil {
		_ = lease.Close()
		return nil, err
	}
	params, err := relay.ConnectionParams(c.Database)
	if err != nil {
		relay.Close()
		_ = lease.Close()
		return nil, err
	}
	runtime, err := lease.StartJDBCObserved(ctx, "databend", driverstore.JDBCSettings{MaxConnections: c.MaxConnections, MetadataReserve: c.MetadataReserve, WaitTimeout: time.Duration(c.PoolWaitMillis) * time.Millisecond}, agentbridge.Options{RetainOriginalResponses: true, ErrorCodes: agentbridge.DatabendCodes, CancelWait: 15 * time.Second}, started)
	if err != nil {
		relay.Close()
		_ = lease.Close()
		return nil, err
	}
	root, cancel := context.WithCancel(context.Background())
	p := &ManagedProcess{config: c, runtime: runtime, relay: relay, ctx: root, cancel: cancel, sessions: map[*ManagedSession]bool{}, workload: make(chan struct{}, c.MaxConnections-c.MetadataReserve), metadata: make(chan struct{}, c.MetadataReserve)}
	p.params, _ = json.Marshal(params)
	if err := ctx.Err(); err != nil {
		_ = p.Close()
		return nil, err
	}
	return p, nil
}

func (p *ManagedProcess) PID() int { return p.runtime.Client.PID() }
func (p *ManagedProcess) StartupReceipt() agentbridge.Receipt {
	return p.runtime.Client.HandshakeReceipt()
}
func (p *ManagedProcess) Alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.closed && p.runtime.Client.Alive()
}

// OpenSession reserves a role-specific slot and pins the original physical
// connection with a native USE before exposing it. This preserves the server's
// timezone/settings defaults. Late/canceled opens detach
// only their captured handle; Close waits for these openings before releasing
// the original process/package. Metadata capacity is independent of workload.
func (p *ManagedProcess) OpenSession(ctx context.Context, metadata bool) (*ManagedSession, []agentbridge.Receipt, error) {
	if ctx == nil {
		return nil, nil, connections.ErrInvalid
	}
	request, cancel := context.WithTimeout(ctx, time.Duration(p.config.Transport.ConnectTimeoutSeconds)*time.Second)
	stop := context.AfterFunc(p.ctx, cancel)
	defer func() { stop(); cancel() }()
	if err := request.Err(); err != nil {
		return nil, nil, err
	}
	slots := p.workload
	if metadata {
		slots = p.metadata
	}
	wait, done := context.WithTimeout(request, time.Duration(p.config.PoolWaitMillis)*time.Millisecond)
	defer done()
	select {
	case <-wait.Done():
		return nil, nil, wait.Err()
	case slots <- struct{}{}:
	}
	retained := false
	defer func() {
		if !retained {
			<-slots
		}
	}()
	p.mu.Lock()
	if p.closed || !p.runtime.Client.Alive() || request.Err() != nil {
		p.mu.Unlock()
		return nil, nil, contract.ErrRevoked
	}
	p.opening.Add(1)
	raw := slices.Clone(p.params)
	p.mu.Unlock()
	defer p.opening.Done()
	var params map[string]any
	_ = json.Unmarshal(raw, &params)
	params["sessionRole"] = "workload"
	if metadata {
		params["sessionRole"] = "metadata"
	}
	original, opened, err := p.runtime.Client.OpenSession(request, params)
	receipts := []agentbridge.Receipt{opened.Clone()}
	if err != nil {
		return nil, receipts, managedFailure(err)
	}
	database := `"` + strings.ReplaceAll(p.config.Database, `"`, `""`) + `"`
	initialized, err := original.Call(request, "execute_query", map[string]any{"sql": "USE " + database, "schema": p.config.Database, "maxRows": 1})
	receipts = append(receipts, initialized.Clone())
	if err == nil {
		_, err = agentbridge.DecodeQuery(initialized, DecodeJDBCValue)
	}
	p.mu.Lock()
	if err != nil || p.closed || request.Err() != nil {
		p.mu.Unlock()
		_, closeErr := original.Close()
		if err == nil {
			err = contract.ErrRevoked
		}
		return nil, receipts, managedFailure(errors.Join(err, closeErr))
	}
	s := &ManagedSession{owner: p, original: original, slots: slots}
	p.sessions[s] = true
	retained = true
	p.mu.Unlock()
	return s, receipts, nil
}

func managedFailure(err error) error {
	if errors.Is(err, agentbridge.ErrLost) || errors.Is(err, agentbridge.ErrClosed) {
		return errors.Join(contract.ErrSQLSessionLost, err)
	}
	return err
}

func (p *ManagedProcess) Close() error {
	p.once.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.cancel()
		sessions := make([]*ManagedSession, 0, len(p.sessions))
		for s := range p.sessions {
			sessions = append(sessions, s)
		}
		p.mu.Unlock()
		for _, s := range sessions {
			_, err := s.Close()
			p.err = errors.Join(p.err, err)
		}
		p.opening.Wait()
		p.err = errors.Join(p.err, p.runtime.Close())
		p.relay.Close()
		p.mu.Lock()
		p.params = nil
		clear(p.sessions)
		p.mu.Unlock()
	})
	return p.err
}

// ManagedSession owns one original logical/physical connection. Calls retain
// executor receipts; cancellation cannot replace them with a control response.
// Close's acknowledgement proves logical detachment. Process Close additionally
// waits for physical disposal and releases the captured package lease.
type ManagedSession struct {
	owner        *ManagedProcess
	original     *agentbridge.Session
	slots        chan struct{}
	once         sync.Once
	closeReceipt agentbridge.Receipt
	err          error
}

func (s *ManagedSession) Alive() bool { return s.owner.Alive() && s.original.Alive() }
func (s *ManagedSession) request(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, connections.ErrInvalid
	}
	request, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.owner.ctx, cancel)
	return request, func() { stop(); cancel() }, nil
}
func (s *ManagedSession) Call(ctx context.Context, method string, params any) (agentbridge.Receipt, error) {
	request, done, err := s.request(ctx)
	if err != nil {
		return agentbridge.Receipt{}, err
	}
	defer done()
	receipt, err := s.original.Call(request, method, params)
	return receipt, managedFailure(err)
}
func (s *ManagedSession) OpenQueryCursor(ctx context.Context, options agentbridge.QueryCursorOptions) (*agentbridge.QueryCursor, agentbridge.QueryPageSnapshot, error) {
	request, done, err := s.request(ctx)
	if err != nil {
		return nil, agentbridge.QueryPageSnapshot{}, err
	}
	defer done()
	options.Decoder = DecodeJDBCValue
	cursor, page, err := s.original.OpenQueryCursor(request, options)
	return cursor, page, managedFailure(err)
}
func (s *ManagedSession) Close() (agentbridge.Receipt, error) {
	s.once.Do(func() {
		s.closeReceipt, s.err = s.original.Close()
		s.owner.mu.Lock()
		delete(s.owner.sessions, s)
		s.owner.mu.Unlock()
		<-s.slots
	})
	return s.closeReceipt.Clone(), s.err
}
