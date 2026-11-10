package etcd

import "github.com/nyakang/nyadb/internal/db/contract"

func (*kvLease) MaintenanceMethods() []string {
	return []string{"/etcdserverpb.KV/Compact", "/etcdserverpb.Maintenance/Alarm"}
}

var _ contract.KVMaintenance = (*kvLease)(nil)
