package dataworkspace

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// A worker publishes ownership separately from its buffered UI notification.
// Revoking a closed/canceled document releases even an unconsumed or late plan.
// Only the UI thread takes a published plan; no worker touches document state.
type dataMutationTicket struct {
	mu      sync.Mutex
	revoked bool
	plan    contract.DataMutationPlan
}

func (t *dataMutationTicket) publish(plan contract.DataMutationPlan) {
	t.mu.Lock()
	if t.revoked {
		t.mu.Unlock()
		if plan != nil {
			_ = plan.Close()
		}
		return
	}
	t.plan = plan
	t.mu.Unlock()
}
func (t *dataMutationTicket) take() contract.DataMutationPlan {
	t.mu.Lock()
	defer t.mu.Unlock()
	plan := t.plan
	t.plan = nil
	return plan
}
func (t *dataMutationTicket) revoke() {
	t.mu.Lock()
	t.revoked = true
	plan := t.plan
	t.plan = nil
	t.mu.Unlock()
	if plan != nil {
		_ = plan.Close()
	}
}

type dataMutationState struct {
	ticket              *dataMutationTicket
	results             <-chan error
	plan                contract.DataMutationPlan
	snapshot            contract.DataMutationSnapshot
	keys                []string
	edits               []contract.DataEdit
	object              contract.ObjectRef
	profile             int64
	generation, request uint64
	submit              func(context.Context, int64, string, string, contract.DataMutationPlan) (*contract.DataEditResult, error)
	ctx                 context.Context
	open                bool
}

func requiresDataMutationPreview(r Runtime, edits []contract.DataEdit) bool {
	for _, e := range edits {
		if slices.Contains(r.PreviewEditKinds, e.Kind) {
			return true
		}
	}
	return false
}

func (s *State) previewDataMutation(services ui.Services, d *Document, runtime Runtime, keys []string, edits []contract.DataEdit) {
	if runtime.PreviewEdits == nil || runtime.SubmitEdits == nil || dataTransactionActive(d) {
		d.edit.Err = locales.Text("数据编辑服务不可用")
		s.clearDataMutationContinuation(d)
		return
	}
	d.edit.Request++
	ctx, cancel := context.WithCancel(context.Background())
	ticket := &dataMutationTicket{}
	out := make(chan error, 1)
	captured := contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Edits: edits}).Edits
	d.mutation = dataMutationState{ticket: ticket, results: out, keys: slices.Clone(keys), edits: captured,
		object: d.object, profile: d.profileID, generation: s.sessionFor(d).Generation, request: d.edit.Request,
		submit: runtime.SubmitEdits, ctx: ctx}
	d.edit.Cancel, d.edit.Busy, d.edit.Err = cancel, true, ""
	preview, profile, database, table := runtime.PreviewEdits, d.profileID, d.database, d.table
	// The executor receives its own deep copy, independent of UI confirmation.
	request := contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Edits: captured}).Edits
	go func() {
		plan, err := preview(ctx, profile, database, table, request)
		ticket.publish(plan)
		if err == nil {
			err = ctx.Err()
		}
		out <- err
		services.Invalidate()
	}()
}

func (s *State) clearDataMutationContinuation(d *Document) {
	d.edit.AfterSave, d.edit.LeaveNext = nil, nil
	if s.guardDoc == d {
		s.guardDoc = nil
	}
}

// Cancellation waits for a pending reader's acknowledgement. A ready preview
// owns no running executor, so its dialog and handle can be released immediately.
func (s *State) cancelDataMutation(d *Document) {
	m := &d.mutation
	if m.ticket != nil {
		m.ticket.revoke()
	}
	if m.plan != nil {
		_ = m.plan.Close()
		m.plan = nil
	}
	if m.ticket != nil || m.open {
		s.clearDataMutationContinuation(d)
		if m.results == nil {
			s.releaseDataMutation(d)
		}
	}
}

func (s *State) releaseDataMutation(d *Document) {
	m := &d.mutation
	if m.ticket != nil {
		m.ticket.revoke()
	}
	if m.plan != nil {
		_ = m.plan.Close()
	}
	// Keep the owned evidence after submission or cancellation. The native
	// handle's Snapshot becomes empty once it is consumed or closed.
	snapshot := m.snapshot
	*m = dataMutationState{snapshot: snapshot}
	if d.edit.Cancel != nil {
		d.edit.Cancel()
	}
	d.edit.Cancel, d.edit.Busy = nil, false
}

