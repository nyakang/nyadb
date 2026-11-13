package kvworkspace

import (
	"context"
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
	"github.com/nyakang/nyadb/locales"
)

type renewalConfirmation struct {
	lease         contract.KVRenewalLease
	plan          contract.KVRenewalPreparation
	scope         contract.KVScope
	authored      contract.KVRequest
	checked, open bool
}
type renewalPacket struct {
	batch   contract.KVRenewalBatch
	preview string
	err     error
}
type renewalOutcome struct {
	renewalPacket
	lease contract.KVRenewalLease
}
type renewalOperation struct {
	kind     string
	scope    contract.KVScope
	authored contract.KVRequest
	request  contract.KVRenewalRequest
	cancel   context.CancelFunc
	canceled bool
	stop     chan struct{}
	updates  <-chan renewalPacket
	out      <-chan renewalOutcome
	finished <-chan struct{}
}

func (d *Document) hasUnknownWrites() bool {
	return d.httpSessionRenewal != nil && d.httpSessionRenewal.unknown || d.unknown != nil || d.renewalUnknown != nil || d.members != nil && d.members.unknown != nil || d.alarms != nil && d.alarms.unknown != nil
}
func (s *State) renewalLeaseFor(d *Document) bool {
	if !s.active(d) || d.Busy() || d.renewalNeedsRecovery {
		return false
	}
	if d.renewalLease == nil {
		if s.deps.RenewalProviderFor == nil {
			d.error = locales.Text("此连接不支持持续续租")
			return false
		}
		p, err := s.deps.RenewalProviderFor(d.profileID)
		if err != nil || p == nil {
			d.renewalNeedsRecovery = true
			d.error = locales.Text("KV 原连接不可用")
			return false
		}
		scope := p.KVScope().Clone()
		if d.scope.Generation != 0 && !sameEventScope(scope, d.scope) {
			d.renewalNeedsRecovery = true
			d.error = locales.Text("KV 原连接不可用")
			return false
		}
		l, err := p.ReserveKVRenewal(d.id)
		if err != nil || l == nil {
			if err != nil {
				d.error = err.Error()
			}
			return false
		}
		if scope.Namespace.ProfileID != d.profileID || scope.Generation == 0 || !sameEventScope(l.Scope(), scope) {
			_ = l.Close()
			d.renewalNeedsRecovery = true
			d.error = locales.Text("KV 原连接不可用")
			return false
		}
		d.renewalLease, d.renewalScope = l, scope
		d.renewalAvailable = true
	}
	if err := d.renewalLease.CheckKVLease(); err != nil {
		d.renewalNeedsRecovery = true
		d.error = err.Error()
		return false
	}
	return true
}
func (s *State) PrepareRenewal(d *Document) {
	if !s.active(d) || d.Busy() || d.method != RenewalMethod {
		return
	}
	if d.hasUnknownWrites() {
		d.error = locales.Text("原写入未确认；恢复后可读取，关闭页签可放弃本地草稿与回执")
		return
	}
	r, err := ParseRenewalRequest(d.Request().JSON)
	if err != nil {
		d.error = locales.Text("请输入完整的非零租约 ID")
		return
	}
	if !s.renewalLeaseFor(d) {
		return
	}
	if d.renewalReceipt != nil {
		d.error = locales.Text("请先清除已停止的续租回执")
		return
	}
	plan, err := d.renewalLease.Prepare(r)
	if err != nil || plan == nil {
		if err != nil {
			d.error = err.Error()
		}
		return
	}
	if plan.Snapshot() != r {
		d.error = locales.Text("KV 请求已更改，请重新预览")
		return
	}
	d.clearConfirmation()
	d.permit = nil
	d.error = ""
	d.renewalConfirmation = &renewalConfirmation{lease: d.renewalLease, plan: plan, scope: d.renewalScope.Clone(), authored: d.Request(), open: true}
}
func (s *State) ConfirmRenewal(d *Document, checked bool) {
	if !s.owned(d) || d.renewalConfirmation == nil || !checked {
		return
	}
	p := d.renewalConfirmation
	if !s.renewalLeaseFor(d) {
		d.renewalConfirmation = nil
		return
	}
	request, err := ParseRenewalRequest(d.Request().JSON)
	if err != nil || p.authored != d.Request() || p.lease != d.renewalLease || !sameEventScope(p.scope, d.renewalScope) || p.plan.Snapshot() != request || d.hasUnknownWrites() || d.renewalReceipt != nil {
		d.renewalConfirmation = nil
		d.error = locales.Text("KV 请求已更改，请重新预览")
		return
	}
	d.renewalConfirmation = nil
	d.selectedRenewal = true
	d.selectedBrowser = false
	d.selectedEvents = false
	d.selectedUnknown = false
	d.renewalReceipt = &RenewalReceipt{Scope: p.scope.Clone(), Authored: p.authored, Batch: contract.KVRenewalBatch{Request: request}}
	lease, plan := p.lease, p.plan
	invalidate := s.Invalidate
	s.renewalStart(d, "renew", p.authored, request, func(ctx context.Context, updates chan<- renewalPacket, op *renewalOperation) renewalOutcome {
		return renewalWorker(ctx, lease, plan, request, updates, op, invalidate)
	})
}
func (s *State) renewalStart(d *Document, kind string, authored contract.KVRequest, request contract.KVRenewalRequest, work func(context.Context, chan<- renewalPacket, *renewalOperation) renewalOutcome) {
	ctx, cancel := context.WithCancel(d.ctx)
	updates, out, finished := make(chan renewalPacket, 1), make(chan renewalOutcome, 1), make(chan struct{})
	op := &renewalOperation{kind: kind, scope: d.renewalScope.Clone(), authored: authored, request: request, cancel: cancel, stop: make(chan struct{}), updates: updates, out: out, finished: finished}
	d.renewalJob = op
	d.error = ""
	d.permit = nil
	invalidate := s.Invalidate
	go func() {
		defer close(finished)
		defer cancel()
		result := work(ctx, updates, op)
		if ctx.Err() != nil && result.lease != nil {
			_ = result.lease.Close()
			result.lease = nil
			result.err = errors.Join(result.err, ctx.Err())
		}
		out <- result
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) StopRenewal(d *Document) {
	if s.owned(d) && d.renewalJob != nil && d.renewalJob.kind == "renew" && !d.renewalJob.canceled {
		d.renewalJob.canceled = true
		close(d.renewalJob.stop)
		if d.renewalReceipt == nil || len(d.renewalReceipt.Batch.Receipts) == 0 {
			d.renewalJob.cancel()
		}
	}
}
func (s *State) PreconnectRenewal(d *Document) {
	if !s.renewalLeaseFor(d) || d.renewalReceipt != nil {
		return
	}
	l := d.renewalLease
	s.renewalStart(d, "connect", contract.KVRequest{}, contract.KVRenewalRequest{}, func(ctx context.Context, _ chan<- renewalPacket, _ *renewalOperation) renewalOutcome {
		return renewalOutcome{renewalPacket: renewalPacket{err: l.Preconnect(ctx)}}
	})
}
func (s *State) ClearRenewalReceipt(d *Document) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.hasUnknownWrites() || d.renewalReceipt == nil || !d.renewalReceipt.Batch.Ended || !s.renewalLeaseFor(d) {
		return
	}
	l := d.renewalLease
	s.renewalStart(d, "clear", contract.KVRequest{}, contract.KVRenewalRequest{}, func(context.Context, chan<- renewalPacket, *renewalOperation) renewalOutcome {
		return renewalOutcome{renewalPacket: renewalPacket{err: l.ClearReceipts()}}
	})
}
func (s *State) RecoverRenewal(d *Document) {
	if !s.active(d) || d.Busy() || !d.renewalNeedsRecovery || s.deps.RenewalProviderFor == nil {
		return
	}
	p, err := s.deps.RenewalProviderFor(d.profileID)
	if err != nil || p == nil {
		d.error = locales.Text("KV 原连接不可用")
		return
	}
	expected := p.KVScope().Clone()
	previous, id, profile := d.renewalLease, d.id, d.profileID
	retained := 0
	request := contract.KVRenewalRequest{}
	if d.renewalReceipt != nil {
		retained = renewalReceiptSize(d.renewalReceipt.Batch)
		request = d.renewalReceipt.Batch.Request
	}
	s.renewalStart(d, "recover", contract.KVRequest{}, request, func(ctx context.Context, _ chan<- renewalPacket, _ *renewalOperation) renewalOutcome {
		if ctx.Err() != nil {
			return renewalOutcome{renewalPacket: renewalPacket{err: ctx.Err()}}
		}
		var recovery contract.KVRenewalRecovery
		var err error
		if previous == nil {
			recovery.Lease, err = p.ReserveKVRenewal(id)
		} else {
			recovery, err = p.RecoverKVRenewal(previous)
		}
		l := recovery.Lease
		if l != nil && (!sameEventScope(l.Scope(), expected) || expected.Namespace.ProfileID != profile || expected.Generation == 0) {
			_ = l.Close()
			l = nil
			err = errors.Join(err, contract.ErrRevoked)
		}
		batch := recovery.Batch
		if len(batch.Receipts) > 256 || renewalReceiptSize(batch) > renewalReceiptBytes-retained {
			batch.Receipts = nil
			batch.Terminal.CaptureIncomplete = true
			err = errors.Join(err, jsondocument.ErrBounds)
		} else {
			batch = batch.Clone()
		}
		return renewalOutcome{renewalPacket: renewalPacket{batch: batch, err: err}, lease: l}
	})
}
func (s *State) consumeRenewal(d *Document) {
	if p := d.renewalConfirmation; p != nil && (p.authored != d.Request() || p.lease.CheckKVLease() != nil || d.hasUnknownWrites()) {
		d.renewalConfirmation = nil
	}
	if op := d.renewalJob; op != nil {
		select {
		case p := <-op.updates:
			s.admitRenewalPacket(d, op, p)
		default:
		}
		select {
		case result := <-op.out:
			select {
			case p := <-op.updates:
				s.admitRenewalPacket(d, op, p)
			default:
			}
			s.admitRenewalPacket(d, op, result.renewalPacket)
			d.renewalJob = nil
			op.cancel()
			if op.kind == "clear" && result.err == nil {
				d.renewalReceipt = nil
			}
			if op.kind == "recover" {
				if r := d.renewalReceipt; r != nil {
					batch := result.batch
					if batch.Request == r.Batch.Request {
						r.Batch.Receipts = append(r.Batch.Receipts, batch.Receipts...)
						if batch.Ended {
							r.Batch.Ended = true
							r.Batch.Terminal = batch.Terminal
						}
					}
					if r.Batch.Terminal.WriteOutcomeUnknown {
						d.renewalUnknown = r
					}
					r.preview = renewalPreview(r.Batch)
				}
				if result.err == nil && result.lease != nil {
					d.renewalLease = result.lease
					d.renewalScope = result.lease.Scope().Clone()
					d.renewalNeedsRecovery = false
				} else if result.lease != nil {
					_ = result.lease.Close()
				}
			}
		default:
		}
	}
	if d.renewalJob == nil && d.renewalLease != nil && d.renewalLease.CheckKVLease() != nil {
		d.renewalNeedsRecovery = true
		d.renewalConfirmation = nil
	}
}
func (s *State) admitRenewalPacket(d *Document, op *renewalOperation, p renewalPacket) {
	if p.err != nil {
		d.error = p.err.Error()
	}
	r := d.renewalReceipt
	if op.kind != "renew" || r == nil || !sameEventScope(r.Scope, op.scope) || r.Authored != op.authored || r.Batch.Request != op.request {
		return
	}
	r.Batch.Receipts = append(r.Batch.Receipts, p.batch.Receipts...)
	if p.batch.Ended {
		r.Batch.Ended = true
		r.Batch.Terminal = p.batch.Terminal
	}
	for _, receipt := range p.batch.Receipts {
		if receipt.WriteOutcomeUnknown {
			d.renewalUnknown = r
		}
	}
	if p.batch.Terminal.WriteOutcomeUnknown {
		d.renewalUnknown = r
	}
	if p.preview != "" {
		r.preview = p.preview
	}
	if p.err != nil {
		r.Error = p.err.Error()
	}
}
