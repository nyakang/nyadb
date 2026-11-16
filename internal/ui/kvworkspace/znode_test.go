package kvworkspace

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func znodeTestState(t *testing.T) (*State, *Document) {
	t.Helper()
	scope := contract.KVScope{Namespace: contract.NamespaceRef{ProfileID: 7}, Generation: 11, Prefix: []byte("/"), Version: "3.9.4", ZNode: contract.KVZNodeIdentity{Protocol: "zookeeper-jsonrpc-v1", ConnectString: "original:2181", Root: "/original", Configuration: "captured", DriverVersion: "v1", DriverSHA256: "original-package"}}
	request := contract.KVRequest{Method: "kv_get", JSON: `{"key":"/example"}`}
	p := &httpTestProvider{fakeProvider: &fakeProvider{lease: &fakeLease{scope: scope}}, presentation: contract.KVRequestPresentation{Product: "ZooKeeper", Default: request, Methods: []contract.KVRequestMethod{{Name: "kv_get", Request: request}}}}
	s := New(Dependencies{ProviderFor: func(int64) (contract.KVRequestsProvider, error) { return p, nil }})
	d := s.Open(1, 7, contract.KVRequest{})
	t.Cleanup(s.CloseAll)
	d.last = &Receipt{Scope: d.scope.Clone(), Authored: request, Result: contract.KVResult{Request: request, Attempted: true, Completed: true, Code: "OK", JSON: `{"ephemeralOwner":9007199254740997,"value":"中文"}`, ZNode: &contract.KVZNodeResult{Captured: true, Acknowledged: true, Native: []byte(`{"jsonrpc":"2.0","id":9007199254740999,"result":{"ephemeralOwner":9007199254740997,"value":"中文"},"future":1e+09}`)}}}
	return s, d
}

func TestZNodeOperationRecordAndExportKeepOriginalEnvelopeWithoutEtcdMetadata(t *testing.T) {
	s, d := znodeTestState(t)
	raw, err := s.OperationRecord(d)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]json.RawMessage
	if json.Unmarshal(raw, &record) != nil || record["native_protobuf"] != nil || record["native_header"] != nil || record["protobuf_json"] != nil {
		t.Fatal("znode receipt fabricated an etcd receipt")
	}
	var native struct {
		Original []byte `json:"original_jsonrpc_base64"`
		Result   string `json:"result_json"`
	}
	if json.Unmarshal(record["znode"], &native) != nil || !bytes.Equal(native.Original, d.last.Result.ZNode.Native) || native.Result != d.last.Result.JSON {
		t.Fatal("original JSON-RPC bytes or exact native numbers changed")
	}
	if copy, ok := s.ReceiptJSON(d); !ok || copy != string(raw) {
		t.Fatal("complete clipboard differs from operation record")
	}
	var name string
	var saved bytes.Buffer
	s.SetFiles(Files{SavePath: func(n string) (string, error) { name = n; return "original", nil }, WriteStream: func(_ string, write func(io.Writer) error) error { return write(&saved) }})
	original := bytes.Clone(d.last.Result.ZNode.Native)
	s.ExportReceipt(d, true)
	d.last.Result.ZNode.Native[0] = 'x'
	consume(t, s, d)
	if name != "response.jsonrpc.json" || !bytes.Equal(saved.Bytes(), original) {
		t.Fatal("export did not capture the original envelope")
	}
}

func TestZNodeRecoveryIdentityIncludesLogicalRootConfigurationAndDriver(t *testing.T) {
	_, d := znodeTestState(t)
	original := d.Scope()
	for _, mutate := range []func(*contract.KVZNodeIdentity){
		func(z *contract.KVZNodeIdentity) { z.Root = "/foreign" },
		func(z *contract.KVZNodeIdentity) { z.Configuration = "changed trust or route" },
		func(z *contract.KVZNodeIdentity) { z.DriverSHA256 = "replacement" },
		func(z *contract.KVZNodeIdentity) { z.ConnectString = "foreign:2181" },
		func(z *contract.KVZNodeIdentity) { z.Username = "foreign" },
	} {
		foreign := original.Clone()
		mutate(&foreign.ZNode)
		if sameKVRecoveryIdentity(original, foreign) {
			t.Fatal("recovery admitted a different original znode identity")
		}
	}
	if !sameKVRecoveryIdentity(original, original.Clone()) {
		t.Fatal("original identity rejected")
	}
}

