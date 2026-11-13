package kvworkspace

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

type httpSessionRenewalState struct {
	lease                contract.KVHTTPSessionRenewalLease
	scope, evidenceScope contract.KVScope
	authored             contract.KVRequest
	request              contract.KVHTTPSessionRenewalRequest
	run                  contract.KVHTTPSessionRenewalRun
	status               contract.KVHTTPSessionRenewalStatus
	job                  *httpSessionRenewalOperation
	cancel               context.CancelFunc
	needsRecovery        bool
	stopping             bool
	selected             int
	preview              string
	confirmation         *httpSessionRenewalConfirmation
	retained             *contract.KVHTTPSessionRenewalSnapshot
	unknown, jsonMode    bool
	incomplete           bool
	revision             uint64
}

type httpSessionRenewalConfirmation struct {
	lease         contract.KVHTTPSessionRenewalLease
	plan          contract.KVHTTPSessionRenewalPreparation
	scope         contract.KVScope
	authored      contract.KVRequest
	request       contract.KVHTTPSessionRenewalRequest
	checked, open bool
}

type httpSessionRenewalOutcome struct {
	run      contract.KVHTTPSessionRenewalRun
	lease    contract.KVHTTPSessionRenewalLease
	err      error
	retained *contract.KVHTTPSessionRenewalSnapshot
}
type httpSessionRenewalOperation struct {
	kind     string
	cancel   context.CancelFunc
	canceled bool
	out      <-chan httpSessionRenewalOutcome
	finished <-chan struct{}
}

func (d *Document) httpSessionRenewalState() *httpSessionRenewalState {
	if d.httpSessionRenewal == nil {
		d.httpSessionRenewal = &httpSessionRenewalState{selected: -1}
	}
	return d.httpSessionRenewal
}
func (d *Document) httpSessionRenewalBusy() bool {
	w := d.httpSessionRenewal
	return w != nil && (w.job != nil || w.run != nil && !w.status.Ended)
}
func (d *Document) httpSessionRenewalRevision() uint64 {
	if d.httpSessionRenewal == nil {
		return 0
	}
	return d.httpSessionRenewal.revision
}
func (s *State) httpSessionRenewalLeaseFor(d *Document) bool {
	if !s.active(d) || d.Busy() {
		return false
	}
	w := d.httpSessionRenewalState()
	if w.needsRecovery {
		return false
	}
	if w.lease == nil {
		if s.deps.HTTPSessionRenewalProviderFor == nil {
			return false
		}
		p, err := s.deps.HTTPSessionRenewalProviderFor(d.profileID)
		if err != nil || p == nil || !sameEventScope(p.KVScope(), d.scope) {
			w.needsRecovery = true
			d.error = locales.Text("KV 原连接不可用")
			return false
		}
		l, err := p.ReserveKVHTTPSessionRenewal(d.id)
		if err != nil || l == nil {
			if err != nil {
				d.error = err.Error()
			}
			return false
		}
		if !sameEventScope(l.Scope(), d.scope) || l.Scope().Namespace.ProfileID != d.profileID || l.Scope().Generation == 0 {
			_ = l.Close()
			w.needsRecovery = true
			d.error = contract.ErrRevoked.Error()
			return false
		}
		w.lease, w.scope = l, l.Scope().Clone()
	}
	if err := w.lease.CheckKVLease(); err != nil {
		w.needsRecovery = true
		d.error = err.Error()
		return false
	}
	return true
}

// Workers publish handles only. They never Poll the original private buffer
// or read a document, profile, editor, selected row or UI preview.
func (s *State) httpSessionRenewalJob(d *Document, kind string, work func(context.Context) httpSessionRenewalOutcome) {
	w := d.httpSessionRenewalState()
	ctx, cancel := context.WithCancel(d.ctx)
	out, finished := make(chan httpSessionRenewalOutcome, 1), make(chan struct{})
	w.job = &httpSessionRenewalOperation{kind: kind, cancel: cancel, out: out, finished: finished}
	d.error, d.permit = "", nil
	invalidate := s.Invalidate
	go func() {
		defer close(finished)
		r := work(ctx)
		out <- r
		if invalidate != nil {
			invalidate()
		}
	}()
}

