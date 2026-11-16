package zookeeper

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

func TestNativeRequestsRetainValuesAndRejectUnsupportedWriteSemantics(t *testing.T) {
	for _, tc := range []struct {
		method, body string
		write        bool
	}{
		{"kv_get", `{"key":"中文/"}`, false},
		{"kv_put", `{"key":"/中文","value":{"encoding":"base64","data":"AP+A"},"writeMode":"create","createMode":"ephemeral_sequential"}`, true},
		{"kv_put", `{"key":"/empty","value":{"encoding":"utf8","data":""}}`, true},
		{"kv_delete", `{"key":"/tree","recursive":false}`, true},
		{"kv_list_prefix", `{"prefix":"/","recursive":false,"limit":2}`, false},
		{"connection_info", `{}`, false},
	} {
		r, w, err := decodeRequest(contract.KVRequest{Method: tc.method, JSON: tc.body})
		if err != nil || w != tc.write || r.Method != tc.method {
			t.Fatal("valid native request rejected", err)
		}
		if tc.method == "kv_get" && !strings.Contains(r.JSON, `"/中文"`) {
			t.Fatal("native path normalization changed")
		}
	}
	for _, body := range []string{`{"key":"/x","value":{"data":"a"},"writeMode":"update","createMode":"ephemeral"}`, `{"key":"/x","value":{"data":"a"},"version":1}`, `{"key":"/","value":{"data":"a"}}`, `{"key":"/x","value":{"data":"a","lease":2}}`, `{"key":"/x","key":"/y","value":{"data":"a"}}`, `{"key":"/x","value":{"data":"\ud800"}}`, `{"key":"/x","value":{"encoding":"base64","data":"AB=="}}`, `{"key":"/a/../b","value":{"data":"a"}}`} {
		if _, _, err := decodeRequest(contract.KVRequest{Method: "kv_put", JSON: body}); err == nil {
			t.Fatal("unsupported native write admitted", body)
		}
	}
	for _, m := range []string{"connect", "disconnect", "cancel_session", "execute_query", "/etcdserverpb.KV/Put"} {
		if _, _, err := decodeRequest(contract.KVRequest{Method: m, JSON: `{}`}); err == nil {
			t.Fatal("foreign or lifecycle method admitted", m)
		}
	}
	cursor := base64.StdEncoding.EncodeToString([]byte(`{"root":"/tree","recursive":false,"offset":2}`))
	if _, _, err := decodeRequest(contract.KVRequest{Method: "kv_list_prefix", JSON: `{"prefix":"/tree","recursive":false,"continuation":"` + cursor + `"}`}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := decodeRequest(contract.KVRequest{Method: "kv_list_prefix", JSON: `{"prefix":"/other","recursive":false,"continuation":"` + cursor + `"}`}); err == nil {
		t.Fatal("cursor transferred to another root")
	}
}

func TestNativeProjectionKeepsExactStatAndAcknowledgedIncompleteWrites(t *testing.T) {
	raw := []byte(`{"found":true,"key":"/x","value":{"encoding":"base64","data":"AP+A"},"metadata":{"czxid":9007199254740993,"mzxid":9007199254740994,"pzxid":9007199254740995,"ctime":123,"mtime":124,"version":2,"cversion":3,"aversion":4,"ephemeralOwner":9007199254740997,"dataLength":3,"numChildren":0},"future":9007199254740999}`)
	p, err := decodeReply("kv_get", raw)
	if err != nil || p.Node.Stat.EphemeralOwner != 9007199254740997 || string(p.Node.Value) != string([]byte{0, 255, 128}) {
		t.Fatal("native stat precision or binary lost", err)
	}
	copy := p.Clone()
	copy.Node.Value[0] = 9
	copy.Node.Stat.Czxid = 1
	if p.Node.Value[0] != 0 || p.Node.Stat.Czxid != 9007199254740993 {
		t.Fatal("native projection aliases clone")
	}
	r := nativeResult(contract.KVRequest{Method: "kv_put", JSON: `{}`}, true, agentbridge.Receipt{Dispatched: true, ExecutorAcknowledged: true, Result: []byte(`{"new_shape":true}`)})
	if !r.Completed || r.WriteOutcomeUnknown || r.ProjectionError == "" {
		t.Fatal("projection failure changed actual write acknowledgment")
	}
	r = nativeResult(contract.KVRequest{Method: "kv_put"}, true, agentbridge.Receipt{Dispatched: true, ExecutorAcknowledged: true, RemoteError: &agentbridge.RemoteError{Code: -1}})
	if r.Completed || !r.WriteOutcomeUnknown || !r.ZNode.Acknowledged {
		t.Fatal("generic original error became a proved rollback")
	}
}
