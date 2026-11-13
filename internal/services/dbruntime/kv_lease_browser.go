package dbruntime

import (
	"bytes"
	"context"
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type kvLeaseBrowsingLease struct{ *kvRequestLease }

func originalKVRequestLease(previous contract.KVRequestLease) *kvRequestLease {
	switch l := previous.(type) {
	case *kvHTTPSessionDestroyLease:
		if l != nil {
			return l.kvRequestLease
		}
	case *kvHTTPSessionKeysLease:
		if l != nil {
			return l.kvRequestLease
		}
	case *kvSnapshotsRequestLease:
		if l != nil {
			return originalKVRequestLease(l.kvRequestLease)
		}
	case *kvSnapshotsAuthLease:
		if l != nil {
			return originalKVRequestLease(l.kvAuthLease)
		}
	case *kvSnapshotsLeaseBrowsingLease:
		if l != nil {
			return originalKVRequestLease(l.kvLeaseBrowsingLease)
		}
	case *kvSnapshotsAuthBrowsingLease:
		if l != nil {
			return originalKVRequestLease(l.kvAuthBrowsingLease)
		}
	case *kvSnapshotsMaintenanceLease:
		if l != nil {
			return originalKVRequestLease(l.kvMaintenanceLease)
		}
	case *kvSnapshotsMaintenanceAuthLease:
		if l != nil {
			return originalKVRequestLease(l.kvMaintenanceAuthLease)
		}
	case *kvSnapshotsMaintenanceBrowsingLease:
		if l != nil {
			return originalKVRequestLease(l.kvMaintenanceBrowsingLease)
		}
	case *kvSnapshotsMaintenanceAuthBrowsingLease:
		if l != nil {
			return originalKVRequestLease(l.kvMaintenanceAuthBrowsingLease)
		}
	case *kvSnapshotsMembersLease:
		if l != nil {
			return originalKVRequestLease(l.kvMembersLease)
		}
	case *kvSnapshotsMembersAuthLease:
		if l != nil {
			return originalKVRequestLease(l.kvMembersAuthLease)
		}
	case *kvSnapshotsMembersBrowsingLease:
		if l != nil {
			return originalKVRequestLease(l.kvMembersBrowsingLease)
		}
	case *kvSnapshotsMembersAuthBrowsingLease:
		if l != nil {
			return originalKVRequestLease(l.kvMembersAuthBrowsingLease)
		}
	case *kvSnapshotsMembersMaintenanceLease:
		if l != nil {
			return originalKVRequestLease(l.kvMembersMaintenanceLease)
		}
	case *kvSnapshotsMembersMaintenanceAuthLease:
		if l != nil {
			return originalKVRequestLease(l.kvMembersMaintenanceAuthLease)
		}
	case *kvSnapshotsMembersMaintenanceBrowsingLease:
		if l != nil {
			return originalKVRequestLease(l.kvMembersMaintenanceBrowsingLease)
		}
	case *kvSnapshotsMembersMaintenanceAuthBrowsingLease:
		if l != nil {
			return originalKVRequestLease(l.kvMembersMaintenanceAuthBrowsingLease)
		}
	case *kvMembersLease:
		if l != nil {
			return originalKVRequestLease(l.kvRequestLease)
		}
	case *kvMembersAuthLease:
		if l != nil {
			return originalKVRequestLease(l.kvAuthLease)
		}
	case *kvMembersBrowsingLease:
		if l != nil {
			return originalKVRequestLease(l.kvLeaseBrowsingLease)
		}
	case *kvMembersAuthBrowsingLease:
		if l != nil {
			return originalKVRequestLease(l.kvAuthBrowsingLease)
		}
	case *kvMembersMaintenanceLease:
		if l != nil {
			return originalKVRequestLease(l.kvMaintenanceLease)
		}
	case *kvMembersMaintenanceAuthLease:
		if l != nil {
			return originalKVRequestLease(l.kvMaintenanceAuthLease)
		}
	case *kvMembersMaintenanceBrowsingLease:
		if l != nil {
			return originalKVRequestLease(l.kvMaintenanceBrowsingLease)
		}
	case *kvMembersMaintenanceAuthBrowsingLease:
		if l != nil {
			return originalKVRequestLease(l.kvMaintenanceAuthBrowsingLease)
		}
	case *kvMaintenanceLease:
		if l != nil {
			return originalKVRequestLease(l.kvRequestLease)
		}
	case *kvMaintenanceAuthLease:
		if l != nil {
			return originalKVRequestLease(l.kvAuthLease)
		}
	case *kvMaintenanceBrowsingLease:
		if l != nil {
			return originalKVRequestLease(l.kvLeaseBrowsingLease)
		}
	case *kvMaintenanceAuthBrowsingLease:
		if l != nil {
			return originalKVRequestLease(l.kvAuthBrowsingLease)
		}
	case *kvRequestLease:
		return l
	case *kvLeaseBrowsingLease:
		if l != nil {
			return l.kvRequestLease
		}
	case *kvAuthLease:
		if l != nil {
			return l.kvRequestLease
		}
	case *kvAuthBrowsingLease:
		if l != nil && l.kvLeaseBrowsingLease != nil {
			return l.kvRequestLease
		}
	}
	return nil
}

func (l *kvLeaseBrowsingLease) translateScope(scope *contract.KVScope) bool {
	if scope.Namespace != l.scope.Namespace || scope.Generation != l.session.raw.Generation() || !bytes.Equal(scope.Prefix, l.scope.Prefix) || scope.Header != l.scope.Header || scope.Version != l.scope.Version {
		return false
	}
	scope.Generation = l.scope.Generation
	return true
}

func sameLeasePageRequest(a, b contract.KVLeasePageRequest) bool {
	return a.Limit == b.Limit && (a.After == nil && b.After == nil || a.After != nil && b.After != nil && *a.After == *b.After)
}

func (l *kvLeaseBrowsingLease) BrowseLeases(ctx context.Context, request contract.KVLeasePageRequest) (contract.KVLeasePage, error) {
	request = request.Clone()
	if err := l.CheckKVLease(); err != nil {
		return contract.KVLeasePage{Scope: l.Scope(), Request: request}, err
	}
	raw := l.raw.(contract.KVLeaseBrowsing)
	page, err := raw.BrowseLeases(ctx, request.Clone())
	page = page.Clone()
	if !l.translateScope(&page.Scope) || !sameLeasePageRequest(page.Request, request) {
		err = errors.Join(err, contract.ErrRevoked)
	}
	// Publication retirement reports an error while preserving the complete
	// captured original result. Only the original UI document admits it.
	return page, errors.Join(err, l.CheckKVLease())
}

func (l *kvLeaseBrowsingLease) ObserveLease(ctx context.Context, request contract.KVLeaseDetailRequest) (contract.KVLeaseDetail, error) {
	if err := l.CheckKVLease(); err != nil {
		return contract.KVLeaseDetail{Scope: l.Scope(), Request: request}, err
	}
	raw := l.raw.(contract.KVLeaseBrowsing)
	receipt, err := raw.ObserveLease(ctx, request)
	receipt = receipt.Clone()
	if !l.translateScope(&receipt.Scope) || receipt.Request != request {
		err = errors.Join(err, contract.ErrRevoked)
	}
	return receipt, errors.Join(err, l.CheckKVLease())
}

var _ contract.KVRequestLease = (*kvLeaseBrowsingLease)(nil)
var _ contract.KVLeaseBrowsing = (*kvLeaseBrowsingLease)(nil)