func (s *State) PrepareHTTPSessionRenewal(d *Document) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.method != HTTPSessionRenewalMethod {
		return
	}
	if d.hasUnknownWrites() {
		d.error = locales.Text("原续租未确认；此文档禁止继续写入")
		return
	}
	r, err := ParseHTTPSessionRenewalRequest(d.Request().JSON)
	if err != nil {
		d.error = locales.Text("KV 请求无效") + ": " + err.Error()
		return
	}
	if !s.httpSessionRenewalLeaseFor(d) {
		return
	}
	w := d.httpSessionRenewal
	if w.run != nil || w.retained != nil {
		d.error = locales.Text("请先复制或导出，再明确清除原续租记录")
		return
	}
	p, err := w.lease.Prepare(r)
	if err != nil || p == nil || p.Snapshot() != r {
		d.error = errors.Join(err, contract.ErrKVPlanUsed).Error()
		return
	}
	d.clearConfirmation()
	d.permit = nil
	w.confirmation = &httpSessionRenewalConfirmation{lease: w.lease, plan: p, scope: w.scope.Clone(), authored: d.Request(), request: r, open: true}
}
func (s *State) ConfirmHTTPSessionRenewal(d *Document, checked bool) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.method != HTTPSessionRenewalMethod || d.httpSessionRenewal == nil || !checked {
		return
	}
	w := d.httpSessionRenewal
	confirm := w.confirmation
	if confirm == nil {
		return
	}
	if !s.httpSessionRenewalLeaseFor(d) {
		w.confirmation = nil
		return
	}
	r, err := ParseHTTPSessionRenewalRequest(d.Request().JSON)
	if err != nil || confirm.authored != d.Request() || confirm.lease != w.lease || !sameEventScope(confirm.scope, w.scope) || confirm.request != r || confirm.plan.Snapshot() != confirm.request || d.hasUnknownWrites() || w.run != nil || w.retained != nil {
		w.confirmation = nil
		d.error = locales.Text("KV 请求已更改，请重新预览")
		return
	}
	w.confirmation = nil
	w.stopping = false
	w.authored, w.request, w.evidenceScope = confirm.authored, r, confirm.scope.Clone()
	w.status, w.preview, w.selected = contract.KVHTTPSessionRenewalStatus{}, "", -1
	w.revision++
	d.clearConfirmation()
	d.selectedHTTPSessionRenewal = true
	d.selectedHTTPWatch = false
	lease, plan := confirm.lease, confirm.plan
	s.httpSessionRenewalJob(d, "start", func(ctx context.Context) httpSessionRenewalOutcome {
		run, err := lease.Start(ctx, plan)
		if run == nil && err == nil {
			err = contract.ErrUnsupported
		}
		if run != nil && run.Request() != r {
			err = errors.Join(err, contract.ErrRevoked, run.Close())
			run = nil
		}
		return httpSessionRenewalOutcome{run: run, err: err}
	})
}

func (s *State) StopHTTPSessionRenewal(d *Document) {
	if !s.owned(d) || d.httpSessionRenewal == nil {
		return
	}
	w := d.httpSessionRenewal
	if w.job != nil {
		w.stopping = true
		w.job.canceled = true
		w.job.cancel()
		return
	}
	if w.run == nil || w.status.Ended {
		return
	}
	w.stopping = true
	run := w.run
	if w.cancel != nil {
		w.cancel()
	}
	s.httpSessionRenewalJob(d, "stop", func(context.Context) httpSessionRenewalOutcome {
		return httpSessionRenewalOutcome{err: run.Stop(context.Background())}
	})
	w.job.canceled = true
}

func (s *State) ClearHTTPSessionRenewal(d *Document) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.hasUnknownWrites() || !s.httpSessionRenewalLeaseFor(d) {
		return
	}
	w := d.httpSessionRenewal
	if w.run == nil && w.retained == nil || !w.status.Ended {
		return
	}
	if err := w.lease.ClearReceipts(); err != nil {
		d.error = err.Error()
		return
	}
	w.run, w.preview, w.selected = nil, "", -1
	w.retained = nil
	w.status = contract.KVHTTPSessionRenewalStatus{}
	w.revision++
	d.permit = nil
}

