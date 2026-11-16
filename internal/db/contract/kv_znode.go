package contract

import "slices"

// KVZNodeIdentity identifies the original logical transport/root and pinned
// configuration, not a database, etcd cluster, server incarnation or ACL grant.
type KVZNodeIdentity struct {
	Protocol, ConnectString, Root, Username, AuthScheme, Configuration string
	DriverVersion, DriverSHA256                                        string
}

// KVZNodeStat preserves the actual signed ZooKeeper stat fields. In particular,
// an ephemeral owner is a native server session ID, not an etcd lease.
type KVZNodeStat struct {
	Czxid          int64 `json:"czxid"`
	Mzxid          int64 `json:"mzxid"`
	Pzxid          int64 `json:"pzxid"`
	Ctime          int64 `json:"ctime"`
	Mtime          int64 `json:"mtime"`
	Version        int32 `json:"version"`
	Cversion       int32 `json:"cversion"`
	Aversion       int32 `json:"aversion"`
	EphemeralOwner int64 `json:"ephemeralOwner"`
	DataLength     int32 `json:"dataLength"`
	NumChildren    int32 `json:"numChildren"`
}

type KVZNode struct {
	Key      string
	Value    []byte // nil for listing/missing; present empty values are a non-nil slice
	Encoding string
	Stat     *KVZNodeStat
}

func (n KVZNode) Clone() KVZNode {
	n.Value = slices.Clone(n.Value)
	if n.Stat != nil {
		v := *n.Stat
		n.Stat = &v
	}
	return n
}

type KVZNodeReply struct {
	Kind                        string
	Found                       *bool
	Node                        *KVZNode
	Keys                        []KVZNode
	Continuation                *string
	CreatedKey                  string
	Version                     *int32
	Mtime                       *int64
	Deleted                     *int64
	ProductName, ProductVersion string
}

func (r KVZNodeReply) Clone() KVZNodeReply {
	if r.Found != nil {
		v := *r.Found
		r.Found = &v
	}
	if r.Node != nil {
		v := r.Node.Clone()
		r.Node = &v
	}
	r.Keys = slices.Clone(r.Keys)
	for i := range r.Keys {
		r.Keys[i] = r.Keys[i].Clone()
	}
	if r.Continuation != nil {
		v := *r.Continuation
		r.Continuation = &v
	}
	if r.Version != nil {
		v := *r.Version
		r.Version = &v
	}
	if r.Mtime != nil {
		v := *r.Mtime
		r.Mtime = &v
	}
	if r.Deleted != nil {
		v := *r.Deleted
		r.Deleted = &v
	}
	return r
}

// Native is the complete original JSON-RPC envelope, including native errors.
// Error text may contain secrets, so generic JSON serialization omits Native;
// explicit original receipt inspection/export retains it separately. Optional
// projection failures do not invalidate an acknowledged successful write.
type KVZNodeResult struct {
	Native                                                              []byte `json:"-"`
	Captured, Acknowledged, CancellationRequested, ProcessExitConfirmed bool
	ErrorCode                                                           *int64
	Reply                                                               *KVZNodeReply
}

func (r KVZNodeResult) Clone() KVZNodeResult {
	r.Native = slices.Clone(r.Native)
	if r.ErrorCode != nil {
		v := *r.ErrorCode
		r.ErrorCode = &v
	}
	if r.Reply != nil {
		v := r.Reply.Clone()
		r.Reply = &v
	}
	return r
}
