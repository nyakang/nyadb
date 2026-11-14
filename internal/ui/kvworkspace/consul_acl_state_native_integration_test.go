package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/locales"
)

func captureNativeHiddenACL(t *testing.T, tt *ui.Tester, d *Document, language string, size [2]int, stage string, labels ...string) {
	t.Helper()
	if d.httpACL == nil || d.httpACL.revealed != "" || d.httpACL.browser != nil && d.httpACL.browser.preview != "" {
		t.Fatal("native ACL image attempted to include revealed private content")
	}
	tt.Frame()
	for _, label := range labels {
		box, ok := tt.Find(locales.Text(label))
		if !ok || box.W <= 0 || box.H <= 0 || box.X < 0 || box.Y < 0 || box.X+box.W > float32(size[0])+1 || box.Y+box.H > float32(size[1])+1 {
			t.Fatal("original ACL workspace control clipped", stage, language, label)
		}
	}
	if dir := os.Getenv("NYADB_CONSUL_ACL_NATIVE_IMAGES"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("%s-%dx%d-workspace-acl-%s.png", language, size[0], size[1], stage)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, tt.Image())
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			t.Fatal("original hidden ACL workspace capture failed")
		}
	}
}

func TestConsulNativePrivateACLBrowserRevealCopyExportOriginalReceipt(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	s, d, _ := consulWorkspaceState(t, p, secret)
	s.ReadHTTPACL(d, contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"})
	consume(t, s, d)
	assertNativePrivateACLState(t, s, d, secret)
	original := d.httpACL.last.Clone()
	originalRequest := d.Request()
	var written bytes.Buffer
	s.SetFiles(Files{SavePath: func(string) (string, error) { return "owned-memory-only-acl.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		written.Reset()
		return write(&written)
	}})
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := workspaceTester(s, d, size[0], size[1])
			workspaceClick(t, tt, "ACL 原响应浏览器")
			if d.httpACL.revealed != "" || d.httpACL.browser.preview != "" {
				t.Fatal("native ACL browser revealed original secrets by default")
			}
			captureNativeHiddenACL(t, tt, d, language, size, "hidden", "显示原 ACL 正文（含秘密）", "返回原 HTTP 回执")
			workspaceClick(t, tt, "显示原 ACL 正文（含秘密）")
			if d.httpACL.browser.preview != clipped(string(original.Receipt.HTTP.Body)) || secret != "" && !strings.Contains(d.httpACL.browser.preview, secret) {
				t.Fatal("native explicit reveal lost original body")
			}
			// Native secrets remain only in memory, never fixture logs or images.
			workspaceClick(t, tt, "隐藏秘密并返回 ACL 列表")
			if secret != "" {
				workspaceClick(t, tt, original.Projection.Rows[0].Identity)
				workspaceClick(t, tt, "复制所选 ACL 原值…")
				if d.httpACL.transfer == nil || d.FileBusy() || tt.Clipboard() != "" {
					t.Fatal("native private copy bypassed original consent")
				}
				workspaceClick(t, tt, "我确认复制或导出此原回执中的秘密")
				workspaceClick(t, tt, "确认传出 ACL 内容")
				consume(t, s, d)
				if tt.Clipboard() != string(original.Projection.Rows[0].Raw) {
					t.Fatal("native private clipboard lost original value")
				}
			}
			workspaceClick(t, tt, "导出完整 ACL 回执…")
			captureNativeHiddenACL(t, tt, d, language, size, "export-consent", "我确认复制或导出此原回执中的秘密", "确认传出 ACL 内容", "取消")
			workspaceClick(t, tt, "我确认复制或导出此原回执中的秘密")
			workspaceClick(t, tt, "确认传出 ACL 内容")
			consume(t, s, d)
			var exported struct {
				Result contract.KVHTTPACLReadResult `json:"result"`
			}
			if json.Unmarshal(written.Bytes(), &exported) != nil || !reflect.DeepEqual(exported.Result, original) || !reflect.DeepEqual(*d.httpACL.last, original) || d.Request() != originalRequest || d.last != nil || d.unknown != nil {
				t.Fatal("native UI transfer changed original private scope or receipt")
			}
			workspaceClick(t, tt, "返回原 HTTP 回执")
		}
	}
}

