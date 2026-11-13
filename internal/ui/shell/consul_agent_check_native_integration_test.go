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

func TestConsulNativePublicShellAgentCheckOriginalTabsConfirmationsParkingAndRecovery(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	p, secret := consulShellGridProfile(t)
	p, gate := consulShellAgentMutationRelay(t, p, "/v1/agent/check/")
	s, tt, p, _ := consulShellGridConnect(t, p, secret)
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("original service request document unavailable")
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
						t.Fatal("Agent service Shell control clipped", stage, language, size, label, r)
					}
				}
				if dir := os.Getenv("NYADB_CONSUL_AGENT_CHECK_IMAGES"); dir != "" {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					file, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%dx%d-shell-agent-check-%s.png", language, size[0], size[1], stage)))
					if err != nil {
						t.Fatal(err)
					}
					err = png.Encode(file, view.Image())
					closeErr := file.Close()
					if err != nil || closeErr != nil {
						t.Fatal(err, closeErr)
					}
				}
			}
		}
		i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	}
	provider, err := s.runtime.KVRequests(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := provider.ReserveKV(9231)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	readMap := func() map[string]json.RawMessage {
		t.Helper()
		native, err := consul.AgentChecksRequest(nil)
		if err != nil {
			t.Fatal(err)
		}
		request, err := consul.DocumentRequest(native)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := reader.Prepare(request)
		if err != nil {
			t.Fatal(err)
		}
		result, err := reader.Execute(context.Background(), plan)
		if err != nil || !result.Completed {
			t.Fatal("original Shell map read failed", err)
		}
		var entries map[string]json.RawMessage
		if json.Unmarshal(result.HTTP.Body, &entries) != nil {
			t.Fatal("original Shell service map invalid")
		}
		return entries
	}
	id := "owned-" + t.Name() + "-Unicode 雪%?#++"
	body, err := json.Marshal(map[string]any{"Name": "owned-" + t.Name(), "ID": id, "TTL": "10m", "Notes": "original 原值 18446744073709551615"})
	if err != nil {
		t.Fatal(err)
	}
	for index, action := range []struct {
		kind                          contract.KVRequestToolKind
		label, field, authored, stage string
	}{
		{contract.KVToolHTTPAgentCheckRegister, "注册或更新本机 Agent 检查", "编辑完整检查定义 JSON", " \n" + string(body) + "\n ", "register"},
		{contract.KVToolHTTPAgentCheckDeregister, "注销本机 Agent 检查", "原生检查 ID", id, "deregister"},
	} {
		consulShellGridClick(t, tt, "KV 请求工具")
		if err := tt.ChooseMenuItem(locales.Text(action.label)); err != nil {
			t.Fatal("original Agent service menu missing", action.kind, err)
		}
		tt.Frame()
		consulShellGridClick(t, tt, action.field)
		tt.Key(ui.Ctrl, ui.KeyA)
		tt.Type(action.authored)
		tt.Frame()
		input, ok := d.RequestToolInput(action.kind)
		if !ok || action.kind == contract.KVToolHTTPAgentCheckRegister && input.HTTPAgentCheck.DefinitionJSON != action.authored || action.kind == contract.KVToolHTTPAgentCheckDeregister && input.HTTPAgentCheck.ID != id {
			t.Fatal("Shell service form changed original input")
		}
		capture(action.stage+"-form", action.field, "生成请求文本", "复制工具输入")
		consulShellGridClick(t, tt, "生成请求文本")
		original := d.Request()
		if d.Busy() {
			t.Fatal("Shell form generation dispatched")
		}
		consulShellGridClick(t, tt, "运行 KV 请求")
		capture(action.stage+"-confirmation", "复制完整请求", "我确认执行此原生写入请求", "确认执行 KV 写入")
		consulShellGridClick(t, tt, "复制完整请求")
		if tt.Clipboard() == "" {
			t.Fatal("Shell immutable confirmation clipboard empty")
		}
		consulShellGridClick(t, tt, "我确认执行此原生写入请求")
		consulShellGridClick(t, tt, "确认执行 KV 写入")
		if !d.Busy() {
			_, confirmation := d.Confirmation()
			t.Fatal("Shell confirmation did not dispatch original service write", action.stage, confirmation, d.Error())
		}
		consulShellGridWait(t, tt, func() bool { return len(gate.processed) != 0 })
		<-gate.processed
		if gate.writes.Load() != int32(index+1) || gate.delivered.Load() != int32(index) {
			t.Fatal("Shell confirmation dispatched more than one original service write or released its reply early")
		}
		s.selectTab(second.id)
		for range 3 {
			tt.Frame()
		}
		if !d.Busy() || s.currentTab() != second {
			t.Fatal("parked service completion consumed or activated original tab")
		}
		consulShellGridWait(t, tt, func() bool { return (readMap()[id] != nil) == (action.kind == contract.KVToolHTTPAgentCheckRegister) })
		gate.release <- struct{}{}
		consulShellGridWait(t, tt, func() bool { return gate.delivered.Load() == int32(index+1) })
		if !d.Busy() || s.currentTab() != second {
			t.Fatal("completed original service reply consumed while its tab was parked")
		}
		s.selectTab(first.id)
		consulShellGridWait(t, tt, func() bool { return !d.Busy() })
		r, ok := d.LastReceipt()
		if !ok || r.Authored != original || !r.Result.Completed || r.Result.HTTP == nil || !r.Result.HTTP.Succeeded || len(r.Result.HTTP.Body) != 0 {
			t.Fatal("Shell lost original empty service receipt", d.Error())
		}
		if action.kind == contract.KVToolHTTPAgentCheckRegister && !bytes.Equal(r.Result.HTTP.Request.Body, []byte(action.authored)) {
			t.Fatal("Shell original service definition changed")
		}
		capture(action.stage+"-receipt", "HTTP 原正文", "复制完整操作记录")
	}
	reader.Close()
	want, err := s.kvWorkspace.OperationRecord(d)
	if err != nil {
		t.Fatal(err)
	}
	scope, authored := d.Scope(), d.Request()
	s.sessions.Disconnect(p.ID)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if !d.NeedsRecovery() || d.Request() != authored {
		t.Fatal("disconnect discarded original service request")
	}
	if err := s.runtime.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	s.kvWorkspace.Recover(d)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	got, err := s.kvWorkspace.OperationRecord(d)
	if err != nil || !bytes.Equal(got, want) || d.Scope().Generation == scope.Generation || d.NeedsRecovery() {
		t.Fatal("Shell service recovery changed original receipt", err)
	}
	capture("recovered", "运行 KV 请求", "复制完整操作记录")
	s.selectTab(second.id)
	s.closeTabID(first.id)
	s.kvWorkspace.DecideLeave(ui.Services{}, true)
	if s.currentTab() != second || d.Editor != nil {
		t.Fatal("inactive service close changed active tab or retained resources")
	}
}
