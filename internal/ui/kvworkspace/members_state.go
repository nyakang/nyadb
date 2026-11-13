package kvworkspace

import (
	"context"
	"errors"
	"reflect"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
	"github.com/nyakang/nyadb/locales"
)

var memberDefragment = contract.KVRequest{Method: "/etcdserverpb.Maintenance/Defragment", JSON: "{}"}

const memberArchiveLimit = 512 << 20

type MemberBindingReceipt struct {
	Target    contract.KVMemberTarget
	Scope     contract.KVMemberScope
	Attempted bool
	Error     string
}

func (r MemberBindingReceipt) Clone() MemberBindingReceipt { r.Scope = r.Scope.Clone(); return r }

type MemberBatchTarget struct {
	Scope   contract.KVMemberScope
	Request contract.KVRequest
	State   string // succeeded, failed, unknown, not-executed
	Result  *contract.KVMemberResult
}

type MemberOrderingRound struct {
	Remaining []uint64
	Statuses  []contract.KVMemberResult
	Chosen    uint64
	Error     string
}

type MemberBatchReceipt struct {
	DocumentID uint64
	Scope      contract.KVScope
	Authored   contract.KVRequest
	Targets    []MemberBatchTarget
	Rounds     []MemberOrderingRound
	Ended      bool
	Error      string
}

func cloneMemberResults(rs []contract.KVMemberResult) []contract.KVMemberResult {
	rs = slices.Clone(rs)
	for i := range rs {
		rs[i] = rs[i].Clone()
	}
	return rs
}

func (r MemberBatchReceipt) Clone() MemberBatchReceipt {
	r.Scope = r.Scope.Clone()
	r.Targets = slices.Clone(r.Targets)
	for i := range r.Targets {
		r.Targets[i].Scope = r.Targets[i].Scope.Clone()
		if r.Targets[i].Result != nil {
			copy := r.Targets[i].Result.Clone()
			r.Targets[i].Result = &copy
		}
	}
	r.Rounds = slices.Clone(r.Rounds)
	for i := range r.Rounds {
		r.Rounds[i].Remaining = slices.Clone(r.Rounds[i].Remaining)
		r.Rounds[i].Statuses = cloneMemberResults(r.Rounds[i].Statuses)
	}
	return r
}

type boundMember struct {
	lease   contract.KVMemberLease
	receipt MemberBindingReceipt
}
type memberBindings struct {
	lease       contract.KVRequestLease
	observation contract.KVMemberObservation
	authored    contract.KVRequest
	entries     []boundMember
}
type memberState struct {
	showResults            bool
	recordView             int
	observation            contract.KVMemberObservation
	directory              *contract.KVMemberDirectory
	bindings               *memberBindings
	history                []MemberBindingReceipt
	statuses, recovered    []contract.KVMemberResult
	batch                  *MemberBatchReceipt
	unknown                *contract.KVMemberResult
	revision               uint64
	row                    int
	endpoint, version      string
	editRow, resultRow     int
	columns, resultColumns ui.ListState
	showJSON, showUnknown  bool
	confirmationColumns    ui.ListState
	preview, previewError  string
	previewRevision        uint64
	previewCaptured        bool
	viewWidth, viewHeight  float32
}
type memberConfirmation struct {
	bindings          *memberBindings
	receipt           MemberBatchReceipt
	followers, finals []contract.KVMemberPreparation
}
type memberOutcome struct {
	observation contract.KVMemberObservation
	directory   *contract.KVMemberDirectory
	bindings    *memberBindings
	statuses    []contract.KVMemberResult
	batch       *MemberBatchReceipt
}

