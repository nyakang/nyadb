package kvworkspace

import (
	"bytes"
	"context"
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
	"github.com/nyakang/nyadb/locales"
)

const eventReceiptBytes = 16 << 20

type eventPacket struct {
	batch   contract.KVWatchBatch
	preview string
	err     error
}
type eventOutcome struct {
	eventPacket
	history  *contract.KVHistoryResult
	recovery contract.KVEventsRecovery
	lease    contract.KVEventsLease
}
type eventOperation struct {
	kind     string
	scope    contract.KVScope
	authored contract.KVRequest
	cancel   context.CancelFunc
	canceled bool
	stop     chan struct{}
	progress chan struct{}
	updates  <-chan eventPacket
	out      <-chan eventOutcome
	finished <-chan struct{}
}

func sameEventScope(a, b contract.KVScope) bool {
	return a.Namespace == b.Namespace && a.Generation == b.Generation && bytes.Equal(a.Prefix, b.Prefix) && a.Header == b.Header && a.Version == b.Version
}
func (s *State) eventLeaseFor(d *Document) bool {
	if !s.active(d) || d.Busy() || d.eventNeedsRecovery {
		return false
	}
	if d.eventLease == nil {
		if s.deps.EventsProviderFor == nil {
			d.error = locales.Text("KV 原连接不可用")
			return false
		}
		p, err := s.deps.EventsProviderFor(d.profileID)
		if err != nil || p == nil {
			d.eventNeedsRecovery = true
			d.error = locales.Text("KV 原连接不可用")
			return false
		}
		// Initial events are bound to the document's original publication. A
		// replacement requires explicit recovery even if no stream was started.
		scope := p.KVScope().Clone()
		if d.scope.Generation != 0 && !sameEventScope(scope, d.scope) {
			d.eventNeedsRecovery = true
			d.error = locales.Text("KV 原连接不可用")
			return false
		}
		l, err := p.ReserveKVEvents(d.id)
		if err != nil || l == nil {
			if err != nil {
				d.error = err.Error()
			}
			return false
		}
		if scope.Namespace.ProfileID != d.profileID || scope.Generation == 0 || !sameEventScope(l.Scope(), scope) {
			_ = l.Close()
			d.eventNeedsRecovery = true
			d.error = locales.Text("KV 原连接不可用")
			return false
		}
		d.eventLease, d.eventScope = l, scope
	}
	if err := d.eventLease.CheckKVLease(); err != nil {
		d.eventNeedsRecovery = true
		d.error = err.Error()
		return false
	}
	return true
}
func (s *State) eventStart(d *Document, kind string, authored contract.KVRequest, work func(context.Context, chan<- eventPacket, *eventOperation) eventOutcome) {
	ctx, cancel := context.WithCancel(d.ctx)
	updates, out, finished := make(chan eventPacket, 1), make(chan eventOutcome, 1), make(chan struct{})
	op := &eventOperation{kind: kind, scope: d.eventScope.Clone(), authored: authored, cancel: cancel, stop: make(chan struct{}), progress: make(chan struct{}, 1), updates: updates, out: out, finished: finished}
	d.eventJob = op
	d.error = ""
	d.permit = nil
	invalidate := s.Invalidate
	go func() {
		defer close(finished)
		defer cancel()
		r := work(ctx, updates, op)
		if ctx.Err() != nil && r.lease != nil {
			_ = r.lease.Close()
			r.lease = nil
			r.err = errors.Join(r.err, ctx.Err())
		}
		out <- r
		if invalidate != nil {
			invalidate()
		}
	}()
}

