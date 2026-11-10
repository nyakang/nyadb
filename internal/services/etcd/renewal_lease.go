package etcd

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type renewalPreparation struct {
	owner   *renewalLease
	request contract.KVRenewalRequest
	used    atomic.Bool
}

func (p *renewalPreparation) Snapshot() contract.KVRenewalRequest { return p.request }

type renewalLease struct {
	base      *kvLease
	run       *renewalRun             // guarded by base.ops
	last      contract.KVRenewalBatch // terminal metadata only; guarded by base.ops
	uncertain bool                    // guarded by base.ops; retained across recovery
	charged   int                     // guarded by Session.mu, retained through Poll
}

func (l *renewalLease) Scope() contract.KVScope { return l.base.Scope() }
func (l *renewalLease) CheckKVLease() error     { return l.base.CheckKVLease() }
func (l *renewalLease) writable() error {
	if l.run != nil {
		terminal, _ := l.run.raw.Terminal()
		l.uncertain = l.uncertain || terminal.WriteOutcomeUnknown
	}
	if l.uncertain {
		return contract.ErrKVRenewalUncertain
	}
	return l.CheckKVLease()
}
func (l *renewalLease) Prepare(r contract.KVRenewalRequest) (contract.KVRenewalPreparation, error) {
	if r.LeaseID == 0 {
		return nil, ErrInvalid
	}
	if !l.base.ops.TryLock() {
		return nil, contract.ErrKVBusy
	}
	defer l.base.ops.Unlock()
	if err := l.writable(); err != nil {
		return nil, err
	}
	if l.run != nil {
		return nil, contract.ErrKVBusy
	}
	return &renewalPreparation{owner: l, request: r}, nil
}

// Charges include complete protobuf, optional JSON and bounded neutral copies.
// The event and renewal documents share one retained-session budget.
func (l *renewalLease) admit(rawCharge int) bool {
	s := l.base.s
	charge := rawCharge * 4
	s.mu.Lock()
	defer s.mu.Unlock()
	if charge > renewalDocumentBytes-l.charged || charge > retainedSessionBytes-s.retainedBytes {
		return false
	}
	l.charged += charge
	s.retainedBytes += charge
	return true
}
func (l *renewalLease) releaseBudget() {
	s := l.base.s
	s.mu.Lock()
	s.retainedBytes -= l.charged
	l.charged = 0
	s.mu.Unlock()
}
func (l *renewalLease) Preconnect(parent context.Context) error {
	b := l.base
	if !b.ops.TryLock() {
		return contract.ErrKVBusy
	}
	defer b.ops.Unlock()
	if l.run != nil {
		return contract.ErrKVBusy
	}
	ctx, done, err := b.request(parent)
	if err != nil {
		return err
	}
	defer done()
	if err = b.connect(ctx); err != nil {
		return err
	}
	p, err := b.client.Prepare(&pb.StatusRequest{})
	if err == nil {
		_, err = b.client.Execute(ctx, p)
	}
	if !b.client.Alive() {
		return b.markLost(err)
	}
	return err
}
func (l *renewalLease) Start(parent context.Context, prepared contract.KVRenewalPreparation) (contract.KVRenewalRun, error) {
	p, ok := prepared.(*renewalPreparation)
	if !ok || p == nil || p.owner != l {
		return nil, contract.ErrKVPlanUsed
	}
	b := l.base
	if !b.ops.TryLock() {
		return nil, contract.ErrKVBusy
	}
	defer b.ops.Unlock()
	if err := l.writable(); err != nil {
		return nil, err
	}
	if l.run != nil {
		return nil, contract.ErrKVBusy
	}
	if p.used.Load() {
		return nil, contract.ErrKVPlanUsed
	}
	ctx, done, err := b.request(parent)
	if err != nil {
		return nil, err
	}
	if err = b.connect(ctx); err != nil {
		done()
		return nil, err
	}
	c := b.client
	plan, err := c.PrepareRenewal(&pb.LeaseKeepAliveRequest{ID: p.request.LeaseID})
	if err != nil {
		done()
		return nil, err
	}
	if !p.used.CompareAndSwap(false, true) {
		done()
		return nil, contract.ErrKVPlanUsed
	}
	raw, err := c.startRenewal(ctx, plan, l.admit)
	if raw == nil {
		p.used.Store(false)
		done()
		return nil, err
	}
	run := &renewalRun{raw: raw, request: p.request}
	l.run = run
	l.last = contract.KVRenewalBatch{}
	go func() {
		<-raw.done
		if !c.Alive() {
			b.mu.Lock()
			b.lost = true
			b.mu.Unlock()
		}
		done()
	}()
	return run, err
}
func (l *renewalLease) ClearReceipts() error {
	b := l.base
	if !b.ops.TryLock() {
		return contract.ErrKVBusy
	}
	defer b.ops.Unlock()
	if err := l.writable(); err != nil {
		return err
	}
	if l.run != nil {
		if _, ended := l.run.raw.Terminal(); !ended {
			return contract.ErrKVBusy
		}
		_ = l.run.raw.Close()
		l.run = nil
	}
	l.releaseBudget()
	return nil
}
func (l *renewalLease) settle(recovering bool) (contract.KVRenewalRecovery, error) {
	b := l.base
	b.mu.Lock()
	b.closed = true
	b.cancel()
	b.mu.Unlock()
	b.ops.Lock()
	defer b.ops.Unlock()
	result := contract.KVRenewalRecovery{Batch: l.last}
	if l.run != nil {
		l.run.raw.cancel()
		<-l.run.raw.done
		for {
			batch := l.run.Poll()
			result.Batch.Request, result.Batch.Ended, result.Batch.Terminal = batch.Request, batch.Ended, batch.Terminal
			result.Batch.Receipts = append(result.Batch.Receipts, batch.Receipts...)
			if len(batch.Receipts) == 0 {
				break
			}
		}
		l.uncertain = l.uncertain || result.Batch.Terminal.WriteOutcomeUnknown
		_ = l.run.raw.Close()
		l.run = nil
	}
	result.Batch.Terminal.WriteOutcomeUnknown = l.uncertain
	l.last = result.Batch
	l.last.Receipts = nil
	if b.client != nil {
		b.closeErr = errors.Join(b.closeErr, b.client.Close())
		b.client = nil
	}
	if !recovering {
		l.releaseBudget()
		b.s.mu.Lock()
		delete(b.s.renewals, l)
		b.s.mu.Unlock()
	}
	return result, b.closeErr
}
func (l *renewalLease) Close() error { _, err := l.settle(false); return err }

