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

func TestConsulNativePublicShellSessionBrowserOriginalMenusParkingReceiptsAndRecovery(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	p, secret := consulShellGridProfile(t)
	s, tt, p, _ := consulShellGridConnect(t, p, secret)
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("original Session document unavailable")
	}
	first := s.currentTab()
	d := first.kv
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	s.kvWorkspace.OpenRequestTool(d, contract.KVToolHTTPSessionCreate)
	s.kvWorkspace.SetRequestToolInput(d, contract.KVToolHTTPSessionCreate, contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{IncludeName: true, Name: t.Name(), TTL: "10m", NodeChecksJSON: "[]", LockDelay: "0s"}})
	s.kvWorkspace.GenerateRequestTool(d)
	s.kvWorkspace.Run(d)
	s.kvWorkspace.Confirm(d, true)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	created, ok := d.LastReceipt()
	if !ok || created.Result.HTTP == nil || created.Result.HTTP.SessionID == "" {
		t.Fatal("original create UUID missing", d.Error())
	}
	id := created.Result.HTTP.SessionID
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("second original Session document unavailable")
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
					r, found := view.Find(locales.Text(label))
					if !found || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
						t.Fatal("original Session browser control clipped", stage, language, size, label, r)
					}
				}
				if dir := os.Getenv("NYADB_CONSUL_SESSION_BROWSER_IMAGES"); dir != "" {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%dx%d-shell-session-browser-%s.png", language, size[0], size[1], stage)))
					if err != nil {
						t.Fatal(err)
					}
					err, closeErr := png.Encode(f, view.Image()), f.Close()
					if err != nil || closeErr != nil {
						t.Fatal(err, closeErr)
					}
				}
			}
		}
		i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPSessionList, contract.KVToolHTTPNodeSessions} {
		label := "列出 HTTP Sessions"
		if kind == contract.KVToolHTTPNodeSessions {
			label = "列出节点 Sessions"
		}
		tt.Frame()
		if err := tt.Click(locales.Text("KV 请求工具")); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if err := tt.ChooseMenuItem(locales.Text(label)); err != nil {
			t.Fatal("public original Session read menu missing", err)
		}
		input := contract.KVRequestToolInput{}
		if kind == contract.KVToolHTTPNodeSessions {
			input.HTTPSession.Node = d.Scope().HTTP.AgentName
		}
		s.kvWorkspace.SetRequestToolInput(d, kind, input)
		capture(string(kind)+"-form", "请求工具操作", "生成请求文本", "复制工具输入")
		s.kvWorkspace.GenerateRequestTool(d)
		request := d.Request()
		if d.Busy() {
			t.Fatal("Session read generation dispatched")
		}
		s.kvWorkspace.Run(d)
		if _, confirmed := d.Confirmation(); confirmed {
			t.Fatal("Session read gained write confirmation")
		}
		s.selectTab(second.id)
		for range 3 {
			tt.Frame()
		}
		if !d.Busy() || s.currentTab() != second {
			t.Fatal("parked original Session read selected another tab")
		}
		s.selectTab(first.id)
		consulShellGridWait(t, tt, func() bool { return !d.Busy() })
		receipt, found := d.LastReceipt()
		if !found || receipt.Result.HTTP == nil || receipt.Result.HTTP.Sessions == nil || receipt.Authored != request {
			t.Fatal("Shell original complete Session observation missing", d.Error())
		}
		tt.Frame()
		if err := tt.Click(locales.Text("Session 列表")); err != nil {
			t.Fatal(err)
		}
		capture(string(kind)+"-list", "筛选已返回的 Session", "Session TTL 筛选", "查看所选原始详情", "返回原 HTTP 回执")
		tt.Frame()
		if err := tt.Click(locales.Text("筛选已返回的 Session")); err != nil {
			t.Fatal(err)
		}
		tt.Type(id)
		tt.Frame()
		if err := tt.Click(id); err != nil {
			t.Fatal("original Session UUID not selectable", err)
		}
		tt.Frame()
		if err := tt.Click(locales.Text("查看所选原始详情")); err != nil {
			t.Fatal(err)
		}
		capture(string(kind)+"-detail", "Session 原始详情", "返回 Session 列表", "返回原 HTTP 回执")
		tt.Frame()
		if err := tt.Click(locales.Text("返回原 HTTP 回执")); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if err := tt.Click(locales.Text("复制完整操作记录")); err != nil {
			t.Fatal(err)
		}
		consulShellGridWait(t, tt, func() bool { return !d.FileBusy() })
		want, err := s.kvWorkspace.OperationRecord(d)
		if err != nil || tt.Clipboard() != string(want) || d.Request() != request {
			t.Fatal("Shell browser changed complete original receipt", err)
		}
	}
	want, err := s.kvWorkspace.OperationRecord(d)
	if err != nil {
		t.Fatal(err)
	}
	request, scope := d.Request(), d.Scope()
	s.sessions.Disconnect(p.ID)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if !d.NeedsRecovery() || d.Request() != request {
		t.Fatal("disconnect discarded original Session request")
	}
	if err := s.runtime.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	s.kvWorkspace.Recover(d)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	got, err := s.kvWorkspace.OperationRecord(d)
	if err != nil || !bytes.Equal(want, got) || d.Scope().Generation == scope.Generation || d.NeedsRecovery() {
		t.Fatal("Session recovery replaced the original observation", err)
	}
	capture("recovered", "运行 KV 请求", "复制完整操作记录")
	s.selectTab(second.id)
	s.closeTabID(first.id)
	s.kvWorkspace.DecideLeave(ui.Services{}, true)
	if s.currentTab() != second || d.Editor != nil {
		t.Fatal("inactive Session browser close retained resources or selected another tab")
	}
}
