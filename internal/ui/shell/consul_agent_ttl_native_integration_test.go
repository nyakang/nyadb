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
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/locales"
)

func TestConsulNativePublicShellAgentTTLOriginalTabsConfirmationsParkingAndRecovery(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	p, secret := consulShellGridProfile(t)
	id := "owned-" + t.Name() + "-Unicode 雪%?#++"
	cfg, err := connections.ConsulProfileConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := consul.New(consul.Config{Endpoint: cfg.Endpoint, Auth: cfg.Options.Auth, CAFile: cfg.Options.CAFile, ServerName: cfg.Options.ServerName, ClientCertFile: cfg.Options.ClientCertFile, ClientKeyFile: cfg.Options.ClientKeyFile}, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	definition, err := json.Marshal(map[string]any{"Name": id, "ID": id, "TTL": "10m", "Notes": "original 原值 18446744073709551615"})
	if err != nil {
		t.Fatal(err)
	}
	register, err := consul.AgentCheckRegisterRequest(definition)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := writer.Do(context.Background(), register)
	if err != nil || !registered.Succeeded {
		t.Fatal("original Shell TTL fixture registration failed", err)
	}
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
				if dir := os.Getenv("NYADB_CONSUL_AGENT_TTL_IMAGES"); dir != "" {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					file, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%dx%d-shell-agent-ttl-%s.png", language, size[0], size[1], stage)))
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
	for index, action := range []struct {
		kind                          contract.KVRequestToolKind
		label, field, authored, stage string
	}{
		{contract.KVToolHTTPAgentCheckPass, "将本机 TTL 检查设为 passing", "原始 note JSON 字符串", "original first\nsecond 雪", "pass"},
		{contract.KVToolHTTPAgentCheckWarn, "将本机 TTL 检查设为 warning", "原始 note JSON 字符串", "original warning 雪", "warn"},
		{contract.KVToolHTTPAgentCheckFail, "将本机 TTL 检查设为 critical", "原始 note JSON 字符串", "original critical 雪", "fail"},
		{contract.KVToolHTTPAgentCheckUpdate, "更新本机 TTL 检查状态与输出", "编辑完整 TTL 状态 JSON", " {\n\"Status\":\"passing\",\"Output\":\"original updated 雪\"\n} ", "update"},
	} {
		consulShellGridClick(t, tt, "KV 请求工具")
		if err := tt.ChooseMenuItem(locales.Text(action.label)); err != nil {
			t.Fatal("original Agent service menu missing", action.kind, err)
		}
		tt.Frame()
		consulShellGridClick(t, tt, "原生检查 ID")
		tt.Key(ui.Ctrl, ui.KeyA)
		tt.Type(id)
		tt.Frame()
		if action.kind != contract.KVToolHTTPAgentCheckUpdate {
			consulShellGridClick(t, tt, "显式包含 note（空文本表示清空 Output）")
		}
		consulShellGridClick(t, tt, action.field)
		tt.Key(ui.Ctrl, ui.KeyA)
		if action.kind == contract.KVToolHTTPAgentCheckUpdate {
			tt.Type(action.authored)
		} else {
			encoded, err := json.Marshal(action.authored)
			if err != nil {
				t.Fatal(err)
			}
			tt.Type(string(encoded))
		}
		tt.Frame()
		input, ok := d.RequestToolInput(action.kind)
		if !ok || input.HTTPAgentTTL.ID != id || action.kind == contract.KVToolHTTPAgentCheckUpdate && input.HTTPAgentTTL.DefinitionJSON != action.authored || action.kind != contract.KVToolHTTPAgentCheckUpdate && (!input.HTTPAgentTTL.IncludeNote || input.HTTPAgentTTL.NoteJSON != func() string { raw, _ := json.Marshal(action.authored); return string(raw) }()) {
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
		wantStatus := map[string]string{"pass": "passing", "warn": "warning", "fail": "critical", "update": "passing"}[action.stage]
		wantOutput := action.authored
		if action.kind == contract.KVToolHTTPAgentCheckUpdate {
			wantOutput = "original updated 雪"
		}
		consulShellGridWait(t, tt, func() bool {
			var check struct{ Status, Output, Notes string }
			return json.Unmarshal(readMap()[id], &check) == nil && check.Status == wantStatus && check.Output == wantOutput && check.Notes == "original 原值 18446744073709551615"
		})
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
		if !bytes.Equal(r.Result.HTTP.Request.Body, []byte(input.HTTPAgentTTL.DefinitionJSON)) {
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