func TestConsulNativeACLReadFormSixResourcesAndOriginalRecovery(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	s, d, manager := consulWorkspaceState(t, p, secret)
	request := d.Request()
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := workspaceTester(s, d, size[0], size[1])
			for _, resource := range []string{"token", "policy", "role", "auth-method", "binding-rule", "templated-policy"} {
				s.OpenHTTPACLReadForm(d)
				input := HTTPACLReadInput{Resource: resource, Lookup: "list"}
				if resource != "templated-policy" {
					input.IncludeIndex, input.Index, input.IncludeWait, input.Wait = true, "0", true, "1ms"
				}
				s.SetHTTPACLReadInput(d, input)
				captureNativeHiddenACL(t, tt, d, language, size, resource+"-form", "读取原连接 ACL", "复制 ACL 读取输入")
				workspaceClick(t, tt, "读取原连接 ACL")
				consume(t, s, d)
				if !d.httpACLContent() || d.httpACL.browser == nil || !d.httpACL.browser.open || d.httpACL.revealed != "" || d.Request() != request || d.last != nil || d.unknown != nil {
					t.Fatal("native ACL form did not retain independent original hidden receipt")
				}
				captureNativeHiddenACL(t, tt, d, language, size, resource+"-result", "显示原 ACL 正文（含秘密）", "返回原 HTTP 回执")
				r := d.httpACL.last
				want, err := input.request()
				if err != nil || !reflect.DeepEqual(r.Request, want) || r.Scope.Generation != d.scope.Generation || r.Scope.Namespace.ProfileID != p.ID || r.Receipt.HTTP == nil || !r.Receipt.HTTP.Captured || !r.Receipt.HTTP.EOF {
					t.Fatal("native ACL form changed original parameters or scope")
				}
				if secret != "" {
					if r.Receipt.HTTP.Status != 200 || r.Projection == nil || !r.Projection.ProjectionComplete || r.Projection.Resource != resource {
						t.Fatal("native ACL resource form lost its original projection")
					}
				} else if r.Receipt.HTTP.Status != 401 || r.Projection != nil {
					t.Fatal("ACL-disabled form read became an empty successful inventory")
				}
			}
		}
	}
	original := d.httpACL.last.Clone()
	input := d.httpACL.form.input
	manager.Disconnect(p.ID)
	s.Consume()
	if err := manager.Connect(context.Background(), p, secret); err != nil {
		t.Fatal("original native Consul reconnect failed")
	}
	s.Recover(d)
	consume(t, s, d)
	if d.NeedsRecovery() || d.httpACL.form.input != input || !reflect.DeepEqual(*d.httpACL.last, original) || d.scope.Generation <= original.Scope.Generation {
		t.Fatal("native form recovery replayed or replaced original draft/receipt")
	}
}

func assertNativePrivateACLState(t *testing.T, s *State, d *Document, secret string) {
	t.Helper()
	if !d.httpACLContent() || !d.httpACL.valid || d.httpACL.last.Receipt.HTTP == nil || !d.httpACL.last.Receipt.Completed || !d.httpACL.last.Receipt.HTTP.Captured || !d.httpACL.last.Receipt.HTTP.EOF || d.httpACL.last.Receipt.WriteOutcomeUnknown {
		t.Fatal("native private ACL state lost complete original receipt")
	}
	r := d.httpACL.last
	if secret != "" {
		var token struct{ SecretID string }
		if r.Receipt.HTTP.Status != 200 || r.Projection == nil || !r.Projection.ContainsSensitiveValues || json.Unmarshal(r.Receipt.HTTP.Body, &token) != nil || token.SecretID != secret {
			t.Fatal("native token self lost original private credential")
		}
	} else if r.Receipt.HTTP.Status != 401 || r.Projection != nil {
		t.Fatal("ACL-disabled response became a successful token inventory")
	}
	if _, ok := s.RevealedHTTPACLReceipt(d); ok {
		t.Fatal("native ACL response revealed secrets without original selection")
	}
}

