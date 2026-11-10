package kvworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type membersTestObservation struct {
	directory contract.KVMemberDirectory
	closed    atomic.Bool
}

func (o *membersTestObservation) Snapshot() contract.KVMemberDirectory { return o.directory.Clone() }
func (o *membersTestObservation) Close() error                         { o.closed.Store(true); return nil }

type membersTestLease struct {
	*fakeLease
	document         uint64
	leader           atomic.Uint64
	sequence         atomic.Uint64
	observes, binds  atomic.Int32
	mu               sync.Mutex
	children         []*membersTestChild
	written          []uint64
	directory        contract.KVMemberDirectory
	mode             map[uint64]string
	entered, release chan struct{}
	observation      *membersTestObservation
}

func (l *membersTestLease) ObserveMembers(context.Context) (contract.KVMemberObservation, error) {
	l.observes.Add(1)
	l.observation = &membersTestObservation{directory: l.directory.Clone()}
	return l.observation, nil
}
func (l *membersTestLease) BindMember(_ context.Context, o contract.KVMemberObservation, target contract.KVMemberTarget) (contract.KVMemberBinding, error) {
	l.binds.Add(1)
	if o != l.observation || l.observation.closed.Load() {
		return contract.KVMemberBinding{}, contract.ErrKVPlanUsed
	}
	var member contract.KVMember
	for _, m := range l.directory.Members {
		if m.ID == target.MemberID {
			member = m.Clone()
			break
		}
	}
	scope := contract.KVMemberScope{Original: l.Scope(), DocumentID: l.document, LeaseID: l.sequence.Add(1), Directory: o.Snapshot(), Member: member, Endpoint: target.Endpoint, Version: target.Version}
	child := &membersTestChild{parent: l, scope: scope}
	scope.Handshake = child.status().Result
	child.scope = scope.Clone()
	if strings.Contains(target.Endpoint, "rejected") || target.Version != l.scope.Version {
		scope.Handshake.Completed = false
		return contract.KVMemberBinding{Scope: scope, Error: "actual candidate identity rejected"}, errors.New("actual candidate identity rejected")
	}
	l.mu.Lock()
	l.children = append(l.children, child)
	l.mu.Unlock()
	return contract.KVMemberBinding{Lease: child, Scope: scope}, nil
}

type membersTestChild struct {
	parent          *membersTestLease
	scope           contract.KVMemberScope
	closed          atomic.Bool
	prepares, reads atomic.Int32
	mu              sync.Mutex
	last            contract.KVMemberResult
}

func (l *membersTestChild) Scope() contract.KVMemberScope { return l.scope.Clone() }
func (l *membersTestChild) CheckMemberLease() error {
	if l.closed.Load() {
		return contract.ErrRevoked
	}
	return l.parent.CheckKVLease()
}
func (l *membersTestChild) status() contract.KVMemberResult {
	h := l.scope.Original.Header
	h.MemberID = l.scope.Member.ID
	status := &contract.KVStatus{Version: l.scope.Version, Leader: l.parent.leader.Load(), IsLearner: l.scope.Member.IsLearner, RaftTerm: h.RaftTerm}
	r := contract.KVResult{Request: contract.KVRequest{Method: "/etcdserverpb.Maintenance/Status", JSON: "{}"}, Attempted: true, Completed: true, Code: "OK", Header: h, NativeCaptured: true, Native: []byte{1, 0, 255}, JSON: `{"leader":"` + strconv.FormatUint(status.Leader, 10) + `"}`, Reply: &contract.KVReply{Kind: "status", Status: status}}
	return contract.KVMemberResult{Scope: l.Scope(), Sequence: uint64(l.reads.Add(1)), Result: r}
}
func (l *membersTestChild) ObserveStatus(context.Context) (contract.KVMemberResult, error) {
	r := l.status()
	if l.parent.mode[l.scope.Member.ID] == "inconsistent" {
		r.Result.Reply.Status.Leader = 0
	}
	l.mu.Lock()
	l.last = r.Clone()
	l.mu.Unlock()
	return r, nil
}

