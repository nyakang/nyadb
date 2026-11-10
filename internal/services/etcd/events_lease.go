package etcd

import (
	"bytes"
	"context"
	"errors"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

const (
	eventDocumentLimit = 4
	eventDocumentBytes = 16 << 20
	eventSessionBytes  = 32 << 20
)

type eventsLease struct {
	base    *kvLease
	run     *eventsRun                // guarded by base.ops
	last    *contract.KVHistoryResult // guarded by base.mu
	charged int                       // guarded by Session.mu, retained through Poll transfers
}

func (l *eventsLease) Scope() contract.KVScope { return l.base.Scope() }
func (l *eventsLease) CheckKVLease() error     { return l.base.CheckKVLease() }

// The reservation also covers neutral frame copies, optional JSON and retained
// history snapshots. Poll does not release it: transferred receipts still belong
// to this document. Only explicit disposal after executor settlement releases it.
func (l *eventsLease) admit(rawCharge int) bool {
	s := l.base.s
	charge := rawCharge * 4
	s.mu.Lock()
	defer s.mu.Unlock()
	if charge > eventDocumentBytes-l.charged || charge > eventSessionBytes-s.eventBytes {
		return false
	}
	l.charged += charge
	s.eventBytes += charge
	return true
}

func (l *eventsLease) releaseBudget() {
	s := l.base.s
	s.mu.Lock()
	s.eventBytes -= l.charged
	l.charged = 0
	s.mu.Unlock()
}

func (l *eventsLease) Preconnect(parent context.Context) error {
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
	if err != nil {
		return err
	}
	_, err = b.client.Execute(ctx, p)
	if !b.client.Alive() {
		return b.markLost(err)
	}
	return err
}

func (l *eventsLease) StartWatch(parent context.Context, input contract.KVWatchRequest) (contract.KVWatchRun, error) {
	r := input.Clone()
	native := nativeWatchRequest(r)
	if err := validateWatchRequest(native); err != nil {
		return nil, err
	}
	b := l.base
	if !b.ops.TryLock() {
		return nil, contract.ErrKVBusy
	}
	defer b.ops.Unlock()
	if l.run != nil {
		return nil, contract.ErrKVBusy
	}
	ctx, done, err := b.request(parent)
	if err != nil {
		return nil, err
	}
	if err = b.connect(ctx); err != nil {
		done()
		return nil, err
	}
	c := b.client // captured original client; never consult a replacement in receive work
	if !c.operation.TryLock() {
		done()
		return nil, contract.ErrKVBusy
	}
	raw, err := c.startWatchOwnedBudget(ctx, native, l.admit)
	if raw == nil {
		done()
		return nil, err
	}
	run := &eventsRun{lease: l, raw: raw, request: r}
	l.run = run
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

func (l *eventsLease) History(parent context.Context, input contract.KVHistoryRequest) (result contract.KVHistoryResult, err error) {
	r := input.Clone()
	result.Authored, result.Window = r.Clone(), r.Clone()
	native := HistoryRequest{Key: bytes.Clone(r.Key), StartRevision: r.StartRevision, EndRevision: r.EndRevision, Limit: r.Limit}
	if err = validateHistoryRequest(native); err != nil {
		return result, err
	}
	b := l.base
	if !b.ops.TryLock() {
		return result, contract.ErrKVBusy
	}
	defer b.ops.Unlock()
	if l.run != nil {
		return result, contract.ErrKVBusy
	}
	ctx, done, err := b.request(parent)
	if err != nil {
		return result, err
	}
	defer done()
	if err = b.connect(ctx); err != nil {
		return result, err
	}
	if !l.admit(32 << 10) {
		result.CaptureIncomplete = true
		return result, ErrBounds
	}
	raw, err := b.client.history(ctx, native, l.admit)
	result = projectHistory(raw)
	b.mu.Lock()
	snapshot := result.Clone()
	l.last = &snapshot
	b.mu.Unlock()
	if !b.client.Alive() {
		err = b.markLost(err)
	}
	return result, err
}

func (l *eventsLease) LastHistory() *contract.KVHistoryResult {
	l.base.mu.Lock()
	defer l.base.mu.Unlock()
	if l.last == nil {
		return nil
	}
	r := l.last.Clone()
	return &r
}

func (l *eventsLease) ClearReceipts() error {
	b := l.base
	if !b.ops.TryLock() {
		return contract.ErrKVBusy
	}
	defer b.ops.Unlock()
	if err := b.CheckKVLease(); err != nil {
		return err
	}
	if l.run != nil {
		if _, ended := l.run.raw.Terminal(); !ended {
			return contract.ErrKVBusy
		}
		_ = l.run.raw.Close()
		l.run = nil
	}
	b.mu.Lock()
	l.last = nil
	b.mu.Unlock()
	l.releaseBudget()
	return nil
}

func (l *eventsLease) settle() (contract.KVEventsRecovery, error) {
	b := l.base
	b.mu.Lock()
	b.closed = true
	b.cancel()
	b.mu.Unlock()
	b.ops.Lock()
	defer b.ops.Unlock()
	result := contract.KVEventsRecovery{History: l.LastHistory()}
	if l.run != nil {
		l.run.raw.cancel()
		<-l.run.raw.done
		for {
			batch := l.run.Poll()
			result.Watch.Frames = append(result.Watch.Frames, batch.Frames...)
			result.Watch.Ended, result.Watch.Incomplete, result.Watch.Terminal = batch.Ended, batch.Incomplete, batch.Terminal
			if len(batch.Frames) == 0 {
				break
			}
		}
		_ = l.run.raw.Close()
		l.run = nil
	}
	if b.client != nil {
		b.closeErr = errors.Join(b.closeErr, b.client.Close())
		b.client = nil
	}
	b.mu.Lock()
	l.last = nil
	b.mu.Unlock()
	l.releaseBudget()
	b.s.mu.Lock()
	delete(b.s.events, l)
	b.s.mu.Unlock()
	return result, b.closeErr
}

func (l *eventsLease) Close() error { _, err := l.settle(); return err }

// Session retirement stops the physical executor while its original document
// still owns parked receipts. Explicit document Close/recovery disposes them.
func (l *eventsLease) retire() error {
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

type eventsRun struct {
	lease   *eventsLease
	raw     *WatchRun
	request contract.KVWatchRequest
	poll    sync.Mutex
}

func (w *eventsRun) Request() contract.KVWatchRequest { return w.request.Clone() }
func (w *eventsRun) Poll() contract.KVWatchBatch {
	w.poll.Lock()
	defer w.poll.Unlock()
	out := contract.KVWatchBatch{}
	for _, f := range w.raw.Poll() {
		out.Frames = append(out.Frames, projectEventFrame(f))
	}
	terminal, ended := w.raw.Terminal()
	out.Ended, out.Terminal = ended, eventTerminal(terminal)
	out.Incomplete = ended && terminal.Reason != "stopped"
	return out
}
func (w *eventsRun) Progress() error {
	if err := w.lease.CheckKVLease(); err != nil {
		return err
	}
	return w.raw.Progress()
}
func (w *eventsRun) Stop(ctx context.Context) error { return w.raw.Stop(ctx) }
func (w *eventsRun) Close() error                   { return w.raw.Close() }

var _ contract.KVEventsLease = (*eventsLease)(nil)
var _ contract.KVWatchRun = (*eventsRun)(nil)