func TestConsulNativePrivateACLWorkspaceParkingRevealAndOriginalRecovery(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	s, d, m := consulWorkspaceState(t, p, secret)
	if !d.HTTPACLAvailable() {
		t.Fatal("original Consul document did not expose its private ACL facet")
	}
	request := contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"}
	originalEditor := d.Request()
	s.ReadHTTPACL(d, request)
	job := d.job
	if job == nil {
		t.Fatal("private original ACL read did not start")
	}
	active := false
	s.deps.Active = func(*Document) bool { return active }
	await(t, job.finished)
	s.Consume()
	if d.job != job || d.httpACLContent() || d.last != nil {
		t.Fatal("parked ACL read entered an inactive document")
	}
	active = true
	consume(t, s, d)
	assertNativePrivateACLState(t, s, d, secret)
	original := d.httpACL.last.Clone()
	if d.Request() != originalEditor || d.last != nil || d.unknown != nil {
		t.Fatal("private ACL response entered generic editor/receipt state")
	}
	if raw, err := s.OperationRecord(d); err == nil || len(raw) != 0 {
		t.Fatal("private ACL response entered generic operation export")
	}
	if !s.RevealHTTPACLReceipt(d, original.LocalRecordID) {
		t.Fatal("explicit original ACL reveal failed")
	}
	shown, ok := s.RevealedHTTPACLReceipt(d)
	if !ok || !reflect.DeepEqual(shown, original) {
		t.Fatal("explicit reveal changed original private receipt")
	}
	shown.Receipt.HTTP.Body[0] ^= 1
	if !reflect.DeepEqual(*d.httpACL.last, original) {
		t.Fatal("revealed receipt aliases original private cache")
	}
	active = false
	s.Consume()
	active = true
	assertNativePrivateACLState(t, s, d, secret)
	for range 2 {
		m.Disconnect(p.ID)
		s.Consume()
		if !d.NeedsRecovery() {
			t.Fatal("original ACL document did not require explicit recovery")
		}
		if err := m.Connect(context.Background(), p, secret); err != nil {
			t.Fatal("original Consul reconnect failed")
		}
		s.Recover(d)
		consume(t, s, d)
		if d.NeedsRecovery() || d.scope.Generation <= original.Scope.Generation || !reflect.DeepEqual(*d.httpACL.last, original) || d.Request() != originalEditor {
			t.Fatal("pure runtime/workspace recovery replayed or relabeled original ACL evidence")
		}
		assertNativePrivateACLState(t, s, d, secret)
	}
	s.ReadHTTPACL(d, request)
	consume(t, s, d)
	assertNativePrivateACLState(t, s, d, secret)
	if d.httpACL.last.LocalRecordID == original.LocalRecordID || d.httpACL.last.Scope.Generation != d.scope.Generation {
		t.Fatal("new explicit ACL read inherited an old receipt identity")
	}
	s.Close(d)
	if d.httpACL != nil || d.job != nil || d.lease != nil {
		t.Fatal("close retained original private ACL state")
	}
}

func TestConsulNativePrivateACLWorkspaceReadKeepsProcessedUnknownWriteIndependent(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	proxy, calls, committed := consulWorkspaceFault(t, p, "http500")
	s, d, m := consulWorkspaceState(t, proxy, secret)
	put, err := consul.KeyPutRequest("native/workspace/acl-original-unknown", []byte{0, 255, 128}, nil, nil)
	if err != nil {
		t.Fatal("owned KV fixture request failed")
	}
	consulWorkspaceInput(t, s, d, put)
	s.Run(d)
	s.Confirm(d, true)
	await(t, committed)
	consume(t, s, d)
	if d.unknown == nil || !d.unknown.Result.WriteOutcomeUnknown || calls.Load() != 1 {
		t.Fatal("actual processed write did not retain unknown original outcome")
	}
	unknown := d.unknown.Clone()
	before, err := s.OperationRecord(d)
	if err != nil {
		t.Fatal("original generic receipt unavailable")
	}
	request := contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"}
	s.ReadHTTPACL(d, request)
	consume(t, s, d)
	assertNativePrivateACLState(t, s, d, secret)
	acl := d.httpACL.last.Clone()
	after, err := s.OperationRecord(d)
	if err != nil || !bytes.Equal(after, before) || !reflect.DeepEqual(*d.unknown, unknown) || !d.hasUnknownWrites() || calls.Load() != 1 {
		t.Fatal("ACL read disclosed secrets through generic record or cleared original unknown write")
	}
	m.Disconnect(p.ID)
	s.Consume()
	if err := m.Connect(context.Background(), proxy, secret); err != nil {
		t.Fatal("original Consul reconnect failed")
	}
	s.Recover(d)
	consume(t, s, d)
	if d.NeedsRecovery() || !reflect.DeepEqual(*d.httpACL.last, acl) || !reflect.DeepEqual(*d.unknown, unknown) || calls.Load() != 1 {
		t.Fatal("pure recovery mixed or replayed original private/unknown receipts")
	}
	s.Run(d)
	if d.job != nil || d.confirmation != nil || !d.hasUnknownWrites() || calls.Load() != 1 {
		t.Fatal("ACL read/recovery removed original write barrier")
	}
}
