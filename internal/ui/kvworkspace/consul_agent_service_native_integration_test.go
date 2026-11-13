package kvworkspace

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
)

func consulAgentServiceScreenshot(t *testing.T, s *State, d *Document, stage string, labels ...string) {
	t.Helper()
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			assertHTTPSessionRenewalControls(t, tt, size, labels...)
			if dir := os.Getenv("NYADB_CONSUL_AGENT_SERVICE_IMAGES"); dir != "" {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				file, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%dx%d-workspace-agent-service-%s.png", language, size[0], size[1], stage)))
				if err != nil {
					t.Fatal(err)
				}
				err = png.Encode(file, tt.Image())
				closeErr := file.Close()
				if err != nil || closeErr != nil {
					t.Fatal(err, closeErr)
				}
			}
		}
	}
}

func consulAgentServiceDefinition(t *testing.T, id, suffix string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"Name": "owned-" + strings.ReplaceAll(t.Name(), "/", "-"), "ID": id,
		"Tags": []string{"original", "中文"}, "Meta": map[string]string{"original": "原值", "Exact": "18446744073709551615"},
		"Check":   map[string]any{"CheckID": id + "-" + suffix, "TTL": "10m", "Status": "critical"},
		"Connect": map[string]any{"SidecarService": map[string]any{"Port": 19331}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return " \n" + string(body) + "\n "
}

func consulAgentServiceMap(t *testing.T, writer contract.KVRequestLease, checks bool) map[string]json.RawMessage {
	t.Helper()
	request, err := consul.AgentServicesRequest(nil)
	if checks {
		request, err = consul.AgentChecksRequest(nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	result := consulRenewalDo(t, writer, request)
	var entries map[string]json.RawMessage
	if json.Unmarshal(result.HTTP.Body, &entries) != nil {
		t.Fatal("original local map invalid")
	}
	return entries
}

func TestConsulNativePublicAgentServiceFormsOriginalDefinitionsChecksReceiptsAndFiles(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	s, d, _ := consulWorkspaceState(t, p, secret)
	kind := contract.KVToolHTTPAgentServiceRegister
	id := "owned-" + t.Name() + "-Unicode 雪%?#++"
	steps := []struct {
		suffix, replacement string
		include             bool
		kept                []string
	}{
		{"a", "", false, []string{"a"}}, {"b", "", false, []string{"a", "b"}},
		{"c", "false", true, []string{"a", "b", "c"}}, {"d", "true", true, []string{"d"}}, {"e", "", true, []string{"e"}},
	}
	var exported []byte
	s.SetFiles(Files{SavePath: func(string) (string, error) { return "owned-original.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var out bytes.Buffer
		err := write(&out)
		exported = bytes.Clone(out.Bytes())
		return err
	}})
	for index, step := range steps {
		raw := consulAgentServiceDefinition(t, id, step.suffix)
		input := contract.KVRequestToolInput{HTTPAgentService: contract.KVHTTPAgentServiceToolInput{DefinitionJSON: raw, IncludeReplacement: step.include, ReplaceExistingChecks: step.replacement}}
		s.OpenRequestTool(d, kind)
		tt := workspaceTester(s, d, 720, 520)
		if index == 0 {
			workspaceClick(t, tt, "编辑完整服务定义 JSON")
			tt.Key(ui.Ctrl, ui.KeyA)
			tt.Type(raw)
			tt.Frame()
			actual, _ := d.RequestToolInput(kind)
			if actual != input {
				t.Fatal("actual native definition typing changed bytes")
			}
		} else {
			s.SetRequestToolInput(d, kind, input)
		}
		if index == 0 {
			consulAgentServiceScreenshot(t, s, d, "register-form", "编辑完整服务定义 JSON", "生成请求文本", "导出工具输入")
		}
		workspaceClick(t, tt, "复制工具输入")
		consume(t, s, d)
		toolBytes, err := encodeRequestToolInput(kind, input)
		if err != nil || tt.Clipboard() != string(toolBytes) {
			t.Fatal("native form clipboard lost authored definition", err)
		}
		s.ExportRequestToolInput(d)
		consume(t, s, d)
		if !bytes.Equal(exported, toolBytes) {
			t.Fatal("native form export lost original input")
		}
		previous := d.last
		workspaceClick(t, tt, "生成请求文本")
		original := d.Request()
		if d.Busy() || d.confirmation != nil || d.last != previous {
			t.Fatal("native form generation dispatched")
		}
		encoded, err := EncodeRequest(original)
		decoded, decodeErr := DecodeRequest(encoded)
		if err != nil || decodeErr != nil || decoded != original {
			t.Fatal("native request file changed original definition")
		}
		s.Run(d)
		if d.confirmation == nil || d.confirmation.authored != original || d.Busy() {
			t.Fatal("native registration bypassed immutable confirmation", d.Error())
		}
		consulAgentServiceScreenshot(t, s, d, "register-"+step.suffix+"-confirmation", "复制完整请求", "我确认执行此原生写入请求", "确认执行 KV 写入")
		tt.Frame()
		workspaceClick(t, tt, "复制完整请求")
		prepared := d.confirmation.plan.Snapshot()
		var envelope struct {
			Body string `json:"body_base64"`
		}
		err = json.Unmarshal([]byte(prepared.JSON), &envelope)
		copiedBody, bodyErr := base64.StdEncoding.Strict().DecodeString(envelope.Body)
		if tt.Clipboard() != prepared.Method+"\n"+prepared.JSON || err != nil || bodyErr != nil || !bytes.Equal(copiedBody, []byte(raw)) {
			t.Fatal("confirmation copy lost complete native definition")
		}
		workspaceClick(t, tt, "确认执行 KV 写入")
		if d.Busy() {
			t.Fatal("unchecked service registration executed")
		}
		workspaceClick(t, tt, "我确认执行此原生写入请求")
		workspaceClick(t, tt, "确认执行 KV 写入")
		s.Confirm(d, true)
		consume(t, s, d)
		if d.last == nil || d.last.Authored != original || !d.last.Result.Completed || d.unknown != nil || d.last.Result.HTTP == nil {
			t.Fatal("complete native registration receipt missing", d.Error())
		}
		h := d.last.Result.HTTP
		if !h.Captured || !h.EOF || !h.Succeeded || !h.Acknowledged || len(h.Body) != 0 || !bytes.Equal(h.Request.Body, []byte(raw)) || h.Request.Query["dc"] != nil || (h.Request.Query["replace-existing-checks"] != nil) != step.include {
			t.Fatal("native empty ACK, original body or check replacement presence changed")
		}
		if step.include && (len(h.Request.Query["replace-existing-checks"]) != 1 || h.Request.Query["replace-existing-checks"][0] != step.replacement) {
			t.Fatal("explicit native check replacement changed")
		}
		services := consulAgentServiceMap(t, writer, false)
		if services[id] == nil || services[id+"-sidecar-proxy"] == nil {
			t.Fatal("actual main service or sidecar missing")
		}
		checks := consulAgentServiceMap(t, writer, true)
		for _, suffix := range []string{"a", "b", "c", "d", "e"} {
			wanted := false
			for _, keep := range step.kept {
				wanted = wanted || keep == suffix
			}
			if (checks[id+"-"+suffix] != nil) != wanted {
				t.Fatal("native replacement did not retain exact check set", step.suffix, suffix)
			}
		}
		consulAgentServiceScreenshot(t, s, d, "register-"+step.suffix+"-receipt", "HTTP 原正文", "HTTP 头与元数据", "复制完整操作记录")
		want, err := s.OperationRecord(d)
		if err != nil {
			t.Fatal(err)
		}
		workspaceClick(t, tt, "复制完整操作记录")
		consume(t, s, d)
		if tt.Clipboard() != string(want) {
			t.Fatal("native registration full receipt clipboard changed")
		}
		s.ExportOperationRecord(d)
		consume(t, s, d)
		if !bytes.Equal(exported, want) {
			t.Fatal("native registration receipt export changed")
		}
	}
	kind = contract.KVToolHTTPAgentServiceDeregister
	s.OpenRequestTool(d, kind)
	tt := workspaceTester(s, d, 720, 520)
	workspaceClick(t, tt, "原生服务 ID")
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Type(id)
	tt.Frame()
	input, _ := d.RequestToolInput(kind)
	if input.HTTPAgentService.ID != id {
		t.Fatal("native deregistration ID typing changed identity")
	}
	consulAgentServiceScreenshot(t, s, d, "deregister-form", "原生服务 ID", "生成请求文本", "导出工具输入")
	workspaceClick(t, tt, "生成请求文本")
	s.Run(d)
	consulAgentServiceScreenshot(t, s, d, "deregister-confirmation", "复制完整请求", "确认执行 KV 写入")
	s.Confirm(d, true)
	s.Confirm(d, true)
	consume(t, s, d)
	if d.last == nil || !d.last.Result.Completed || !d.last.Result.HTTP.Succeeded || len(d.last.Result.HTTP.Body) != 0 || d.unknown != nil {
		t.Fatal("native deregistration empty success lost", d.Error())
	}
	services := consulAgentServiceMap(t, writer, false)
	if services[id] != nil || services[id+"-sidecar-proxy"] != nil || consulAgentServiceMap(t, writer, true)[id+"-e"] != nil {
		t.Fatal("actual deregistration retained service/sidecar/check")
	}
	consulAgentServiceScreenshot(t, s, d, "deregister-receipt", "HTTP 原正文", "复制完整操作记录")
	s.Run(d)
	s.Confirm(d, true)
	consume(t, s, d)
	if d.last.Result.HTTP.Status != 404 || !d.last.Result.Completed || !d.last.Result.HTTP.Acknowledged || d.last.Result.HTTP.Succeeded || d.unknown != nil {
		t.Fatal("original missing ID rejection guessed an unknown write")
	}
	consulAgentServiceScreenshot(t, s, d, "missing-id-receipt", "HTTP 原正文", "复制完整操作记录")
}

func TestConsulNativePublicAgentServiceProcessedWritesParkingACKRecoveryAndNoReplay(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentServiceRegister, contract.KVToolHTTPAgentServiceDeregister} {
		for _, mode := range []string{"lost", "partial", "http400", "http500", "http408", "invalid", "cancel", "disconnect", "replace", "parked", "close"} {
			t.Run(string(kind)+"/"+mode, func(t *testing.T) {
				id := "owned-" + strings.ReplaceAll(t.Name(), "/", "-")
				input := contract.KVRequestToolInput{HTTPAgentService: contract.KVHTTPAgentServiceToolInput{DefinitionJSON: consulAgentServiceDefinition(t, id, "original")}}
				if kind == contract.KVToolHTTPAgentServiceDeregister {
					r, err := consul.AgentServiceRegisterRequest([]byte(input.HTTPAgentService.DefinitionJSON), nil)
					if err != nil {
						t.Fatal(err)
					}
					consulRenewalDo(t, writer, r)
					input.HTTPAgentService = contract.KVHTTPAgentServiceToolInput{ID: id}
				}
				proxy, calls, writes, processed := consulOriginalToolFault(t, p, mode, func(r *http.Request) bool {
					return r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/v1/agent/service/")
				})
				s, d, manager := consulWorkspaceState(t, proxy, secret)
				s.deps.HTTPSessionRenewalProviderFor = manager.KVHTTPSessionRenewal
				d.httpSessionRenewalAvailable = true
				before := calls.Load()
				consulWorkspaceToolInput(t, s, d, kind, input)
				original := d.Request()
				s.Run(d)
				if d.confirmation == nil || d.confirmation.authored != original || calls.Load() != before || writes.Load() != 0 {
					t.Fatal("service form generation/preparation dispatched")
				}
				s.Confirm(d, true)
				s.Confirm(d, true)
				job, lease := d.job, d.lease
				await(t, processed)
				active := false
				s.deps.Active = func(*Document) bool { return active }
				switch mode {
				case "cancel":
					s.RequestCancel(d)
					if !d.Busy() || !d.CancelRequested() {
						t.Fatal("cancel settled before original executor ACK")
					}
				case "disconnect":
					manager.Disconnect(p.ID)
					s.DisconnectProfile(p.ID)
				case "replace":
					if err := manager.Connect(context.Background(), proxy, secret); err != nil {
						t.Fatal(err)
					}
				case "close":
					s.Close(d)
				}
				await(t, job.finished)
				actual := consulAgentServiceMap(t, writer, false)
				if (actual[id] != nil) != (kind == contract.KVToolHTTPAgentServiceRegister) || writes.Load() != 1 {
					t.Fatal("fault did not follow exactly one actual original mutation")
				}
				if mode == "close" {
					if lease.CheckKVLease() == nil || d.Editor != nil || d.unknown != nil || d.requestTools != nil {
						t.Fatal("original close retained service request resources")
					}
					return
				}
				s.Consume()
				if d.job != job || d.unknown != nil || !d.Busy() {
					t.Fatal("parked original mutation completion consumed")
				}
				active = true
				s.Consume()
				if d.unknown == nil || d.unknown.Authored != original || !d.unknown.Result.WriteOutcomeUnknown || d.unknown.Result.HTTP.Acknowledged || !d.unknown.Result.HTTP.Ended || writes.Load() != 1 {
					t.Fatal("original processed Agent mutation promoted or replayed", d.Error())
				}
				unknown := d.unknown.Clone()
				if mode == "partial" && (unknown.Result.HTTP.Captured || unknown.Result.HTTP.EOF || len(unknown.Result.HTTP.Body) != 0) {
					t.Fatal("empty partial Agent response guessed complete ACK")
				}
				consulAgentServiceScreenshot(t, s, d, string(kind)+"-unknown-"+mode, "复制完整操作记录", "HTTP 原正文")
				if mode == "disconnect" {
					if err := manager.Connect(context.Background(), proxy, secret); err != nil {
						t.Fatal(err)
					}
				}
				before = calls.Load()
				s.Recover(d)
				consume(t, s, d)
				if d.NeedsRecovery() || calls.Load() != before || d.Request() != original || !reflect.DeepEqual(d.unknown, &unknown) {
					t.Fatal("pure Agent recovery dispatched or changed complete original evidence", d.Error())
				}
				consulAgentServiceScreenshot(t, s, d, string(kind)+"-recovered-"+mode, "运行 KV 请求", "复制完整操作记录")
				s.Run(d)
				if d.confirmation != nil || d.Busy() || calls.Load() != before {
					t.Fatal("unknown service mutation authorized replay")
				}
				consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPAgentServices, contract.KVRequestToolInput{})
				s.Run(d)
				consume(t, s, d)
				if agentReceiptProjection(d.last) == nil || !reflect.DeepEqual(d.unknown, &unknown) || writes.Load() != 1 {
					t.Fatal("later Agent read confirmed original unknown mutation")
				}
				for _, blocked := range []struct {
					kind  contract.KVRequestToolKind
					input contract.KVRequestToolInput
				}{{kind, input}, {contract.KVToolPut, contract.KVRequestToolInput{Key: "native/workspace/agent-later", ValueBase64: "AP8="}}} {
					before = calls.Load()
					consulWorkspaceToolInput(t, s, d, blocked.kind, blocked.input)
					s.Run(d)
					if d.confirmation != nil || d.Busy() || calls.Load() != before || writes.Load() != 1 {
						t.Fatal("regeneration or KV operation bypassed original Agent barrier")
					}
				}
				consulHTTPSessionRenewalInput(s, d, renewalUUID, "100ms", 2, 16<<20)
				s.PrepareHTTPSessionRenewal(d)
				if d.httpSessionRenewal.confirmation != nil || d.Busy() || !reflect.DeepEqual(d.unknown, &unknown) {
					t.Fatal("unknown Agent write authorized later Session renewal")
				}
			})
		}
	}
}

func TestConsulNativePublicAgentServiceOriginalPartialSidecarAndACLDenial(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	id := "owned-" + t.Name()
	t.Run("server-rejected-unknown-field-retains-original", func(t *testing.T) {
		s, d, _ := consulWorkspaceState(t, p, secret)
		unknownID := id + "-unknown-field"
		raw := fmt.Sprintf(" {\n\"Name\":%q,\"ID\":%q,\"UnknownNative\":18446744073709551615\n} ", unknownID, unknownID)
		input := contract.KVRequestToolInput{HTTPAgentService: contract.KVHTTPAgentServiceToolInput{DefinitionJSON: raw}}
		consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPAgentServiceRegister, input)
		original := d.Request()
		s.Run(d)
		s.Confirm(d, true)
		consume(t, s, d)
		if d.unknown == nil || d.unknown.Authored != original || d.unknown.Result.HTTP == nil || d.unknown.Result.HTTP.Status != 400 || !d.unknown.Result.HTTP.Captured || !d.unknown.Result.HTTP.EOF || !bytes.Equal(d.unknown.Result.HTTP.Request.Body, []byte(raw)) || !bytes.Contains(d.unknown.Result.HTTP.Body, []byte("UnknownNative")) {
			t.Fatal("server-rejected original definition was rewritten or lost", d.Error())
		}
		if consulAgentServiceMap(t, writer, false)[unknownID] != nil {
			t.Fatal("server-rejected unknown field unexpectedly registered a service")
		}
		retained := d.unknown.Clone()
		s.Recover(d)
		consume(t, s, d)
		s.Run(d)
		if d.confirmation != nil || d.Busy() || !reflect.DeepEqual(d.unknown, &retained) {
			t.Fatal("rejected original definition authorized implicit replay")
		}
	})
	if limitedSecret := os.Getenv("NYADB_CONSUL_READ_TOKEN"); limitedSecret != "" {
		s, d, _ := consulWorkspaceState(t, p, limitedSecret)
		definition := consulAgentServiceDefinition(t, id, "denied")
		r, err := consul.AgentServiceRegisterRequest([]byte(definition), nil)
		if err != nil {
			t.Fatal(err)
		}
		consulRenewalDo(t, writer, r)
		for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentServiceRegister, contract.KVToolHTTPAgentServiceDeregister} {
			input := contract.KVRequestToolInput{HTTPAgentService: contract.KVHTTPAgentServiceToolInput{DefinitionJSON: definition}}
			if kind == contract.KVToolHTTPAgentServiceDeregister {
				input.HTTPAgentService = contract.KVHTTPAgentServiceToolInput{ID: id}
			}
			consulWorkspaceToolInput(t, s, d, kind, input)
			s.Run(d)
			s.Confirm(d, true)
			consume(t, s, d)
			if d.last == nil || d.last.Result.HTTP.Status != 403 || !d.last.Result.Completed || !d.last.Result.HTTP.Acknowledged || d.last.Result.HTTP.Succeeded || d.unknown != nil || len(d.last.Result.HTTP.Body) == 0 {
				t.Fatal("original Agent ACL refusal lost complete known denial", d.Error())
			}
			consulAgentServiceScreenshot(t, s, d, string(kind)+"-acl-or-boundary", "HTTP 原正文", "复制完整操作记录")
		}
		if consulAgentServiceMap(t, writer, false)[id] == nil {
			t.Fatal("denied deregistration changed original service")
		}
	} else {
		s, d, _ := consulWorkspaceState(t, p, secret)
		for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentServiceRegister, contract.KVToolHTTPAgentServiceDeregister} {
			consulAgentServiceScreenshot(t, s, d, string(kind)+"-acl-or-boundary", "运行 KV 请求")
		}
	}
	s, d, _ := consulWorkspaceState(t, p, secret)
	partialID := id + "-partial"
	raw, err := json.Marshal(map[string]any{"Name": partialID, "ID": partialID, "Connect": map[string]any{"SidecarService": map[string]any{"Port": 19332, "Check": map[string]any{"Args": []string{"/bin/true"}, "Interval": "10s"}}}})
	if err != nil {
		t.Fatal(err)
	}
	consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPAgentServiceRegister, contract.KVRequestToolInput{HTTPAgentService: contract.KVHTTPAgentServiceToolInput{DefinitionJSON: string(raw)}})
	original := d.Request()
	s.Run(d)
	s.Confirm(d, true)
	consume(t, s, d)
	if d.unknown == nil || d.unknown.Result.HTTP.Status != 500 || !d.unknown.Result.HTTP.Captured || !d.unknown.Result.HTTP.EOF || d.unknown.Authored != original || !bytes.Equal(d.unknown.Result.HTTP.Request.Body, raw) || len(d.unknown.Result.HTTP.Body) == 0 {
		t.Fatal("actual partial sidecar registration guessed known failure", d.Error())
	}
	if consulAgentServiceMap(t, writer, false)[partialID] == nil {
		t.Fatal("sidecar failure fixture never created actual main service")
	}
	unknown := d.unknown.Clone()
	consulAgentServiceScreenshot(t, s, d, "partial-sidecar-unknown", "HTTP 原正文", "复制完整操作记录")
	consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPAgentServices, contract.KVRequestToolInput{})
	s.Run(d)
	consume(t, s, d)
	s.Recover(d)
	consume(t, s, d)
	if !reflect.DeepEqual(d.unknown, &unknown) {
		t.Fatal("partial sidecar receipt changed after actual map read/recovery")
	}
	consulAgentServiceScreenshot(t, s, d, "partial-sidecar-after-read", "HTTP 原正文", "复制完整操作记录")
}
