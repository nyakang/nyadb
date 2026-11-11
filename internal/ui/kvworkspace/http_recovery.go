package kvworkspace

import (
	"bytes"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func sameKVRecoveryIdentity(a, b contract.KVScope) bool {
	return a.Namespace == b.Namespace && bytes.Equal(a.Prefix, b.Prefix) && a.Header.ClusterID == b.Header.ClusterID && a.Header.MemberID == b.Header.MemberID && a.Version == b.Version && a.HTTP == b.HTTP
}

func (s *State) consumeRequestRecovery(d *Document, job *operation, result *outcome) {
	if result.err != nil {
		return
	}
	if result.recoveryUnknown != nil {
		unknown := result.recoveryUnknown
		if !unknown.WriteOutcomeUnknown || d.unknown != nil && d.unknown.Result.Request != unknown.Request {
			if result.lease != nil {
				result.lease.Close()
				result.lease = nil
			}
			result.err = contract.ErrRevoked
			d.error = contract.ErrRevoked.Error()
			return
		}
		if d.unknown == nil {
			d.unknown = &Receipt{Scope: job.scope.Clone(), Authored: unknown.Request, Result: unknown.Clone()}
		}
	}
	if d.last == nil && result.recoveryResult != nil && result.recoveryResult.HTTP != nil {
		last := result.recoveryResult
		d.last = &Receipt{Scope: job.scope.Clone(), Authored: last.Request, Result: last.Clone()}
	}
}