func dataMutationDraftsMatch(d *Document, keys []string, edits []contract.DataEdit) bool {
	if d.page == nil || len(keys) != len(edits) || len(keys) != len(d.edit.Drafts) {
		return false
	}
	for i, key := range keys {
		draft := d.edit.Drafts[key]
		if draft == nil || mysqlDataRowKey(d, draft.Row) != key || !reflect.DeepEqual(
			contract.DataEdit{Kind: draft.Kind, Original: cloneSnapshot(draft.Original), Values: cloneValues(draft.Values)}, edits[i]) {
			return false
		}
	}
	return true
}
func (s *State) validDataMutation(d *Document) bool {
	m := &d.mutation
	return !d.closed && s.documents[d.id] == d && d.edit.Request == m.request && d.profileID == m.profile &&
		d.object == m.object && s.canUseSession(d) && s.sessionFor(d).Generation == m.generation &&
		m.ctx != nil && m.ctx.Err() == nil && dataMutationDraftsMatch(d, m.keys, m.edits)
}
func validDataMutationSnapshot(m *dataMutationState, snapshot contract.DataMutationSnapshot) bool {
	if snapshot.Object != m.object || snapshot.NativeJSON == "" || len(snapshot.Items) != len(m.edits) || !reflect.DeepEqual(snapshot.Edits, m.edits) {
		return false
	}
	for i, item := range snapshot.Items {
		if item.Kind != m.edits[i].Kind || item.AffectedRelationships < 0 {
			return false
		}
	}
	return true
}

func (s *State) consumeDataMutation(d *Document) {
	m := &d.mutation
	if m.results == nil {
		// A ready handle can be revoked by its original provider/session while the
		// confirmation is open. Settle without interpreting a read as an unknown write.
		if m.open && !s.validDataMutation(d) {
			s.releaseDataMutation(d)
			s.clearDataMutationContinuation(d)
			d.edit.Err = locales.Text("原数据库会话已失效，请刷新表数据")
		}
		return
	}
	select {
	case err := <-m.results:
		m.results = nil
		m.plan = m.ticket.take()
		if err == nil && !s.validDataMutation(d) {
			err = contract.ErrRevoked
		}
		if err == nil && m.plan == nil {
			err = contract.ErrUnsupported
		}
		if err == nil {
			snapshot := m.plan.Snapshot()
			if !validDataMutationSnapshot(m, snapshot) {
				err = contract.ErrDataConflict
			} else {
				m.snapshot = contract.CloneDataMutationSnapshot(snapshot)
			}
		}
		if err != nil {
			s.releaseDataMutation(d)
			s.clearDataMutationContinuation(d)
			d.edit.Err = err.Error()
			return
		}
		m.open = true
	default:
	}
}

func dataMutationBlocked(snapshot contract.DataMutationSnapshot) bool {
	for _, item := range snapshot.Items {
		if item.Blocked {
			return true
		}
	}
	return false
}

func (s *State) confirmDataMutation(services ui.Services, d *Document) {
	m := &d.mutation
	if !m.open || m.plan == nil || dataMutationBlocked(m.snapshot) {
		return
	}
	if !s.validDataMutation(d) || !reflect.DeepEqual(m.plan.Snapshot(), m.snapshot) {
		s.releaseDataMutation(d)
		s.clearDataMutationContinuation(d)
		d.edit.Err = contract.ErrDataConflict.Error()
		return
	}
	// Capture the original one-attempt handle and owning executor on the UI
	// thread. No worker rereads a draft, document, active profile or runtime.
	plan, submit, profile, database, table := m.plan, m.submit, m.profile, d.database, d.table
	edits := contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Edits: m.edits}).Edits
	keys, generation, request := slices.Clone(m.keys), m.generation, m.request
	snapshot, object := m.snapshot, m.object
	m.plan = nil
	s.releaseDataMutation(d)
	d.mutation.snapshot = snapshot
	ctx, cancel := context.WithCancel(context.Background())
	d.edit.Cancel, d.edit.Busy = cancel, true
	out := make(chan mysqlEditResult, 1)
	d.edit.Results = out
	history := mysqlDataWriteHistoryRequest(database, table, edits, false)
	for i := range history {
		history[i].SQL, history[i].Summary = locales.Text("已确认的原生实体更改"), true
	}
	complete := s.beginActivity(d, "data_write", dataActivitySQL(history))
	go func() {
		started := time.Now()
		result, err := submit(ctx, profile, database, table, plan)
		_ = plan.Close()
		contract.CompleteActivity(complete, dataMutationActivityOutcome(err, object, edits, result))
		for i := range history {
			history[i].At, history[i].Duration = time.Now(), time.Since(started)
		}
		out <- mysqlEditResult{History: history, Generation: generation, Request: request, Profile: profile,
			Keys: keys, MutationEdits: edits, Saved: result, Err: err}
		services.Invalidate()
	}()
}

