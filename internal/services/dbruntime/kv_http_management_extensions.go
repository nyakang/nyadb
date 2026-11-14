package dbruntime

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
)

// Optional facets are advertised only when the original provider supports
// them. Embedded facets retain the same public generation and physical lease.
type kvHTTPManagementRestoreLease struct {
	*kvHTTPManagementLease
	contract.KVHTTPArchiveRestoring
}
type kvHTTPManagementEnterpriseLease struct {
	*kvHTTPManagementLease
	contract.KVHTTPEnterpriseDeletionPlanning
}
type kvHTTPManagementExtendedLease struct {
	*kvHTTPManagementLease
	contract.KVHTTPArchiveRestoring
	contract.KVHTTPEnterpriseDeletionPlanning
}
type kvHTTPManagementFullRestoreLease struct {
	*kvHTTPManagementFullLease
	contract.KVHTTPArchiveRestoring
}
type kvHTTPManagementFullEnterpriseLease struct {
	*kvHTTPManagementFullLease
	contract.KVHTTPEnterpriseDeletionPlanning
}
type kvHTTPManagementFullExtendedLease struct {
	*kvHTTPManagementFullLease
	contract.KVHTTPArchiveRestoring
	contract.KVHTTPEnterpriseDeletionPlanning
}

type managementRestoreBinding struct{ base *kvHTTPManagementLease }

func (b managementRestoreBinding) PrepareHTTPArchiveRestore(r contract.KVHTTPArchiveRestoreRequest) (contract.KVHTTPArchiveRestorePreparation, error) {
	return b.base.prepareHTTPArchiveRestore(r)
}
func (b managementRestoreBinding) ExecuteHTTPArchiveRestore(ctx context.Context, p contract.KVHTTPArchiveRestorePreparation) (contract.KVHTTPArchiveRestoreResult, error) {
	return b.base.executeHTTPArchiveRestore(ctx, p)
}
func (b managementRestoreBinding) LastHTTPArchiveRestore() contract.KVHTTPArchiveRestoreResult {
	return b.base.lastHTTPArchiveRestore()
}

type managementEnterpriseBinding struct{ base *kvHTTPManagementLease }

func (b managementEnterpriseBinding) PrepareHTTPEnterpriseDeletion(ctx context.Context, r contract.KVHTTPManagementRequest) (contract.KVHTTPEnterpriseDeletionPreparation, contract.KVHTTPEnterpriseImpact, error) {
	return b.base.prepareHTTPEnterpriseDeletion(ctx, r)
}

func bindManagementExtensions(l *kvHTTPManagementLease, full *kvHTTPManagementFullLease) contract.KVRequestLease {
	_, restore := l.base.raw.(contract.KVHTTPArchiveRestoring)
	_, enterprise := l.base.raw.(contract.KVHTTPEnterpriseDeletionPlanning)
	r, e := managementRestoreBinding{l}, managementEnterpriseBinding{l}
	if full != nil {
		switch {
		case restore && enterprise:
			return &kvHTTPManagementFullExtendedLease{full, r, e}
		case restore:
			return &kvHTTPManagementFullRestoreLease{full, r}
		case enterprise:
			return &kvHTTPManagementFullEnterpriseLease{full, e}
		default:
			return full
		}
	}
	switch {
	case restore && enterprise:
		return &kvHTTPManagementExtendedLease{l, r, e}
	case restore:
		return &kvHTTPManagementRestoreLease{l, r}
	case enterprise:
		return &kvHTTPManagementEnterpriseLease{l, e}
	default:
		return l
	}
}
