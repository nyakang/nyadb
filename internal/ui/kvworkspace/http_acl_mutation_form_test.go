package kvworkspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func TestHTTPACLMutationInputRetainsDormantFieldsAndExactDefinition(t *testing.T) {
	input := HTTPACLMutationInput{Resource: "token", Operation: "create", Identity: "dormant", Definition: " {\n\"Description\":\"原文\"\n} ", Description: "dormant", IncludeDescription: true}
	r, err := input.request()
	if err != nil || string(r.Definition) != input.Definition || r.Identity != "" || r.Description != nil {
		t.Fatal("create normalized original bytes or sent dormant fields")
	}
	for _, operation := range []string{"update", "delete", "clone"} {
		input.Operation = operation
		r, err = input.request()
		if err != nil || r.Identity != input.Identity {
			t.Fatal("qualified original identity lost")
		}
		if operation == "clone" && (r.Description == nil || *r.Description != input.Description) || operation == "delete" && (len(r.Definition) != 0 || r.Description != nil) {
			t.Fatal("dormant payload entered native request")
		}
	}
	input.Operation = "unfinished"
	raw, err := encodeHTTPACLMutationInput(input)
	decoded, err2 := decodeHTTPACLMutationInput(raw)
	if err != nil || err2 != nil || decoded != input {
		t.Fatal("input file lost incomplete original draft")
	}
	for _, bad := range [][]byte{[]byte(`null`), bytes.Replace(raw, []byte(`"consul-acl-write"`), []byte(`"consul-original-acl-write-receipt"`), 1), bytes.Replace(raw, []byte(`"Resource": "token"`), []byte(`"Resource":null`), 1), bytes.Replace(raw, []byte(`"Resource": "token"`), []byte(`"Resource":"token","Resource":"role"`), 1), bytes.Replace(raw, []byte(`"Resource": "token"`), []byte(`"resource":"token"`), 1)} {
		if _, err := decodeHTTPACLMutationInput(bad); err == nil {
			t.Fatal("foreign input envelope accepted")
		}
	}
}

func TestHTTPACLMutationFilesRequireOriginalConsentAndParkClipboard(t *testing.T) {
	s, d, l, _ := aclMutationWorkspace(t)
	s.OpenHTTPACLMutationForm(d)
	input := HTTPACLMutationInput{Resource: "token", Operation: "create", Definition: ` {"SecretID":"synthetic-authored-secret"} `}
	s.SetHTTPACLMutationForm(d, input)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services() }, 720, 520)
	tt.Frame()
	if s.confirmHTTPACLMutationTransfer(services, d, true) || !s.prepareHTTPACLMutationTransfer(d, "copy-acl-write-input") || s.confirmHTTPACLMutationTransfer(services, d, false) || d.FileBusy() {
		t.Fatal("private input transfer bypassed confirmation")
	}
	if !s.confirmHTTPACLMutationTransfer(services, d, true) || s.confirmHTTPACLMutationTransfer(services, d, true) {
		t.Fatal("private input transfer not one use")
	}
	active := false
	s.deps.Active = func(*Document) bool { return active }
	await(t, d.io.finished)
	s.Consume()
	if tt.Clipboard() != "" || !d.FileBusy() {
		t.Fatal("parked private copy published")
	}
	active = true
	consume(t, s, d)
	copied, err := decodeHTTPACLMutationInput([]byte(tt.Clipboard()))
	if err != nil || copied != input || l.mutations.Load() != 0 || l.preparations.Load() != 0 {
		t.Fatal("private copy changed input or granted execution")
	}
	for _, change := range []string{"input", "park", "disconnect"} {
		if !s.prepareHTTPACLMutationTransfer(d, "copy-acl-write-input") {
			t.Fatal("private snapshot absent")
		}
		switch change {
		case "input":
			input.Definition += " "
			s.SetHTTPACLMutationForm(d, input)
		case "park":
			active = false
			s.Consume()
			active = true
		case "disconnect":
			s.DisconnectProfile(d.profileID)
		}
		if s.confirmHTTPACLMutationTransfer(services, d, true) || d.FileBusy() {
			t.Fatal("stale private snapshot exported")
		}
	}
}