func TestZNodeUIShowsNativeReceiptAndExplicitExportWithoutRaftMetadata(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	s, d := znodeTestState(t)
	for _, size := range [][2]int{{720, 520}, {1280, 800}} {
		tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, size[0], size[1])
		tt.Frame()
		for _, label := range []string{"KV 原生回执", "复制完整操作记录", "复制原请求"} {
			r, ok := tt.Find(locales.Text(label))
			if !ok || r.W <= 0 || r.H <= 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
				t.Fatal("native znode control clipped", label, size, r)
			}
		}
		if err := tt.Click(locales.Text("复制完整操作记录")); err != nil {
			t.Fatal(err)
		}
	}
	// Generic JSON serialization must not expose a private native error frame.
	d.last.Result.ZNode.Native = []byte(`{"error":{"message":"private credential"}}`)
	raw, err := json.Marshal(d.last.Result)
	if err != nil || strings.Contains(string(raw), "private credential") {
		t.Fatal("generic receipt serialization leaked native error text")
	}
}

func TestZNodeRecoveryRestoresCompleteLastAndSeparateUnknownEvidence(t *testing.T) {
	s, d := znodeTestState(t)
	unknown := d.last.Result.Clone()
	unknown.Request = contract.KVRequest{Method: "kv_put", JSON: `{"key":"/original"}`}
	unknown.Completed, unknown.WriteOutcomeUnknown = false, true
	last := d.last.Result.Clone()
	d.last = nil
	job := &operation{scope: d.scope.Clone()}
	result := &outcome{recoveryResult: &last, recoveryUnknown: &unknown}
	s.consumeRequestRecovery(d, job, result)
	if result.err != nil || d.last == nil || d.unknown == nil || d.unknown.Result.Request != unknown.Request {
		t.Fatal("recovery lost independent original znode receipts")
	}
	result.recoveryUnknown.ZNode.Native[0] = 'x'
	if d.unknown.Result.ZNode.Native[0] == 'x' {
		t.Fatal("recovered evidence aliases worker result")
	}
}

func TestZNodeRequestFilesRetainDraftsAndCannotChangeDocumentProtocol(t *testing.T) {
	s, d := znodeTestState(t)
	for _, method := range []string{"kv_get", "kv_put", "kv_delete", "kv_list_prefix", "connection_info"} {
		request := contract.KVRequest{Method: method, JSON: "{\n  \"unfinished draft\": "}
		raw, err := EncodeRequest(request)
		if err != nil {
			t.Fatal("native draft file rejected", method, err)
		}
		decoded, err := DecodeRequest(raw)
		if err != nil || decoded != request {
			t.Fatal("draft file changed original method/text", method, err)
		}
	}
	if !s.documentMethodAllowed(d, "kv_get") || s.documentMethodAllowed(d, "connect") || s.documentMethodAllowed(d, "/etcdserverpb.KV/Put") {
		t.Fatal("native document admitted a foreign file protocol")
	}
	otherState, protobuf, _ := testState(t)
	if otherState.documentMethodAllowed(protobuf, "kv_get") || !otherState.documentMethodAllowed(protobuf, DefaultRequest().Method) {
		t.Fatal("native file could replace a protobuf document")
	}
	for _, method := range []string{"connect", "disconnect", "cancel_session", "execute_query"} {
		if _, err := EncodeRequest(contract.KVRequest{Method: method, JSON: `{}`}); err == nil {
			t.Fatal("native lifecycle operation admitted as a request file", method)
		}
	}
}
