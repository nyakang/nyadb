package contract

import (
	"context"
	"slices"
)

const KVHTTPSessionKeysMaxRows = 10000
const KVHTTPSessionKeysMaxBytes int64 = 32 << 20

// These explicit bounds apply to the complete optional root observation,
// including unmatched keys and original HTTP receipt accounting. A failed
// projection still retains the original HTTP evidence under the lease's
// existing capture/receipt bounds. The UUID is not an etcd lease ID.
type KVHTTPSessionKeysRequest struct {
	ID       string
	MaxRows  int
	MaxBytes int64
}

// Matches index the original root result, retaining server order and exact
// bytes, flags, indices and field presence without duplicating entry values.
// ProjectionComplete means the whole captured response was projected within
// its bounds. Complete additionally excludes an explicitly ACL-filtered
// response. It describes the original scoped observation, never a permission
// grant, cluster-wide absence, fixed snapshot or atomic destroy authority.
type KVHTTPSessionKeysObservation struct {
	Scope              KVScope
	Request            KVHTTPSessionKeysRequest
	Root               KVResult
	Matches            []int
	Bytes              int64
	ProjectionComplete bool
	Complete           bool
}

func (o KVHTTPSessionKeysObservation) Clone() KVHTTPSessionKeysObservation {
	o.Scope = o.Scope.Clone()
	o.Root = o.Root.Clone()
	o.Matches = slices.Clone(o.Matches)
	return o
}

// This optional read-only facet uses the original KVRequestLease. It performs
// one explicit whole-root recursive read. Cancellation waits for that executor;
// recovery transfers evidence without observing keys again or confirming an
// earlier unknown write. Public runtime/UI publication is a separate step.
type KVHTTPSessionKeysBrowsing interface {
	ObserveHTTPSessionKeys(context.Context, KVHTTPSessionKeysRequest) (KVHTTPSessionKeysObservation, error)
}
