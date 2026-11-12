package kvworkspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

type httpWatchState struct {
	lease                   contract.KVHTTPWatchLease
	scope, evidenceScope    contract.KVScope
	authored                contract.KVRequest
	request                 contract.KVHTTPWatchRequest
	run                     contract.KVHTTPWatchRun
	status                  contract.KVHTTPWatchStatus
	job                     *httpWatchOperation
	cancel                  context.CancelFunc
	needsRecovery           bool
	selected                int
	preview                 string
	indexRelation, index    string
	aclFiltered, incomplete bool
	revision                uint64
}

type httpWatchOutcome struct {
	run   contract.KVHTTPWatchRun
	lease contract.KVHTTPWatchLease
	err   error
}
type httpWatchOperation struct {
	kind     string
	cancel   context.CancelFunc
	canceled bool
	out      <-chan httpWatchOutcome
	finished <-chan struct{}
}

func (d *Document) httpWatchState() *httpWatchState {
	if d.httpWatch == nil {
		d.httpWatch = &httpWatchState{selected: -1}
	}
	return d.httpWatch
}
func (d *Document) httpWatchBusy() bool {
	w := d.httpWatch
	return w != nil && (w.job != nil || w.run != nil && !w.status.Ended)
}
func (d *Document) httpWatchRevision() uint64 {
	if d.httpWatch == nil {
		return 0
	}
	return d.httpWatch.revision
}
func (s *State) httpWatchLeaseFor(d *Document) bool {
	if !s.active(d) || d.Busy() {
		return false
	}
	w := d.httpWatchState()
	if w.needsRecovery {
		return false
	}
	if w.lease == nil {
		if s.deps.HTTPWatchProviderFor == nil {
			return false
		}
		p, err := s.deps.HTTPWatchProviderFor(d.profileID)
		if err != nil || p == nil || !sameEventScope(p.KVScope(), d.scope) {
			w.needsRecovery = true
			d.error = locales.Text("KV 原连接不可用")
			return false
		}
		l, err := p.ReserveKVHTTPWatch(d.id)
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
func (s *State) httpWatchJob(d *Document, kind string, work func(context.Context) httpWatchOutcome) {
	w := d.httpWatchState()
	ctx, cancel := context.WithCancel(d.ctx)
	out, finished := make(chan httpWatchOutcome, 1), make(chan struct{})
	w.job = &httpWatchOperation{kind: kind, cancel: cancel, out: out, finished: finished}
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

func (s *State) StartHTTPWatch(d *Document) {
	if !s.active(d) || d.Busy() || d.method != HTTPWatchMethod {
		return
	}
	r, err := ParseHTTPWatchRequest(d.Request().JSON)
	if err != nil {
		d.error = locales.Text("KV 请求无效") + ": " + err.Error()
		return
	}
	if !s.httpWatchLeaseFor(d) {
		return
	}
	w := d.httpWatch
	if w.run != nil {
		d.error = locales.Text("请先复制或导出，再明确清除原 Watch 观察")
		return
	}
	p, err := w.lease.Prepare(r)
	if err != nil || p == nil || p.Snapshot() != r {
		d.error = errors.Join(err, contract.ErrKVPlanUsed).Error()
		return
	}
	w.authored, w.request, w.evidenceScope = d.Request(), r, w.scope.Clone()
	w.status, w.preview, w.selected = contract.KVHTTPWatchStatus{}, "", -1
	w.revision++
	d.confirmation = nil
	d.selectedHTTPWatch = true
	lease := w.lease
	s.httpWatchJob(d, "start", func(ctx context.Context) httpWatchOutcome {
		run, err := lease.Start(ctx, p)
		if run != nil && run.Request() != r {
			err = errors.Join(err, contract.ErrRevoked, run.Close())
			run = nil
		}
		return httpWatchOutcome{run: run, err: err}
	})
}

func (s *State) StopHTTPWatch(d *Document) {
	if !s.owned(d) || d.httpWatch == nil {
		return
	}
	w := d.httpWatch
	if w.job != nil {
		w.job.canceled = true
		w.job.cancel()
		return
	}
	if w.run == nil || w.status.Ended {
		return
	}
	run := w.run
	if w.cancel != nil {
		w.cancel()
	}
	s.httpWatchJob(d, "stop", func(context.Context) httpWatchOutcome {
		return httpWatchOutcome{err: run.Stop(context.Background())}
	})
	w.job.canceled = true
}

func (s *State) ClearHTTPWatch(d *Document) {
	if !s.active(d) || d.Busy() || d.FileBusy() || !s.httpWatchLeaseFor(d) {
		return
	}
	w := d.httpWatch
	if w.run == nil || !w.status.Ended {
		return
	}
	if err := w.lease.ClearObservations(); err != nil {
		d.error = err.Error()
		return
	}
	w.run, w.preview, w.selected = nil, "", -1
	w.status = contract.KVHTTPWatchStatus{}
	w.revision++
	d.permit = nil
}

func (s *State) RecoverHTTPWatch(d *Document) {
	if !s.active(d) || d.Busy() || s.deps.HTTPWatchProviderFor == nil {
		return
	}
	w := d.httpWatchState()
	if !w.needsRecovery {
		return
	}
	p, err := s.deps.HTTPWatchProviderFor(d.profileID)
	if err != nil || p == nil {
		d.error = locales.Text("KV 原连接不可用")
		return
	}
	expected := p.KVScope().Clone()
	if expected.Namespace.ProfileID != d.profileID || expected.Generation == 0 || !sameKVRecoveryIdentity(watchScope(d), expected) {
		d.error = contract.ErrRevoked.Error()
		return
	}
	old, id := w.lease, d.id
	s.httpWatchJob(d, "recover", func(ctx context.Context) httpWatchOutcome {
		if err := ctx.Err(); err != nil {
			return httpWatchOutcome{err: err}
		}
		var r contract.KVHTTPWatchRecovery
		var err error
		if old == nil {
			r.Lease, err = p.ReserveKVHTTPWatch(id)
		} else {
			r, err = p.RecoverKVHTTPWatch(old)
		}
		if r.Lease != nil && !sameEventScope(r.Lease.Scope(), expected) {
			err = errors.Join(err, ctx.Err(), contract.ErrRevoked, r.Lease.Close())
			r.Lease = nil
		}
		if err != nil {
			if r.Lease != nil {
				_ = r.Lease.Close()
			}
			return httpWatchOutcome{err: err}
		}
		if r.Lease == nil {
			return httpWatchOutcome{err: contract.ErrUnsupported}
		}
		var archive contract.KVHTTPWatchRun
		if a, ok := r.Lease.(contract.KVHTTPWatchArchiveLease); ok {
			archive = a.RetainedWatch()
		}
		if r.Watch != nil && archive == nil {
			// Never silently lose transferred original evidence from a provider
			// that cannot expose its archive to this presentation contract.
			return httpWatchOutcome{lease: r.Lease, err: contract.ErrUnsupported}
		}
		return httpWatchOutcome{lease: r.Lease, run: archive, err: ctx.Err()}
	})
}
func watchScope(d *Document) contract.KVScope {
	if d.httpWatch != nil && d.httpWatch.scope.Generation != 0 {
		return d.httpWatch.scope.Clone()
	}
	return d.scope.Clone()
}

func (s *State) consumeHTTPWatch(d *Document) {
	w := d.httpWatch
	if w == nil {
		return
	}
	if op := w.job; op != nil {
		select {
		case r := <-op.out:
			w.job = nil
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
					w.needsRecovery = r.err != nil || op.canceled
					w.selected = -1
				}
			}
		default:
		}
	}
	if w.run != nil {
		previous := w.status
		if indexed, ok := w.run.(contract.KVHTTPWatchIndexedRun); ok {
			w.status = indexed.PollStatus()
		} else {
			b := w.run.Poll()
			w.status = contract.KVHTTPWatchStatus{Observations: previous.Observations + len(b.Observations), Ended: b.Ended,
				TerminalOverflow: b.TerminalOverflow, RetainedBytes: b.RetainedBytes, Terminal: b.Terminal}
		}
		if w.status.Ended && w.cancel != nil {
			w.cancel()
			w.cancel = nil
		}
		if w.status != previous {
			w.revision++
			d.permit = nil
		}
		if w.selected < 0 && w.status.Observations > 0 {
			s.SelectHTTPWatchObservation(d, w.status.Observations-1)
		}
	}
	if w.lease != nil && w.lease.CheckKVLease() != nil {
		w.needsRecovery = true
		if w.run != nil && !w.status.Ended && w.job == nil {
			s.StopHTTPWatch(d)
		}
	}
}

func (s *State) SelectHTTPWatchObservation(d *Document, ordinal int) {
	if !s.active(d) || d.httpWatch == nil || d.httpWatch.run == nil {
		return
	}
	w := d.httpWatch
	var o contract.KVHTTPWatchObservation
	var ok bool
	if indexed, yes := w.run.(contract.KVHTTPWatchIndexedRun); yes {
		o, ok = indexed.Observation(ordinal)
	} else {
		r := w.run.Snapshot()
		if ordinal >= 0 && ordinal < len(r.Observations) {
			o, ok = r.Observations[ordinal], true
		}
	}
	if !ok {
		return
	}
	w.selected, w.indexRelation, w.index = ordinal, o.IndexRelation, "—"
	if o.HTTP.Metadata.Index != nil {
		w.index = strconv.FormatUint(*o.HTTP.Metadata.Index, 10)
	}
	w.aclFiltered = o.HTTP.Metadata.ACLFiltered != nil && *o.HTTP.Metadata.ACLFiltered
	w.incomplete = !o.ProjectionComplete
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP %d · index %s (%s)\n%s\n", o.HTTP.Status, w.index, o.IndexRelation, o.ReceivedAt.Format("2006-01-02T15:04:05.999Z07:00"))
	fmt.Fprintf(&b, "matched %d / observed %d\n", len(o.Matches), len(o.HTTP.Entries))
	if ordinal > 0 && o.ProjectionComplete && !w.aclFiltered {
		if indexed, yes := w.run.(contract.KVHTTPWatchIndexedRun); yes {
			previous, found := indexed.Observation(ordinal - 1)
			if found && previous.ProjectionComplete && (previous.HTTP.Metadata.ACLFiltered == nil || !*previous.HTTP.Metadata.ACLFiltered) {
				before := make(map[string]contract.KVHTTPEntry, len(previous.Matches))
				for _, i := range previous.Matches {
					if i >= 0 && i < len(previous.HTTP.Entries) {
						e := previous.HTTP.Entries[i]
						before[e.Key] = e
					}
				}
				added, changed := 0, 0
				for _, i := range o.Matches {
					if i < 0 || i >= len(o.HTTP.Entries) {
						continue
					}
					e := o.HTTP.Entries[i]
					if old, found := before[e.Key]; !found {
						added++
					} else if !sameHTTPWatchEntry(old, e) {
						changed++
					}
					delete(before, e.Key)
				}
				fmt.Fprintf(&b, locales.Text("相邻观察差异：新增 %d · 变化 %d · 本次未见 %d；不代表期间全部事件")+"\n", added, changed, len(before))
			}
		}
	}
	for _, i := range o.Matches {
		if i < 0 || i >= len(o.HTTP.Entries) {
			continue
		}
		e := o.HTTP.Entries[i]
		fmt.Fprintf(&b, "%q · Flags %d · %d bytes\n", clipped(e.Key), e.Flags, len(e.Value))
		if b.Len() >= previewLimit {
			break
		}
	}
	b.WriteString(httpReceiptPreview(&o.HTTP))
	w.preview = clipped(b.String())
}

func sameHTTPWatchEntry(a, b contract.KVHTTPEntry) bool {
	return bytes.Equal(a.Value, b.Value) && a.ValuePresent == b.ValuePresent && a.ValueNull == b.ValueNull &&
		a.Flags == b.Flags && a.CreateIndex == b.CreateIndex && a.ModifyIndex == b.ModifyIndex && a.LockIndex == b.LockIndex &&
		a.Session == b.Session && a.SessionNull == b.SessionNull
}

func (s *State) closeHTTPWatch(d *Document) {
	w := d.httpWatch
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
	d.httpWatch = nil
}
