package dbruntime

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// Each wrapper embeds a concrete supported combination, never optional
// interfaces that would advertise missing provider capabilities.
type kvMembersLease struct {
	*kvRequestLease
	*kvMemberFacet
}
type kvMembersAuthLease struct {
	*kvAuthLease
	*kvMemberFacet
}
type kvMembersBrowsingLease struct {
	*kvLeaseBrowsingLease
	*kvMemberFacet
}
type kvMembersAuthBrowsingLease struct {
	*kvAuthBrowsingLease
	*kvMemberFacet
}
type kvMembersMaintenanceLease struct {
	*kvMaintenanceLease
	*kvMemberFacet
}
type kvMembersMaintenanceAuthLease struct {
	*kvMaintenanceAuthLease
	*kvMemberFacet
}
type kvMembersMaintenanceBrowsingLease struct {
	*kvMaintenanceBrowsingLease
	*kvMemberFacet
}
type kvMembersMaintenanceAuthBrowsingLease struct {
	*kvMaintenanceAuthBrowsingLease
	*kvMemberFacet
}

type kvMemberFacet struct{ base *kvRequestLease }

func bindKVMembers(bound contract.KVRequestLease) contract.KVRequestLease {
	base := originalKVRequestLease(bound)
	if base == nil {
		return bound
	}
	if _, ok := base.raw.(contract.KVMembers); !ok {
		return bound
	}
	f := &kvMemberFacet{base}
	switch l := bound.(type) {
	case *kvRequestLease:
		return &kvMembersLease{l, f}
	case *kvAuthLease:
		return &kvMembersAuthLease{l, f}
	case *kvLeaseBrowsingLease:
		return &kvMembersBrowsingLease{l, f}
	case *kvAuthBrowsingLease:
		return &kvMembersAuthBrowsingLease{l, f}
	case *kvMaintenanceLease:
		return &kvMembersMaintenanceLease{l, f}
	case *kvMaintenanceAuthLease:
		return &kvMembersMaintenanceAuthLease{l, f}
	case *kvMaintenanceBrowsingLease:
		return &kvMembersMaintenanceBrowsingLease{l, f}
	case *kvMaintenanceAuthBrowsingLease:
		return &kvMembersMaintenanceAuthBrowsingLease{l, f}
	}
	return bound
}

func (l *kvRequestLease) translateMemberOriginal(s *contract.KVScope) bool {
	if s.Namespace != l.scope.Namespace || s.Generation != l.session.raw.Generation() || !bytes.Equal(s.Prefix, l.scope.Prefix) || s.Header != l.scope.Header || s.Version != l.scope.Version {
		return false
	}
	s.Generation = l.scope.Generation
	return true
}

func (l *kvRequestLease) translateMemberDirectory(d *contract.KVMemberDirectory) bool {
	return d.DocumentID == l.document && l.translateMemberOriginal(&d.Scope)
}

func (l *kvRequestLease) translateMemberScope(s *contract.KVMemberScope) bool {
	a := l.translateMemberOriginal(&s.Original)
	b := l.translateMemberDirectory(&s.Directory)
	return a && b && s.DocumentID == l.document
}

func (l *kvRequestLease) translateMemberResult(r *contract.KVMemberResult) bool {
	ok := l.translateMemberScope(&r.Scope)
	if r.DirectoryCheck.DocumentID != 0 {
		ok = l.translateMemberDirectory(&r.DirectoryCheck) && ok
	}
	return ok
}

type kvMemberObservation struct {
	authority *kvMemberObservation
	owner     *kvRequestLease
	raw       contract.KVMemberObservation
	closed    atomic.Bool
	once      sync.Once
	closeErr  error
}

func (o *kvMemberObservation) Snapshot() contract.KVMemberDirectory {
	d := o.raw.Snapshot().Clone()
	if !o.owner.translateMemberDirectory(&d) && d.Error == "" {
		d.Error = contract.ErrRevoked.Error()
	}
	return d
}

