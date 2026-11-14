package consul

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestSessionListReadToolsKeepOriginalNodeAndRejectHiddenWriteInputs(t *testing.T) {
	for _, node := range []string{"node", "原始 节点 ?%#", " spaced node "} {
		input := contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{Node: node}}
		request, err := (requestBuilder{}).BuildKVRequest(contract.KVToolHTTPNodeSessions, input)
		if err != nil {
			t.Fatal(err)
		}
		native, canonical, writes, err := decodeDocumentRequest(request)
		if err != nil || writes || canonical != request || native.Method != "GET" || native.Path != "/v1/session/node/"+url.PathEscape(node) || native.Body != nil || len(native.Query) != 0 || input.HTTPSession.Node != node {
			t.Fatal("original node read was normalized or given write authority")
		}
	}
	request, err := (requestBuilder{}).BuildKVRequest(contract.KVToolHTTPSessionList, contract.KVRequestToolInput{})
	if err != nil {
		t.Fatal(err)
	}
	native, _, writes, err := decodeDocumentRequest(request)
	if err != nil || writes || native.Path != "/v1/session/list" || native.Body != nil || len(native.Query) != 0 {
		t.Fatal("list read acquired an authored prefix, body or scope")
	}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPSessionList, contract.KVToolHTTPNodeSessions} {
		for _, input := range []contract.KVRequestToolInput{
			{Key: "foreign"}, {ValueBase64: "AA=="}, {Flags: "0"}, {CompareIndex: "0"}, {KeysOnly: true}, {Recursive: true}, {TransactionJSON: "[]"},
			{HTTPSession: contract.KVHTTPSessionToolInput{ID: sessionToolID}},
			{HTTPSession: contract.KVHTTPSessionToolInput{IncludeNode: true}},
			{HTTPSession: contract.KVHTTPSessionToolInput{TTL: "10m"}},
		} {
			if kind == contract.KVToolHTTPNodeSessions {
				input.HTTPSession.Node = "original"
			}
			if _, err := (requestBuilder{}).BuildKVRequest(kind, input); err == nil {
				t.Fatal("hidden incompatible fields accepted", kind)
			}
		}
	}
	for _, node := range []string{"", "nul\x00", string([]byte{255}), strings.Repeat("n", 4097)} {
		if _, err := (requestBuilder{}).BuildKVRequest(contract.KVToolHTTPNodeSessions, contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{Node: node}}); err == nil {
			t.Fatal("invalid original node accepted")
		}
	}
	for _, node := range []string{".", "..", "/leading", "trailing/", "node//other", "node/../other", "node/./other"} {
		if _, err := (requestBuilder{}).BuildKVRequest(contract.KVToolHTTPNodeSessions, contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{Node: node}}); !errors.Is(err, ErrUnsupported) {
			t.Fatal("node path that Consul may rewrite was accepted", node, err)
		}
	}
	tools := requestTools("original-prefix")
	if len(tools) != 35 || tools[11].Kind != contract.KVToolHTTPSessionList || tools[12].Kind != contract.KVToolHTTPNodeSessions || tools[11].Default != (contract.KVRequestToolInput{}) || tools[12].Default != (contract.KVRequestToolInput{}) {
		t.Fatal("public Session read tools missing or invented an authored node or prefix")
	}
}