func (s *State) RecoverHTTPSessionRenewal(d *Document) {
	if !s.active(d) || d.Busy() || s.deps.HTTPSessionRenewalProviderFor == nil {
		return
	}
	w := d.httpSessionRenewalState()
	if !w.needsRecovery {
		return
	}
	p, err := s.deps.HTTPSessionRenewalProviderFor(d.profileID)
	if err != nil || p == nil {
		d.error = locales.Text("KV 原连接不可用")
		return
	}
	expected := p.KVScope().Clone()
	if expected.Namespace.ProfileID != d.profileID || expected.Generation == 0 || !sameKVRecoveryIdentity(httpSessionRenewalScope(d), expected) {
		d.error = contract.ErrRevoked.Error()
		return
	}
	old, id := w.lease, d.id
	s.httpSessionRenewalJob(d, "recover", func(ctx context.Context) httpSessionRenewalOutcome {
		if err := ctx.Err(); err != nil {
			return httpSessionRenewalOutcome{err: err}
		}
		var r contract.KVHTTPSessionRenewalRecovery
		var err error
		if old == nil {
			r.Lease, err = p.ReserveKVHTTPSessionRenewal(id)
		} else {
			r, err = p.RecoverKVHTTPSessionRenewal(old)
		}
		if r.Lease != nil && !sameEventScope(r.Lease.Scope(), expected) {
			err = errors.Join(err, ctx.Err(), contract.ErrRevoked, r.Lease.Close())
			r.Lease = nil
		}
		if err != nil {
			if r.Lease != nil {
				_ = r.Lease.Close()
			}
			return httpSessionRenewalOutcome{err: err, retained: r.Renewal}
		}
		if r.Lease == nil {
			return httpSessionRenewalOutcome{err: contract.ErrUnsupported, retained: r.Renewal}
		}
		var archive contract.KVHTTPSessionRenewalRun
		if a, ok := r.Lease.(contract.KVHTTPSessionRenewalArchiveLease); ok {
			archive = a.RetainedRenewal()
		}
		if r.Renewal != nil && archive == nil {
			// Never silently lose transferred original evidence from a provider
			// that cannot expose its archive to this presentation contract.
			return httpSessionRenewalOutcome{lease: r.Lease, retained: r.Renewal, err: ctx.Err()}
		}
		return httpSessionRenewalOutcome{lease: r.Lease, run: archive, err: ctx.Err()}
	})
}
func httpSessionRenewalScope(d *Document) contract.KVScope {
	if d.httpSessionRenewal != nil && d.httpSessionRenewal.scope.Generation != 0 {
		return d.httpSessionRenewal.scope.Clone()
	}
	return d.scope.Clone()
}

func (s *State) consumeHTTPSessionRenewal(d *Document) {
	w := d.httpSessionRenewal
	if w == nil {
		return
	}
	if op := w.job; op != nil {
		select {
		case r := <-op.out:
			w.job = nil
			if op.kind == "recover" && r.retained != nil {
				snapshot := r.retained.Clone()
				w.retained = &snapshot
				w.run = nil
				w.selected = -1
				w.status = contract.KVHTTPSessionRenewalStatus{Observations: len(snapshot.Observations), Ended: snapshot.Ended, TerminalOverflow: snapshot.TerminalOverflow, RetainedBytes: snapshot.RetainedBytes, Terminal: snapshot.Terminal}
				w.unknown = w.unknown || snapshot.Terminal.WriteOutcomeUnknown
				w.revision++
				d.permit = nil
			}

			if r.err != nil {
				d.error = r.err.Error()
			}
			if op.kind == "start" {
				w.run, w.cancel = r.run, op.cancel
				if r.err != nil || op.canceled {
					op.cancel()
				}
				if r.run == nil {
					op.cancel()
				}
			} else {
				op.cancel()
				if op.kind == "recover" && r.lease != nil {
					w.lease, w.scope, w.run = r.lease, r.lease.Scope().Clone(), r.run
					if r.run != nil {
						w.retained = nil
					}
					w.needsRecovery = r.err != nil || op.canceled
					w.selected = -1
				}
			}
		default:
		}
	}
	if w.run != nil {
		previous := w.status
		if indexed, ok := w.run.(contract.KVHTTPSessionRenewalIndexedRun); ok {
			w.status = indexed.PollStatus()
		} else {
			b := w.run.Poll()
			w.status = contract.KVHTTPSessionRenewalStatus{Observations: previous.Observations + len(b.Observations), Ended: b.Ended,
				TerminalOverflow: b.TerminalOverflow, RetainedBytes: b.RetainedBytes, Terminal: b.Terminal}
		}
		w.unknown = w.unknown || w.status.Terminal.WriteOutcomeUnknown
		if w.status.Ended && w.cancel != nil {
			w.cancel()
			w.cancel = nil
		}
		if w.status != previous {
			w.revision++
			d.permit = nil
		}
		if w.selected < 0 && w.status.Observations > 0 {
			s.SelectHTTPSessionRenewalObservation(d, w.status.Observations-1)
		}
	}
	if w.retained != nil && w.selected < 0 && len(w.retained.Observations) > 0 {
		s.SelectHTTPSessionRenewalObservation(d, len(w.retained.Observations)-1)
	}
	if w.lease != nil && w.lease.CheckKVLease() != nil {
		w.needsRecovery = true
		if w.run != nil && !w.status.Ended && w.job == nil {
			s.StopHTTPSessionRenewal(d)
		}
	}
	if confirm := w.confirmation; confirm != nil && (confirm.authored != d.Request() || confirm.lease != w.lease || !sameEventScope(confirm.scope, w.scope) || confirm.plan.Snapshot() != confirm.request || w.needsRecovery || d.hasUnknownWrites()) {
		w.confirmation = nil
	}
	if w.job == nil && (w.run == nil || w.status.Ended) {
		w.stopping = false
	}
}