func (o *kvMemberObservation) Close() error {
	if o.authority != o {
		return contract.ErrKVPlanUsed
	}
	o.closed.Store(true)
	o.owner.membersMu.Lock()
	delete(o.owner.memberObservations, o)
	o.owner.membersMu.Unlock()
	o.once.Do(func() { o.closeErr = o.raw.Close() })
	return o.closeErr
}

func (f *kvMemberFacet) ObserveMembers(ctx context.Context) (contract.KVMemberObservation, error) {
	l := f.base
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	raw, err := l.raw.(contract.KVMembers).ObserveMembers(ctx)
	if raw == nil {
		return nil, err
	}
	o := &kvMemberObservation{owner: l, raw: raw}
	o.authority = o
	d := raw.Snapshot().Clone()
	if !l.translateMemberDirectory(&d) {
		err = errors.Join(err, contract.ErrRevoked)
	}
	err = errors.Join(err, l.CheckKVLease())
	if err == nil {
		l.membersMu.Lock()
		if l.memberObservations == nil {
			l.memberObservations = map[*kvMemberObservation]bool{}
		}
		l.memberObservations[o] = true
		l.membersMu.Unlock()
	}
	return o, err
}

func (f *kvMemberFacet) BindMember(ctx context.Context, observation contract.KVMemberObservation, target contract.KVMemberTarget) (contract.KVMemberBinding, error) {
	l := f.base
	if err := l.CheckKVLease(); err != nil {
		return contract.KVMemberBinding{}, err
	}
	o, ok := observation.(*kvMemberObservation)
	if !ok || o == nil || o.owner != l {
		return contract.KVMemberBinding{}, contract.ErrKVPlanUsed
	}
	l.membersMu.Lock()
	valid := l.memberObservations[o] && !o.closed.Load()
	l.membersMu.Unlock()
	if !valid {
		return contract.KVMemberBinding{}, contract.ErrRevoked
	}
	d := o.Snapshot()
	if d.DocumentID != l.document || d.Scope.Generation != l.scope.Generation || d.Error != "" {
		return contract.KVMemberBinding{}, contract.ErrRevoked
	}
	var selected *contract.KVMember
	for i := range d.Members {
		if d.Members[i].ID == target.MemberID {
			selected = &d.Members[i]
			break
		}
	}
	if selected == nil {
		return contract.KVMemberBinding{}, contract.ErrKVPlanUsed
	}
	binding, err := l.raw.(contract.KVMembers).BindMember(ctx, o.raw, target)
	binding.Scope = binding.Scope.Clone()
	if !l.translateMemberScope(&binding.Scope) {
		err = errors.Join(err, contract.ErrRevoked)
	}
	err = errors.Join(err, l.CheckKVLease())
	if binding.Lease != nil {
		rawScope := binding.Lease.Scope().Clone()
		if !l.translateMemberScope(&rawScope) || !reflect.DeepEqual(rawScope, binding.Scope) || rawScope.LeaseID == 0 || !reflect.DeepEqual(rawScope.Member, *selected) || !reflect.DeepEqual(rawScope.Directory, d) || rawScope.Endpoint != target.Endpoint || rawScope.Version != target.Version {
			err = errors.Join(err, contract.ErrRevoked)
		}
		if err != nil {
			err = errors.Join(err, binding.Lease.Close())
			binding.Lease = nil
		} else {
			member := &kvMemberLease{owner: l, raw: binding.Lease, scope: binding.Scope.Clone()}
			member.authority = member
			binding.Lease = member
			if _, ok := member.raw.(contract.KVMemberFollowerPreparation); ok {
				binding.Lease = &kvMemberFollowerLease{member}
			}
		}
	}
	if err != nil {
		binding.Error = err.Error()
	}
	return binding, err
}

type kvMemberLease struct {
	authority *kvMemberLease
	owner     *kvRequestLease
	raw       contract.KVMemberLease
	scope     contract.KVMemberScope
	closed    atomic.Bool
	once      sync.Once
	closeErr  error
}

type kvMemberFollowerLease struct{ *kvMemberLease }

func (l *kvMemberFollowerLease) PrepareFollowerDefragment() (contract.KVMemberPreparation, error) {
	return l.prepareDefragment(true)
}

