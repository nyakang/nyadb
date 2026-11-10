package etcd

import "github.com/nyakang/nyadb/internal/db/contract"

func (*kvLease) MaintenanceMethods() []string {
	return []string{"/etcdserverpb.KV/Compact"}
}

var _ contract.KVMaintenance = (*kvLease)(nil)