func (d *Document) MembersAvailable() bool { _, ok := d.lease.(contract.KVMembers); return ok }
func (d *Document) MemberDirectoryReceipt() (contract.KVMemberDirectory, bool) {
	if d.members == nil || d.members.directory == nil {
		return contract.KVMemberDirectory{}, false
	}
	return d.members.directory.Clone(), true
}
func (d *Document) MemberBatchReceipt() (MemberBatchReceipt, bool) {
	if d.members == nil || d.members.batch == nil {
		return MemberBatchReceipt{}, false
	}
	return d.members.batch.Clone(), true
}
func (d *Document) MemberUnknownReceipt() (contract.KVMemberResult, bool) {
	if d.members == nil || d.members.unknown == nil {
		return contract.KVMemberResult{}, false
	}
	return d.members.unknown.Clone(), true
}
func (d *Document) memberState() *memberState {
	if d.members == nil {
		d.members = &memberState{row: -1, editRow: -1, resultRow: -1}
	}
	return d.members
}
func (d *Document) memberContent() bool {
	m := d.members
	return m != nil && (m.directory != nil || len(m.history) != 0 || len(m.recovered) != 0 || m.batch != nil || m.unknown != nil)
}
func (d *Document) memberRevision() uint64 {
	if d.members == nil {
		return 0
	}
	return d.members.revision
}

func closeMemberBindings(b *memberBindings) {
	if b != nil {
		for _, entry := range b.entries {
			if entry.lease != nil {
				_ = entry.lease.Close()
			}
		}
	}
}
func (d *Document) releaseMemberAuthority() {
	if d.members == nil {
		return
	}
	m := d.members
	closeMemberBindings(m.bindings)
	if m.observation != nil {
		_ = m.observation.Close()
	}
	m.observation, m.bindings = nil, nil
	m.revision++
	d.clearConfirmation()
}
func (r *memberOutcome) close() {
	if r == nil {
		return
	}
	closeMemberBindings(r.bindings)
	if r.observation != nil {
		_ = r.observation.Close()
	}
}

func (s *State) memberRoom(d *Document, reserve int) bool {
	raw, err := s.MembersRecord(d)
	if err != nil || reserve < 0 || len(raw) > memberArchiveLimit-reserve {
		d.error = locales.Text("完整成员记录超过限额")
		return false
	}
	return true
}

func (s *State) ObserveMembers(d *Document) {
	if !s.check(d) || !d.MembersAvailable() || !s.memberRoom(d, 128<<20) {
		return
	}
	lease := d.lease
	id := d.id
	d.clearConfirmation()
	s.start(d, "member-directory", d.Request(), func(ctx context.Context) outcome {
		o, err := lease.(contract.KVMembers).ObserveMembers(ctx)
		directory := contract.KVMemberDirectory{DocumentID: id, Scope: lease.Scope()}
		if o != nil {
			directory = o.Snapshot().Clone()
		}
		if err != nil {
			directory.Error = err.Error()
		}
		return outcome{members: &memberOutcome{observation: o, directory: &directory}, err: err}
	})
}

func memberDirectoryReady(d *Document) bool {
	m := d.members
	if m == nil || m.observation == nil || m.directory == nil {
		return false
	}
	r := m.directory
	v := r.Result
	return r.DocumentID == d.id && sameEventScope(r.Scope, d.scope) && r.Error == "" && v.Completed && v.NativeCaptured && v.JSON != "" && v.ProjectionError == "" && v.JSONCaptureError == "" && v.Reply != nil && v.Reply.Kind == "member_list" && len(r.Members) > 0 && reflect.DeepEqual(m.observation.Snapshot(), *r)
}

// Cheap presentation predicates do not clone large evidence every frame. The
// action and confirmation gates still validate the complete original handles.
func memberDirectoryVisibleReady(d *Document) bool {
	m := d.members
	return m != nil && m.observation != nil && m.directory != nil && m.directory.Error == "" && m.directory.DocumentID == d.id && sameEventScope(m.directory.Scope, d.scope) && m.directory.Result.Completed && m.directory.Result.NativeCaptured
}
func memberSelectionVisibleReady(d *Document) bool {
	if !memberDirectoryVisibleReady(d) {
		return false
	}
	b := d.members.bindings
	if b == nil || b.lease != d.lease || b.observation != d.members.observation || b.authored != d.Request() || len(b.entries) == 0 {
		return false
	}
	for _, entry := range b.entries {
		if entry.lease == nil || entry.receipt.Error != "" || entry.receipt.Scope.Member.IsLearner || entry.lease.CheckMemberLease() != nil {
			return false
		}
	}
	return true
}