type kvMemberPreparation struct {
	owner   *kvMemberLease
	raw     contract.KVMemberPreparation
	request contract.KVRequest
}

func (p *kvMemberPreparation) Snapshot() contract.KVRequest { return p.request }
func (*kvMemberPreparation) Writes() bool                   { return true }
func (l *kvMemberLease) Scope() contract.KVMemberScope      { return l.scope.Clone() }
func (l *kvMemberLease) CheckMemberLease() error {
	if l.authority != l {
		return contract.ErrKVPlanUsed
	}
	if l.closed.Load() {
		return contract.ErrRevoked
	}
	if err := l.owner.CheckKVLease(); err != nil {
		return err
	}
	return l.raw.CheckMemberLease()
}

func (l *kvMemberLease) translateResult(r *contract.KVMemberResult) bool {
	return l.owner.translateMemberResult(r) && reflect.DeepEqual(r.Scope, l.scope)
}

func (l *kvMemberLease) ObserveStatus(ctx context.Context) (contract.KVMemberResult, error) {
	if err := l.CheckMemberLease(); err != nil {
		return contract.KVMemberResult{Scope: l.Scope()}, err
	}
	r, err := l.raw.ObserveStatus(ctx)
	r = r.Clone()
	if !l.translateResult(&r) {
		err = errors.Join(err, contract.ErrRevoked)
	}
	return r, errors.Join(err, l.CheckMemberLease())
}

func (l *kvMemberLease) PrepareDefragment() (contract.KVMemberPreparation, error) {
	return l.prepareDefragment(false)
}

func (l *kvMemberLease) prepareDefragment(followerOnly bool) (contract.KVMemberPreparation, error) {
	if err := l.CheckMemberLease(); err != nil {
		return nil, err
	}
	var p contract.KVMemberPreparation
	var err error
	if followerOnly {
		f, ok := l.raw.(contract.KVMemberFollowerPreparation)
		if !ok {
			return nil, contract.ErrUnsupported
		}
		p, err = f.PrepareFollowerDefragment()
	} else {
		p, err = l.raw.PrepareDefragment()
	}
	if err != nil {
		return nil, err
	}
	if p == nil || !p.Writes() || p.Snapshot() != (contract.KVRequest{Method: "/etcdserverpb.Maintenance/Defragment", JSON: "{}"}) {
		return nil, contract.ErrKVPlanUsed
	}
	if err = l.CheckMemberLease(); err != nil {
		return nil, err
	}
	return &kvMemberPreparation{owner: l, raw: p, request: p.Snapshot()}, nil
}

func (l *kvMemberLease) ExecuteMember(ctx context.Context, prepared contract.KVMemberPreparation) (contract.KVMemberResult, error) {
	p, ok := prepared.(*kvMemberPreparation)
	if !ok || p == nil || p.owner != l {
		return contract.KVMemberResult{Scope: l.Scope()}, contract.ErrKVPlanUsed
	}
	if err := l.CheckMemberLease(); err != nil {
		return contract.KVMemberResult{Scope: l.Scope(), Result: contract.KVResult{Request: p.request}}, err
	}
	r, err := l.raw.ExecuteMember(ctx, p.raw)
	r = r.Clone()
	if !l.translateResult(&r) || r.Result.Request != p.request {
		err = errors.Join(err, contract.ErrRevoked)
	}
	return r, errors.Join(err, l.CheckMemberLease())
}

func (l *kvMemberLease) LastMemberResult() contract.KVMemberResult {
	r := l.raw.LastMemberResult().Clone()
	if r.Scope.LeaseID != 0 && !l.translateResult(&r) && r.Error == "" {
		r.Error = contract.ErrRevoked.Error()
	}
	return r
}

func (l *kvMemberLease) Close() error {
	if l.authority != l {
		return contract.ErrKVPlanUsed
	}
	l.closed.Store(true)
	l.once.Do(func() { l.closeErr = l.raw.Close() })
	return l.closeErr
}

var _ contract.KVMembers = (*kvMembersLease)(nil)
var _ contract.KVMemberLease = (*kvMemberLease)(nil)
