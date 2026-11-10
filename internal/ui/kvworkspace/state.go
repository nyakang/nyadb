// Package kvworkspace owns native KV request documents, original receipts,
// confirmation, cancellation and recovery. Hosts inject feature providers and
// own profile activation, workbench tabs and native file pickers.
package kvworkspace

import (
	"context"
	"fmt"
	"sort"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

const previewLimit = 64 << 10
const inputLimit = 16 << 20

type Dependencies struct {
	ProviderFor       func(int64) (contract.KVRequestsProvider, error)
	EventsProviderFor func(int64) (contract.KVEventsProvider, error)
	ProfileName       func(int64) string
	Active            func(*Document) bool
	Activate          func(*Document)
	ConnectProfile    func(ui.Services, int64)
	EditFocus         *string
	EditTarget        func(theme.EditTarget)
	Status            func(string)
	Files             Files
}
type Receipt struct {
	Scope    contract.KVScope
	Authored contract.KVRequest
	Result   contract.KVResult
	Error    string
}

func (r Receipt) Clone() Receipt { r.Scope = r.Scope.Clone(); r.Result = r.Result.Clone(); return r }

type confirmation struct {
	lease    contract.KVRequestLease
	plan     contract.KVPreparation
	authored contract.KVRequest
	checked  bool
	open     bool
}
type outcome struct {
	result contract.KVResult
	lease  contract.KVRequestLease
	err    error
}
type operation struct {
	kind     string
	scope    contract.KVScope
	authored contract.KVRequest
	cancel   context.CancelFunc
	canceled bool
	out      <-chan outcome
	finished <-chan struct{}
}

// Document is UI-thread owned. Workers capture immutable requests and their
// original lease; private buffered channels are consumed only by State.Consume.
type Document struct {
	id                           uint64
	profileID                    int64
	method                       string
	Editor                       *editor.State
	saved                        contract.KVRequest
	path                         string
	scope                        contract.KVScope
	lease                        contract.KVRequestLease
	job                          *operation
	io                           *fileOperation
	confirmation                 *confirmation
	last, unknown                *Receipt
	selectedUnknown              bool
	error                        string
	needsRecovery, closed, focus bool
	ctx                          context.Context
	cancel                       context.CancelFunc
	permit                       *leaveStamp
	eventLease                   contract.KVEventsLease
	eventScope                   contract.KVScope
	eventJob                     *eventOperation
	eventReceipt                 *EventReceipt
	eventNeedsRecovery           bool
}
type leaveStamp struct {
	request       contract.KVRequest
	last, unknown *Receipt
	events        *EventReceipt
}

func (d *Document) ID() uint64       { return d.id }
func (d *Document) ProfileID() int64 { return d.profileID }
func (d *Document) Scope() contract.KVScope {
	if eventMethod(d.method) && d.eventScope.Generation != 0 {
		return d.eventScope.Clone()
	}
	return d.scope.Clone()
}
func (d *Document) Request() contract.KVRequest {
	if d.Editor == nil {
		return contract.KVRequest{}
	}
	return contract.KVRequest{Method: d.method, JSON: d.Editor.Buffer.String()}
}
func (d *Document) Dirty() bool    { return !d.closed && d.Request() != d.saved }
func (d *Document) Busy() bool     { return d.job != nil || d.eventJob != nil }
func (d *Document) FileBusy() bool { return d.io != nil }
func (d *Document) CancelRequested() bool {
	return d.job != nil && d.job.canceled || d.eventJob != nil && d.eventJob.canceled
}
func (d *Document) NeedsRecovery() bool {
	if eventMethod(d.method) {
		return d.eventNeedsRecovery
	}
	return d.needsRecovery
}
func (d *Document) Error() string { return d.error }
func (d *Document) LastReceipt() (Receipt, bool) {
	if d.last == nil {
		return Receipt{}, false
	}
	return d.last.Clone(), true
}
func (d *Document) UnknownReceipt() (Receipt, bool) {
	if d.unknown == nil {
		return Receipt{}, false
	}
	return d.unknown.Clone(), true
}
func (d *Document) Confirmation() (contract.KVRequest, bool) {
	if d.confirmation == nil {
		return contract.KVRequest{}, false
	}
	return d.confirmation.plan.Snapshot(), true
}
func (d *Document) Name() string { return fmt.Sprintf("KV %d", d.id) }

type leaveDecision struct {
	doc  *Document
	next func(ui.Services)
}
type State struct {
	deps         Dependencies
	documents    map[uint64]*Document
	leave        *leaveDecision
	SplitPercent float32
	Invalidate   func()
}

func New(deps Dependencies) *State {
	return &State{deps: deps, documents: map[uint64]*Document{}, SplitPercent: 50}
}
func (s *State) owned(d *Document) bool { return d != nil && !d.closed && s.documents[d.id] == d }
func (s *State) active(d *Document) bool {
	return s.owned(d) && (s.deps.Active == nil || s.deps.Active(d))
}
func (s *State) Open(id uint64, profile int64, request contract.KVRequest) *Document {
	if id == 0 || profile <= 0 {
		return nil
	}
	if old := s.documents[id]; old != nil {
		s.Close(old)
	}
	if request.Method == "" {
		request = DefaultRequest()
	}
	ctx, cancel := context.WithCancel(context.Background())
	buffer := editor.NewBuffer(request.JSON)
	buffer.SetLanguage(editor.LanguageNamed("JSON"))
	d := &Document{id: id, profileID: profile, method: request.Method, saved: request, Editor: &editor.State{Buffer: buffer}, ctx: ctx, cancel: cancel, focus: true}
	s.documents[id] = d
	if s.deps.ProviderFor == nil {
		d.needsRecovery = true
		d.error = locales.Text("KV 原连接不可用")
		return d
	}
	p, err := s.deps.ProviderFor(profile)
	if err == nil && p != nil {
		d.lease, err = p.ReserveKV(id)
	}
	if err != nil || d.lease == nil {
		d.needsRecovery = true
		d.error = locales.Text("KV 原连接不可用")
		return d
	}
	d.scope = d.lease.Scope().Clone()
	if d.scope.Namespace.ProfileID != profile || d.scope.Generation == 0 {
		_ = d.lease.Close()
		d.lease = nil
		d.needsRecovery = true
		d.error = locales.Text("KV 原连接不可用")
	}
	return d
}
func (s *State) SetMethod(d *Document, method string) {
	if s.owned(d) && !d.Busy() && d.method != method {
		d.method = method
		d.confirmation = nil
		d.permit = nil
	}
}
func (s *State) Focus(d *Document) {
	if s.owned(d) {
		d.focus = true
	}
}
func (s *State) check(d *Document) bool {
	if !s.active(d) || d.Busy() || d.needsRecovery || d.lease == nil {
		return false
	}
	if err := d.lease.CheckKVLease(); err != nil {
		d.needsRecovery = true
		d.confirmation = nil
		d.error = locales.Text("KV 原连接不可用")
		return false
	}
	return true
}
func (s *State) start(d *Document, kind string, authored contract.KVRequest, work func(context.Context) outcome) {
	ctx, cancel := context.WithCancel(d.ctx)
	out := make(chan outcome, 1)
	finished := make(chan struct{})
	d.job = &operation{kind: kind, scope: d.scope.Clone(), authored: authored, cancel: cancel, out: out, finished: finished}
	d.error = ""
	d.permit = nil
	invalidate := s.Invalidate
	go func() {
		defer close(finished)
		defer cancel()
		r := work(ctx)
		r.result = r.result.Clone()
		if ctx.Err() != nil && r.lease != nil {
			_ = r.lease.Close()
			r.lease = nil
			if r.err == nil {
				r.err = ctx.Err()
			}
		}
		out <- r
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) Preconnect(d *Document) {
	if s.owned(d) && eventMethod(d.method) {
		s.PreconnectEvents(d)
		return
	}
	if !s.check(d) {
		return
	}
	lease := d.lease
	s.start(d, "connect", contract.KVRequest{}, func(ctx context.Context) outcome { return outcome{err: lease.Preconnect(ctx)} })
}

// Run prepares on the UI thread. Writes require a separate explicit confirmation
// of the same authored text, canonical snapshot and original lease.
func (s *State) Run(d *Document) {
	if s.owned(d) && eventMethod(d.method) {
		s.RunEvents(d)
		return
	}
	if !s.check(d) {
		return
	}
	authored := d.Request()
	if len(authored.JSON) > inputLimit {
		d.error = locales.Text("KV 请求文本超过 16 MiB")
		return
	}
	plan, err := d.lease.Prepare(authored)
	if err != nil {
		d.error = locales.Text("KV 请求无效") + ": " + err.Error()
		return
	}
	if plan == nil {
		d.error = locales.Text("KV 请求无效")
		return
	}
	if plan.Writes() {
		if d.unknown != nil {
			d.error = locales.Text("原写入未确认；恢复后可读取，关闭页签可放弃本地草稿与回执")
			return
		}
		d.confirmation = &confirmation{lease: d.lease, plan: plan, authored: authored, open: true}
		return
	}
	s.execute(d, plan, authored)
}
func (s *State) execute(d *Document, plan contract.KVPreparation, authored contract.KVRequest) {
	lease := d.lease
	d.confirmation = nil
	s.start(d, "execute", authored, func(ctx context.Context) outcome {
		r, err := lease.Execute(ctx, plan)
		return outcome{result: r, err: err}
	})
}
func (s *State) Confirm(d *Document, checked bool) {
	if !s.owned(d) || d.confirmation == nil {
		return
	}
	p := d.confirmation
	if !checked || !s.check(d) {
		return
	}
	if p.lease != d.lease || p.authored != d.Request() || d.unknown != nil {
		d.confirmation = nil
		d.error = locales.Text("KV 请求已更改，请重新预览")
		return
	}
	s.execute(d, p.plan, p.authored)
}
func (s *State) CancelConfirmation(d *Document) {
	if s.owned(d) {
		d.confirmation = nil
	}
}
func (s *State) RequestCancel(d *Document) {
	if s.owned(d) && d.eventJob != nil && !d.eventJob.canceled {
		d.eventJob.canceled = true
		d.eventJob.cancel()
	}
	if s.owned(d) && d.job != nil && !d.job.canceled {
		d.job.canceled = true
		d.job.cancel()
	}
}
func (s *State) Recover(d *Document) {
	if s.owned(d) && eventMethod(d.method) {
		s.RecoverEvents(d)
		return
	}
	if !s.active(d) || d.Busy() || !d.needsRecovery || s.deps.ProviderFor == nil {
		return
	}
	p, err := s.deps.ProviderFor(d.profileID)
	if err != nil || p == nil {
		d.error = locales.Text("KV 原连接不可用")
		return
	}
	previous := d.lease
	id := d.id
	s.start(d, "recover", contract.KVRequest{}, func(ctx context.Context) outcome {
		if err := ctx.Err(); err != nil {
			return outcome{err: err}
		}
		if previous == nil {
			l, err := p.ReserveKV(id)
			return outcome{lease: l, err: err}
		}
		r, err := p.RecoverKV(previous)
		return outcome{lease: r.Lease, err: err}
	})
}

// Consume admits only the active original document. Parked completions keep
// their own channel and cannot enter another profile/document with the same ID.
func (s *State) Consume() {
	for _, d := range s.documents {
		if !s.active(d) {
			continue
		}
		s.consumeEvents(d)
		if d.confirmation != nil && d.confirmation.authored != d.Request() {
			d.confirmation = nil
		}
		if d.job != nil {
			select {
			case r := <-d.job.out:
				job := d.job
				d.job = nil
				job.cancel()
				if r.err != nil {
					d.error = r.err.Error()
				}
				if job.kind == "execute" {
					receipt := &Receipt{Scope: job.scope.Clone(), Authored: job.authored, Result: r.result.Clone()}
					if r.err != nil {
						receipt.Error = r.err.Error()
					}
					d.last = receipt
					if r.result.WriteOutcomeUnknown {
						d.unknown = receipt
					}
				}
				if job.kind == "recover" && r.err == nil && r.lease != nil {
					scope := r.lease.Scope().Clone()
					if scope.Namespace.ProfileID != d.profileID || scope.Generation == 0 {
						_ = r.lease.Close()
						d.error = locales.Text("KV 原连接不可用")
					} else {
						d.lease = r.lease
						d.scope = scope
						d.needsRecovery = false
						d.confirmation = nil
					}
				}
				if d.lease == nil || d.lease.CheckKVLease() != nil {
					d.needsRecovery = true
					d.confirmation = nil
				}
				if s.deps.Status != nil && job.kind == "execute" {
					s.deps.Status(ReceiptStatus(r.result))
				}
			default:
			}
		}
		s.consumeFiles(d)
		if d.job == nil && d.lease != nil && d.lease.CheckKVLease() != nil {
			d.needsRecovery = true
			d.confirmation = nil
		}
	}
}
func (s *State) DisconnectProfile(id int64) {
	for _, d := range s.documents {
		if d.profileID == id {
			d.needsRecovery = true
			d.eventNeedsRecovery = true
			d.confirmation = nil
			s.RequestCancel(d)
		}
	}
}
func (s *State) GuardLeave(d *Document, next func(ui.Services)) bool {
	if !s.owned(d) {
		return false
	}
	if d.permit != nil && d.permit.request == d.Request() && d.permit.last == d.last && d.permit.unknown == d.unknown && d.permit.events == d.eventReceipt && !d.Busy() && !d.FileBusy() {
		return false
	}
	if !d.Dirty() && d.unknown == nil && !d.Busy() && !d.FileBusy() {
		return false
	}
	s.leave = &leaveDecision{doc: d, next: next}
	if s.deps.Activate != nil {
		s.deps.Activate(d)
	}
	return true
}
func (s *State) DecideLeave(services ui.Services, discard bool) {
	if s.leave == nil {
		return
	}
	d := s.leave.doc
	if !discard {
		s.leave = nil
		return
	}
	if d.Busy() || d.FileBusy() {
		return
	}
	next := s.leave.next
	s.leave = nil
	d.permit = &leaveStamp{request: d.Request(), last: d.last, unknown: d.unknown, events: d.eventReceipt}
	if next != nil {
		next(services)
	}
}
func (s *State) GuardWorkspaceClose(next func(ui.Services)) bool {
	ids := make([]uint64, 0, len(s.documents))
	for id := range s.documents {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if s.GuardLeave(s.documents[id], next) {
			return true
		}
	}
	return false
}
func (s *State) Close(d *Document) {
	if !s.owned(d) {
		return
	}
	d.closed = true
	d.cancel()
	if d.job != nil {
		d.job.cancel()
	}
	if d.eventJob != nil {
		d.eventJob.cancel()
	}
	if d.io != nil {
		d.io.cancel()
	}
	if d.lease != nil {
		_ = d.lease.Close()
	}
	if d.eventLease != nil {
		_ = d.eventLease.Close()
	}
	if d.eventJob != nil {
		<-d.eventJob.finished
		select {
		case r := <-d.eventJob.out:
			if r.lease != nil {
				_ = r.lease.Close()
			}
		default:
		}
	}
	if d.job != nil {
		<-d.job.finished
		select {
		case r := <-d.job.out:
			if r.lease != nil {
				_ = r.lease.Close()
			}
		default:
		}
	}
	delete(s.documents, d.id)
	if s.leave != nil && s.leave.doc == d {
		s.leave = nil
	}
	d.Editor = nil
	d.confirmation = nil
	d.last = nil
	d.unknown = nil
	d.job = nil
	d.io = nil
	d.lease = nil
	d.permit = nil
	d.saved = contract.KVRequest{}
	d.scope = contract.KVScope{}
	d.eventLease, d.eventJob, d.eventReceipt = nil, nil, nil
	d.eventScope = contract.KVScope{}
	d.method, d.error, d.path = "", "", ""
}
func (s *State) CloseAll() {
	for _, d := range s.documents {
		s.Close(d)
	}
	s.leave = nil
	s.deps = Dependencies{}
	s.Invalidate = nil
}
func ReceiptStatus(r contract.KVResult) string {
	if r.WriteOutcomeUnknown {
		return locales.Text("KV 写入结果未确认")
	}
	if r.Completed {
		if r.Reply != nil && r.Reply.Kind == "txn" && !r.Reply.Succeeded {
			return locales.Text("KV 比较未满足")
		}
		return locales.Text("KV 原生请求已完成")
	}
	if !r.Attempted {
		return locales.Text("KV 请求未派发")
	}
	return locales.Text("KV 请求未完成")
}