func TestHTTPACLMutationFilesKeepCompleteReceiptAndNeverAuthorizeWrite(t *testing.T) {
	s, d, l, _ := aclMutationWorkspace(t)
	s.OpenHTTPACLMutationForm(d)
	input := HTTPACLMutationInput{Resource: "token", Operation: "create", Definition: ` {"Description":"owned"} `}
	s.SetHTTPACLMutationForm(d, input)
	var saved bytes.Buffer
	var savedPath string
	s.SetFiles(Files{SavePath: func(name string) (string, error) { savedPath = name; return "owned-memory-only.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error { saved.Reset(); return write(&saved) }})
	if !s.prepareHTTPACLMutationTransfer(d, "save-acl-write-input") || !s.confirmHTTPACLMutationTransfer(ui.Services{}, d, true) {
		t.Fatal("private save absent")
	}
	consume(t, s, d)
	decoded, err := decodeHTTPACLMutationInput(saved.Bytes())
	if err != nil || decoded != input || d.Dirty() || savedPath != "acl-write.nyakv-request.json" || l.mutations.Load() != 0 {
		t.Fatal("save changed authored input or did not save original version")
	}
	s.PrepareHTTPACLMutationForm(d)
	consume(t, s, d)
	s.ConfirmHTTPACLMutation(d, true)
	consume(t, s, d)
	original := d.httpACLMutation.last.Clone()
	if !s.prepareHTTPACLMutationTransfer(d, "export-acl-write-receipt") || !s.confirmHTTPACLMutationTransfer(ui.Services{}, d, true) {
		t.Fatal("private receipt export absent")
	}
	consume(t, s, d)
	var exported struct {
		Version int
		Kind    string
		Result  contract.KVHTTPACLMutationResult
	}
	if json.Unmarshal(saved.Bytes(), &exported) != nil || exported.Version != 1 || exported.Kind != "consul-original-acl-write-receipt" || !reflect.DeepEqual(exported.Result, original) || l.mutations.Load() != 1 || d.last != nil || d.unknown != nil {
		t.Fatal("export normalized original receipt or entered generic flow")
	}
	if _, err := decodeHTTPACLMutationInput(saved.Bytes()); err == nil {
		t.Fatal("secret receipt loaded as write input")
	}
	raw, _ := encodeHTTPACLMutationInput(HTTPACLMutationInput{Resource: "policy", Operation: "update", Identity: "unfinished", Definition: "{"})
	s.SetFiles(Files{OpenPath: func() (string, error) { return "owned-input.json", nil }, OpenReader: func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil }})
	s.openHTTPACLMutationInput(d)
	consume(t, s, d)
	if d.httpACLMutation.form.input.Identity != "unfinished" || d.httpACLMutation.form.input.Definition != "{" || l.mutations.Load() != 1 || d.httpACLMutation.confirmation != nil || !reflect.DeepEqual(*d.httpACLMutation.last, original) {
		t.Fatal("loading draft executed or lost original private receipt")
	}
}

func TestHTTPACLMutationFileCancelWaitsForPickerAndNeverWrites(t *testing.T) {
	s, d, _, _ := aclMutationWorkspace(t)
	s.OpenHTTPACLMutationForm(d)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	writes := 0
	s.SetFiles(Files{SavePath: func(string) (string, error) { close(entered); <-release; return "owned-never-written.json", nil }, WriteStream: func(string, func(io.Writer) error) error {
		writes++
		return errors.New("synthetic-secret-provider-error")
	}})
	s.prepareHTTPACLMutationTransfer(d, "save-as-acl-write-input")
	s.confirmHTTPACLMutationTransfer(ui.Services{}, d, true)
	await(t, entered)
	s.CancelFile(d)
	s.Consume()
	if !d.FileBusy() {
		t.Fatal("canceled picker invented completion")
	}
	once.Do(func() { close(release) })
	consume(t, s, d)
	if writes != 0 || strings.Contains(d.Error(), "synthetic-secret") {
		t.Fatal("canceled transfer wrote or leaked provider error")
	}
}

func TestHTTPACLMutationFormBilingualConfirmationAndPrivateReceipt(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			t.Run(fmt.Sprintf("%s-%dx%d", language, size[0], size[1]), func(t *testing.T) {
				s, d, l, _ := aclMutationWorkspace(t)
				var saved bytes.Buffer
				s.SetFiles(Files{SavePath: func(string) (string, error) { return "owned-memory.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error { saved.Reset(); return write(&saved) }})
				tt := workspaceTester(s, d, size[0], size[1])
				s.OpenHTTPACLMutationForm(d)
				cases := []HTTPACLMutationInput{{Resource: "token", Operation: "create", Definition: ` {"Description":"original"} `}, {Resource: "token", Operation: "update", Identity: "original-token", Definition: `{}`}, {Resource: "token", Operation: "clone", Identity: "original-token", IncludeDescription: true}, {Resource: "policy", Operation: "delete", Identity: "original-policy"}, {Resource: "role", Operation: "delete", Identity: "original-role"}, {Resource: "auth-method", Operation: "delete", Identity: "original-method"}, {Resource: "binding-rule", Operation: "update", Identity: "original-binding", Definition: `{}`}}
				for index, input := range cases {
					s.SetHTTPACLMutationForm(d, input)
					tt.Frame()
					assertHTTPSessionRenewalControls(t, tt, size, "ACL 资源类型", "ACL 写入操作", "检查并确认 ACL 写入", "返回请求文本")
					captureACLUnit(t, tt, language, size, fmt.Sprintf("write-%d-form", index))
					workspaceClick(t, tt, "检查并确认 ACL 写入")
					consume(t, s, d)
					if d.httpACLMutation.confirmation == nil || l.mutations.Load() != int32(index) {
						t.Fatal("form bypassed original confirmation")
					}
					tt.Frame()
					assertHTTPSessionRenewalControls(t, tt, size, "我确认执行此原连接 ACL 写入", "确认执行 ACL 写入", "取消")
					captureACLUnit(t, tt, language, size, fmt.Sprintf("write-%d-confirm", index))
					workspaceClick(t, tt, "我确认执行此原连接 ACL 写入")
					workspaceClick(t, tt, "确认执行 ACL 写入")
					consume(t, s, d)
					tt.Frame()
					if !d.httpACLMutationContent() || d.httpACLMutation.revealed != "" || l.mutations.Load() != int32(index+1) || d.last != nil {
						t.Fatal("private native result missing or exposed")
					}
					expected, _ := input.request()
					if !reflect.DeepEqual(d.httpACLMutation.last.Request, expected) {
						t.Fatal("form changed original write request")
					}
					assertHTTPSessionRenewalControls(t, tt, size, "显示 ACL 写入原正文（含秘密）", "导出完整 ACL 写入回执…", "返回 ACL 写入输入")
					captureACLUnit(t, tt, language, size, fmt.Sprintf("write-%d-hidden", index))
					workspaceClick(t, tt, "返回 ACL 写入输入")
				}
				s.openHTTPACLMutationReceipt(d)
				tt.Frame()
				workspaceClick(t, tt, "导出完整 ACL 写入回执…")
				tt.Frame()
				assertHTTPSessionRenewalControls(t, tt, size, "我确认传出此原输入或回执中的秘密", "确认传出 ACL 写入内容", "取消")
				captureACLUnit(t, tt, language, size, "write-export-consent")
				workspaceClick(t, tt, "我确认传出此原输入或回执中的秘密")
				workspaceClick(t, tt, "确认传出 ACL 写入内容")
				consume(t, s, d)
				if saved.Len() == 0 || l.mutations.Load() != int32(len(cases)) {
					t.Fatal("confirmed receipt transfer absent or executed a write")
				}
			})
		}
	}
}

func TestHTTPACLMutationMenuUsesOriginalProviderWithoutDispatch(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		s, d, l, _ := aclMutationWorkspace(t)
		tt := workspaceTester(s, d, 720, 520)
		workspaceClick(t, tt, "KV 请求工具")
		if err := tt.ChooseMenuItem(locales.Text("ACL 原生写入")); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if d.httpACLMutation == nil || d.httpACLMutation.form == nil || !d.httpACLMutation.form.open || d.Busy() || l.preparations.Load() != 0 || l.mutations.Load() != 0 {
			t.Fatal("menu did not open a pure original write form")
		}
	}
}