type membersTestPlan struct {
	owner           *membersTestChild
	follower, final bool
	used            atomic.Bool
}

func (*membersTestPlan) Snapshot() contract.KVRequest { return memberDefragment }
func (*membersTestPlan) Writes() bool                 { return true }
func (l *membersTestChild) PrepareDefragment() (contract.KVMemberPreparation, error) {
	l.prepares.Add(1)
	return &membersTestPlan{owner: l}, nil
}
func (l *membersTestChild) PrepareFollowerDefragment() (contract.KVMemberPreparation, error) {
	l.prepares.Add(1)
	return &membersTestPlan{owner: l, follower: true}, nil
}
func (l *membersTestChild) PrepareFinalDefragment() (contract.KVMemberPreparation, error) {
	l.prepares.Add(1)
	return &membersTestPlan{owner: l, final: true}, nil
}
func (l *membersTestChild) ExecuteMember(ctx context.Context, prepared contract.KVMemberPreparation) (r contract.KVMemberResult, err error) {
	p := prepared.(*membersTestPlan)
	if p.owner != l || !p.used.CompareAndSwap(false, true) {
		return r, contract.ErrKVPlanUsed
	}
	mode := l.parent.mode[l.scope.Member.ID]
	if mode == "became-leader" {
		l.parent.leader.Store(l.scope.Member.ID)
	}
	if mode == "no-final-leader" {
		l.parent.leader.Store(0)
	}
	b := l.status()
	r = contract.KVMemberResult{Scope: l.Scope(), Sequence: b.Sequence, DirectoryCheck: l.parent.directory.Clone(), Before: b.Result.Clone(), Result: contract.KVResult{Request: memberDefragment}}
	defer func() {
		if err != nil {
			r.Error = err.Error()
		}
		l.mu.Lock()
		l.last = r.Clone()
		l.mu.Unlock()
	}()
	if b.Result.Reply.Status.Leader == 0 {
		return r, contract.ErrKVMemberOrderUnknown
	}
	if p.follower && b.Result.Reply.Status.Leader == l.scope.Member.ID {
		return r, contract.ErrKVMemberBecameLeader
	}
	l.parent.mu.Lock()
	l.parent.written = append(l.parent.written, l.scope.Member.ID)
	l.parent.mu.Unlock()
	r.Result.Attempted = true
	if mode == "cancel" {
		close(l.parent.entered)
		<-ctx.Done()
		<-l.parent.release
		r.Result.WriteOutcomeUnknown = true
		return r, ctx.Err()
	}
	if mode == "lost" {
		r.Result.WriteOutcomeUnknown = true
		r.Result.Code = "Unavailable"
		return r, errors.New("actual original ACK lost")
	}
	if mode == "denied" {
		r.Result.Code = "PermissionDenied"
		return r, errors.New("original maintenance denied")
	}
	r.Result.Completed, r.Result.NativeCaptured = true, true
	r.Result.JSON, r.Result.Code = "{}", "OK"
	r.Result.Reply = &contract.KVReply{Kind: "defragment"}
	return r, nil
}
func (l *membersTestChild) LastMemberResult() contract.KVMemberResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last.Clone()
}
func (l *membersTestChild) Close() error { l.closed.Store(true); return nil }

