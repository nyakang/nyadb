package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/locales"
)

func TestConsulNativePublicShellSessionToolsOriginalFormsTabsReceiptsAndRecovery(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	p, secret := consulShellGridProfile(t)
	s, tt, p, services := consulShellGridConnect(t, p, secret)
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("native tool document unavailable")
	}
	first := s.currentTab()
	d := first.kv
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if len(d.RequestTools()) != 35 {
		t.Fatal("Shell missing public Session tools")
	}
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("second original document unavailable")
	}
	second := s.currentTab()
	consulShellGridWait(t, tt, func() bool { return !second.kv.Busy() })
	s.selectTab(first.id)
	tt.Frame()
	if err := tt.Click(locales.Text("KV 请求工具")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem(locales.Text("创建 HTTP Session")); err != nil {
		t.Fatal("Shell public menu missing Session create", err)
	}
	input := contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{IncludeName: true, Name: t.Name(), TTL: "10m", NodeChecksJSON: "[]", LockDelay: "0s"}}
	s.kvWorkspace.SetRequestToolInput(d, contract.KVToolHTTPSessionCreate, input)
	screenshot := func(stage string) {
		for _, lang := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return lang })
			for _, size := range [][2]int{{1280, 800}, {720, 520}} {
				view := ui.NewTester(func(c *ui.Context) { s.View(c) }, size[0], size[1])
				view.Frame()
				labels := []string{"运行 KV 请求", "复制完整操作记录"}
				if stage == "form" {
					labels = []string{"请求工具操作", "生成请求文本", "复制工具输入", "导出工具输入", "包含 Session 名称", "包含 Session 节点", "NodeChecks JSON（留空省略，[] 为无检查）"}
				}
				if _, ok := d.Confirmation(); ok {
					labels = []string{"我确认执行此原生写入请求", "确认执行 KV 写入", "复制完整请求"}
				}
				for _, label := range labels {
					r, ok := view.Find(locales.Text(label))
					if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
						t.Fatal("Shell Session tool control clipped", stage, lang, size, label, r)
					}
				}
				dir := os.Getenv("NYADB_CONSUL_SESSION_TOOLS_IMAGES")
				if dir == "" {
					continue
				}
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%dx%d-shell-session-tool-%s.png", lang, size[0], size[1], stage)))
				if err != nil {
					t.Fatal(err)
				}
				err = png.Encode(f, view.Image())
				ce := f.Close()
				if err != nil || ce != nil {
					t.Fatal(err, ce)
				}
			}
		}
		i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	}
	screenshot("form")
	original := d.Request()
	s.kvWorkspace.CopyRequestToolInput(services, d)
	consulShellGridWait(t, tt, func() bool { return !d.FileBusy() })
	var tool struct {
		Version int
		Tool    contract.KVRequestToolKind
		Input   contract.KVRequestToolInput
	}
	if json.Unmarshal([]byte(tt.Clipboard()), &tool) != nil || tool.Version != 1 || tool.Tool != contract.KVToolHTTPSessionCreate || tool.Input != input || d.Request() != original {
		t.Fatal("Shell tool copy changed original input")
	}
	s.kvWorkspace.GenerateRequestTool(d)
	generated := d.Request()
	s.kvWorkspace.Run(d)
	if _, ok := d.Confirmation(); !ok || d.Busy() {
		t.Fatal("Shell generated Session write bypassed confirmation", d.Error())
	}
	screenshot("confirmation")
	s.kvWorkspace.Confirm(d, false)
	if d.Busy() {
		t.Fatal("unchecked Shell Session create dispatched")
	}
	s.kvWorkspace.Confirm(d, true)
	s.kvWorkspace.Confirm(d, true)
	s.selectTab(second.id)
	for range 3 {
		tt.Frame()
	}
	if !d.Busy() || s.currentTab() != second {
		t.Fatal("parked Session completion altered active document")
	}
	s.selectTab(first.id)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	receipt, ok := d.LastReceipt()
	if !ok || receipt.Authored != generated || receipt.Result.HTTP == nil || !receipt.Result.HTTP.Acknowledged || receipt.Result.HTTP.SessionID == "" {
		t.Fatal("Shell original Session UUID receipt missing", d.Error())
	}
	id := receipt.Result.HTTP.SessionID
	screenshot("receipt")
	tt.Frame()
	if err := tt.Click(locales.Text("复制完整操作记录")); err != nil {
		t.Fatal(err)
	}
	consulShellGridWait(t, tt, func() bool { return !d.FileBusy() })
	want, err := s.kvWorkspace.OperationRecord(d)
	if err != nil || tt.Clipboard() != string(want) {
		t.Fatal("Shell original complete receipt copy changed", err)
	}
	scope := d.Scope()
	s.sessions.Disconnect(p.ID)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if !d.NeedsRecovery() || d.Request() != generated || s.currentTab() != first {
		t.Fatal("disconnect replaced original Session draft")
	}
	if err := s.runtime.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	s.kvWorkspace.Recover(d)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	got, err := s.kvWorkspace.OperationRecord(d)
	if err != nil || !bytes.Equal(want, got) || d.NeedsRecovery() || d.Scope().Generation == scope.Generation {
		t.Fatal("Shell recovery changed evidence or original generation", err)
	}
	screenshot("recovered")
	s.selectTab(second.id)
	s.closeTabID(first.id)
	s.kvWorkspace.DecideLeave(ui.Services{}, true)
	if s.currentTab() != second || d.Editor != nil || len(d.RequestTools()) != 0 {
		t.Fatal("inactive original tool close changed selection or retained inputs")
	}
	provider, err := s.runtime.KVRequests(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := provider.ReserveKV(9651)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	request, err := consul.DocumentRequest(consul.Request{Method: "GET", Path: "/v1/session/list"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := writer.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	out, err := writer.Execute(context.Background(), plan)
	if err != nil || out.HTTP == nil {
		t.Fatal("native original list failed", err)
	}
	var sessions []struct{ ID, Name string }
	if json.Unmarshal(out.HTTP.Body, &sessions) != nil {
		t.Fatal("native Session list invalid")
	}
	count := 0
	for _, session := range sessions {
		if session.Name == t.Name() {
			count++
			if session.ID != id {
				t.Fatal("Shell create original UUID changed")
			}
		}
	}
	if count != 1 {
		t.Fatal("Shell recovery replayed Session creation", count)
	}
	request, err = consul.DocumentRequest(consul.Request{Method: "PUT", Path: "/v1/session/destroy/" + id})
	if err != nil {
		t.Fatal(err)
	}
	plan, err = writer.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Execute(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
}