// Retirement settles the original executor but leaves parked receipts and
// retained budgets with their original document until explicit disposal.
func (l *renewalLease) retire() error {
	b := l.base
	b.mu.Lock()
	b.lost = true
	b.cancel()
	b.mu.Unlock()
	b.ops.Lock()
	defer b.ops.Unlock()
	if l.run != nil {
		l.run.raw.cancel()
		<-l.run.raw.done
	}
	if b.client != nil {
		b.closeErr = errors.Join(b.closeErr, b.client.Close())
		b.client = nil
	}
	return b.closeErr
}

type renewalRun struct {
	raw     *RenewalRun
	request contract.KVRenewalRequest
	poll    sync.Mutex
}

func (r *renewalRun) Request() contract.KVRenewalRequest { return r.request }
func (r *renewalRun) Poll() contract.KVRenewalBatch {
	r.poll.Lock()
	defer r.poll.Unlock()
	batch := contract.KVRenewalBatch{Request: r.request}
	for _, receipt := range r.raw.Poll() {
		batch.Receipts = append(batch.Receipts, projectRenewalReceipt(receipt))
	}
	t, ended := r.raw.Terminal()
	batch.Ended = ended
	batch.Terminal = contract.KVRenewalTerminal{Reason: t.Reason, Code: status.Code(t.Cause).String(), Attempts: t.Attempts, Acknowledged: t.Acknowledged, WriteOutcomeUnknown: t.WriteOutcomeUnknown, CaptureIncomplete: t.CaptureIncomplete}
	if t.Cause != nil {
		batch.Terminal.Message = t.Cause.Error()
	}
	return batch
}
func (r *renewalRun) Stop(ctx context.Context) error { return r.raw.Stop(ctx) }
func (r *renewalRun) Close() error                   { return r.raw.Close() }

func projectRenewalReceipt(r RenewalReceipt) contract.KVRenewalReceipt {
	out := contract.KVRenewalReceipt{Sequence: r.Sequence, Attempted: r.Attempted, Completed: r.Completed, WriteOutcomeUnknown: r.WriteOutcomeUnknown, Native: bytes.Clone(r.Native), Code: r.Code.String(), LeaseID: r.LeaseID, TTL: r.TTL, Header: contract.KVHeader{ClusterID: r.Header.ClusterID, MemberID: r.Header.MemberID, Revision: r.Header.Revision, RaftTerm: r.Header.RaftTerm}}
	if len(r.Native) == 0 {
		return out
	}
	native := &pb.LeaseKeepAliveResponse{}
	budget := 256 << 10
	if err := proto.Unmarshal(r.Native, native); err != nil || !jsonBound(native.ProtoReflect(), &budget, 0) {
		out.JSONCaptureError = "renewal JSON projection invalid or exceeds 256 KiB; original protobuf retained"
		return out
	}
	encoded, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(native)
	if err != nil || len(encoded) > 256<<10 {
		out.JSONCaptureError = "renewal JSON projection invalid or exceeds 256 KiB; original protobuf retained"
	} else {
		out.JSON = string(encoded)
	}
	return out
}

var _ contract.KVRenewalLease = (*renewalLease)(nil)
var _ contract.KVRenewalRun = (*renewalRun)(nil)
