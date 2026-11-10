package dbruntime

import (
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type kvMaintenanceLease struct{ *kvRequestLease }
type kvMaintenanceAuthLease struct{ *kvAuthLease }
type kvMaintenanceBrowsingLease struct{ *kvLeaseBrowsingLease }
type kvMaintenanceAuthBrowsingLease struct{ *kvAuthBrowsingLease }

func (l *kvMaintenanceLease) MaintenanceMethods() []string {
	return slices.Clone(l.raw.(contract.KVMaintenance).MaintenanceMethods())
}
func (l *kvMaintenanceAuthLease) MaintenanceMethods() []string {
	return slices.Clone(l.raw.(contract.KVMaintenance).MaintenanceMethods())
}
func (l *kvMaintenanceBrowsingLease) MaintenanceMethods() []string {
	return slices.Clone(l.raw.(contract.KVMaintenance).MaintenanceMethods())
}
func (l *kvMaintenanceAuthBrowsingLease) MaintenanceMethods() []string {
	return slices.Clone(l.raw.(contract.KVMaintenance).MaintenanceMethods())
}

func bindKVMaintenance(bound contract.KVRequestLease) contract.KVRequestLease {
	base := originalKVRequestLease(bound)
	if base == nil {
		return bound
	}
	if _, ok := base.raw.(contract.KVMaintenance); !ok {
		return bound
	}
	switch l := bound.(type) {
	case *kvRequestLease:
		return &kvMaintenanceLease{l}
	case *kvAuthLease:
		return &kvMaintenanceAuthLease{l}
	case *kvLeaseBrowsingLease:
		return &kvMaintenanceBrowsingLease{l}
	case *kvAuthBrowsingLease:
		return &kvMaintenanceAuthBrowsingLease{l}
	}
	return bound
}

var _ contract.KVMaintenance = (*kvMaintenanceLease)(nil)
var _ contract.KVMaintenance = (*kvMaintenanceAuthLease)(nil)
var _ contract.KVMaintenance = (*kvMaintenanceBrowsingLease)(nil)
var _ contract.KVMaintenance = (*kvMaintenanceAuthBrowsingLease)(nil)
