package kvworkspace

import (
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/locales"
)

func TestHTTPACLReadFormBilingualInputFilesAndExplicitRead(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			t.Run(fmt.Sprintf("%s-%dx%d", language, size[0], size[1]), func(t *testing.T) {
				s, d, l, _ := aclWorkspace(t)
				input := HTTPACLReadInput{Resource: "token", Lookup: "list", IncludePolicy: true, Policy: "", IncludeIndex: true, Index: "18446744073709551615", IncludeWait: true, Wait: "1ms"}
				var saved bytes.Buffer
				loaded := defaultHTTPACLReadInput()
				loadBytes, _ := encodeHTTPACLReadInput(loaded)
				s.SetFiles(Files{OpenPath: func() (string, error) { return "owned-loaded-input.json", nil }, OpenReader: func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(loadBytes)), nil }, SavePath: func(string) (string, error) { return "owned-saved-input.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error { saved.Reset(); return write(&saved) }})
				tt := workspaceTester(s, d, size[0], size[1])
				workspaceClick(t, tt, "读取 ACL 资源")
				s.SetHTTPACLReadInput(d, input)
				tt.Frame()
				assertHTTPSessionRenewalControls(t, tt, size, "ACL 资源类型", "ACL 查询方式", "ACL 原标识或名称", "打开 ACL 读取输入", "保存 ACL 读取输入", "另存 ACL 读取输入", "复制 ACL 读取输入", "读取原连接 ACL")
				captureACLUnit(t, tt, language, size, "read-form")
				workspaceClick(t, tt, "复制 ACL 读取输入")
				consume(t, s, d)
				got, err := decodeHTTPACLReadInput([]byte(tt.Clipboard()))
				if err != nil || got != input {
					t.Fatal("ACL input clipboard changed original fields")
				}
				workspaceClick(t, tt, "保存 ACL 读取输入")
				consume(t, s, d)
				got, err = decodeHTTPACLReadInput(saved.Bytes())
				if err != nil || got != input || d.Dirty() {
					t.Fatal("ACL input save lost original draft")
				}
				workspaceClick(t, tt, "打开 ACL 读取输入")
				consume(t, s, d)
				if d.httpACL.form.input != loaded || l.plans.Load() != 0 || l.reads.Load() != 0 || d.httpACLContent() {
					t.Fatal("opening ACL input executed or changed identity")
				}
				workspaceClick(t, tt, "读取原连接 ACL")
				consume(t, s, d)
				tt.Frame()
				if !d.httpACLContent() || d.httpACL.browser == nil || !d.httpACL.browser.open || d.httpACL.revealed != "" || l.reads.Load() != 1 || l.executes.Load() != 0 {
					t.Fatal("explicit original read did not produce private hidden receipt")
				}
				if _, ok := tt.Find(locales.Text("ACL 原响应浏览器")); !ok {
					t.Fatal("original private receipt view unavailable")
				}
				captureACLUnit(t, tt, language, size, "form-original-result")
			})
		}
	}
}
