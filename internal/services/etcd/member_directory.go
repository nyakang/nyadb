package etcd

import (
	"context"
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

// These caches are separate from the existing event/renewal budgets. Every
// member record retains its complete native and bounded JSON representations.
const (
	memberDocumentBytes    = 256 << 20
	memberSessionBytes     = 512 << 20
	memberObservationLimit = 8
)

type memberObservation struct {
	owner           *kvLease
	snapshot        contract.KVMemberDirectory
	charge          int
	closed, revoked bool
}

func (o *memberObservation) Snapshot() contract.KVMemberDirectory {
	o.owner.mu.Lock()
	defer o.owner.mu.Unlock()
	return o.snapshot.Clone()
}

func (o *memberObservation) Close() error {
	l := o.owner
	l.mu.Lock()
	defer l.mu.Unlock()
	if o.closed {
		return nil
	}
	o.closed = true
	if l.memberObservations[o] {
		delete(l.memberObservations, o)
		l.s.mu.Lock()
		l.s.memberRetainedBytes -= o.charge
		l.s.mu.Unlock()
		l.memberRetainedBytes -= o.charge
	}
	o.charge = 0
	o.snapshot = contract.KVMemberDirectory{}
	return nil
}

// Revocation removes authority and the parent's caches. A handle already
// delivered to a caller retains its original read evidence until explicit Close.
func (l *kvLease) revokeMemberObservations() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for o := range l.memberObservations {
		o.revoked = true
		l.s.mu.Lock()
		l.s.memberRetainedBytes -= o.charge
		l.s.mu.Unlock()
		l.memberRetainedBytes -= o.charge
		o.charge = 0
	}
	l.memberObservations = nil
}

func memberBytes(m contract.KVMember) int {
	n := 128 + len(m.Name) + len(m.NativeExtensions)
	for _, v := range m.PeerURLs {
		n += 24 + len(v)
	}
	for _, v := range m.ClientURLs {
		n += 24 + len(v)
	}
	return n
}

func directoryBytes(d contract.KVMemberDirectory) int {
	r := d.Result
	n := 1024 + len(d.Scope.Prefix) + len(d.Error) + len(r.Native) + len(r.JSON) + len(r.Request.JSON) + len(r.Request.Method) + len(r.JSONCaptureError) + len(r.ProjectionError)
	for _, m := range d.Members {
		n += memberBytes(m)
	}
	if r.Reply != nil {
		for _, m := range r.Reply.Members {
			n += memberBytes(m)
		}
	}
	return n
}

// ObserveMembers is a read on the original document transport. It never changes
// the original ordinary KV LastResult, including an uncertain write receipt.
func (l *kvLease) ObserveMembers(parent context.Context) (contract.KVMemberObservation, error) {
	if !l.ops.TryLock() {
		return nil, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	ctx, done, err := l.request(parent)
	if err != nil {
		return nil, err
	}
	defer done()
	l.mu.Lock()
	full := len(l.memberObservations) >= memberObservationLimit
	l.mu.Unlock()
	if full {
		return nil, ErrBounds
	}
	d := contract.KVMemberDirectory{Scope: l.Scope(), DocumentID: l.document}
	request := contract.KVRequest{Method: "/etcdserverpb.Cluster/MemberList", JSON: `{"linearizable":true}`}
	d.Result.Request = request
	if err = l.connect(ctx); err == nil {
		var plan Plan
		plan, err = l.client.Prepare(&pb.MemberListRequest{Linearizable: true})
		if err == nil {
			var native Response
			native, err = l.client.Execute(ctx, plan)
			d.Result = projectKVResult(request, native)
			l.client.releaseLastResponse()
			if d.Result.Reply != nil && d.Result.Reply.Kind == "member_list" {
				for _, member := range d.Result.Reply.Members {
					d.Members = append(d.Members, member.Clone())
				}
			}
			if !l.client.Alive() {
				err = l.markLost(err)
			}
		}
	}
	if err != nil {
		d.Error = err.Error()
	}
	o := &memberObservation{owner: l, snapshot: d}
	charge := directoryBytes(d)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.lost {
		o.revoked = true
		return o, errors.Join(err, contract.ErrRevoked)
	}
	l.s.mu.Lock()
	defer l.s.mu.Unlock()
	if l.s.closed || l.s.revoked {
		o.revoked = true
		return o, errors.Join(err, contract.ErrRevoked)
	}
	if charge > memberDocumentBytes-l.memberRetainedBytes || charge > memberSessionBytes-l.s.memberRetainedBytes {
		o.revoked = true
		return o, errors.Join(err, ErrBounds)
	}
	if l.memberObservations == nil {
		l.memberObservations = map[*memberObservation]bool{}
	}
	o.charge = charge
	l.memberObservations[o] = true
	l.memberRetainedBytes += charge
	l.s.memberRetainedBytes += charge
	return o, err
}

func (l *kvLease) memberDirectory(observation contract.KVMemberObservation, target contract.KVMemberTarget) (contract.KVMemberDirectory, contract.KVMember, error) {
	if err := l.CheckKVLease(); err != nil {
		return contract.KVMemberDirectory{}, contract.KVMember{}, err
	}
	o, ok := observation.(*memberObservation)
	if !ok || o == nil || o.owner != l {
		return contract.KVMemberDirectory{}, contract.KVMember{}, contract.ErrKVPlanUsed
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	d := o.snapshot
	if o.closed || o.revoked || !l.memberObservations[o] || l.closed || l.lost || !d.Result.Completed || !d.Result.NativeCaptured || d.Result.ProjectionError != "" || d.Result.JSONCaptureError != "" || d.Error != "" || d.Result.Reply == nil || d.Result.Reply.Kind != "member_list" {
		return contract.KVMemberDirectory{}, contract.KVMember{}, contract.ErrRevoked
	}
	for _, member := range d.Members {
		if member.ID == target.MemberID {
			return d.Clone(), member.Clone(), nil
		}
	}
	return contract.KVMemberDirectory{}, contract.KVMember{}, ErrInvalid
}