func (s *State) BindMembers(d *Document) {
	if !s.check(d) || !d.MembersAvailable() || !memberDirectoryReady(d) {
		return
	}
	targets, err := memberTargets(d.Request())
	if err != nil || len(targets) == 0 {
		d.error = locales.Text("请明确选择成员、endpoint 和版本")
		return
	}
	m := d.memberState()
	if len(m.history)+len(targets) > 32 || !s.memberRoom(d, 192<<20) {
		d.error = locales.Text("完整成员记录超过限额")
		return
	}
	// The worker owns only captured immutable targets and the original authority.
	lease, observation, authored := d.lease, m.observation, d.Request()
	directory := m.directory.Clone()
	b := &memberBindings{lease: lease, observation: observation, authored: authored, entries: make([]boundMember, len(targets))}
	for i, target := range targets {
		var selected *contract.KVMember
		for j := range directory.Members {
			if directory.Members[j].ID == target.MemberID {
				selected = &directory.Members[j]
				break
			}
		}
		if selected == nil {
			d.error = locales.Text("请选择原目录中的成员")
			return
		}
		b.entries[i].receipt = MemberBindingReceipt{Target: target, Scope: contract.KVMemberScope{Original: directory.Scope.Clone(), DocumentID: directory.DocumentID, Directory: directory.Clone(), Member: selected.Clone(), Endpoint: target.Endpoint, Version: target.Version}}
	}
	archive := memberArchiveFor(d)
	d.clearConfirmation()
	s.start(d, "member-bind", authored, func(ctx context.Context) outcome {
		var lastErr error
		for i := range b.entries {
			entry := &b.entries[i]
			if err := ctx.Err(); err != nil {
				lastErr = err
				break
			}
			if !memberArchiveRoom(archive, b.entries[:i], nil, 192<<20) {
				lastErr = jsondocument.ErrBounds
				break
			}
			binding, err := lease.(contract.KVMembers).BindMember(ctx, observation, entry.receipt.Target)
			entry.receipt.Attempted = true
			if binding.Scope.DocumentID != 0 {
				entry.receipt.Scope = binding.Scope.Clone()
			}
			entry.lease = binding.Lease
			entry.receipt.Error = binding.Error
			if err != nil {
				entry.receipt.Error = err.Error()
				lastErr = err
			}
		}
		return outcome{members: &memberOutcome{bindings: b}, err: lastErr}
	})
}

func validMemberStatus(r contract.KVMemberResult, scope contract.KVMemberScope) bool {
	v := r.Result
	return reflect.DeepEqual(r.Scope, scope) && r.Error == "" && v.Attempted && v.Completed && !v.WriteOutcomeUnknown && v.NativeCaptured && v.JSON != "" && v.JSONCaptureError == "" && v.ProjectionError == "" && v.Reply != nil && v.Reply.Kind == "status" && v.Reply.Status != nil && v.Reply.Status.Version == scope.Version && v.Header.ClusterID == scope.Original.Header.ClusterID && v.Header.MemberID == scope.Member.ID && v.Header.RaftTerm != 0 && v.Reply.Status.IsLearner == scope.Member.IsLearner
}

func memberBindingsReady(d *Document, b *memberBindings) bool {
	if b == nil || d.members == nil || d.members.bindings != b || b.lease != d.lease || b.observation != d.members.observation || b.authored != d.Request() || !memberDirectoryReady(d) {
		return false
	}
	targets, err := memberTargets(b.authored)
	if err != nil || len(targets) == 0 || len(targets) != len(b.entries) {
		return false
	}
	for i, entry := range b.entries {
		r := entry.receipt
		if !r.Attempted || r.Error != "" || entry.lease == nil || entry.lease.CheckMemberLease() != nil || r.Target != targets[i] || r.Scope.Member.IsLearner || !reflect.DeepEqual(entry.lease.Scope(), r.Scope) || !reflect.DeepEqual(r.Scope.Directory, *d.members.directory) || !validMemberStatus(contract.KVMemberResult{Scope: r.Scope, Result: r.Scope.Handshake}, r.Scope) {
			return false
		}
	}
	return true
}

