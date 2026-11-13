package shell

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func TestConsulNativePublicShellAgentBrowserOriginalTabsMapsParkingDisconnectAndRecovery(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	p, secret := consulShellGridProfile(t)
	s, tt, p, _ := consulShellGridConnect(t, p, secret)
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("original health document unavailable")
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
	capture := func(stage string, labels ...string) {
		t.Helper()
		for _, language := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return language })
			for _, size := range [][2]int{{1280, 800}, {720, 520}} {
				view := ui.NewTester(func(c *ui.Context) { s.View(c) }, size[0], size[1])
				view.Frame()
				for _, label := range labels {
					r, ok := view.Find(locales.Text(label))
					if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
						t.Fatal("health Shell control clipped", stage, language, size, label, r)
					}
				}
				if dir := os.Getenv("NYADB_CONSUL_AGENT_UNIT_IMAGES"); dir != "" {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%dx%d-shell-agent-%s.png", language, size[0], size[1], stage)))
					if err != nil {
						t.Fatal(err)
					}
					err = png.Encode(f, view.Image())
					closeErr := f.Close()
					if err != nil || closeErr != nil {
						t.Fatal(err, closeErr)
					}
				}
			}
		}
		i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	}
	for _, entry := range []struct {
		kind  contract.KVRequestToolKind
		label string
	}{{contract.KVToolHTTPAgentSelf, "读取本机 Agent 信息"}, {contract.KVToolHTTPAgentServices, "读取本机 Agent 服务"}, {contract.KVToolHTTPAgentChecks, "读取本机 Agent 检查"}} {
		tt.Frame()
		if err := tt.Click(locales.Text("KV 请求工具")); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if err := tt.ChooseMenuItem(locales.Text(entry.label)); err != nil {
			t.Fatal("Agent menu missing", entry.kind, err)
		}
		capture(string(entry.kind)+"-form", "生成请求文本", "复制工具输入", "返回请求文本")
		s.kvWorkspace.GenerateRequestTool(d)
		if d.Busy() {
			t.Fatal("Agent form generation dispatched")
		}
	}
	s.kvWorkspace.OpenRequestTool(d, contract.KVToolHTTPAgentServices)
	s.kvWorkspace.GenerateRequestTool(d)
	request := d.Request()
	s.kvWorkspace.Run(d)
	s.selectTab(second.id)
	for range 3 {
		tt.Frame()
	}
	if !d.Busy() || s.currentTab() != second {
		t.Fatal("parked health request consumed or activated original tab")
	}
	s.selectTab(first.id)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	r, ok := d.LastReceipt()
	if !ok || r.Result.HTTP == nil || r.Result.HTTP.Agent == nil || !r.Result.HTTP.Agent.ProjectionComplete || r.Authored != request {
		t.Fatal("Shell original health receipt missing", d.Error())
	}
	tt.Frame()
	if err := tt.Click(locales.Text("本机 Agent 服务与检查")); err != nil {
		t.Fatal(err)
	}
	capture("list", "筛选已返回的 Agent 条目", "返回原 HTTP 回执")
	tt.Frame()
	if err := tt.Click(locales.Text("返回原 HTTP 回执")); err != nil {
		t.Fatal(err)
	}
	want, err := s.kvWorkspace.OperationRecord(d)
	if err != nil {
		t.Fatal(err)
	}
	scope := d.Scope()
	s.sessions.Disconnect(p.ID)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if !d.NeedsRecovery() || d.Request() != request {
		t.Fatal("disconnect discarded original health text")
	}
	if err := s.runtime.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	s.kvWorkspace.Recover(d)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	got, err := s.kvWorkspace.OperationRecord(d)
	if err != nil || !bytes.Equal(got, want) || d.Scope().Generation == scope.Generation || d.NeedsRecovery() {
		t.Fatal("health recovery changed original observation", err)
	}
	capture("recovered", "运行 KV 请求", "复制完整操作记录")
	s.selectTab(second.id)
	s.closeTabID(first.id)
	s.kvWorkspace.DecideLeave(ui.Services{}, true)
	if s.currentTab() != second || d.Editor != nil {
		t.Fatal("inactive health close retained resources or changed active tab")
	}
}
