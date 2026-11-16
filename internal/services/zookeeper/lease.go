package zookeeper

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

type preparation struct {
	owner   *documentLease
	request contract.KVRequest
	write   bool
	used    atomic.Bool
}

func (p *preparation) Snapshot() contract.KVRequest { return p.request }
func (p *preparation) Writes() bool                 { return p.write }

type documentLease struct {
	s                                *Session
	document                         uint64
	ctx                              context.Context
	cancel                           context.CancelFunc
	ops, mu                          sync.Mutex
	closed, lost, retired, recovered bool
	physical                         originalRuntime    // guarded by ops
	last, unknown                    *contract.KVResult // guarded by mu
	writeUnknown                     bool
	retained                         int64 // guarded by receiptBudgetMu
	closeErr                         error // guarded by ops
}

func (l *documentLease) Scope() contract.KVScope { return l.s.KVScope() }
func (l *documentLease) CheckKVLease() error {
	l.mu.Lock()
	closed, lost := l.closed, l.lost || l.retired
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
func (l *documentLease) Prepare(r contract.KVRequest) (contract.KVPreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	r, w, err := decodeRequest(r)
	if err != nil {
		return nil, err
	}
	return &preparation{owner: l, request: r, write: w}, nil
}
func (l *documentLease) request(parent context.Context) (context.Context, func(), error) {
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
func (l *documentLease) lose(err error) error {
	l.mu.Lock()
	l.lost = true
	l.mu.Unlock()
	return errors.Join(contract.ErrKVLeaseLost, err, l.closePhysical())
}
func (l *documentLease) closePhysical() error {
	if l.physical != nil {
		l.closeErr = errors.Join(l.closeErr, l.physical.Close())
		l.physical = nil
	}
	return l.closeErr
}
func (l *documentLease) connect(ctx context.Context) error {
	if l.physical != nil {
		if !l.physical.Alive() {
			return l.lose(agentbridge.ErrLost)
		}
		return nil
	}
	p, err := l.s.opener(ctx)
	if err != nil {
		if p != nil {
			p.Close()
		}
		return l.lose(err)
	}
	if p == nil {
		return l.lose(ErrInvalid)
	}
	version := ""
	for _, r := range p.OpeningReceipts() {
		if r.ZNode != nil && r.ZNode.Reply != nil && r.ZNode.Reply.Kind == "connection_info" {
			version = r.ZNode.Reply.ProductVersion
		}
	}
	if version != l.Scope().Version || !l.s.Alive() || ctx.Err() != nil {
		p.Close()
		return l.lose(errors.Join(ctx.Err(), contract.ErrRevoked))
	}
	l.physical = p
	return nil
}

func (l *documentLease) Preconnect(parent context.Context) error {
	if parent == nil {
		return ErrInvalid
	}
	if err := parent.Err(); err != nil {
		return err
	}
	if !l.ops.TryLock() {
		return contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	ctx, done, err := l.request(parent)
	if err != nil {
		return err
	}
	defer done()
	return l.connect(ctx)
}

func (l *documentLease) Execute(parent context.Context, prepared contract.KVPreparation) (result contract.KVResult, err error) {
	p, ok := prepared.(*preparation)
	if !ok || p == nil || p.owner != l {
		return result, contract.ErrKVPlanUsed
	}
	result.Request = p.request
	if parent == nil {
		return result, ErrInvalid
	}
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
	l.mu.Lock()
	unknown := l.writeUnknown
	l.mu.Unlock()
	if p.write && unknown {
		return result, contract.ErrKVWriteUnknown
	}
	if err = l.reserveResult(); err != nil {
		return result, err
	}
	defer l.releaseReservation()
	if err = l.connect(ctx); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, l.lose(err)
	}
	if !p.used.CompareAndSwap(false, true) {
		return result, contract.ErrKVPlanUsed
	}
	r, err := l.physical.Call(ctx, p.request.Method, json.RawMessage(p.request.JSON))
	if !r.Dispatched {
		p.used.Store(false)
	}
	result = nativeResult(p.request, p.write, r)
	if cacheErr := l.cacheResult(result); cacheErr != nil {
		return result, l.lose(errors.Join(err, cacheErr))
	}
	if !l.physical.Alive() {
		err = l.lose(err)
	}
	return result, err
}

func (l *documentLease) LastResult() contract.KVResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		return contract.KVResult{}
	}
	return l.last.Clone()
}
func (l *documentLease) recovery() contract.KVRecovery {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := contract.KVRecovery{}
	if l.last != nil {
		out.Last = l.last.Clone()
	}
	if l.unknown != nil {
		v := l.unknown.Clone()
		out.Unknown = &v
	}
	return out
}

func resultCost(r contract.KVResult) int64 {
	n := int64(len(r.Request.JSON) + len(r.JSON) + len(r.JSONCaptureError) + len(r.ProjectionError) + len(r.Native))
	if z := r.ZNode; z != nil {
		n += int64(len(z.Native))
		if p := z.Reply; p != nil {
			if p.Node != nil {
				n += int64(len(p.Node.Key)+len(p.Node.Value)) + 128
			}
			n += int64(len(p.CreatedKey))
			for _, v := range p.Keys {
				n += int64(len(v.Key)+len(v.Value)) + 128
			}
			if p.Continuation != nil {
				n += int64(len(*p.Continuation))
			}
		}
	}
	return n + 256
}
func (l *documentLease) reserveResult() error {
	receiptBudgetMu.Lock()
	defer receiptBudgetMu.Unlock()
	if l.s.retained+l.s.reserved+responseReservation > sessionBudget || l.retained+responseReservation > documentBudget {
		return ErrBounds
	}
	l.s.reserved += responseReservation
	return nil
}
func (l *documentLease) releaseReservation() {
	receiptBudgetMu.Lock()
	l.s.reserved -= responseReservation
	receiptBudgetMu.Unlock()
}
func (l *documentLease) cacheResult(result contract.KVResult) error {
	copy := result.Clone()
	receiptBudgetMu.Lock()
	defer receiptBudgetMu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	unknown := l.unknown
	if copy.WriteOutcomeUnknown && unknown == nil {
		unknown = &copy
	}
	cost := resultCost(copy)
	if unknown != nil && unknown != &copy {
		cost += resultCost(*unknown)
	}
	if cost > documentBudget || cost > l.retained+responseReservation {
		return ErrBounds
	}
	l.s.retained += cost - l.retained
	l.retained = cost
	l.last = &copy
	l.unknown = unknown
	l.writeUnknown = l.writeUnknown || copy.WriteOutcomeUnknown
	return nil
}
func (l *documentLease) clearReceipts() {
	receiptBudgetMu.Lock()
	defer receiptBudgetMu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.s.retained -= l.retained
	l.retained = 0
	l.last, l.unknown = nil, nil
	l.writeUnknown = false
}

func (l *documentLease) retire() error {
	l.mu.Lock()
	l.retired = true
	l.cancel()
	l.mu.Unlock()
	l.ops.Lock()
	defer l.ops.Unlock()
	return l.closePhysical()
}
func (l *documentLease) Close() error {
	l.mu.Lock()
	l.closed = true
	l.cancel()
	l.mu.Unlock()
	l.ops.Lock()
	defer l.ops.Unlock()
	err := l.closePhysical()
	l.clearReceipts()
	l.s.mu.Lock()
	delete(l.s.leases, l)
	l.s.mu.Unlock()
	return err
}