// Only actual commit confirmation and completely corresponding outcomes may
// authorize generated identities or row removal. Observation cannot fill gaps.
func dataMutationCommitted(d *Document, keys []string, edits []contract.DataEdit, result *contract.DataEditResult) bool {
	return dataMutationDraftsMatch(d, keys, edits) && dataMutationReceiptComplete(d.object, edits, result)
}

func dataMutationReceiptComplete(object contract.ObjectRef, edits []contract.DataEdit, result *contract.DataEditResult) bool {
	if result == nil || !result.CommitConfirmed || result.CommitUncertain || result.Incomplete || len(result.Outcomes) != len(edits) || result.Applied != len(edits) {
		return false
	}
	for i, outcome := range result.Outcomes {
		edit := edits[i]
		if outcome.Kind != edit.Kind || !reflect.DeepEqual(outcome.Original, edit.Original) {
			return false
		}
		if edit.Kind == "insert" || edit.Kind == "update" {
			if outcome.Saved == nil || outcome.Saved.Ref.Object != object || len(outcome.Saved.Ref.Columns) == 0 || len(outcome.Saved.Ref.Columns) != len(outcome.Saved.Ref.TypedValues) || len(outcome.Saved.Ref.Columns) != len(outcome.Saved.Ref.Values) {
				return false
			}
		}
	}
	return true
}

func (s *State) dataMutationModal(c *ui.Context, d *Document) {
	m := &d.mutation
	if !m.open {
		if m.plan != nil {
			s.releaseDataMutation(d)
			s.clearDataMutationContinuation(d)
		}
		return
	}
	if !s.consumeFor(d) {
		return
	}
	theme.Modal(c, &m.open, func() {
		ui.Column(c).Width(720).MaxWidthPercent(95).Gap(8).Children(func() {
			ui.Text(c, locales.Text("确认实体更改")).FontSize(theme.Font(c, theme.FontSize))
			ui.Text(c, d.database+" · "+d.table)
			ui.Text(c, locales.Text("请检查原始实体、关联关系和属性；确认后将提交此批次更改。"))
			ui.Scroll(c).Height(260).FillWidth().Children(func() {
				for _, item := range m.snapshot.Items {
					identity := item.EntityID
					if identity == "" {
						identity = locales.Text("待生成")
					}
					ui.Text(c, fmt.Sprintf("%s · %s · %s: %d", dataMutationKind(item.Kind), identity, locales.Text("关联关系"), item.AffectedRelationships))
				}
				if dataMutationBlocked(m.snapshot) {
					ui.Text(c, locales.Text("普通删除包含有关联关系的节点。请取消后选择删除方式。此批次尚未写入。")).TextColor(theme.Colors(c).Warning)
				}
				ui.Text(c, dataMutationPreview(m.snapshot.NativeJSON)).Label(locales.Text("实体更改完整预览"))
			})
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, locales.Text("复制完整预览")).Clicked() {
					c.WriteClipboard(m.snapshot.NativeJSON)
				}
				if ui.Button(c, locales.Text("取消")).Clicked() {
					m.open = false
				}
				if ui.Button(c, locales.Text("确认并提交更改")).Disabled(dataMutationBlocked(m.snapshot)).Clicked() {
					s.confirmDataMutation(c.Services(), d)
				}
			})
		})
	})
	// Dismissal is equivalent to cancellation, never submission or discard.
	if !m.open && m.plan != nil {
		s.releaseDataMutation(d)
		s.clearDataMutationContinuation(d)
	}
}

func dataEditDeletesRow(kind string) bool { return kind == "delete" || kind == "detach_delete" }

func dataMutationKind(kind string) string {
	switch kind {
	case "insert":
		return locales.Text("创建实体")
	case "delete":
		return locales.Text("删除实体")
	case "detach_delete":
		return locales.Text("删除实体及关联关系")
	case "update":
		return locales.Text("更新属性")
	}
	return kind
}
func dataMutationPreview(raw string) string {
	if len(raw) <= 64<<10 {
		return raw
	}
	raw = raw[:64<<10]
	for !utf8.ValidString(raw) {
		raw = raw[:len(raw)-1]
	}
	return raw + "\n… " + locales.Text("预览已截断；可复制完整内容")
}

func dataMutationActivityOutcome(err error, object contract.ObjectRef, edits []contract.DataEdit, result *contract.DataEditResult) contract.ActivityResult {
	outcome := dataActivityOutcome(err, result, len(edits), false)
	if err == nil && !dataMutationReceiptComplete(object, edits, result) {
		outcome.State, outcome.AffectedRows = "partial", 0
		if result == nil || !result.CommitConfirmed || result.CommitUncertain {
			outcome.State = "uncertain"
		}
		outcome.Error = locales.Text("提交结果不完整，请核验数据库")
	}
	return outcome
}