func (s *State) ObserveMemberStatus(d *Document, index int) {
	if !s.check(d) || d.members == nil || d.members.bindings == nil {
		return
	}
	b := d.members.bindings
	if index < 0 || index >= len(b.entries) || b.entries[index].lease == nil || !s.memberRoom(d, 192<<20) {
		return
	}
	lease := b.entries[index].lease
	s.start(d, "member-status", d.Request(), func(ctx context.Context) outcome {
		r, err := lease.ObserveStatus(ctx)
		if err != nil {
			r.Error = err.Error()
		}
		return outcome{members: &memberOutcome{statuses: []contract.KVMemberResult{r.Clone()}}, err: err}
	})
}

func (s *State) PrepareMemberBatch(d *Document) {
	if !s.check(d) || d.hasUnknownWrites() {
		return
	}
	m := d.memberState()
	if !memberBindingsReady(d, m.bindings) {
		d.error = locales.Text("请先检查所选原成员")
		return
	}
	b := m.bindings
	p := &memberConfirmation{bindings: b, receipt: MemberBatchReceipt{DocumentID: d.id, Scope: d.scope.Clone(), Authored: d.Request()}}
	for _, entry := range b.entries {
		f, ok := entry.lease.(contract.KVMemberFollowerPreparation)
		last, finalOK := entry.lease.(contract.KVMemberFinalPreparation)
		if !ok || !finalOK {
			d.error = locales.Text("此连接不支持受检成员批次")
			return
		}
		follower, err := f.PrepareFollowerDefragment()
		if err != nil || follower == nil || !follower.Writes() || follower.Snapshot() != memberDefragment {
			d.error = locales.Text("KV 请求无效")
			return
		}
		final, err := last.PrepareFinalDefragment()
		if err != nil || final == nil || !final.Writes() || final.Snapshot() != memberDefragment {
			d.error = locales.Text("KV 请求无效")
			return
		}
		p.followers = append(p.followers, follower)
		p.finals = append(p.finals, final)
		p.receipt.Targets = append(p.receipt.Targets, MemberBatchTarget{Scope: entry.receipt.Scope.Clone(), Request: memberDefragment, State: "not-executed"})
	}
	if !memberArchiveRoom(memberArchiveFor(d), nil, &p.receipt, 384<<20) {
		d.error = locales.Text("完整成员记录超过限额")
		return
	}
	d.error = ""
	d.clearConfirmation()
	d.confirmation = &confirmation{lease: d.lease, authored: d.Request(), editor: d.Request(), members: p, open: true}
	d.permit = nil
}

func validMemberConfirmation(d *Document, p *confirmation) bool {
	m := p.members
	if m == nil || !memberBindingsReady(d, m.bindings) || m.receipt.DocumentID != d.id || m.receipt.Authored != d.Request() || !sameEventScope(m.receipt.Scope, d.scope) || len(m.receipt.Targets) != len(m.bindings.entries) || len(m.followers) != len(m.receipt.Targets) || len(m.finals) != len(m.receipt.Targets) {
		return false
	}
	for i, entry := range m.bindings.entries {
		if !reflect.DeepEqual(m.receipt.Targets[i].Scope, entry.receipt.Scope) || m.receipt.Targets[i].Request != memberDefragment || m.followers[i].Snapshot() != memberDefragment || m.finals[i].Snapshot() != memberDefragment {
			return false
		}
	}
	return true
}

