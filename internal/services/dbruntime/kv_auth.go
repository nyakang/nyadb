package dbruntime

import (
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type kvAuthLease struct{ *kvRequestLease }
type kvAuthBrowsingLease struct{ *kvLeaseBrowsingLease }

func (l *kvAuthLease) AuthMethods() []string {
	return slices.Clone(l.raw.(contract.KVAuthManagement).AuthMethods())
}

func (l *kvAuthBrowsingLease) AuthMethods() []string {
	return slices.Clone(l.raw.(contract.KVAuthManagement).AuthMethods())
}

var _ contract.KVAuthManagement = (*kvAuthLease)(nil)
var _ contract.KVAuthManagement = (*kvAuthBrowsingLease)(nil)
