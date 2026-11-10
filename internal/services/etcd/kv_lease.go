package etcd

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

type kvPreparation struct {
	owner   *kvLease
	request contract.KVRequest
	native  proto.Message
	write   bool
	used    atomic.Bool
	record  contract.KVRequestRecord
}

func (p *kvPreparation) Snapshot() contract.KVRequest              { return p.request }
func (p *kvPreparation) Writes() bool                              { return p.write }
func (p *kvPreparation) RecordedRequest() contract.KVRequestRecord { return p.record.Clone() }

type kvLease struct {
	s            *Session
	document     uint64
	ctx          context.Context
	cancel       context.CancelFunc
	ops, mu      sync.Mutex
	closed, lost bool
	client       *Client           // guarded by ops
	last         contract.KVResult // guarded by mu
	closeErr     error             // guarded by ops
}

func (l *kvLease) Scope() contract.KVScope { return l.s.KVScope() }
func (l *kvLease) CheckKVLease() error {
	l.mu.Lock()
	closed, lost := l.closed, l.lost
	l.mu.Unlock()
	if closed {
		return contract.ErrRevoked
	}
	if lost {
		return contract.ErrKVLeaseLost
	}
	if !l.s.Alive() {
		return contract.ErrRevoked
	}
	return nil
}
func (l *kvLease) Prepare(r contract.KVRequest) (contract.KVPreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	m, r, write, err := decodeKVRequest(r)
	if err != nil {
		return nil, err
	}
	return &kvPreparation{owner: l, request: r, native: m, write: write, record: recordedAuthRequest(r, m)}, nil
}
func (l *kvLease) request(parent context.Context) (context.Context, func(), error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, nil, err
	}
	ctx, done, err := l.s.request(parent)
	if err != nil {
		return nil, nil, err
	}
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	return child, func() { stop(); cancel(); done() }, nil
}
func (l *kvLease) markLost(err error) error {
	l.mu.Lock()
	l.lost = true
	l.mu.Unlock()
	if l.client != nil {
		err = errors.Join(err, l.client.Close())
	}
	return errors.Join(contract.ErrKVLeaseLost, err)
}
func (l *kvLease) connect(ctx context.Context) error {
	if l.client != nil {
		if !l.client.Alive() {
			return l.markLost(ErrRetired)
		}
		return nil
	}
	c, err := l.s.open(ctx)
	if err != nil {
		return l.markLost(err)
	}
	l.client = c
	return nil
}
func (l *kvLease) Preconnect(parent context.Context) error {
	if !l.ops.TryLock() {
		return contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	ctx, done, err := l.request(parent)
	if err != nil {
		return err
	}
	defer done()
	if err = l.connect(ctx); err != nil {
		return err
	}
	plan, err := l.client.Prepare(&pb.StatusRequest{})
	if err != nil {
		return l.markLost(err)
	}
	_, err = l.client.Execute(ctx, plan)
	if !l.client.Alive() {
		return l.markLost(err)
	}
	return err
}
func (l *kvLease) Execute(parent context.Context, prepared contract.KVPreparation) (result contract.KVResult, err error) {
	p, ok := prepared.(*kvPreparation)
	if !ok || p == nil || p.owner != l {
		return result, contract.ErrKVPlanUsed
	}
	result.Request = p.record.Request
	result.RequestOmittedFields = p.record.Clone().OmittedFields
	if err = parent.Err(); err != nil {
		return result, err
	}
	if !l.ops.TryLock() {
		return result, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	ctx, done, err := l.request(parent)
	if err != nil {
		return result, err
	}
	defer done()
	if p.used.Load() {
		return result, contract.ErrKVPlanUsed
	}
	defer func() {
		l.mu.Lock()
		if !l.closed {
			l.last = result.Clone()
		}
		l.mu.Unlock()
	}()
	if err = l.connect(ctx); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, l.markLost(err)
	}
	if !p.used.CompareAndSwap(false, true) {
		return result, contract.ErrKVPlanUsed
	}
	native, err := l.client.Prepare(p.native)
	if err != nil {
		p.used.Store(false)
		if !l.client.Alive() {
			err = l.markLost(err)
		}
		return result, err
	}
	r, err := l.client.Execute(ctx, native)
	if !r.Attempted {
		p.used.Store(false)
	}
	result = projectKVResult(p.record.Request, r)
	result.RequestOmittedFields = p.record.Clone().OmittedFields
	// Close/recovery waits for this executor; its actual receipt is retained even
	// if publication/activation changed before a UI consumer admits the result.
	l.mu.Lock()
	l.last = result.Clone()
	l.mu.Unlock()
	if !l.client.Alive() {
		err = l.markLost(err)
	}
	return result, err
}
func (l *kvLease) LastResult() contract.KVResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last.Clone()
}
func (l *kvLease) settle() (contract.KVResult, error) {
	l.mu.Lock()
	l.closed = true
	l.cancel()
	l.mu.Unlock()
	l.ops.Lock()
	defer l.ops.Unlock()
	last := l.LastResult()
	if l.client != nil {
		l.closeErr = errors.Join(l.closeErr, l.client.Close())
		l.client = nil
	}
	l.mu.Lock()
	l.last = contract.KVResult{}
	l.mu.Unlock()
	l.s.mu.Lock()
	delete(l.s.leases, l)
	l.s.mu.Unlock()
	return last, l.closeErr
}
func (l *kvLease) Close() error { _, err := l.settle(); return err }
