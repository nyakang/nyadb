package objectworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type settingsDocument struct {
	lease                                                                               contract.SettingsDocumentLease
	generation                                                                          uint64
	snapshot                                                                            contract.SettingsSnapshot
	receipt                                                                             contract.SettingsReceipt
	last                                                                                contract.SettingsResult
	draft, baseline, diagnostic                                                         string
	editing, preview, guard, fresh, lost, uncertain, cancelRequested, leaveAcknowledged bool
	plan                                                                                *contract.SettingsPlan
	results                                                                             <-chan settingsReply
	next                                                                                func(ui.Services)
	tab                                                                                 int
}
type settingsReply struct {
	request, generation uint64
	lease               contract.SettingsDocumentLease
	operation           string
	snapshot            contract.SettingsSnapshot
	result              contract.SettingsResult
	err                 error
}
type SettingsEvent struct {
	DocumentID, Generation uint64
	Object                 contract.ObjectRef
	State                  string
}

func settingsObject(o contract.ObjectRef) bool {
	return o.ProfileID > 0 && o.Catalog != "" && o.Schema == "" && o.Kind == "settings" && o.Name == "settings"
}
func (s *State) SettingsScopeSupported(o contract.ObjectRef) bool {
	if !settingsObject(o) || s.deps.SettingsDocumentsFor == nil {
		return false
	}
	p, err := s.deps.SettingsDocumentsFor(o.ProfileID)
	return err == nil && p != nil && p.SupportsSettingsDocument(o)
}
func (s *State) OpenSettings(id uint64, o contract.ObjectRef) *Document {
	if !s.SettingsScopeSupported(o) {
		return nil
	}
	if old := s.documents[id]; old != nil {
		s.Close(old)
	}
	d := &Document{id: id, profileID: o.ProfileID, database: o.Catalog, table: o.Name, object: o, objectKind: "索引设置", settings: &settingsDocument{}}
	s.documents[id] = d
	return d
}
func (d *Document) SettingsMode() bool  { return d != nil && !d.closed && d.settings != nil }
func (x *settingsDocument) dirty() bool { return x != nil && x.editing && x.draft != x.baseline }
func (s *State) settingsReady(d *Document) bool {
	if !d.SettingsMode() || d.loading || d.settings.lost || s.session().ProfileID != d.profileID {
		return false
	}
	w := s.sessionFor(d.profileID)
	return w.Connected && w.ProfileID == d.profileID && (d.settings.lease == nil || w.Generation == d.settings.generation)
}
func prettySettings(raw string) string {
	var v json.RawMessage
	if json.Unmarshal([]byte(raw), &v) != nil {
		return raw
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return raw
	}
	return string(b)
}
func (s *State) reserveSettings(d *Document) error {
	p, err := s.deps.SettingsDocumentsFor(d.profileID)
	if err != nil {
		return err
	}
	lease, err := p.ReserveSettingsDocument(d.object)
	if err != nil {
		return err
	}
	d.settings.lease, d.settings.generation = lease, s.sessionFor(d.profileID).Generation
	return nil
}
func (s *State) LoadSettings(d *Document, invalidate func(), refresh bool) {
	if !s.settingsReady(d) {
		return
	}
	x := d.settings
	if !refresh && (x.snapshot.Token != "" || d.err != "") {
		return
	}
	if x.lease == nil {
		if err := s.reserveSettings(d); err != nil {
			d.err = err.Error()
			return
		}
	}
	x.fresh = false
	s.runSettings(d, invalidate, "read", func(ctx context.Context, lease contract.SettingsDocumentLease) (contract.SettingsSnapshot, contract.SettingsResult, error) {
		v, e := lease.Read(ctx)
		return v, contract.SettingsResult{}, e
	})
}
func (s *State) runSettings(d *Document, invalidate func(), operation string, run func(context.Context, contract.SettingsDocumentLease) (contract.SettingsSnapshot, contract.SettingsResult, error)) {
	if !s.settingsReady(d) || d.settings.lease == nil {
		return
	}
	x := d.settings
	lease, generation := x.lease, x.generation
	d.request++
	request := d.request
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel, d.loading, d.err = cancel, true, ""
	x.cancelRequested = false
	x.leaveAcknowledged = false
	ch := make(chan settingsReply, 1)
	x.results = ch
	go func() {
		snapshot, result, err := run(ctx, lease)
		ch <- settingsReply{request: request, generation: generation, lease: lease, operation: operation, snapshot: snapshot, result: result, err: err}
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) CanPrepareSettings(d *Document) bool {
	return s.settingsReady(d) && d.settings.fresh && !d.settings.uncertain && d.settings.receipt.Raw == "" && d.settings.snapshot.Token != ""
}
func (s *State) EditSettings(d *Document) {
	if !s.CanPrepareSettings(d) {
		return
	}
	d.settings.editing = true
	d.settings.tab = 1
}
func (s *State) PrepareSettings(d *Document, reset bool) error {
	if !s.CanPrepareSettings(d) {
		return contract.ErrSettingsPlanUsed
	}
	x := d.settings
	draft := x.draft
	if reset {
		draft = ""
	}
	p, err := x.lease.Prepare(x.snapshot, draft, reset)
	if err != nil {
		d.err = err.Error()
		return err
	}
	p.Fields = slices.Clone(p.Fields)
	x.plan = &p
	x.preview = true
	d.err = ""
	return nil
}
func (s *State) ApplySettings(d *Document, invalidate func()) {
	if !s.CanPrepareSettings(d) || d.settings.plan == nil {
		return
	}
	x := d.settings
	p := *x.plan
	p.Fields = slices.Clone(p.Fields)
	if !p.Reset && x.draft != p.Draft {
		d.err = "设置草稿在预览后已改变，请重新预览"
		x.preview = false
		x.plan = nil
		return
	}
	x.preview = false
	x.plan = nil
	x.fresh = false
	s.runSettings(d, invalidate, "apply", func(ctx context.Context, lease contract.SettingsDocumentLease) (contract.SettingsSnapshot, contract.SettingsResult, error) {
		v, e := lease.Apply(ctx, p)
		return contract.SettingsSnapshot{}, v, e
	})
}
func (s *State) ObserveSettings(d *Document, invalidate func()) {
	if !s.settingsReady(d) || d.settings.receipt.Raw == "" {
		return
	}
	original := d.settings.receipt
	s.runSettings(d, invalidate, "observe", func(ctx context.Context, lease contract.SettingsDocumentLease) (contract.SettingsSnapshot, contract.SettingsResult, error) {
		v, e := lease.Observe(ctx, original, 30*time.Second)
		return contract.SettingsSnapshot{}, v, e
	})
}
func (s *State) CancelSettings(d *Document) {
	if !d.SettingsMode() || !d.loading || d.settings.cancelRequested {
		return
	}
	d.settings.cancelRequested = true
	if d.cancel != nil {
		d.cancel()
	}
}
func (s *State) RecoverSettings(d *Document) {
	if !d.SettingsMode() || d.loading || s.session().ProfileID != d.profileID || !s.sessionFor(d.profileID).Connected {
		return
	}
	x := d.settings
	if x.lease == nil {
		if err := s.reserveSettings(d); err != nil {
			d.err = err.Error()
		}
		return
	}
	p, err := s.deps.SettingsDocumentsFor(d.profileID)
	if err == nil {
		var r contract.SettingsRecovery
		r, err = p.RecoverSettingsDocument(d.object, x.lease)
		if err == nil {
			x.lease = r.Lease
			x.receipt = r.Receipt
			x.uncertain = x.uncertain || r.Uncertain
			if r.Snapshot.Token != "" {
				x.snapshot = r.Snapshot
			}
			x.generation = s.sessionFor(d.profileID).Generation
			x.lost, x.fresh = false, false
			x.plan = nil
			x.preview = false
			d.err = "设置连接已恢复；请显式读取设置或观察原任务"
			return
		}
	}
	d.err = err.Error()
	x.lost = true
}
func (s *State) SettingsSnapshot(d *Document) contract.SettingsSnapshot {
	if !d.SettingsMode() {
		return contract.SettingsSnapshot{}
	}
	return d.settings.snapshot
}
func (s *State) SettingsResult(d *Document) contract.SettingsResult {
	if !d.SettingsMode() {
		return contract.SettingsResult{}
	}
	return d.settings.last
}
func (s *State) SettingsUncertain(d *Document) bool { return d.SettingsMode() && d.settings.uncertain }
func (s *State) consumeSettings(services ui.Services) {
	active := s.session()
	for _, d := range s.documents {
		if !d.SettingsMode() || d.profileID != active.ProfileID {
			continue
		}
		x := d.settings
		current := s.sessionFor(d.profileID)
		if x.lease != nil && (!current.Connected || current.Generation != x.generation) {
			x.lost, x.fresh = true, false
			if !d.loading {
				d.err = "原设置会话已失效，请显式恢复"
			}
		}
		if x.results == nil {
			continue
		}
		select {
		case reply := <-x.results:
			if reply.request != d.request {
				continue
			}
			x.results = nil
			d.loading = false
			if d.cancel != nil {
				d.cancel()
				d.cancel = nil
			}
			canceled := x.cancelRequested
			x.cancelRequested = false
			if reply.operation != "read" {
				x.last = reply.result
				if reply.result.Receipt.Raw != "" {
					x.receipt = reply.result.Receipt
				}
				if reply.result.State == "uncertain" {
					x.uncertain = true
				}
			}
			var diagnostic *contract.SettingsRequestError
			if errors.As(reply.err, &diagnostic) && diagnostic.Raw != "" {
				x.diagnostic = diagnostic.Raw
			}
			if reply.generation != current.Generation || !current.Connected {
				x.lost, x.fresh = true, false
				d.err = "原设置会话已失效，请显式恢复"
				continue
			}
			if errors.Is(reply.err, contract.ErrSettingsLeaseLost) || errors.Is(reply.err, contract.ErrRevoked) {
				x.lost, x.fresh = true, false
			}
			source := reply.snapshot
			if reply.result.Snapshot.Token != "" {
				source = reply.result.Snapshot
			}
			if source.Token != "" {
				keep := x.dirty() && reply.result.State != "succeeded"
				x.snapshot = source
				x.baseline = prettySettings(source.Raw)
				x.fresh = true
				if !keep {
					x.draft = x.baseline
					x.editing = false
				}
				if reply.operation == "observe" {
					x.receipt = contract.SettingsReceipt{}
					if s.deps.SettingsChanged != nil {
						s.deps.SettingsChanged(services, SettingsEvent{DocumentID: d.id, Generation: reply.generation, Object: d.object, State: reply.result.State})
					}
				}
				if keep && reply.operation == "read" {
					d.err = "当前设置已读取；草稿保留，请合并后重新预览"
				}
			}
			if reply.err != nil {
				d.err = reply.err.Error()
				if errors.Is(reply.err, contract.ErrSettingsConflict) {
					x.fresh = false
				}
			}
			if canceled && reply.err != nil {
				d.err = "已停止等待；原服务器设置任务仍需显式确认"
			}
			if reply.operation == "apply" && reply.result.State == "rejected" {
				d.err = "原服务器拒绝设置请求；草稿和诊断保留，请恢复后重新读取设置"
				x.tab = 2
			}
			if reply.operation == "apply" && reply.result.State == "enqueued" {
				d.err = "设置请求已入队；请显式观察原任务确认结果"
				x.tab = 2
			}
			if reply.operation == "observe" && reply.err == nil && source.Token != "" {
				x.diagnostic = ""
				if reply.result.State == "succeeded" {
					d.err = "设置任务成功；已读取当前原索引设置"
				} else {
					d.err = "原设置任务未成功；完整诊断和草稿已保留"
				}
			}
		default:
		}
	}
}
func (s *State) guardSettingsLeave(d *Document, next func(ui.Services)) bool {
	if !d.SettingsMode() {
		return false
	}
	x := d.settings
	if x.leaveAcknowledged && !d.loading && !x.dirty() {
		return false
	}
	if !d.loading && !x.dirty() && x.receipt.Raw == "" && !x.uncertain {
		return false
	}
	if s.deps.ActivateDesign != nil {
		s.deps.ActivateDesign(d)
	}
	x.guard = true
	x.next = next
	return true
}
func (s *State) discardSettingsDraft(d *Document) {
	if d.SettingsMode() {
		x := d.settings
		x.draft = x.baseline
		x.editing = false
		x.preview = false
		x.plan = nil
	}
}
func (s *State) closeSettings(d *Document) {
	if d.settings == nil {
		return
	}
	x := d.settings
	if d.cancel != nil {
		d.cancel()
	}
	if x.lease != nil {
		_ = x.lease.Close()
	}
	x.results = nil
	x.next = nil
	x.plan = nil
	d.settings = nil
}
