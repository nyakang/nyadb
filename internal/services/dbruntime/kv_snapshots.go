package dbruntime

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type kvSnapshotsRequestLease struct {
	*kvRequestLease
	*kvSnapshotFacet
}
type kvSnapshotsAuthLease struct {
	*kvAuthLease
	*kvSnapshotFacet
}
type kvSnapshotsLeaseBrowsingLease struct {
	*kvLeaseBrowsingLease
	*kvSnapshotFacet
}
type kvSnapshotsAuthBrowsingLease struct {
	*kvAuthBrowsingLease
	*kvSnapshotFacet
}
type kvSnapshotsMaintenanceLease struct {
	*kvMaintenanceLease
	*kvSnapshotFacet
}
type kvSnapshotsMaintenanceAuthLease struct {
	*kvMaintenanceAuthLease
	*kvSnapshotFacet
}
type kvSnapshotsMaintenanceBrowsingLease struct {
	*kvMaintenanceBrowsingLease
	*kvSnapshotFacet
}
type kvSnapshotsMaintenanceAuthBrowsingLease struct {
	*kvMaintenanceAuthBrowsingLease
	*kvSnapshotFacet
}
type kvSnapshotsMembersLease struct {
	*kvMembersLease
	*kvSnapshotFacet
}
type kvSnapshotsMembersAuthLease struct {
	*kvMembersAuthLease
	*kvSnapshotFacet
}
type kvSnapshotsMembersBrowsingLease struct {
	*kvMembersBrowsingLease
	*kvSnapshotFacet
}
type kvSnapshotsMembersAuthBrowsingLease struct {
	*kvMembersAuthBrowsingLease
	*kvSnapshotFacet
}
type kvSnapshotsMembersMaintenanceLease struct {
	*kvMembersMaintenanceLease
	*kvSnapshotFacet
}
type kvSnapshotsMembersMaintenanceAuthLease struct {
	*kvMembersMaintenanceAuthLease
	*kvSnapshotFacet
}
type kvSnapshotsMembersMaintenanceBrowsingLease struct {
	*kvMembersMaintenanceBrowsingLease
	*kvSnapshotFacet
}
type kvSnapshotsMembersMaintenanceAuthBrowsingLease struct {
	*kvMembersMaintenanceAuthBrowsingLease
	*kvSnapshotFacet
}

type kvSnapshotFacet struct{ base *kvRequestLease }

func bindKVSnapshots(bound contract.KVRequestLease) contract.KVRequestLease {
	base := originalKVRequestLease(bound)
	if base == nil {
		return bound
	}
	if _, ok := base.raw.(contract.KVSnapshots); !ok {
		return bound
	}
	f := &kvSnapshotFacet{base}
	switch l := bound.(type) {
	case *kvRequestLease:
		return &kvSnapshotsRequestLease{l, f}
	case *kvAuthLease:
		return &kvSnapshotsAuthLease{l, f}
	case *kvLeaseBrowsingLease:
		return &kvSnapshotsLeaseBrowsingLease{l, f}
	case *kvAuthBrowsingLease:
		return &kvSnapshotsAuthBrowsingLease{l, f}
	case *kvMaintenanceLease:
		return &kvSnapshotsMaintenanceLease{l, f}
	case *kvMaintenanceAuthLease:
		return &kvSnapshotsMaintenanceAuthLease{l, f}
	case *kvMaintenanceBrowsingLease:
		return &kvSnapshotsMaintenanceBrowsingLease{l, f}
	case *kvMaintenanceAuthBrowsingLease:
		return &kvSnapshotsMaintenanceAuthBrowsingLease{l, f}
	case *kvMembersLease:
		return &kvSnapshotsMembersLease{l, f}
	case *kvMembersAuthLease:
		return &kvSnapshotsMembersAuthLease{l, f}
	case *kvMembersBrowsingLease:
		return &kvSnapshotsMembersBrowsingLease{l, f}
	case *kvMembersAuthBrowsingLease:
		return &kvSnapshotsMembersAuthBrowsingLease{l, f}
	case *kvMembersMaintenanceLease:
		return &kvSnapshotsMembersMaintenanceLease{l, f}
	case *kvMembersMaintenanceAuthLease:
		return &kvSnapshotsMembersMaintenanceAuthLease{l, f}
	case *kvMembersMaintenanceBrowsingLease:
		return &kvSnapshotsMembersMaintenanceBrowsingLease{l, f}
	case *kvMembersMaintenanceAuthBrowsingLease:
		return &kvSnapshotsMembersMaintenanceAuthBrowsingLease{l, f}
	}
	return bound
}

