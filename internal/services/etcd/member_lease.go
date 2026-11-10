package etcd

import (
	"bytes"
	"context"
	"errors"
	"math"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

const memberTransportLimit = 8

var (
	memberStatusRequest = contract.KVRequest{Method: "/etcdserverpb.Maintenance/Status", JSON: "{}"}
	memberDefragRequest = contract.KVRequest{Method: "/etcdserverpb.Maintenance/Defragment", JSON: "{}"}
)

// Receipt pointers are immutable. The parent's latest and unknown slots can
// share one original receipt without duplicating its retained byte charge.
type memberReceipt struct {
	result contract.KVMemberResult
	charge int
}

type memberLease struct {
	authority    *memberLease
	owner        *kvLease
	client       *Client // guarded by owner.ops
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.Mutex
	scope        contract.KVMemberScope
	last         *memberReceipt
	closed, lost bool
	sequence     uint64 // guarded by owner.ops
	charge       int    // immutable scope's retained bytes; guarded by owner.ops
	closeErr     error  // guarded by owner.ops
}

type memberPreparation struct {
	owner        *memberLease
	authority    *memberPreparation
	used         atomic.Bool
	followerOnly bool
	ordered      bool
}

func (*memberPreparation) Snapshot() contract.KVRequest { return memberDefragRequest }
func (*memberPreparation) Writes() bool                 { return true }

func resultBytes(r contract.KVResult) int {
	n := 1024 + len(r.Native) + len(r.JSON) + len(r.Request.Method) + len(r.Request.JSON) + len(r.Code) + len(r.JSONCaptureError) + len(r.ProjectionError)
	if r.Reply != nil && r.Reply.Status != nil {
		s := r.Reply.Status
		n += 256 + len(s.Version)
		for _, e := range s.Errors {
			n += 24 + len(e)
		}
	}
	return n
}

func memberScopeBytes(s contract.KVMemberScope) int {
	return 2048 + len(s.Original.Prefix) + len(s.Endpoint) + len(s.Version) + memberBytes(s.Member) + directoryBytes(s.Directory) + resultBytes(s.Handshake)
}

func memberResultBytes(r contract.KVMemberResult) int {
	return memberScopeBytes(r.Scope) + directoryBytes(r.DirectoryCheck) + resultBytes(r.Before) + resultBytes(r.Result) + len(r.Error) + len(r.BeforeError) + 1024
}

// Reserve before any maintenance dispatch. A successful native ACK is always
// retained; the projection/cache layer never reclassifies its write outcome.
func (l *kvLease) reserveMemberBytes(n int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.s.mu.Lock()
	defer l.s.mu.Unlock()
	if l.closed || l.lost || l.s.closed || l.s.revoked {
		return contract.ErrRevoked
	}
	if n < 0 || n > memberDocumentBytes-l.memberRetainedBytes || n > memberSessionBytes-l.s.memberRetainedBytes {
		return ErrBounds
	}
	l.memberRetainedBytes += n
	l.s.memberRetainedBytes += n
	return nil
}

func (l *kvLease) adjustMemberBytes(delta int) {
	l.mu.Lock()
	l.s.mu.Lock()
	l.memberRetainedBytes += delta
	l.s.memberRetainedBytes += delta
	l.s.mu.Unlock()
	l.mu.Unlock()
}

func (l *kvLease) BindMember(parent context.Context, observation contract.KVMemberObservation, target contract.KVMemberTarget) (binding contract.KVMemberBinding, err error) {
	if !l.ops.TryLock() {
		return binding, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	ctx, done, err := l.request(parent)
	if err != nil {
		return binding, err
	}
	defer done()
	directory, member, err := l.memberDirectory(observation, target)
	if err != nil {
		return binding, err
	}
	binding.Scope = contract.KVMemberScope{Original: l.Scope(), DocumentID: l.document, Directory: directory, Member: member, Endpoint: target.Endpoint, Version: target.Version}
	defer func() {
		if err != nil {
			binding.Error = err.Error()
		}
	}()
	if !validVersion(target.Version) || len(l.members) >= memberTransportLimit || len(l.memberLatest) >= memberTransportLimit {
		return binding, ErrBounds
	}
	if err = l.connect(ctx); err != nil {
		return binding, err
	}
	// Capture this original document's transport configuration and credentials.
	// One candidate, one handshake, no root-session opener or endpoint fallback.
	cfg := l.client.config
	cfg.ProbeKey = bytes.Clone(cfg.ProbeKey)
	cfg.BrowsePrefix = bytes.Clone(cfg.BrowsePrefix)
	cfg.DiscoverAccess = false
	cfg.Endpoint = target.Endpoint
	l.s.mu.Lock()
	secret := l.s.secret
	if l.s.memberSequence == math.MaxUint64 {
		l.s.mu.Unlock()
		return binding, ErrBounds
	}
	l.s.memberSequence++
	binding.Scope.LeaseID = l.s.memberSequence
	l.s.mu.Unlock()
	// Native Status is <=16 MiB, JSON <=32 MiB and typed strings <=16 MiB.
	reserved := memberScopeBytes(binding.Scope) + (68 << 20)
	if err = l.reserveMemberBytes(reserved); err != nil {
		return binding, err
	}
	defer func() {
		if binding.Lease == nil {
			l.adjustMemberBytes(-reserved)
		}
	}()
	c, observed, err := l.client.OpenMemberObserved(ctx, cfg.Endpoint, secret, Identity{Header: Header{ClusterID: directory.Result.Header.ClusterID, MemberID: member.ID}, Version: target.Version})
	binding.Scope.Handshake = projectKVResult(memberStatusRequest, observed)
	if err != nil {
		return binding, err
	}
	if err = l.CheckKVLease(); err == nil {
		err = ctx.Err()
	}
	status := binding.Scope.Handshake.Reply
	if err == nil && (status == nil || status.Status == nil || status.Status.IsLearner != member.IsLearner || !binding.Scope.Handshake.NativeCaptured || binding.Scope.Handshake.ProjectionError != "" || binding.Scope.Handshake.JSONCaptureError != "") {
		err = ErrProtocol
	}
	if err != nil {
		_ = c.Close()
		return binding, err
	}
	childCtx, cancel := context.WithCancel(l.ctx)
	charge := memberScopeBytes(binding.Scope)
	m := &memberLease{owner: l, client: c, ctx: childCtx, cancel: cancel, scope: binding.Scope.Clone(), charge: charge}
	m.authority = m
	if l.members == nil {
		l.members = map[*memberLease]bool{}
	}
	l.members[m] = true
	l.adjustMemberBytes(charge - reserved)
	binding.Lease = m
	return binding, nil
}

func (m *memberLease) Scope() contract.KVMemberScope {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.scope.Clone()
}

func (m *memberLease) CheckMemberLease() error {
	if m.authority != m {
		return contract.ErrKVPlanUsed
	}
	m.mu.Lock()
	closed, lost := m.closed, m.lost
	m.mu.Unlock()
	if closed {
		return contract.ErrRevoked
	}
	if lost {
		return contract.ErrKVLeaseLost
	}
	return m.owner.CheckKVLease()
}

func (m *memberLease) request(parent context.Context) (context.Context, func(), error) {
	if err := m.CheckMemberLease(); err != nil {
		return nil, nil, err
	}
	ctx, done, err := m.owner.request(parent)
	if err != nil {
		return nil, nil, err
	}
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(m.ctx, cancel)
	return child, func() { stop(); cancel(); done() }, nil
}

func (m *memberLease) PrepareDefragment() (contract.KVMemberPreparation, error) {
	return m.prepareDefragment(false, false)
}

func (m *memberLease) PrepareFollowerDefragment() (contract.KVMemberPreparation, error) {
	return m.prepareDefragment(true, true)
}

func (m *memberLease) PrepareFinalDefragment() (contract.KVMemberPreparation, error) {
	return m.prepareDefragment(false, true)
}

func (m *memberLease) prepareDefragment(followerOnly, ordered bool) (contract.KVMemberPreparation, error) {
	if err := m.CheckMemberLease(); err != nil {
		return nil, err
	}
	if m.Scope().Member.IsLearner {
		return nil, contract.ErrUnsupported
	}
	p := &memberPreparation{owner: m, followerOnly: followerOnly, ordered: ordered}
	p.authority = p
	return p, nil
}

func sameMember(a, b contract.KVMember) bool {
	return a.ID == b.ID && a.Name == b.Name && a.IsLearner == b.IsLearner && slices.Equal(a.PeerURLs, b.PeerURLs) && slices.Equal(a.ClientURLs, b.ClientURLs) && bytes.Equal(a.NativeExtensions, b.NativeExtensions)
}

// Called with parent ops held, using the original root document transport.
func (l *kvLease) freshMemberDirectory(ctx context.Context) (d contract.KVMemberDirectory, err error) {
	d.Scope, d.DocumentID = l.Scope(), l.document
	r := contract.KVRequest{Method: "/etcdserverpb.Cluster/MemberList", JSON: `{"linearizable":true}`}
	d.Result.Request = r
	defer func() {
		if err != nil {
			d.Error = err.Error()
		}
	}()
	if err = l.connect(ctx); err != nil {
		return d, err
	}
	p, err := l.client.Prepare(&pb.MemberListRequest{Linearizable: true})
	if err != nil {
		return d, err
	}
	native, err := l.client.Execute(ctx, p)
	d.Result = projectKVResult(r, native)
	l.client.releaseLastResponse()
	if d.Result.Reply != nil {
		for _, member := range d.Result.Reply.Members {
			d.Members = append(d.Members, member.Clone())
		}
	}
	if !l.client.Alive() {
		err = l.markLost(err)
	}
	if err == nil && (!d.Result.Completed || !d.Result.NativeCaptured || d.Result.Reply == nil || d.Result.Reply.Kind != "member_list" || d.Result.ProjectionError != "" || d.Result.JSONCaptureError != "") {
		err = ErrProtocol
	}
	return d, err
}

func (m *memberLease) retainOwned(r contract.KVMemberResult, reserved int) {
	l := m.owner
	charge := memberResultBytes(r)
	receipt := &memberReceipt{result: r.Clone(), charge: charge}
	if l.memberLatest == nil {
		l.memberLatest = map[uint64]*memberReceipt{}
	}
	old := l.memberLatest[r.Scope.LeaseID]
	l.memberLatest[r.Scope.LeaseID] = receipt
	if r.Result.WriteOutcomeUnknown {
		l.memberUnknown = receipt
	}
	delta := charge - reserved
	if old != nil && old != l.memberUnknown {
		delta -= old.charge
	}
	l.adjustMemberBytes(delta)
	m.mu.Lock()
	m.last = receipt
	m.mu.Unlock()
}

func (m *memberLease) finishOwned(r *contract.KVMemberResult, err error, reserved int) error {
	if err != nil {
		r.Error = err.Error()
	}
	m.retainOwned(*r, reserved)
	if m.client != nil {
		m.client.releaseLastResponse()
	}
	if m.client != nil && !m.client.Alive() {
		m.mu.Lock()
		m.lost = true
		m.mu.Unlock()
		err = errors.Join(err, contract.ErrKVLeaseLost, m.client.Close())
	}
	return err
}

func (m *memberLease) ObserveStatus(parent context.Context) (r contract.KVMemberResult, err error) {
	r.Scope, r.Result.Request = m.Scope(), memberStatusRequest
	l := m.owner
	if !l.ops.TryLock() {
		return r, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	ctx, done, err := m.request(parent)
	if err != nil {
		return r, err
	}
	defer done()
	reserved := memberScopeBytes(r.Scope) + (68 << 20) + 8192
	if err = l.reserveMemberBytes(reserved); err != nil {
		return r, err
	}
	m.sequence++
	r.Sequence = m.sequence
	defer func() { err = m.finishOwned(&r, err, reserved) }()
	p, err := m.client.Prepare(&pb.StatusRequest{})
	if err != nil {
		return r, err
	}
	native, err := m.client.Execute(ctx, p)
	r.Result = projectKVResult(memberStatusRequest, native)
	return r, err
}

func (m *memberLease) ExecuteMember(parent context.Context, prepared contract.KVMemberPreparation) (r contract.KVMemberResult, err error) {
	r.Scope, r.Result.Request = m.Scope(), memberDefragRequest
	p, ok := prepared.(*memberPreparation)
	if !ok || p == nil || p.owner != m || p.authority != p {
		return r, contract.ErrKVPlanUsed
	}
	l := m.owner
	if !l.ops.TryLock() {
		return r, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	ctx, done, err := m.request(parent)
	if err != nil {
		return r, err
	}
	defer done()
	if l.writeUnknown || l.memberUnknown != nil {
		return r, contract.ErrKVWriteUnknown
	}
	if r.Scope.Member.IsLearner {
		return r, contract.ErrUnsupported
	}
	if p.used.Load() {
		return r, contract.ErrKVPlanUsed
	}
	// Full fresh directory (native/JSON/two member projections), fresh Status
	// and native ACK fit within 200 MiB plus the captured original scope.
	reserved := memberScopeBytes(r.Scope) + (200 << 20)
	if err = l.reserveMemberBytes(reserved); err != nil {
		return r, err
	}
	if !p.used.CompareAndSwap(false, true) {
		l.adjustMemberBytes(-reserved)
		return r, contract.ErrKVPlanUsed
	}
	m.sequence++
	r.Sequence = m.sequence
	defer func() { err = m.finishOwned(&r, err, reserved) }()
	r.DirectoryCheck, err = l.freshMemberDirectory(ctx)
	if err != nil {
		return r, err
	}
	found := false
	for _, member := range r.DirectoryCheck.Members {
		if member.ID == r.Scope.Member.ID {
			found = sameMember(member, r.Scope.Member)
			break
		}
	}
	if !found {
		return r, contract.ErrRevoked
	}
	if err = ctx.Err(); err != nil {
		return r, err
	}
	nativePlan, err := m.client.Prepare(&pb.DefragmentRequest{})
	if err != nil {
		return r, err
	}
	nativePlan.followerOnly = p.followerOnly
	if p.ordered {
		for _, member := range r.DirectoryCheck.Members {
			if !member.IsLearner {
				nativePlan.leaders = append(nativePlan.leaders, member.ID)
			}
		}
	}
	native, err := m.client.Execute(ctx, nativePlan)
	r.Result = projectKVResult(memberDefragRequest, native)
	if before, ok := native.Before(); ok {
		r.Before = projectKVResult(memberStatusRequest, before)
		if !before.Completed && err != nil {
			r.BeforeError = err.Error()
		}
	}
	return r, err
}

func (m *memberLease) LastMemberResult() contract.KVMemberResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.last == nil {
		return contract.KVMemberResult{}
	}
	return m.last.result.Clone()
}

func (m *memberLease) closeOwned() error {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	m.last = nil
	m.scope = contract.KVMemberScope{}
	m.mu.Unlock()
	if m.client != nil {
		m.closeErr = errors.Join(m.closeErr, m.client.Close())
		m.client = nil
	}
	if m.charge != 0 {
		m.owner.adjustMemberBytes(-m.charge)
		m.charge = 0
	}
	delete(m.owner.members, m)
	return m.closeErr
}

func (m *memberLease) Close() error {
	if m.authority != m {
		return contract.ErrKVPlanUsed
	}
	m.mu.Lock()
	m.closed = true
	m.cancel()
	m.mu.Unlock()
	m.owner.ops.Lock()
	defer m.owner.ops.Unlock()
	return m.closeOwned()
}

func (l *kvLease) closeMembersOwned() {
	for m := range l.members {
		l.closeErr = errors.Join(l.closeErr, m.closeOwned())
	}
}

func (l *kvLease) memberResultsOwned() []contract.KVMemberResult {
	var out []contract.KVMemberResult
	var ids []uint64
	for id := range l.memberLatest {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	unknownIncluded := false
	for _, id := range ids {
		r := l.memberLatest[id]
		out = append(out, r.result.Clone())
		unknownIncluded = unknownIncluded || r == l.memberUnknown
	}
	if l.memberUnknown != nil && !unknownIncluded {
		out = append(out, l.memberUnknown.result.Clone())
	}
	return out
}

func (l *kvLease) releaseMemberReceiptsOwned() {
	charge := 0
	unknownIncluded := false
	for _, r := range l.memberLatest {
		charge += r.charge
		unknownIncluded = unknownIncluded || r == l.memberUnknown
	}
	if l.memberUnknown != nil && !unknownIncluded {
		charge += l.memberUnknown.charge
	}
	l.adjustMemberBytes(-charge)
	l.memberLatest, l.memberUnknown = nil, nil
}

var _ contract.KVMembers = (*kvLease)(nil)
var _ contract.KVMemberLease = (*memberLease)(nil)