func memberOrder(round MemberOrderingRound, targets []MemberBatchTarget) (int, error) {
	if len(round.Statuses) != len(round.Remaining) {
		return -1, contract.ErrKVMemberOrderUnknown
	}
	var leader, term uint64
	for i, r := range round.Statuses {
		var target *MemberBatchTarget
		for j := range targets {
			if targets[j].Scope.Member.ID == round.Remaining[i] {
				target = &targets[j]
				break
			}
		}
		if target == nil || !validMemberStatus(r, target.Scope) {
			return -1, contract.ErrKVMemberOrderUnknown
		}
		status := r.Result.Reply.Status
		if status.Leader == 0 || i != 0 && (status.Leader != leader || r.Result.Header.RaftTerm != term) {
			return -1, contract.ErrKVMemberOrderUnknown
		}
		leader, term = status.Leader, r.Result.Header.RaftTerm
		known := false
		for _, member := range target.Scope.Directory.Members {
			if member.ID == leader && !member.IsLearner {
				known = true
				break
			}
		}
		if !known {
			return -1, contract.ErrKVMemberOrderUnknown
		}
	}
	for _, id := range round.Remaining {
		if id != leader {
			for i := range targets {
				if targets[i].Scope.Member.ID == id {
					return i, nil
				}
			}
		}
	}
	if len(round.Remaining) == 1 {
		for i := range targets {
			if targets[i].Scope.Member.ID == leader {
				return i, nil
			}
		}
	}
	return -1, contract.ErrKVMemberOrderUnknown
}

func (s *State) executeMemberBatch(d *Document, c *confirmation) {
	p := c.members
	batch, archive := p.receipt.Clone(), memberArchiveFor(d)
	entries := slices.Clone(p.bindings.entries)
	followers, finals := slices.Clone(p.followers), slices.Clone(p.finals)
	d.clearConfirmation()
	s.start(d, "member-batch", c.authored, func(ctx context.Context) outcome {
		var lastErr error
		for {
			round := MemberOrderingRound{}
			for _, target := range batch.Targets {
				if target.State == "not-executed" {
					round.Remaining = append(round.Remaining, target.Scope.Member.ID)
				}
			}
			if len(round.Remaining) == 0 {
				break
			}
			for _, id := range round.Remaining {
				if err := ctx.Err(); err != nil {
					lastErr = err
					break
				}
				withRound := batch
				withRound.Rounds = append(slices.Clone(batch.Rounds), round)
				if !memberArchiveRoom(archive, nil, &withRound, 192<<20) {
					lastErr = jsondocument.ErrBounds
					break
				}
				for i, entry := range entries {
					if batch.Targets[i].Scope.Member.ID == id {
						r, err := entry.lease.ObserveStatus(ctx)
						if err != nil {
							r.Error = err.Error()
						}
						round.Statuses = append(round.Statuses, r.Clone())
						if err != nil {
							lastErr = err
						}
						break
					}
				}
				if lastErr != nil {
					break
				}
			}
			chosen := -1
			if lastErr == nil {
				chosen, lastErr = memberOrder(round, batch.Targets)
			}
			if lastErr != nil {
				round.Error = lastErr.Error()
				batch.Rounds = append(batch.Rounds, round)
				break
			}
			round.Chosen = batch.Targets[chosen].Scope.Member.ID
			batch.Rounds = append(batch.Rounds, round)
			if err := ctx.Err(); err != nil {
				lastErr = err
				break
			}
			if !memberArchiveRoom(archive, nil, &batch, 384<<20) {
				lastErr = jsondocument.ErrBounds
				break
			}
			plan := followers[chosen]
			if len(round.Remaining) == 1 {
				plan = finals[chosen]
			}
			r, err := entries[chosen].lease.ExecuteMember(ctx, plan)
			if !reflect.DeepEqual(r.Scope, batch.Targets[chosen].Scope) || r.Result.Request != memberDefragment {
				err = errors.Join(err, contract.ErrRevoked)
			}
			if err != nil {
				r.Error = err.Error()
			}
			original := r.Clone()
			batch.Targets[chosen].Result = &original
			state := "failed"
			if r.Result.WriteOutcomeUnknown {
				state = "unknown"
			} else if r.Result.Completed {
				state = "succeeded"
			}
			batch.Targets[chosen].State = state
			// Even a completed native ACK can arrive with a publication error;
			// preserve its success, then stop rather than operating another target.
			if err != nil || !r.Result.Completed || r.Result.WriteOutcomeUnknown || !r.Result.NativeCaptured || r.Result.JSONCaptureError != "" || r.Result.ProjectionError != "" {
				lastErr = err
				if lastErr == nil {
					lastErr = errors.New("original member step did not complete")
				}
				break
			}
		}
		batch.Ended = true
		if lastErr != nil {
			batch.Error = lastErr.Error()
		}
		return outcome{members: &memberOutcome{batch: &batch}, err: lastErr}
	})
}

