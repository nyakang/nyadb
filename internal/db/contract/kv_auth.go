package contract

import "slices"

// KVAuthPermission keeps native permission semantics: an empty End is an exact
// key, a single NUL End is unbounded, and every other End is exclusive. These
// byte ranges are independent of the connection's display prefix.
type KVAuthPermission struct {
	Access           string
	Key, End         []byte
	NativeExtensions []byte
}

func (p KVAuthPermission) Clone() KVAuthPermission {
	p.Key = slices.Clone(p.Key)
	p.End = slices.Clone(p.End)
	p.NativeExtensions = slices.Clone(p.NativeExtensions)
	return p
}

// Revision is the AuthStatus auth-store revision, not a KV revision or a public
// session generation. User/role write replies retain their own native headers.
type KVAuthReceipt struct {
	Enabled      bool
	Revision     uint64
	Users, Roles []string
	Permissions  []KVAuthPermission
}

func (r KVAuthReceipt) Clone() KVAuthReceipt {
	r.Users = slices.Clone(r.Users)
	r.Roles = slices.Clone(r.Roles)
	r.Permissions = slices.Clone(r.Permissions)
	for i := range r.Permissions {
		r.Permissions[i] = r.Permissions[i].Clone()
	}
	return r
}

// KVRequestRecord is an explicitly redacted record of an immutable request.
// OmittedFields describes information intentionally absent from Request.JSON;
// it never implies a different request was dispatched. The preparation's full
// Snapshot remains transient and its native execution remains one-use.
type KVRequestRecord struct {
	Request       KVRequest
	OmittedFields []string
}

func (r KVRequestRecord) Clone() KVRequestRecord {
	r.OmittedFields = slices.Clone(r.OmittedFields)
	return r
}

type KVPreparationRecord interface {
	RecordedRequest() KVRequestRecord
}

// KVAuthManagement advertises the native Auth methods accepted by this original
// request lease. It does not grant administration rights; the server authorizes
// each RPC. Discovery must not perform a request or infer support from a prefix.
type KVAuthManagement interface {
	AuthMethods() []string
}