func membersTestState(t *testing.T) (*State, *Document, *membersTestLease, *fakeProvider) {
	t.Helper()
	l := &membersTestLease{fakeLease: &fakeLease{scope: testScope()}, document: 1, mode: map[uint64]string{}}
	l.leader.Store(3)
	l.directory = contract.KVMemberDirectory{DocumentID: 1, Scope: l.Scope(), Result: contract.KVResult{Request: contract.KVRequest{Method: "/etcdserverpb.Cluster/MemberList", JSON: `{"linearizable":true}`}, Attempted: true, Completed: true, NativeCaptured: true, Native: []byte{0, 1, 255}, JSON: "{}", Reply: &contract.KVReply{Kind: "member_list"}}}
	for _, id := range []uint64{3, 1, 2, math.MaxUint64} {
		l.directory.Members = append(l.directory.Members, contract.KVMember{ID: id, Name: "member-" + strconv.FormatUint(id, 10), PeerURLs: []string{"http://peer:2380"}, ClientURLs: []string{"http://declared:2379"}, IsLearner: id == math.MaxUint64})
	}
	p := &fakeProvider{lease: l.fakeLease, reserve: func(uint64) (contract.KVRequestLease, error) { return l, nil }}
	s := New(Dependencies{ProviderFor: func(int64) (contract.KVRequestsProvider, error) { return p, nil }})
	d := s.Open(1, 7, DefaultMembersRequest())
	d.selectedMembers = true
	t.Cleanup(s.CloseAll)
	return s, d, l, p
}
func selectMembers(t *testing.T, s *State, d *Document, ids ...uint64) {
	t.Helper()
	for _, id := range ids {
		s.SetMemberTarget(d, contract.KVMemberTarget{MemberID: id, Endpoint: "http://explicit-" + strconv.FormatUint(id, 10) + ":2379", Version: "3.7.3"}, true)
	}
}
func bindMembers(t *testing.T, s *State, d *Document) {
	t.Helper()
	s.ObserveMembers(d)
	consume(t, s, d)
	s.BindMembers(d)
	consume(t, s, d)
	if !memberBindingsReady(d, d.members.bindings) {
		t.Fatal("original member selection did not bind", d.Error())
	}
}

func TestMembersTextOnlyFilesFullUnsignedIdentitiesAndNoCopiedAuthority(t *testing.T) {
	s, d, l, _ := membersTestState(t)
	selectMembers(t, s, d, math.MaxUint64)
	raw, err := EncodeRequest(d.Request())
	if err != nil {
		t.Fatal(err)
	}
	r, err := DecodeRequest(raw)
	if err != nil || r != d.Request() {
		t.Fatal("text-only file changed exact selected identity", err)
	}
	copy := s.Open(2, 7, r)
	s.Run(copy)
	if copy.confirmation != nil || l.binds.Load() != 0 || l.observes.Load() != 0 {
		t.Fatal("request file supplied directory authority")
	}
	for _, text := range []string{`{"targets":[{"member_id":18446744073709551615,"endpoint":"a","version":"b"}]}`, `{"targets":[{"member_id":"18446744073709551616","endpoint":"a","version":"b"}]}`, `{"targets":null}`, `{"targets":[],"receipt":{}}`, `{"targets":[],"targets":[]}`} {
		if _, err := memberTargets(contract.KVRequest{Method: MembersMethod, JSON: text}); err == nil {
			t.Fatal("ambiguous selection accepted", text)
		}
	}
	s.ObserveMembers(d)
	consume(t, s, d)
	s.BindMembers(d)
	consume(t, s, d)
	if d.members.bindings.entries[0].lease == nil {
		t.Fatal("learner Status binding unavailable")
	}
	s.Run(d)
	if d.confirmation != nil {
		t.Fatal("learner entered Defragment batch")
	}
}