// RunEvents and the supporting methods remain injectable; host/UI publication
// is a separate acceptance step. Neither method prepares or executes KV writes.
func (s *State) RunEvents(d *Document) {
	if !s.active(d) || d.Busy() || !eventMethod(d.method) {
		return
	}
	authored := d.Request()
	var watch contract.KVWatchRequest
	var history contract.KVHistoryRequest
	var err error
	if authored.Method == WatchMethod {
		watch, err = ParseWatchRequest(authored.JSON)
	} else {
		history, err = ParseHistoryRequest(authored.JSON)
	}
	if err != nil {
		d.error = locales.Text("KV 请求无效") + ": " + err.Error()
		return
	}
	if !s.eventLeaseFor(d) {
		return
	}
	if d.eventReceipt != nil {
		d.error = contract.ErrKVBusy.Error()
		return
	}
	lease := d.eventLease
	r := &EventReceipt{Scope: d.eventScope.Clone(), Authored: authored}
	d.eventReceipt = r
	if authored.Method == WatchMethod {
		captured := watch.Clone()
		r.Watch = &captured
		invalidate := s.Invalidate
		s.eventStart(d, "watch", authored, func(ctx context.Context, updates chan<- eventPacket, op *eventOperation) eventOutcome {
			return eventWatchWorker(ctx, lease, captured.Clone(), updates, op, invalidate)
		})
	} else {
		captured := history.Clone()
		s.eventStart(d, "history", authored, func(ctx context.Context, _ chan<- eventPacket, _ *eventOperation) eventOutcome {
			h, err := lease.History(ctx, captured.Clone())
			if !sameHistoryRequest(h.Authored, captured) {
				err = errors.Join(err, contract.ErrRevoked)
				h.Complete = false
			}
			// A malformed injected result cannot make the UI retain unbounded data.
			if historyReceiptSize(h) > eventReceiptBytes {
				h = contract.KVHistoryResult{Authored: captured.Clone(), Window: captured.Clone(), CaptureIncomplete: true, Terminal: contract.KVEventTerminal{Reason: "document_bounds"}}
				err = errors.Join(err, jsondocument.ErrBounds)
			}
			h = h.Clone()
			preview := eventHistoryPreview(h)
			return eventOutcome{eventPacket: eventPacket{err: err, preview: preview}, history: &h}
		})
	}
}
func sameHistoryRequest(a, b contract.KVHistoryRequest) bool {
	return bytes.Equal(a.Key, b.Key) && a.StartRevision == b.StartRevision && a.EndRevision == b.EndRevision && a.Limit == b.Limit
}
func sameWatchRequest(a, b contract.KVWatchRequest) bool {
	return bytes.Equal(a.Key, b.Key) && bytes.Equal(a.RangeEnd, b.RangeEnd) && a.StartRevision == b.StartRevision && a.WatchID == b.WatchID && a.PrevKV == b.PrevKV && a.ProgressNotify == b.ProgressNotify && a.NoPut == b.NoPut && a.NoDelete == b.NoDelete
}
func (s *State) PreconnectEvents(d *Document) {
	if !s.eventLeaseFor(d) {
		return
	}
	lease := d.eventLease
	s.eventStart(d, "connect", contract.KVRequest{}, func(ctx context.Context, _ chan<- eventPacket, _ *eventOperation) eventOutcome {
		return eventOutcome{eventPacket: eventPacket{err: lease.Preconnect(ctx)}}
	})
}
func (s *State) StopEvents(d *Document) {
	if s.owned(d) && d.eventJob != nil && !d.eventJob.canceled {
		d.eventJob.canceled = true
		if d.eventJob.kind == "watch" {
			close(d.eventJob.stop)
		} else {
			d.eventJob.cancel()
		}
	}
}
func (s *State) ProgressEvents(d *Document) {
	if !s.active(d) || d.eventJob == nil || d.eventJob.kind != "watch" || d.eventJob.canceled {
		return
	}
	select {
	case d.eventJob.progress <- struct{}{}:
	default:
	}
}
func (s *State) ClearEventReceipt(d *Document) {
	if !s.eventLeaseFor(d) {
		return
	}
	lease := d.eventLease
	s.eventStart(d, "clear", contract.KVRequest{}, func(ctx context.Context, _ chan<- eventPacket, _ *eventOperation) eventOutcome {
		if err := ctx.Err(); err != nil {
			return eventOutcome{eventPacket: eventPacket{err: err}}
		}
		return eventOutcome{eventPacket: eventPacket{err: lease.ClearReceipts()}}
	})
}
func (s *State) RecoverEvents(d *Document) {
	if !s.active(d) || d.Busy() || !d.eventNeedsRecovery || s.deps.EventsProviderFor == nil {
		return
	}
	p, err := s.deps.EventsProviderFor(d.profileID)
	if err != nil || p == nil {
		d.error = locales.Text("KV 原连接不可用")
		return
	}
	expected := p.KVScope().Clone()
	previous, id := d.eventLease, d.id
	profileID := d.profileID
	retainedSize := 0
	if d.eventReceipt != nil {
		for _, f := range d.eventReceipt.Batch.Frames {
			retainedSize += eventFrameSize(f)
		}
		if d.eventReceipt.History != nil {
			retainedSize += historyReceiptSize(*d.eventReceipt.History)
		}
	}
	s.eventStart(d, "recover", contract.KVRequest{}, func(ctx context.Context, _ chan<- eventPacket, _ *eventOperation) eventOutcome {
		if err := ctx.Err(); err != nil {
			return eventOutcome{eventPacket: eventPacket{err: err}}
		}
		var recovery contract.KVEventsRecovery
		var err error
		if previous == nil {
			recovery.Lease, err = p.ReserveKVEvents(id)
		} else {
			recovery, err = p.RecoverKVEvents(previous)
		}
		l := recovery.Lease
		recovery.Lease = nil
		if l != nil && (!sameEventScope(l.Scope(), expected) || expected.Namespace.ProfileID != profileID || expected.Generation == 0) {
			_ = l.Close()
			l = nil
			err = errors.Join(err, contract.ErrRevoked)
		}
		for _, f := range recovery.Watch.Frames {
			retainedSize += eventFrameSize(f)
		}
		if recovery.History != nil {
			retainedSize += historyReceiptSize(*recovery.History)
		}
		if retainedSize > eventReceiptBytes {
			recovery.Watch = contract.KVWatchBatch{Ended: true, Incomplete: true, Terminal: contract.KVEventTerminal{Reason: "document_bounds"}}
			recovery.History = nil
			err = errors.Join(err, jsondocument.ErrBounds)
		} else {
			recovery.Watch = recovery.Watch.Clone()
			if recovery.History != nil {
				h := recovery.History.Clone()
				recovery.History = &h
			}
		}
		return eventOutcome{eventPacket: eventPacket{err: err}, lease: l, recovery: recovery}
	})
}
func (s *State) consumeEvents(d *Document) {
	if d.eventJob != nil {
		op := d.eventJob
		// Drain the earlier update before its final packet. Both private buffers
		// are consumed solely by the active original document on the UI thread.
		select {
		case packet := <-op.updates:
			s.admitEventPacket(d, op, packet)
		default:
		}
		select {
		case result := <-op.out:
			// A worker can enqueue its last update between the two selects.
			select {
			case packet := <-op.updates:
				s.admitEventPacket(d, op, packet)
			default:
			}
			s.admitEventPacket(d, op, result.eventPacket)
			d.eventJob = nil
			op.cancel()
			if op.kind == "history" && d.eventReceipt != nil {
				d.eventReceipt.History = result.history
				d.eventReceipt.Batch.Ended = true
				if result.history != nil {
					d.eventReceipt.Batch.Incomplete = !result.history.Complete || result.history.CaptureIncomplete
					d.eventReceipt.Batch.Terminal = result.history.Terminal
				}
			}
			if op.kind == "clear" && result.err == nil {
				d.eventReceipt = nil
			}
			if op.kind == "recover" {
				if d.eventReceipt != nil {
					d.eventReceipt.Batch.Frames = append(d.eventReceipt.Batch.Frames, result.recovery.Watch.Frames...)
					if result.recovery.Watch.Ended {
						d.eventReceipt.Batch.Ended = true
						d.eventReceipt.Batch.Incomplete = result.recovery.Watch.Incomplete
						d.eventReceipt.Batch.Terminal = result.recovery.Watch.Terminal
					}
					if d.eventReceipt.History == nil && result.recovery.History != nil {
						d.eventReceipt.History = result.recovery.History
					}
				}
				if result.err == nil && result.lease != nil {
					d.eventLease = result.lease
					d.eventScope = result.lease.Scope().Clone()
					d.eventNeedsRecovery = false
				} else if result.lease != nil {
					_ = result.lease.Close()
				}
			}
		default:
		}
	}
	if d.eventJob == nil && d.eventLease != nil && d.eventLease.CheckKVLease() != nil {
		d.eventNeedsRecovery = true
	}
}
func (s *State) admitEventPacket(d *Document, op *eventOperation, p eventPacket) {
	if p.err != nil {
		d.error = p.err.Error()
	}
	r := d.eventReceipt
	if r == nil || !sameEventScope(r.Scope, op.scope) || r.Authored != op.authored {
		return
	}
	r.Batch.Frames = append(r.Batch.Frames, p.batch.Frames...)
	if p.batch.Ended {
		r.Batch.Ended = true
		r.Batch.Incomplete = p.batch.Incomplete
		r.Batch.Terminal = p.batch.Terminal
	}
	if p.preview != "" {
		r.preview = p.preview
	}
	if p.err != nil {
		r.Error = p.err.Error()
	}
}
