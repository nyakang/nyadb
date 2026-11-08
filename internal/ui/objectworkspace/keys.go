package objectworkspace

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type keyForm struct{ UID, Name, Description, Actions, Indexes, ExpiresAt string }
type keysDocument struct {
	lease                                            contract.KeysDocumentLease
	generation                                       uint64
	page                                             *contract.KeyPage
	record                                           contract.KeyRecord
	last                                             contract.KeyResult
	form, baseline                                   keyForm
	action, diagnostic, secret, confirmation         string
	selected, tab                                    int
	limit                                            uint64
	editing, preview, guard, secretOpen, fresh, lost bool
	uncertain, cancelRequested, leaveAcknowledged    bool
	plan                                             *contract.KeyPlan
	results                                          <-chan keysReply
	next                                             func(ui.Services)
}
type keysReply struct {
	request, generation uint64
	operation           string
	page                contract.KeyPage
	record              contract.KeyRecord
	result              contract.KeyResult
	err                 error
}

func cloneKeyRecord(r contract.KeyRecord) contract.KeyRecord {
	r.Actions, r.Indexes = slices.Clone(r.Actions), slices.Clone(r.Indexes)
	return r
}
func cloneKeyPage(p contract.KeyPage) contract.KeyPage {
	p.Results = slices.Clone(p.Results)
	for i := range p.Results {
		p.Results[i] = cloneKeyRecord(p.Results[i])
	}
	return p
}
func cloneKeyResult(r contract.KeyResult) contract.KeyResult {
	r.Record = cloneKeyRecord(r.Record)
	return r
}
func cloneKeyPlan(p contract.KeyPlan) contract.KeyPlan {
	p.Original = cloneKeyRecord(p.Original)
	p.Draft.Actions, p.Draft.Indexes = slices.Clone(p.Draft.Actions), slices.Clone(p.Draft.Indexes)
	return p
}
func keyRecordForm(r contract.KeyRecord) keyForm {
	return keyForm{UID: r.UID, Name: r.Name, Description: r.Description, Actions: strings.Join(r.Actions, ","), Indexes: strings.Join(r.Indexes, ","), ExpiresAt: r.ExpiresAt}
}
func (f keyForm) draft() contract.KeyDraft {
	values := func(raw string) []string {
		items := strings.Split(raw, ",")
		for i := range items {
			items[i] = strings.TrimSpace(items[i])
		}
		return items
	}
	return contract.KeyDraft{UID: strings.TrimSpace(f.UID), Name: f.Name, Description: f.Description, Actions: values(f.Actions), Indexes: values(f.Indexes), ExpiresAt: strings.TrimSpace(f.ExpiresAt)}
}
func keysObject(o contract.ObjectRef) bool {
	return o.ProfileID > 0 && o.Catalog == "" && o.Schema == "" && o.Kind == "keys" && o.Name == "keys"
}
func (s *State) KeysScopeSupported(o contract.ObjectRef) bool {
	if !keysObject(o) || s.deps.KeysDocumentsFor == nil {
		return false
	}
	p, err := s.deps.KeysDocumentsFor(o.ProfileID)
	return err == nil && p != nil && p.SupportsKeysDocument(o)
}
func (s *State) OpenKeys(id uint64, o contract.ObjectRef) *Document {
	if !s.KeysScopeSupported(o) {
		return nil
	}
	if old := s.documents[id]; old != nil {
		s.Close(old)
	}
	d := &Document{id: id, profileID: o.ProfileID, table: o.Name, object: o, objectKind: "API 密钥", keys: &keysDocument{selected: -1, limit: 20}}
	s.documents[id] = d
	return d
}
func (d *Document) KeysMode() bool { return d != nil && !d.closed && d.keys != nil }
func (x *keysDocument) dirty() bool {
	return x != nil && x.editing && (x.action == "create" || x.form != x.baseline)
}
func (x *keysDocument) hideSecret() { x.secret, x.secretOpen = "", false }
func (s *State) HideKeySecret(d *Document) {
	if d.KeysMode() {
		d.keys.hideSecret()
	}
}
func (s *State) keysReady(d *Document) bool {
	if !d.KeysMode() || d.loading || d.keys.lost || s.session().ProfileID != d.profileID {
		return false
	}
	w := s.sessionFor(d.profileID)
	return w.Connected && w.ProfileID == d.profileID && (d.keys.lease == nil || w.Generation == d.keys.generation)
}
func (s *State) reserveKeys(d *Document) error {
	p, err := s.deps.KeysDocumentsFor(d.profileID)
	if err != nil {
		return err
	}
	l, err := p.ReserveKeysDocument(d.object)
	if err != nil {
		return err
	}
	d.keys.lease, d.keys.generation = l, s.sessionFor(d.profileID).Generation
	return nil
}
func (s *State) LoadKeys(d *Document, invalidate func(), refresh bool) {
	if !s.keysReady(d) || d.keys.editing || d.keys.preview {
		return
	}
	x := d.keys
	if !refresh && (x.page != nil || d.err != "") {
		return
	}
	offset := uint64(0)
	if x.page != nil {
		offset = x.page.Offset
	}
	s.loadKeysPage(d, invalidate, offset)
}
func (s *State) loadKeysPage(d *Document, invalidate func(), offset uint64) {
	if !s.keysReady(d) || d.keys.editing || d.keys.preview {
		return
	}
	x := d.keys
	if x.lease == nil {
		if err := s.reserveKeys(d); err != nil {
			d.err = err.Error()
			return
		}
	}
	limit := x.limit
	x.fresh = false
	s.runKeys(d, invalidate, "list", func(ctx context.Context, l contract.KeysDocumentLease) keysReply {
		page, err := l.List(ctx, offset, limit)
		return keysReply{page: cloneKeyPage(page), err: err}
	})
}
func (s *State) PageKeys(d *Document, invalidate func(), direction int) {
	if !s.keysReady(d) || d.keys.page == nil || d.keys.editing || d.keys.preview {
		return
	}
	p := d.keys.page
	offset := p.Offset
	if direction < 0 {
		offset -= min(offset, p.Limit)
	} else if direction > 0 && p.Offset+uint64(len(p.Results)) < p.Total {
		offset += p.Limit
	} else {
		return
	}
	s.loadKeysPage(d, invalidate, offset)
}
func (s *State) runKeys(d *Document, invalidate func(), operation string, run func(context.Context, contract.KeysDocumentLease) keysReply) {
	if !s.keysReady(d) || d.keys.lease == nil {
		return
	}
	x := d.keys
	x.hideSecret()
	l, generation := x.lease, x.generation
	d.request++
	request := d.request
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel, d.loading, d.err = cancel, true, ""
	x.cancelRequested, x.leaveAcknowledged = false, false
	ch := make(chan keysReply, 1)
	x.results = ch
	go func() {
		r := run(ctx, l)
		r.operation, r.request, r.generation = operation, request, generation
		ch <- r
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) SelectKey(d *Document, row int) {
	if !d.KeysMode() || d.loading || d.keys.editing || d.keys.preview || d.keys.page == nil || row < 0 || row >= len(d.keys.page.Results) {
		return
	}
	x := d.keys
	x.hideSecret()
	x.selected, x.record = row, cloneKeyRecord(x.page.Results[row])
}
func (s *State) ReadKey(d *Document, invalidate func()) {
	if !s.keysReady(d) || d.keys.record.Token == "" || d.keys.preview {
		return
	}
	r := cloneKeyRecord(d.keys.record)
	d.keys.fresh = false
	s.runKeys(d, invalidate, "read", func(ctx context.Context, l contract.KeysDocumentLease) keysReply {
		r, e := l.Read(ctx, r)
		return keysReply{record: cloneKeyRecord(r), err: e}
	})
}
func (s *State) CanCreateKey(d *Document) bool {
	return s.keysReady(d) && !d.keys.uncertain
}
func (s *State) CanModifyKey(d *Document) bool {
	return s.CanCreateKey(d) && d.keys.fresh && d.keys.record.Token != ""
}
func (s *State) EditKey(d *Document, create bool) {
	if !s.CanCreateKey(d) || d.keys.preview || d.keys.editing || !create && !s.CanModifyKey(d) {
		return
	}
	if d.keys.lease == nil {
		if err := s.reserveKeys(d); err != nil {
			d.err = err.Error()
			return
		}
	}
	x := d.keys
	x.hideSecret()
	x.action, x.form = "update", keyRecordForm(x.record)
	if create {
		x.action, x.form = "create", keyForm{Actions: "search", Indexes: "*"}
	}
	x.baseline, x.editing, x.tab = x.form, true, 2
	x.leaveAcknowledged = false
}
func (s *State) PrepareKey(d *Document, deleteKey bool) error {
	if !s.CanCreateKey(d) || d.keys.lease == nil {
		return contract.ErrKeyPlanUsed
	}
	x := d.keys
	action, draft := x.action, x.form.draft()
	original := cloneKeyRecord(x.record)
	if deleteKey {
		if x.editing || !s.CanModifyKey(d) {
			return contract.ErrKeyPlanUsed
		}
		action, draft = "delete", contract.KeyDraft{}
	} else if !x.editing || action == "update" && !s.CanModifyKey(d) {
		return contract.ErrKeyPlanUsed
	}
	if action == "create" {
		original = contract.KeyRecord{}
	}
	p, err := x.lease.Prepare(action, original, draft)
	if err != nil {
		d.err = err.Error()
		return err
	}
	x.hideSecret()
	p = cloneKeyPlan(p)
	x.plan, x.preview, x.leaveAcknowledged = &p, true, false
	x.confirmation = ""
	d.err = ""
	return nil
}
func (s *State) ApplyKey(d *Document, invalidate func()) {
	if !s.CanConfirmKey(d) {
		return
	}
	x := d.keys
	p := cloneKeyPlan(*x.plan)
	if p.Action != "create" && (!s.CanModifyKey(d) || p.Original.Token != x.record.Token) || p.Action != "delete" && (!x.editing || p.Action != x.action || !keyDraftEqual(p.Draft, x.form.draft())) {
		x.preview, x.plan = false, nil
		d.err = "密钥草稿或选择已改变，请重新预览"
		return
	}
	x.preview, x.plan, x.fresh = false, nil, false
	s.runKeys(d, invalidate, "apply", func(ctx context.Context, l contract.KeysDocumentLease) keysReply {
		r, e := l.Apply(ctx, p)
		return keysReply{result: cloneKeyResult(r), err: e}
	})
}
func (s *State) CanConfirmKey(d *Document) bool {
	return s.CanCreateKey(d) && d.keys.plan != nil && d.keys.preview && (d.keys.plan.Action != "delete" || d.keys.confirmation == d.keys.plan.Original.UID)
}
func keyDraftEqual(a, b contract.KeyDraft) bool {
	return a.UID == b.UID && a.Name == b.Name && a.Description == b.Description && a.ExpiresAt == b.ExpiresAt && slices.Equal(a.Actions, b.Actions) && slices.Equal(a.Indexes, b.Indexes)
}
func (s *State) KeySecret(d *Document) (string, error) {
	if !s.keysReady(d) || d.keys.lease == nil || d.keys.record.Token == "" {
		return "", contract.ErrKeyLeaseLost
	}
	return d.keys.lease.Reveal(cloneKeyRecord(d.keys.record))
}
func (s *State) RevealKey(d *Document) {
	secret, err := s.KeySecret(d)
	if err != nil {
		if d.KeysMode() {
			d.err = err.Error()
		}
		return
	}
	d.keys.secret, d.keys.secretOpen = secret, true
}
func (s *State) DiscardKeyDraft(d *Document) {
	if !d.KeysMode() || d.loading {
		return
	}
	x := d.keys
	x.hideSecret()
	x.form, x.baseline, x.action = keyForm{}, keyForm{}, ""
	x.editing, x.preview, x.plan = false, false, nil
}
func (s *State) CancelKeys(d *Document) {
	if !d.KeysMode() || !d.loading || d.keys.cancelRequested {
		return
	}
	d.keys.hideSecret()
	d.keys.cancelRequested = true
	if d.cancel != nil {
		d.cancel()
	}
}
func (s *State) RecoverKeys(d *Document) {
	if !d.KeysMode() || d.loading || s.session().ProfileID != d.profileID || !s.sessionFor(d.profileID).Connected {
		return
	}
	x := d.keys
	x.hideSecret()
	if x.lease == nil {
		if err := s.reserveKeys(d); err != nil {
			d.err = err.Error()
		}
		return
	}
	p, err := s.deps.KeysDocumentsFor(d.profileID)
	if err == nil {
		var r contract.KeyRecovery
		r, err = p.RecoverKeysDocument(d.object, x.lease)
		if err == nil {
			x.lease, x.generation = r.Lease, s.sessionFor(d.profileID).Generation
			x.last, x.uncertain = cloneKeyResult(r.Last), x.uncertain || r.Uncertain
			x.lost, x.fresh, x.preview, x.plan = false, false, false, nil
			if r.Page.Raw != "" {
				page := cloneKeyPage(r.Page)
				x.page = &page
			}
			x.record = contract.KeyRecord{}
			if x.page != nil && x.selected >= 0 && x.selected < len(x.page.Results) {
				x.record = cloneKeyRecord(x.page.Results[x.selected])
			}
			if r.Last.Record.Token != "" {
				x.record = cloneKeyRecord(r.Last.Record)
			}
			d.err = "密钥连接已恢复；请显式读取当前密钥，原请求不会重发"
			return
		}
	}
	x.lost = true
	d.err = err.Error()
}
func (s *State) KeysPage(d *Document) contract.KeyPage {
	if !d.KeysMode() || d.keys.page == nil {
		return contract.KeyPage{}
	}
	return cloneKeyPage(*d.keys.page)
}
func (s *State) KeyRecord(d *Document) contract.KeyRecord {
	if !d.KeysMode() {
		return contract.KeyRecord{}
	}
	return cloneKeyRecord(d.keys.record)
}
func (s *State) KeyResult(d *Document) contract.KeyResult {
	if !d.KeysMode() {
		return contract.KeyResult{}
	}
	return cloneKeyResult(d.keys.last)
}
func (s *State) KeysUncertain(d *Document) bool { return d.KeysMode() && d.keys.uncertain }
func (s *State) consumeKeys() {
	active := s.session()
	for _, d := range s.documents {
		if !d.KeysMode() {
			continue
		}
		x := d.keys
		if s.deps.DocumentActive != nil && !s.deps.DocumentActive(d.id) {
			x.hideSecret()
		}
		orphanedActive := active.ProfileID == 0 && s.deps.DocumentActive != nil && s.deps.DocumentActive(d.id)
		if d.profileID != active.ProfileID && !orphanedActive {
			x.hideSecret()
			continue
		}
		current := s.sessionFor(d.profileID)
		if x.lease != nil && (!current.Connected || current.Generation != x.generation) {
			x.hideSecret()
			x.lost, x.fresh = true, false
			if !d.loading {
				d.err = "原密钥会话已失效，请显式恢复"
			}
		}
		if x.results == nil {
			continue
		}
		select {
		case r := <-x.results:
			if r.request != d.request {
				continue
			}
			x.results, d.loading, x.cancelRequested = nil, false, false
			if d.cancel != nil {
				d.cancel()
				d.cancel = nil
			}
			if r.operation == "apply" {
				x.last, x.tab = cloneKeyResult(r.result), 3
				x.uncertain = x.uncertain || r.result.State == "uncertain"
				if r.result.State == "completed" {
					s.DiscardKeyDraft(d)
				}
			}
			var diagnostic *contract.KeyRequestError
			if errors.As(r.err, &diagnostic) {
				x.diagnostic = diagnostic.Raw
			}
			if r.generation != current.Generation || !current.Connected {
				x.lost, x.fresh = true, false
				d.err = "原密钥会话已失效，请显式恢复"
				continue
			}
			if errors.Is(r.err, contract.ErrKeyLeaseLost) || errors.Is(r.err, contract.ErrRevoked) {
				x.lost, x.fresh = true, false
			}
			if r.err == nil {
				x.diagnostic = ""
				switch r.operation {
				case "list":
					page := cloneKeyPage(r.page)
					x.page, x.record, x.selected, x.fresh = &page, contract.KeyRecord{}, -1, true
				case "read":
					x.record, x.fresh = cloneKeyRecord(r.record), true
					if x.dirty() {
						d.err = "当前密钥已读取；草稿保留，请合并后重新预览"
					}
				case "apply":
					x.record, x.selected = cloneKeyRecord(r.result.Record), -1
					x.fresh = r.result.Record.Token != ""
					d.err = "密钥操作已同步完成；请显式刷新列表"
				}
				if x.page != nil && x.record.Token != "" {
					for i, old := range x.page.Results {
						if old.UID == x.record.UID {
							x.page.Results[i], x.selected = cloneKeyRecord(x.record), i
						}
					}
				}
			} else {
				d.err = r.err.Error()
				if errors.Is(r.err, contract.ErrKeyConflict) {
					x.fresh = false
				}
			}
		default:
		}
	}
}
func (s *State) guardKeysLeave(d *Document, next func(ui.Services)) bool {
	if !d.KeysMode() {
		return false
	}
	x := d.keys
	x.hideSecret()
	if x.leaveAcknowledged && !d.loading && !x.dirty() && !x.preview || !d.loading && !x.dirty() && !x.preview && !x.uncertain {
		return false
	}
	if s.deps.ActivateDesign != nil {
		s.deps.ActivateDesign(d)
	}
	x.guard, x.next = true, next
	return true
}
func (s *State) closeKeys(d *Document) {
	if d.keys == nil {
		return
	}
	x := d.keys
	x.hideSecret()
	d.request++
	if d.cancel != nil {
		d.cancel()
	}
	if x.lease != nil {
		_ = x.lease.Close()
	}
	*x = keysDocument{}
	d.keys = nil
}