func (w *httpSessionRenewalState) snapshot() contract.KVHTTPSessionRenewalSnapshot {
	if w.run != nil {
		return w.run.Snapshot()
	}
	if w.retained != nil {
		return w.retained.Clone()
	}
	return contract.KVHTTPSessionRenewalSnapshot{}
}
func (s *State) SelectHTTPSessionRenewalObservation(d *Document, ordinal int) {
	if !s.active(d) || d.httpSessionRenewal == nil {
		return
	}
	w := d.httpSessionRenewal
	var o contract.KVHTTPSessionRenewalObservation
	var ok bool
	if indexed, yes := w.run.(contract.KVHTTPSessionRenewalIndexedRun); yes {
		o, ok = indexed.Observation(ordinal)
	} else {
		snapshot := w.snapshot()
		if ordinal >= 0 && ordinal < len(snapshot.Observations) {
			o, ok = snapshot.Observations[ordinal], true
		}
	}
	if !ok {
		return
	}
	w.selected, w.incomplete = ordinal, !o.ProjectionComplete
	w.unknown = w.unknown || o.HTTP.WriteOutcomeUnknown
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s · HTTP %d\n%s\n", o.HTTP.Request.Method, o.HTTP.Request.Path, o.HTTP.Status, o.ReceivedAt.Format("2006-01-02T15:04:05.999Z07:00"))
	if o.ProjectionComplete {
		v := o.Session
		fmt.Fprintf(&b, "UUID %s\nTTL %s · LockDelay %d · Behavior %s\nNode %q · Name %q\n", v.ID, v.TTL, v.LockDelay, v.Behavior, clipped(v.Node), clipped(v.Name))
	}
	if o.ProjectionError != "" {
		fmt.Fprintln(&b, clipped(o.ProjectionError))
	}
	b.WriteString(httpReceiptPreview(&o.HTTP))
	w.preview = clipped(b.String())
}

func (s *State) closeHTTPSessionRenewal(d *Document) {
	w := d.httpSessionRenewal
	if w == nil {
		return
	}
	if w.cancel != nil {
		w.cancel()
	}
	if w.job != nil {
		w.job.cancel()
	}
	if w.lease != nil {
		_ = w.lease.Close()
	}
	if w.run != nil {
		_ = w.run.Close()
	}
	if w.job != nil {
		<-w.job.finished
		r := <-w.job.out
		if r.run != nil {
			_ = r.run.Close()
		}
		if r.lease != nil {
			_ = r.lease.Close()
		}
	}
	d.httpSessionRenewal = nil
}

func (d *Document) httpSessionRenewalContent() bool {
	return d.httpSessionRenewal != nil && (d.httpSessionRenewal.run != nil || d.httpSessionRenewal.retained != nil)
}
func (d *Document) HTTPSessionRenewalConfirmation() (contract.KVRequest, bool) {
	if d.httpSessionRenewal == nil || d.httpSessionRenewal.confirmation == nil {
		return contract.KVRequest{}, false
	}
	return d.httpSessionRenewal.confirmation.authored, true
}
