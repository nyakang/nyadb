package kvworkspace

import (
	"bytes"
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
	"github.com/nyakang/nyadb/locales"
)

func captureNativeACLMutationForm(t *testing.T, tt *ui.Tester, d *Document, language string, size [2]int, stage string, secrets []string, labels ...string) {
	t.Helper()
	x := d.httpACLMutation
	if x == nil || x.form == nil || x.revealed != "" || x.form.preview != "" {
		t.Fatal("native ACL write image attempted to show private receipt")
	}
	raw, _ := json.Marshal(x.form.input)
	for _, secret := range secrets {
		if secret != "" && strings.Contains(string(raw), secret) {
			t.Fatal("native ACL image input contained a credential")
		}
	}
	tt.Frame()
	assertHTTPSessionRenewalControls(t, tt, size, labels...)
	if dir := os.Getenv("NYADB_CONSUL_ACL_MUTATION_NATIVE_IMAGES"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal("native ACL write image directory unavailable")
		}
		path := filepath.Join(dir, fmt.Sprintf("%s-%dx%d-acl-write-%s.png", language, size[0], size[1], stage))
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal("native ACL write image path unavailable")
		}
		err = png.Encode(f, tt.Image())
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			t.Fatal("native hidden ACL write capture failed")
		}
	}
}

func TestConsulNativeACLMutationFormOriginalMenuConfirmationSecretReceiptAndFiles(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			s, d, _ := consulWorkspaceState(t, p, secret)
			originalEditor := d.Request()
			tt := workspaceTester(s, d, size[0], size[1])
			var written bytes.Buffer
			s.SetFiles(Files{SavePath: func(string) (string, error) { return "owned-memory-acl-write.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error { written.Reset(); return write(&written) }})
			workspaceClick(t, tt, "KV 请求工具")
			if err := tt.ChooseMenuItem(locales.Text("ACL 原生写入")); err != nil {
				t.Fatal("native ACL mutation menu entry unavailable")
			}
			tt.Frame()
			input := HTTPACLMutationInput{Resource: "token", Operation: "create", Definition: ` {"Description":"owned native form token"} `}
			s.SetHTTPACLMutationForm(d, input)
			captureNativeACLMutationForm(t, tt, d, language, size, "input", []string{secret}, "ACL 资源类型", "ACL 写入操作", "检查并确认 ACL 写入")
			workspaceClick(t, tt, "检查并确认 ACL 写入")
			consume(t, s, d)

			if secret == "" {
				assertNativeACLDisabledPreview(t, s, d)
				captureNativeACLMutationForm(t, tt, d, language, size, "failed-preview", nil, "显示 ACL 影响记录（含秘密）", "导出完整 ACL 影响记录…", "返回 ACL 写入输入")
				workspaceClick(t, tt, "导出完整 ACL 影响记录…")
				captureNativeACLMutationForm(t, tt, d, language, size, "export-confirmation", nil, "我确认传出此原输入或回执中的秘密", "确认传出 ACL 写入内容", "取消")
				workspaceClick(t, tt, "我确认传出此原输入或回执中的秘密")
				workspaceClick(t, tt, "确认传出 ACL 写入内容")
				consume(t, s, d)
				var exported struct{ Observation httpACLMutationObservation }
				if json.Unmarshal(written.Bytes(), &exported) != nil || !reflect.DeepEqual(exported.Observation, *d.httpACLMutation.observation) {
					t.Fatal("ACL-disabled original preview export changed")
				}
				s.Close(d)
				continue
			}
			if d.Busy() || d.httpACLMutation.confirmation == nil || d.httpACLMutationContent() {
				t.Fatal("native form bypassed original write confirmation")
			}
			captureNativeACLMutationForm(t, tt, d, language, size, "confirmation", []string{secret}, "我确认执行此原连接 ACL 写入", "确认执行 ACL 写入", "取消")
			workspaceClick(t, tt, "查看 ACL 影响记录")
			captureNativeACLMutationForm(t, tt, d, language, size, "impact", []string{secret}, "显示 ACL 影响记录（含秘密）", "导出完整 ACL 影响记录…", "返回 ACL 写入确认")
			workspaceClick(t, tt, "返回 ACL 写入确认")
			workspaceClick(t, tt, "我确认执行此原连接 ACL 写入")
			workspaceClick(t, tt, "确认执行 ACL 写入")
			consume(t, s, d)
			if !d.httpACLMutationContent() || d.Request() != originalEditor || d.last != nil || d.unknown != nil || d.hasUnknownWrites() {
				t.Fatal("native form write did not retain independent original receipt")
			}
			original := d.httpACLMutation.last.Clone()
			secrets := []string{secret}
			if secret != "" {
				if !original.Receipt.Completed || original.Projection == nil || !original.Projection.ContainsSensitiveValues {
					t.Fatal("native form lost generated token secret evidence")
				}
				id := nativeWorkspaceACLIdentity(t, original, "AccessorID")
				generated := nativeWorkspaceACLIdentity(t, original, "SecretID")
				secrets = append(secrets, generated)
				t.Cleanup(func() {
					nativeWorkspaceACLMutation(t, writer, contract.KVHTTPACLMutationRequest{Resource: "token", Operation: "delete", Identity: id})
				})
			} else if original.Receipt.HTTP == nil || original.Receipt.HTTP.Status != 401 {
				t.Fatal("ACL-disabled form write invented success")
			}
			captureNativeACLMutationForm(t, tt, d, language, size, "hidden-receipt", secrets, "显示 ACL 写入原正文（含秘密）", "导出完整 ACL 写入回执…", "返回 ACL 写入输入")
			workspaceClick(t, tt, "显示 ACL 写入原正文（含秘密）")
			if d.httpACLMutation.form.preview != clipped(string(original.Receipt.HTTP.Body)) {
				t.Fatal("native explicit reveal changed original response")
			}
			workspaceClick(t, tt, "隐藏 ACL 写入秘密")
			workspaceClick(t, tt, "导出完整 ACL 写入回执…")
			if d.FileBusy() || written.Len() != 0 {
				t.Fatal("native secret export bypassed confirmation")
			}
			captureNativeACLMutationForm(t, tt, d, language, size, "export-confirmation", secrets, "我确认传出此原输入或回执中的秘密", "确认传出 ACL 写入内容", "取消")
			workspaceClick(t, tt, "我确认传出此原输入或回执中的秘密")
			workspaceClick(t, tt, "确认传出 ACL 写入内容")
			consume(t, s, d)
			var exported struct {
				Result contract.KVHTTPACLMutationResult
			}
			if json.Unmarshal(written.Bytes(), &exported) != nil || !reflect.DeepEqual(exported.Result, original) || !reflect.DeepEqual(*d.httpACLMutation.last, original) {
				t.Fatal("native form export changed original private receipt")
			}
			s.Close(d)
		}
	}
}