func TestMembersImmutableCheckedBatchFreshFollowerOrderAndOriginalEmptyAcks(t *testing.T) {
	s, d, l, _ := membersTestState(t)
	selectMembers(t, s, d, 3, 1, 2)
	bindMembers(t, s, d)
	s.Run(d)
	s.Confirm(d, false)
	if d.Busy() || len(l.written) != 0 || d.confirmation == nil {
		t.Fatal("unchecked batch dispatched")
	}
	d.Editor.Buffer.Set(d.Request().JSON + " ")
	s.Confirm(d, true)
	if d.confirmation != nil || len(l.written) != 0 {
		t.Fatal("changed selection reused captured confirmation")
	}
	s.BindMembers(d)
	consume(t, s, d)
	s.Run(d)
	s.Confirm(d, true)
	consume(t, s, d)
	b, ok := d.MemberBatchReceipt()
	if !ok || !b.Ended || b.Error != "" || !reflect.DeepEqual(l.written, []uint64{1, 2, 3}) || len(b.Rounds) != 3 {
		t.Fatal("fresh follower ordering changed original selection", l.written, d.Error())
	}
	for _, target := range b.Targets {
		if target.State != "succeeded" || target.Result == nil || !target.Result.Result.NativeCaptured || len(target.Result.Result.Native) != 0 || target.Result.Result.Header != (contract.KVHeader{}) || !target.Result.Before.Completed {
			t.Fatal("native zero ACK/original status lost")
		}
	}
	b.Targets[0].Scope.Member.PeerURLs[0] = "changed"
	if d.members.batch.Targets[0].Scope.Member.PeerURLs[0] == "changed" {
		t.Fatal("inspection changed original confirmation scope")
	}
	raw, err := s.MembersRecord(d)
	if err != nil || !strings.Contains(string(raw), `"18446744073709551615"`) || !strings.Contains(string(raw), `"native_captured": true`) || !json.Valid(raw) {
		t.Fatal("complete archive narrowed/dropped native evidence", err)
	}
}

func TestMembersStopOnFailedUnknownOrFinalLeadershipGateAndRetainUnexecuted(t *testing.T) {
	for _, mode := range []string{"denied", "lost", "became-leader", "inconsistent", "no-final-leader"} {
		t.Run(mode, func(t *testing.T) {
			s, d, l, _ := membersTestState(t)
			ids := []uint64{3, 1, 2}
			if mode == "no-final-leader" {
				ids = []uint64{3}
				l.mode[3] = mode
			} else {
				l.mode[2] = mode
			}
			selectMembers(t, s, d, ids...)
			bindMembers(t, s, d)
			s.Run(d)
			s.Confirm(d, true)
			consume(t, s, d)
			b := d.members.batch
			if b == nil || !b.Ended || b.Error == "" {
				t.Fatal("failed batch did not retain terminal observation")
			}
			if mode == "inconsistent" {
				if len(l.written) != 0 || len(b.Rounds[0].Statuses) != 3 {
					t.Fatal("inconsistent ordering dispatched")
				}
				return
			}
			if mode == "no-final-leader" {
				if b.Targets[0].State != "failed" || b.Targets[0].Result.Result.Attempted {
					t.Fatal("last target with absent leader dispatched")
				}
				return
			}
			if b.Targets[0].State != "not-executed" || b.Targets[1].State != "succeeded" {
				t.Fatal("partial successes/unexecuted leader lost", b.Targets)
			}
			if mode == "lost" {
				if b.Targets[2].State != "unknown" || !d.hasUnknownWrites() {
					t.Fatal("missing independent member unknown")
				}
				s.ObserveMemberStatus(d, 1)
				consume(t, s, d)
				_ = d.members.bindings.entries[2].lease.Close()
				s.SetMethod(d, "/etcdserverpb.KV/Put")
				d.Editor.Buffer.Set(`{"key":"YQ==","value":"Yg=="}`)
				s.Run(d)
				if d.confirmation != nil || l.executes.Load() != 0 || !d.hasUnknownWrites() {
					t.Fatal("later Status/Close cleared unknown barrier")
				}
			} else if b.Targets[2].State != "failed" || d.hasUnknownWrites() {
				t.Fatal("definite failure became unknown")
			}
			if mode == "became-leader" && (len(l.written) != 1 || b.Targets[2].Result.Result.Attempted || !b.Targets[2].Result.Before.Completed) {
				t.Fatal("fresh leader gate lost complete observation")
			}
		})
	}
}