type kvSnapshotPreparation struct {
	authority *kvSnapshotPreparation
	owner     *kvRequestLease
	raw       contract.KVSnapshotPreparation
	request   contract.KVSnapshotRequest
	scope     contract.KVSnapshotScope
	used      *atomic.Bool
}

func (p *kvSnapshotPreparation) Snapshot() contract.KVSnapshotRequest { return p.request }
func (p *kvSnapshotPreparation) Scope() contract.KVSnapshotScope      { return p.scope.Clone() }
func (l *kvRequestLease) translateSnapshotScope(s *contract.KVSnapshotScope) bool {
	return s.DocumentID == l.document && l.translateMemberOriginal(&s.Original)
}
func (f *kvSnapshotFacet) PrepareSnapshot(r contract.KVSnapshotRequest) (contract.KVSnapshotPreparation, error) {
	l := f.base
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	p, err := l.raw.(contract.KVSnapshots).PrepareSnapshot(r)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, contract.ErrUnsupported
	}
	s := p.Scope().Clone()
	if p.Snapshot() != r || !l.translateSnapshotScope(&s) {
		return nil, contract.ErrRevoked
	}
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	out := &kvSnapshotPreparation{owner: l, raw: p, request: r, scope: s, used: &atomic.Bool{}}
	out.authority = out
	return out, nil
}
func (f *kvSnapshotFacet) ExecuteSnapshot(ctx context.Context, prepared contract.KVSnapshotPreparation, outputs contract.KVSnapshotOutputs) (contract.KVSnapshotResult, error) {
	l := f.base
	p, ok := prepared.(*kvSnapshotPreparation)
	if !ok || p == nil || p.authority != p || p.owner != l || p.used == nil {
		return contract.KVSnapshotResult{}, contract.ErrKVPlanUsed
	}
	fallback := contract.KVSnapshotResult{Scope: p.Scope(), Request: p.request}
	if err := ctx.Err(); err != nil {
		return fallback, err
	}
	if err := l.CheckKVLease(); err != nil {
		return fallback, err
	}
	// The raw executor keeps its own one-use flag. Do not consume a wrapper on
	// non-dispatch busy/invalid-output failures; neither layer can replay I/O.
	if p.used.Load() {
		return fallback, contract.ErrKVPlanUsed
	}
	r, err := l.raw.(contract.KVSnapshots).ExecuteSnapshot(ctx, p.raw, outputs)
	r = r.Clone()
	if r.Ended || r.Attempted {
		p.used.Store(true)
	}
	if !l.translateSnapshotScope(&r.Scope) || r.Request != p.request {
		err = errors.Join(err, contract.ErrRevoked)
	}
	return r, errors.Join(err, l.CheckKVLease())
}
func (f *kvSnapshotFacet) LastSnapshotResult() contract.KVSnapshotResult {
	r := f.base.raw.(contract.KVSnapshots).LastSnapshotResult().Clone()
	if r.Ended && !f.base.translateSnapshotScope(&r.Scope) && r.LocalError == "" {
		r.LocalError = contract.ErrRevoked.Error()
	}
	return r
}

var _ contract.KVSnapshots = (*kvSnapshotsRequestLease)(nil)
var _ contract.KVSnapshots = (*kvSnapshotsAuthLease)(nil)
var _ contract.KVSnapshots = (*kvSnapshotsLeaseBrowsingLease)(nil)
var _ contract.KVSnapshots = (*kvSnapshotsAuthBrowsingLease)(nil)
var _ contract.KVSnapshots = (*kvSnapshotsMaintenanceLease)(nil)
var _ contract.KVSnapshots = (*kvSnapshotsMaintenanceAuthLease)(nil)
var _ contract.KVSnapshots = (*kvSnapshotsMaintenanceBrowsingLease)(nil)
var _ contract.KVSnapshots = (*kvSnapshotsMaintenanceAuthBrowsingLease)(nil)
var _ contract.KVSnapshots = (*kvSnapshotsMembersLease)(nil)
var _ contract.KVSnapshots = (*kvSnapshotsMembersAuthLease)(nil)
var _ contract.KVSnapshots = (*kvSnapshotsMembersBrowsingLease)(nil)
var _ contract.KVSnapshots = (*kvSnapshotsMembersAuthBrowsingLease)(nil)
var _ contract.KVSnapshots = (*kvSnapshotsMembersMaintenanceLease)(nil)
var _ contract.KVSnapshots = (*kvSnapshotsMembersMaintenanceAuthLease)(nil)
var _ contract.KVSnapshots = (*kvSnapshotsMembersMaintenanceBrowsingLease)(nil)
var _ contract.KVSnapshots = (*kvSnapshotsMembersMaintenanceAuthBrowsingLease)(nil)
