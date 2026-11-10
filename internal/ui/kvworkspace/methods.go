package kvworkspace

import "github.com/nyakang/nyadb/internal/db/contract"

type Method struct{ Name, Path string }

var methods = []Method{
	{"Range", "/etcdserverpb.KV/Range"}, {"Put", "/etcdserverpb.KV/Put"}, {"DeleteRange", "/etcdserverpb.KV/DeleteRange"}, {"Txn", "/etcdserverpb.KV/Txn"},
	{"Status", "/etcdserverpb.Maintenance/Status"}, {"LeaseGrant", "/etcdserverpb.Lease/LeaseGrant"}, {"LeaseRevoke", "/etcdserverpb.Lease/LeaseRevoke"}, {"LeaseTimeToLive", "/etcdserverpb.Lease/LeaseTimeToLive"}, {"LeaseLeases", "/etcdserverpb.Lease/LeaseLeases"}, {"LeaseKeepAlive", "/etcdserverpb.Lease/LeaseKeepAlive"},
}

func Methods() []Method { return append([]Method(nil), methods...) }
func DefaultRequest() contract.KVRequest {
	return contract.KVRequest{Method: "/etcdserverpb.KV/Range", JSON: "{\n  \"key\": \"AA==\",\n  \"limit\": \"100\"\n}"}
}