func TestMembersCancellationWaitsParkingAndPureRecoveryKeepsOldEvidence(t *testing.T) {
	s, d, l, p := membersTestState(t)
	l.mode[2] = "cancel"
	l.entered, l.release = make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(l.release) }) })
	selectMembers(t, s, d, 3, 1, 2)
	bindMembers(t, s, d)
	active := true
	s.deps.Active = func(*Document) bool { return active }
	s.Run(d)
	s.Confirm(d, true)
	await(t, l.entered)
	active = false
	s.RequestCancel(d)
	s.Consume()
	if !d.Busy() || d.members.batch != nil {
		t.Fatal("parking/cancel settled before original executor")
	}
	releaseOnce.Do(func() { close(l.release) })
	await(t, d.job.finished)
	s.Consume()
	if !d.Busy() || d.members.batch != nil {
		t.Fatal("parked completion entered UI")
	}
	active = true
	consume(t, s, d)
	original := d.members.unknown.Clone()
	if original.Scope.Original.Generation != 11 || d.members.batch.Targets[0].State != "not-executed" {
		t.Fatal("original parked identity/result lost")
	}
	newLease := &fakeLease{scope: testScope()}
	newLease.scope.Generation = 12
	var recovered atomic.Int32
	p.recover = func(contract.KVRequestLease) (contract.KVRecovery, error) {
		recovered.Add(1)
		return contract.KVRecovery{Lease: newLease, MemberResults: []contract.KVMemberResult{original.Clone()}}, nil
	}
	d.needsRecovery = true
	s.Recover(d)
	consume(t, s, d)
	if recovered.Load() != 1 || d.scope.Generation != 12 || len(d.members.recovered) != 1 || d.members.recovered[0].Scope.Original.Generation != 11 || !reflect.DeepEqual(*d.members.unknown, original) || d.members.observation != nil || d.members.bindings != nil || len(l.written) != 2 {
		t.Fatal("pure recovery lost old evidence or reopened/replayed member work")
	}
	if !s.GuardLeave(d, nil) {
		t.Fatal("unknown member/receipts bypassed leave guard")
	}
	s.DecideLeave(ui.Services{}, true)
	if s.GuardLeave(d, nil) {
		t.Fatal("exact leave permit not accepted")
	}
	d.members.revision++
	if !s.GuardLeave(d, nil) {
		t.Fatal("new original evidence bypassed old leave permit")
	}
	s.Close(d)
	if d.members != nil || d.Editor != nil || !l.observation.closed.Load() {
		t.Fatal("close retained member caches/authority")
	}
}

func TestMembersRejectedHandshakesRemainCompleteAcrossAnotherBinding(t *testing.T) {
	s, d, l, _ := membersTestState(t)
	s.ObserveMembers(d)
	consume(t, s, d)
	s.SetMemberTarget(d, contract.KVMemberTarget{MemberID: 1, Endpoint: "http://rejected:2379", Version: "3.7.3"}, true)
	s.BindMembers(d)
	consume(t, s, d)
	if len(d.members.history) != 1 || d.members.history[0].Error == "" || !d.members.history[0].Scope.Handshake.NativeCaptured || d.members.bindings.entries[0].lease != nil {
		t.Fatal("failed handshake omitted original evidence")
	}
	s.SetMemberTarget(d, contract.KVMemberTarget{MemberID: 1, Endpoint: "http://explicit:2379", Version: "3.7.3"}, true)
	s.BindMembers(d)
	consume(t, s, d)
	s.Run(d)
	if d.confirmation == nil || len(d.members.history) != 2 || l.binds.Load() != 2 {
		t.Fatal("new candidate rewrote earlier failed handshake")
	}
	raw, err := s.MembersRecord(d)
	if err != nil || !strings.Contains(string(raw), "actual candidate identity rejected") || !strings.Contains(string(raw), "http://rejected:2379") {
		t.Fatal("failed original handshake absent from complete archive", err)
	}
}
