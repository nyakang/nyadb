package shell

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
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/kvworkspace"
	"github.com/nyakang/nyadb/locales"
)

func TestConsulNativePublicShellACLOriginalFormsParkingFilesAndRecovery(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	p, secret := consulShellGridProfile(t)
	p, gate := consulShellAgentMutationRelay(t, p, "/v1/acl/token/self", "GET")
	s, tt, p, _ := consulShellGridConnect(t, p, secret)
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("original ACL document unavailable")
	}
	first := s.currentTab()
	d := first.kv
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("second original document unavailable")
	}
	second := s.currentTab()
	consulShellGridWait(t, tt, func() bool { return !second.kv.Busy() })
	s.selectTab(first.id)
	originalRequest := d.Request()
	var exported bytes.Buffer
	s.kvWorkspace.SetFiles(kvworkspace.Files{SavePath: func(string) (string, error) { return "owned-memory-only-acl.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error { exported.Reset(); return write(&exported) }})
	capture := func(stage string, labels ...string) {
		t.Helper()
		for _, language := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return language })
			for _, size := range [][2]int{{1280, 800}, {720, 520}} {
				view := ui.NewTester(func(c *ui.Context) { s.View(c) }, size[0], size[1])
				view.Frame()
				for _, label := range labels {
					box, ok := view.Find(locales.Text(label))
					if !ok || box.W <= 0 || box.H <= 0 || box.X < 0 || box.Y < 0 || box.X+box.W > float32(size[0])+1 || box.Y+box.H > float32(size[1])+1 {
						t.Fatal("original ACL Shell control clipped", stage, language, label)
					}
				}
				// Only hidden lists, authored noncredential input and consent are
				// photographed. Actual token bodies and exported values stay in memory.
				if dir := os.Getenv("NYADB_CONSUL_ACL_NATIVE_IMAGES"); dir != "" {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%dx%d-shell-acl-%s.png", language, size[0], size[1], stage)))
					if err != nil {
						t.Fatal(err)
					}
					err = png.Encode(f, view.Image())
					closeErr := f.Close()
					if err != nil || closeErr != nil {
						t.Fatal("original ACL Shell capture failed")
					}
				}
			}
		}
		i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	}
	consulShellGridClick(t, tt, "读取 ACL 资源")
	capture("form", "读取原连接 ACL", "复制 ACL 读取输入", "保存 ACL 读取输入")
	consulShellGridClick(t, tt, "读取原连接 ACL")
	select {
	case <-gate.processed:
	case <-time.After(20 * time.Second):
		t.Fatal("original ACL read was not processed")
	}
	s.selectTab(second.id)
	gate.release <- struct{}{}
	consulShellGridWait(t, tt, func() bool { return gate.delivered.Load() == 1 })
	if !d.Busy() || s.currentTab() != second {
		t.Fatal("foreign Shell tab consumed the original completed ACL read")
	}
	s.selectTab(first.id)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if d.Request() != originalRequest {
		t.Fatal("ACL form replaced generic original editor")
	}
	if _, ok := d.LastReceipt(); ok {
		t.Fatal("ACL response entered generic receipt")
	}
	capture("hidden", "显示原 ACL 正文（含秘密）", "返回原 HTTP 回执")
	export := func(captureConsent bool) contract.KVHTTPACLReadResult {
		t.Helper()
		consulShellGridClick(t, tt, "导出完整 ACL 回执…")
		if captureConsent {
			capture("export-consent", "我确认复制或导出此原回执中的秘密", "确认传出 ACL 内容", "取消")
		}
		consulShellGridClick(t, tt, "我确认复制或导出此原回执中的秘密")
		consulShellGridClick(t, tt, "确认传出 ACL 内容")
		consulShellGridWait(t, tt, func() bool { return !d.FileBusy() })
		var record struct {
			Result contract.KVHTTPACLReadResult `json:"result"`
		}
		if json.Unmarshal(exported.Bytes(), &record) != nil || record.Result.LocalRecordID == "" || record.Result.Scope.Namespace.ProfileID != p.ID || record.Result.Receipt.HTTP == nil {
			t.Fatal("Shell export lost original ACL identity")
		}
		return record.Result
	}
	original := export(true)
	if original.Scope.Generation != d.Scope().Generation {
		t.Fatal("Shell ACL result used a foreign generation")
	}
	consulShellGridClick(t, tt, "返回原 HTTP 回执")
	s.sessions.Disconnect(p.ID)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if !d.NeedsRecovery() {
		t.Fatal("original ACL document did not require recovery")
	}
	if err := s.runtime.Connect(context.Background(), p, secret); err != nil {
		t.Fatal("original Consul reconnect failed")
	}
	s.kvWorkspace.Recover(d)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if d.NeedsRecovery() || d.Scope().Generation <= original.Scope.Generation || gate.writes.Load() != 1 {
		t.Fatal("original Shell recovery replayed ACL request")
	}
	consulShellGridClick(t, tt, "ACL 原响应浏览器")
	capture("recovered", "显示原 ACL 正文（含秘密）", "返回原 HTTP 回执")
	if got := export(false); !reflect.DeepEqual(got, original) || gate.writes.Load() != 1 {
		t.Fatal("recovered Shell export relabeled or replaced original ACL receipt")
	}
	consulShellGridClick(t, tt, "返回原 HTTP 回执")
	s.selectTab(second.id)
	s.closeTabID(first.id)
	s.kvWorkspace.DecideLeave(ui.Services{}, true)
	if s.currentTab() != second || d.Editor != nil {
		t.Fatal("inactive ACL close changed tab or retained document")
	}
}