func (s *State) consumeMembers(d *Document, job *operation, out outcome) {
	r := out.members
	if r == nil {
		return
	}
	m := d.memberState()
	if r.directory != nil {
		m.showResults = false
		s.MembersSplitPercent = 60
		d.releaseMemberAuthority()
		copy := r.directory.Clone()
		m.directory = &copy
		if copy.DocumentID == d.id && sameEventScope(copy.Scope, job.scope) && copy.Error == "" && job.lease == d.lease && !job.canceled {
			m.observation = r.observation
		} else if r.observation != nil {
			_ = r.observation.Close()
		}
		m.row = -1
	}
	if r.bindings != nil {
		closeMemberBindings(m.bindings)
		m.bindings = r.bindings
		for _, entry := range r.bindings.entries {
			m.history = append(m.history, entry.receipt.Clone())
		}
		if job.lease != d.lease || job.canceled {
			closeMemberBindings(m.bindings)
			m.bindings = nil
		}
	}
	for _, status := range r.statuses {
		if status.Scope.DocumentID != d.id || !sameEventScope(status.Scope.Original, job.scope) {
			continue
		}
		found := false
		for i := range m.statuses {
			if m.statuses[i].Scope.LeaseID == status.Scope.LeaseID && sameEventScope(m.statuses[i].Scope.Original, status.Scope.Original) {
				m.statuses[i] = status.Clone()
				found = true
				break
			}
		}
		if !found {
			m.statuses = append(m.statuses, status.Clone())
		}
		m.showResults, m.recordView = true, 4
		s.MembersSplitPercent = 30
	}
	if r.batch != nil && r.batch.DocumentID == d.id && sameEventScope(r.batch.Scope, job.scope) && r.batch.Authored == job.authored {
		copy := r.batch.Clone()
		m.batch = &copy
		m.showResults, m.recordView = true, 1
		m.resultRow = 0
		s.MembersSplitPercent = 30
		for _, target := range copy.Targets {
			if target.Result != nil && target.Result.Result.WriteOutcomeUnknown && m.unknown == nil {
				unknown := target.Result.Clone()
				m.unknown = &unknown
			}
		}
		for i, target := range copy.Targets {
			if target.State == "unknown" || target.State == "failed" {
				m.resultRow = i
				break
			}
		}
	}
	m.revision++
	d.permit = nil
}

func (s *State) consumeMemberRecovery(d *Document, job *operation, results []contract.KVMemberResult) {
	if len(results) == 0 {
		return
	}
	m := d.memberState()
	for _, r := range results {
		if r.Scope.DocumentID != d.id || !sameEventScope(r.Scope.Original, job.scope) {
			continue
		}
		found := false
		for _, old := range m.recovered {
			if reflect.DeepEqual(old, r) {
				found = true
				break
			}
		}
		if !found {
			m.recovered = append(m.recovered, r.Clone())
		}
		if r.Result.WriteOutcomeUnknown && m.unknown == nil {
			unknown := r.Clone()
			m.unknown = &unknown
		}
	}
	m.revision++
	d.permit = nil
}
