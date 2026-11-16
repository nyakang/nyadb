package zookeeper

import (
	"encoding/base64"
	"encoding/json"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

func nativeResult(request contract.KVRequest, write bool, r agentbridge.Receipt) contract.KVResult {
	n := &contract.KVZNodeResult{Native: r.OriginalResponse(), Acknowledged: r.ExecutorAcknowledged, CancellationRequested: r.CancellationRequested, ProcessExitConfirmed: r.ProcessExitConfirmed}
	n.Captured = r.ExecutorAcknowledged && len(n.Native) > 0
	out := contract.KVResult{Request: request, Attempted: r.Dispatched, Completed: r.ExecutorAcknowledged && r.RemoteError == nil, WriteOutcomeUnknown: write && r.Dispatched && (!r.ExecutorAcknowledged || r.RemoteError != nil), ZNode: n, Code: "NotDispatched"}
	if r.Dispatched {
		out.Code = "RuntimeLost"
	}
	if r.ExecutorAcknowledged {
		out.Code = "OK"
	}
	if r.RemoteError != nil {
		v := r.RemoteError.Code
		n.ErrorCode = &v
		out.Code = "NativeError"
	}
	if out.Completed {
		out.JSON = string(r.Result)
		if reply, err := decodeReply(request.Method, r.Result); err == nil {
			n.Reply = &reply
		} else {
			out.ProjectionError = ErrProjection.Error()
		}
	}
	return out
}

func stat(fields map[string]json.RawMessage) (*contract.KVZNodeStat, error) {
	for _, key := range []string{"czxid", "mzxid", "pzxid", "ctime", "mtime", "version", "cversion", "aversion", "ephemeralOwner", "dataLength", "numChildren"} {
		if fields[key] == nil || string(fields[key]) == "null" {
			return nil, ErrProjection
		}
	}
	raw, _ := json.Marshal(fields)
	var s contract.KVZNodeStat
	if json.Unmarshal(raw, &s) != nil || s.Version < 0 || s.Cversion < 0 || s.Aversion < 0 || s.DataLength < 0 || s.NumChildren < 0 {
		return nil, ErrProjection
	}
	return &s, nil
}

func decodeReply(method string, raw []byte) (contract.KVZNodeReply, error) {
	f, err := object(raw, frameLimit)
	if err != nil {
		return contract.KVZNodeReply{}, ErrProjection
	}
	out := contract.KVZNodeReply{Kind: method}
	switch method {
	case "connect":
		ok, err := boolField(f, "ok", false)
		if err != nil || !ok {
			return out, ErrProjection
		}
	case "connection_info":
		info, err := object(f["databaseInfo"], frameLimit)
		if err != nil {
			return out, ErrProjection
		}
		out.ProductName, err = textField(info, "productName", true)
		if err != nil || out.ProductName != "ZooKeeper" {
			return out, ErrProjection
		}
		out.ProductVersion, err = textField(info, "productVersion", false)
		return out, err
	case "kv_get":
		found, err := boolField(f, "found", false)
		if err != nil || f["found"] == nil {
			return out, ErrProjection
		}
		out.Found = &found
		key, err := textField(f, "key", true)
		if err != nil || !validPath(key) {
			return out, ErrProjection
		}
		node := contract.KVZNode{Key: key}
		out.Node = &node
		if !found {
			if string(f["value"]) != "null" || string(f["metadata"]) != "null" {
				return out, ErrProjection
			}
			return out, nil
		}
		value, err := object(f["value"], frameLimit)
		if err != nil {
			return out, ErrProjection
		}
		node.Encoding, err = textField(value, "encoding", true)
		if err != nil {
			return out, ErrProjection
		}
		data, err := textField(value, "data", true)
		if err != nil {
			return out, ErrProjection
		}
		switch node.Encoding {
		case "utf8":
			node.Value = []byte(data)
		case "base64":
			node.Value, err = base64.StdEncoding.Strict().DecodeString(data)
			if err != nil {
				return out, ErrProjection
			}
		default:
			return out, ErrProjection
		}
		if node.Value == nil {
			node.Value = []byte{}
		}
		metadata, err := object(f["metadata"], frameLimit)
		if err != nil {
			return out, ErrProjection
		}
		node.Stat, err = stat(metadata)
		if err != nil || int64(node.Stat.DataLength) != int64(len(node.Value)) {
			return out, ErrProjection
		}
		out.Node = &node
	case "kv_list_prefix":
		var rows []json.RawMessage
		if f["keys"] == nil || string(f["keys"]) == "null" || json.Unmarshal(f["keys"], &rows) != nil || len(rows) > 10000 {
			return out, ErrProjection
		}
		out.Keys = make([]contract.KVZNode, 0, len(rows))
		for _, row := range rows {
			v, err := object(row, frameLimit)
			if err != nil {
				return out, ErrProjection
			}
			key, err := textField(v, "key", true)
			if err != nil || !validPath(key) {
				return out, ErrProjection
			}
			st, err := stat(v)
			if err != nil {
				return out, err
			}
			out.Keys = append(out.Keys, contract.KVZNode{Key: key, Stat: st})
		}
		if f["continuation"] == nil {
			return out, ErrProjection
		}
		if string(f["continuation"]) != "null" {
			c, err := textField(f, "continuation", true)
			if err != nil {
				return out, ErrProjection
			}
			out.Continuation = &c
		}
	case "kv_put":
		if f["version"] == nil || f["mtime"] == nil || json.Unmarshal(f["version"], &out.Version) != nil || json.Unmarshal(f["mtime"], &out.Mtime) != nil || out.Version == nil || out.Mtime == nil || *out.Version < 0 {
			return out, ErrProjection
		}
		if f["createdKey"] != nil {
			out.CreatedKey, err = textField(f, "createdKey", true)
			key, e := textField(f, "key", true)
			if err != nil || e != nil || key != out.CreatedKey || !validPath(key) {
				return out, ErrProjection
			}
		}
	case "kv_delete":
		if f["deleted"] == nil || json.Unmarshal(f["deleted"], &out.Deleted) != nil || out.Deleted == nil || *out.Deleted < 0 {
			return out, ErrProjection
		}
	default:
		return out, ErrProjection
	}
	return out, nil
}
