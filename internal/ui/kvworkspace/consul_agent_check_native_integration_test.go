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

func consulAgentCheckScreenshot(t *testing.T, s *State, d *Document, stage string, labels ...string) {
	t.Helper()
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			assertHTTPSessionRenewalControls(t, tt, size, labels...)
			if dir := os.Getenv("NYADB_CONSUL_AGENT_CHECK_IMAGES"); dir != "" {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				file, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%dx%d-workspace-agent-check-%s.png", language, size[0], size[1], stage)))
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

func consulAgentCheckDefinition(t *testing.T, id, notes, status, serviceID string) string {
	t.Helper()
	payload := map[string]any{"Name": "owned-" + strings.ReplaceAll(t.Name(), "/", "-"), "ID": id, "TTL": "10m", "Status": status, "Notes": notes}
	if serviceID != "" {
		payload["ServiceID"] = serviceID
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return " \n" + string(raw) + "\n "
}

func TestConsulNativePublicAgentCheckFormsOriginalDefinitionsDefaultsReceiptsAndFiles(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	s, d, _ := consulWorkspaceState(t, p, secret)
	serviceID := "owned-service-" + t.Name()
	service, err := consul.AgentServiceRegisterRequest([]byte(fmt.Sprintf(`{"Name":%q,"ID":%q}`, serviceID, serviceID)), nil)
	if err != nil {
		t.Fatal(err)
	}
	consulRenewalDo(t, writer, service)
	id := "owned-" + t.Name() + "-Unicode 雪%?#++"
	name := "owned-default-" + t.Name()
	steps := []struct{ stage, id, raw, notes, status, serviceID string }{
		{"original", id, consulAgentCheckDefinition(t, id, "original 中文 18446744073709551615", "critical", ""), "original 中文 18446744073709551615", "critical", ""},
		{"updated", id, consulAgentCheckDefinition(t, id, "updated 原值", "warning", ""), "updated 原值", "critical", ""},
		{"omitted", name, fmt.Sprintf(` {"Name":%q,"TTL":"10m","Notes":"omitted original ID"} `, name), "omitted original ID", "critical", ""},
		{"empty", name, fmt.Sprintf(` {"Name":%q,"ID":"","TTL":"10m","Notes":"empty original ID"} `, name), "empty original ID", "critical", ""},
		{"null", name, fmt.Sprintf(` {"Name":%q,"ID":null,"TTL":"10m","Notes":"null original ID"} `, name), "null original ID", "critical", ""},
		{"service", id + "-service", consulAgentCheckDefinition(t, id+"-service", "original service binding", "critical", serviceID), "original service binding", "critical", serviceID},
	}
	var exported []byte
	s.SetFiles(Files{SavePath: func(string) (string, error) { return "owned-original-check.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var out bytes.Buffer
		err := write(&out)
		exported = bytes.Clone(out.Bytes())
		return err
	}})
	for index, step := range steps {
		kind := contract.KVToolHTTPAgentCheckRegister
		input := contract.KVRequestToolInput{HTTPAgentCheck: contract.KVHTTPAgentCheckToolInput{DefinitionJSON: step.raw}}
		s.OpenRequestTool(d, kind)
		tt := workspaceTester(s, d, 720, 520)
		if index == 0 {
			workspaceClick(t, tt, "编辑完整检查定义 JSON")
			tt.Key(ui.Ctrl, ui.KeyA)
			tt.Type(step.raw)
			tt.Frame()
			actual, _ := d.RequestToolInput(kind)
			if actual != input {
				t.Fatal("actual check form typing changed original bytes")
			}
			consulAgentCheckScreenshot(t, s, d, "register-form", "编辑完整检查定义 JSON", "生成请求文本", "导出工具输入")
		} else {
			s.SetRequestToolInput(d, kind, input)
		}
		workspaceClick(t, tt, "复制工具输入")
		consume(t, s, d)
		encodedInput, err := encodeRequestToolInput(kind, input)
		if err != nil || tt.Clipboard() != string(encodedInput) {
			t.Fatal("check input clipboard changed complete original definition", err)
		}
		s.ExportRequestToolInput(d)
		consume(t, s, d)
		if !bytes.Equal(exported, encodedInput) {
			t.Fatal("check input export lost authored bytes")
		}
		previous := d.last
		workspaceClick(t, tt, "生成请求文本")
		original := d.Request()
		if d.Busy() || d.confirmation != nil || d.last != previous {
			t.Fatal("check form generation dispatched original request")
		}
		encoded, err := EncodeRequest(original)
		decoded, decodeErr := DecodeRequest(encoded)
		if err != nil || decodeErr != nil || decoded != original {
			t.Fatal("check request file changed original definition")
		}
		s.Run(d)
		if d.confirmation == nil || d.confirmation.authored != original || d.Busy() {
			t.Fatal("check registration bypassed immutable confirmation", d.Error())
		}
		consulAgentCheckScreenshot(t, s, d, "register-"+step.stage+"-confirmation", "复制完整请求", "我确认执行此原生写入请求", "确认执行 KV 写入")
		tt.Frame()
		workspaceClick(t, tt, "复制完整请求")
		prepared := d.confirmation.plan.Snapshot()
		var envelope struct {
			Body string `json:"body_base64"`
		}
		err = json.Unmarshal([]byte(prepared.JSON), &envelope)
		body, bodyErr := base64.StdEncoding.Strict().DecodeString(envelope.Body)
		if err != nil || bodyErr != nil || tt.Clipboard() != prepared.Method+"\n"+prepared.JSON || !bytes.Equal(body, []byte(step.raw)) {
			t.Fatal("confirmation copy changed original check definition")
		}
		workspaceClick(t, tt, "确认执行 KV 写入")
		if d.Busy() {
			t.Fatal("unchecked check registration executed")
		}
		workspaceClick(t, tt, "我确认执行此原生写入请求")
		workspaceClick(t, tt, "确认执行 KV 写入")
		s.Confirm(d, true)
		consume(t, s, d)
		if d.last == nil || d.last.Authored != original || !d.last.Result.Completed || d.unknown != nil || d.last.Result.HTTP == nil {
			t.Fatal("original complete check receipt missing", d.Error())
		}
		h := d.last.Result.HTTP
		if !h.Captured || !h.EOF || !h.Succeeded || !h.Acknowledged || len(h.Body) != 0 || !bytes.Equal(h.Request.Body, []byte(step.raw)) || len(h.Request.Query) != 0 {
			t.Fatal("check empty ACK, original body or local scope changed")
		}
		var check struct{ CheckID, Notes, Status, ServiceID string }
		if json.Unmarshal(consulAgentServiceMap(t, writer, true)[step.id], &check) != nil || check.CheckID != step.id || check.Notes != step.notes || check.Status != step.status || check.ServiceID != step.serviceID {
			t.Fatal("native check default, TTL state, notes or service binding changed", step.stage, check)
		}
		consulAgentCheckScreenshot(t, s, d, "register-"+step.stage+"-receipt", "HTTP 原正文", "HTTP 头与元数据", "复制完整操作记录")
		want, err := s.OperationRecord(d)
		if err != nil {
			t.Fatal(err)
		}
		workspaceClick(t, tt, "复制完整操作记录")
		consume(t, s, d)
		if tt.Clipboard() != string(want) {
			t.Fatal("check receipt clipboard changed original complete evidence")
		}
		s.ExportOperationRecord(d)
		consume(t, s, d)
		if !bytes.Equal(exported, want) {
			t.Fatal("check receipt export changed original complete evidence")
		}
	}
	kind := contract.KVToolHTTPAgentCheckDeregister
	s.OpenRequestTool(d, kind)
	tt := workspaceTester(s, d, 720, 520)
	workspaceClick(t, tt, "原生检查 ID")
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Type(id)
	tt.Frame()
	input, _ := d.RequestToolInput(kind)
	if input.HTTPAgentCheck.ID != id {
		t.Fatal("check removal typing changed original ID")
	}
	consulAgentCheckScreenshot(t, s, d, "deregister-form", "原生检查 ID", "生成请求文本", "导出工具输入")
	workspaceClick(t, tt, "生成请求文本")
	s.Run(d)
	consulAgentCheckScreenshot(t, s, d, "deregister-confirmation", "复制完整请求", "确认执行 KV 写入")
	s.Confirm(d, true)
	s.Confirm(d, true)
	consume(t, s, d)
	if d.last == nil || !d.last.Result.Completed || !d.last.Result.HTTP.Succeeded || len(d.last.Result.HTTP.Body) != 0 || d.unknown != nil || consulAgentServiceMap(t, writer, true)[id] != nil {
		t.Fatal("native check removal lost complete empty ACK or retained original ID", d.Error())
	}
	consulAgentCheckScreenshot(t, s, d, "deregister-receipt", "HTTP 原正文", "复制完整操作记录")
	s.Run(d)
	s.Confirm(d, true)
	consume(t, s, d)
	if !d.last.Result.Completed || d.last.Result.HTTP.Status != 404 || !d.last.Result.HTTP.Acknowledged || d.last.Result.HTTP.Succeeded || d.unknown != nil {
		t.Fatal("original missing check ID guessed unknown write")
	}
	consulAgentCheckScreenshot(t, s, d, "missing-id-receipt", "HTTP 原正文", "复制完整操作记录")
	s.OpenRequestTool(d, kind)
	s.SetRequestToolInput(d, kind, contract.KVRequestToolInput{HTTPAgentCheck: contract.KVHTTPAgentCheckToolInput{ID: id + "-service"}})
	s.GenerateRequestTool(d)
	s.Run(d)
	s.Confirm(d, true)
	consume(t, s, d)
	if !d.last.Result.HTTP.Succeeded || consulAgentServiceMap(t, writer, true)[id+"-service"] != nil || consulAgentServiceMap(t, writer, false)[serviceID] == nil {
		t.Fatal("native check removal changed original associated service")
	}
	consulAgentCheckScreenshot(t, s, d, "service-preserved-receipt", "HTTP 原正文", "复制完整操作记录")
}
func TestConsulNativePublicAgentCheckProcessedWritesParkingACKRecoveryAndNoReplay(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentCheckRegister, contract.KVToolHTTPAgentCheckDeregister} {
		for _, mode := range []string{"lost", "partial", "http400", "http500", "http408", "invalid", "cancel", "disconnect", "replace", "parked", "close"} {
			t.Run(string(kind)+"/"+mode, func(t *testing.T) {
				id := "owned-" + strings.ReplaceAll(t.Name(), "/", "-")
				input := contract.KVRequestToolInput{HTTPAgentCheck: contract.KVHTTPAgentCheckToolInput{DefinitionJSON: consulAgentCheckDefinition(t, id, "original 中文", "critical", "")}}
				if kind == contract.KVToolHTTPAgentCheckDeregister {
					r, err := consul.AgentCheckRegisterRequest([]byte(input.HTTPAgentCheck.DefinitionJSON))
					if err != nil {
						t.Fatal(err)
					}
					consulRenewalDo(t, writer, r)
					input.HTTPAgentCheck = contract.KVHTTPAgentCheckToolInput{ID: id}
				}
				proxy, calls, writes, processed := consulOriginalToolFault(t, p, mode, func(r *http.Request) bool {
					return r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/v1/agent/check/")
				})
				s, d, manager := consulWorkspaceState(t, proxy, secret)
				s.deps.HTTPSessionRenewalProviderFor = manager.KVHTTPSessionRenewal
				d.httpSessionRenewalAvailable = true
				before := calls.Load()
				consulWorkspaceToolInput(t, s, d, kind, input)
				original := d.Request()
				s.Run(d)
				if d.confirmation == nil || d.confirmation.authored != original || calls.Load() != before || writes.Load() != 0 {
					t.Fatal("check form generation/preparation dispatched")
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
				actual := consulAgentServiceMap(t, writer, true)
				if (actual[id] != nil) != (kind == contract.KVToolHTTPAgentCheckRegister) || writes.Load() != 1 {
					t.Fatal("fault did not follow exactly one actual original mutation")
				}
				if mode == "close" {
					if lease.CheckKVLease() == nil || d.Editor != nil || d.unknown != nil || d.requestTools != nil {
						t.Fatal("original close retained check request resources")
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
				consulAgentCheckScreenshot(t, s, d, string(kind)+"-unknown-"+mode, "复制完整操作记录", "HTTP 原正文")
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
				consulAgentCheckScreenshot(t, s, d, string(kind)+"-recovered-"+mode, "运行 KV 请求", "复制完整操作记录")
				s.Run(d)
				if d.confirmation != nil || d.Busy() || calls.Load() != before {
					t.Fatal("unknown check mutation authorized replay")
				}
				consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPAgentChecks, contract.KVRequestToolInput{})
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

func TestConsulNativePublicAgentCheckOriginalRejectedDefinitionsAndACLDenial(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	for _, test := range []struct {
		name   string
		status int
		fields string
	}{
		{"unknown-field", 400, `,"TTL":"10m","UnknownNative":18446744073709551615`},
		{"disabled-script", 500, `,"Args":["/bin/true"],"Interval":"10s"`},
		{"missing-service", 404, `,"TTL":"10m","ServiceID":"owned-no-such-service"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, d, _ := consulWorkspaceState(t, p, secret)
			id := "owned-" + strings.ReplaceAll(t.Name(), "/", "-")
			raw := fmt.Sprintf(" {\n\"Name\":%q,\"ID\":%q%s\n} ", id, id, test.fields)
			consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPAgentCheckRegister, contract.KVRequestToolInput{HTTPAgentCheck: contract.KVHTTPAgentCheckToolInput{DefinitionJSON: raw}})
			original := d.Request()
			s.Run(d)
			s.Confirm(d, true)
			consume(t, s, d)
			if d.unknown == nil || d.unknown.Authored != original || d.unknown.Result.HTTP == nil || d.unknown.Result.HTTP.Status != test.status || !d.unknown.Result.HTTP.Captured || !d.unknown.Result.HTTP.EOF || d.unknown.Result.HTTP.Acknowledged || !bytes.Equal(d.unknown.Result.HTTP.Request.Body, []byte(raw)) || len(d.unknown.Result.HTTP.Body) == 0 {
				t.Fatal("server-rejected check definition lost original complete unknown", d.Error())
			}
			if consulAgentServiceMap(t, writer, true)[id] != nil {
				t.Fatal("rejected check definition unexpectedly registered original ID")
			}
			retained := d.unknown.Clone()
			consulAgentCheckScreenshot(t, s, d, "rejected-"+test.name+"-unknown", "HTTP 原正文", "复制完整操作记录")
			consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPAgentChecks, contract.KVRequestToolInput{})
			s.Run(d)
			consume(t, s, d)
			s.Recover(d)
			consume(t, s, d)
			if !reflect.DeepEqual(d.unknown, &retained) {
				t.Fatal("later check map/pure recovery replaced rejected original evidence")
			}
			consulAgentCheckScreenshot(t, s, d, "rejected-"+test.name+"-after-read", "HTTP 原正文", "复制完整操作记录")
			consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPAgentCheckRegister, contract.KVRequestToolInput{HTTPAgentCheck: contract.KVHTTPAgentCheckToolInput{DefinitionJSON: raw}})
			s.Run(d)
			if d.confirmation != nil || d.Busy() || !reflect.DeepEqual(d.unknown, &retained) {
				t.Fatal("rejected original check definition authorized implicit replay")
			}
		})
	}
	id := "owned-denied-" + t.Name()
	definition := consulAgentCheckDefinition(t, id, "original denied 中文", "critical", "")
	request, err := consul.AgentCheckRegisterRequest([]byte(definition))
	if err != nil {
		t.Fatal(err)
	}
	consulRenewalDo(t, writer, request)
	limitedSecret := os.Getenv("NYADB_CONSUL_READ_TOKEN")
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentCheckRegister, contract.KVToolHTTPAgentCheckDeregister} {
		if limitedSecret == "" {
			s, d, _ := consulWorkspaceState(t, p, secret)
			consulAgentCheckScreenshot(t, s, d, string(kind)+"-acl-or-boundary", "运行 KV 请求")
			continue
		}
		s, d, _ := consulWorkspaceState(t, p, limitedSecret)
		input := contract.KVRequestToolInput{HTTPAgentCheck: contract.KVHTTPAgentCheckToolInput{DefinitionJSON: definition}}
		if kind == contract.KVToolHTTPAgentCheckDeregister {
			input.HTTPAgentCheck = contract.KVHTTPAgentCheckToolInput{ID: id}
		}
		consulWorkspaceToolInput(t, s, d, kind, input)
		s.Run(d)
		s.Confirm(d, true)
		consume(t, s, d)
		if d.last == nil || d.last.Result.HTTP.Status != 403 || !d.last.Result.Completed || !d.last.Result.HTTP.Acknowledged || d.last.Result.HTTP.Succeeded || d.unknown != nil || len(d.last.Result.HTTP.Body) == 0 {
			t.Fatal("original check ACL refusal lost complete known denial", d.Error())
		}
		consulAgentCheckScreenshot(t, s, d, string(kind)+"-acl-or-boundary", "HTTP 原正文", "复制完整操作记录")
	}
	if consulAgentServiceMap(t, writer, true)[id] == nil {
		t.Fatal("denied check removal changed original local map")
	}
}
