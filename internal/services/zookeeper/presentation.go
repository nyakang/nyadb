package zookeeper

import "github.com/nyakang/nyadb/internal/db/contract"

// Templates only author requests. The original document lease owns preparation
// and dispatch, including native create modes and unconditional version -1 writes.
func (s *Session) KVRequestPresentation() contract.KVRequestPresentation {
	methods := []contract.KVRequestMethod{
		{Name: "kv_list_prefix", Request: contract.KVRequest{Method: "kv_list_prefix", JSON: `{ "prefix": "/", "recursive": false, "limit": 100 }`}},
		{Name: "kv_get", Request: contract.KVRequest{Method: "kv_get", JSON: `{ "key": "/example" }`}},
		{Name: "kv_put", Request: contract.KVRequest{Method: "kv_put", JSON: `{ "key": "/example", "value": { "encoding": "utf8", "data": "" }, "writeMode": "create", "createMode": "persistent" }`}},
		{Name: "kv_delete", Request: contract.KVRequest{Method: "kv_delete", JSON: `{ "key": "/example", "recursive": false }`}},
		{Name: "connection_info", Request: contract.KVRequest{Method: "connection_info", JSON: `{}`}},
	}
	return contract.KVRequestPresentation{Product: "ZooKeeper", Default: methods[0].Request, Methods: methods}
}

var _ contract.KVRequestPresentationProvider = (*Session)(nil)
