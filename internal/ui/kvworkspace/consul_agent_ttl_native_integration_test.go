package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/url"
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

func consulAgentTTLScreenshot(t *testing.T, s *State, d *Document, stage string, labels ...string) {
	t.Helper()
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			assertHTTPSessionRenewalControls(t, tt, size, labels...)
			if dir := os.Getenv("NYADB_CONSUL_AGENT_TTL_IMAGES"); dir != "" {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				file, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%dx%d-workspace-agent-ttl-%s.png", language, size[0], size[1], stage)))
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

func consulAgentTTLStatus(kind contract.KVRequestToolKind) string {
	switch kind {
	case contract.KVToolHTTPAgentCheckWarn:
		return "warning"
	case contract.KVToolHTTPAgentCheckFail:
		return "critical"
	default:
		return "passing"
	}
}

func consulAgentTTLInput(t *testing.T, kind contract.KVRequestToolKind, id, output string) contract.KVRequestToolInput {
	t.Helper()
	input := contract.KVRequestToolInput{HTTPAgentTTL: contract.KVHTTPAgentTTLToolInput{ID: id}}
	if kind == contract.KVToolHTTPAgentCheckUpdate {
		raw, err := json.Marshal(map[string]any{"Status": consulAgentTTLStatus(kind), "Output": output})
		if err != nil {
			t.Fatal(err)
		}
		input.HTTPAgentTTL.DefinitionJSON = " \n" + string(raw) + "\n "
	} else {
		encoded, err := json.Marshal(output)
		if err != nil {
			t.Fatal(err)
		}
		input.HTTPAgentTTL.IncludeNote, input.HTTPAgentTTL.NoteJSON = true, string(encoded)
	}
	return input
}

func TestConsulNativePublicAgentTTLFormsOriginalStatesNotesReceiptsAndFiles(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	s, d, _ := consulWorkspaceState(t, p, secret)
	id := "owned-" + t.Name() + "-雪%?#++"
	register, err := consul.AgentCheckRegisterRequest([]byte(consulAgentCheckDefinition(t, id, "original definition Notes 中文", "critical", "")))
	if err != nil {
		t.Fatal(err)
	}
	consulRenewalDo(t, writer, register)
	var exported []byte
	s.SetFiles(Files{SavePath: func(string) (string, error) { return "owned-original-ttl.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var out bytes.Buffer
		err := write(&out)
		exported = bytes.Clone(out.Bytes())
		return err
	}})
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentCheckPass, contract.KVToolHTTPAgentCheckWarn, contract.KVToolHTTPAgentCheckFail, contract.KVToolHTTPAgentCheckUpdate} {
		for _, stage := range []string{"original", "omitted", "empty", "null", "long"} {
			if kind != contract.KVToolHTTPAgentCheckUpdate && stage == "null" {
				continue
			}
			output := "original first\nsecond\r\nNUL\x00雪 %?#++"
			if stage == "long" {
				output = strings.Repeat("x", 8192)
			}
			input := consulAgentTTLInput(t, kind, id, output)
			if stage == "omitted" || stage == "empty" || stage == "null" {
				output = ""
				if kind == contract.KVToolHTTPAgentCheckUpdate {
					field := ""
					if stage == "empty" {
						field = `,"Output":""`
					}
					if stage == "null" {
						field = `,"Output":null`
					}
					input.HTTPAgentTTL.DefinitionJSON = " {\n\"Status\":\"passing\"" + field + "\n} "
				} else {
					input.HTTPAgentTTL.IncludeNote, input.HTTPAgentTTL.NoteJSON = stage != "omitted", ""
				}
			}
			s.OpenRequestTool(d, kind)
			tt := workspaceTester(s, d, 720, 520)
			if stage == "original" {
				workspaceClick(t, tt, "原生检查 ID")
				tt.Key(ui.Ctrl, ui.KeyA)
				tt.Type(id)
				tt.Frame()
				if kind == contract.KVToolHTTPAgentCheckUpdate {
					workspaceClick(t, tt, "编辑完整 TTL 状态 JSON")
					tt.Type(input.HTTPAgentTTL.DefinitionJSON)
				} else {
					workspaceClick(t, tt, "显式包含 note（空文本表示清空 Output）")
					workspaceClick(t, tt, "原始 note JSON 字符串")
					tt.Type(input.HTTPAgentTTL.NoteJSON)
				}
				tt.Frame()
				actual, _ := d.RequestToolInput(kind)
				if actual != input {
					t.Fatalf("native TTL form changed original: kind=%s actual=%+v expected=%+v", kind, actual.HTTPAgentTTL, input.HTTPAgentTTL)
				}
				consulAgentTTLScreenshot(t, s, d, string(kind)+"-form", "原生检查 ID", "复制工具输入", "生成请求文本")
			} else {
				s.SetRequestToolInput(d, kind, input)
			}
			workspaceClick(t, tt, "复制工具输入")
			consume(t, s, d)
			encodedInput, err := encodeRequestToolInput(kind, input)
			if err != nil || tt.Clipboard() != string(encodedInput) {
				t.Fatal("TTL clipboard changed original input", err)
			}
			s.ExportRequestToolInput(d)
			consume(t, s, d)
			if !bytes.Equal(exported, encodedInput) {
				t.Fatal("TTL input file changed original values")
			}
			previous := d.last
			workspaceClick(t, tt, "生成请求文本")
			original := d.Request()
			if d.Busy() || d.confirmation != nil || d.last != previous {
				t.Fatal("TTL generation acquired authority")
			}
			encoded, err := EncodeRequest(original)
			decoded, decodeErr := DecodeRequest(encoded)
			if err != nil || decodeErr != nil || decoded != original {
				t.Fatal("TTL request file rewrote original input")
			}
			s.Run(d)
			if d.confirmation == nil || d.confirmation.authored != original || d.Busy() {
				t.Fatal("TTL write bypassed original confirmation", d.Error())
			}
			consulAgentTTLScreenshot(t, s, d, string(kind)+"-"+stage+"-confirmation", "复制完整请求", "我确认执行此原生写入请求", "确认执行 KV 写入")
			tt.Frame()
			workspaceClick(t, tt, "复制完整请求")
			prepared := d.confirmation.plan.Snapshot()
			if tt.Clipboard() != prepared.Method+"\n"+prepared.JSON {
				t.Fatal("TTL confirmation copy changed original request")
			}
			workspaceClick(t, tt, "确认执行 KV 写入")
			if d.Busy() {
				t.Fatal("unchecked TTL write executed")
			}
			workspaceClick(t, tt, "我确认执行此原生写入请求")
			workspaceClick(t, tt, "确认执行 KV 写入")
			s.Confirm(d, true)
			consume(t, s, d)
			if d.last == nil || d.last.Authored != original || !d.last.Result.Completed || d.unknown != nil || d.last.Result.HTTP == nil {
				t.Fatal("original TTL receipt missing", d.Error())
			}
			h := d.last.Result.HTTP
			if !h.Captured || !h.EOF || !h.Acknowledged || !h.Succeeded || len(h.Body) != 0 || !bytes.Equal(h.Request.Body, []byte(input.HTTPAgentTTL.DefinitionJSON)) || url.Values(h.Request.Query).Has("note") != input.HTTPAgentTTL.IncludeNote || url.Values(h.Request.Query).Get("note") != func() string {
				if input.HTTPAgentTTL.IncludeNote {
					return output
				}
				return ""
			}() {
				t.Fatal("TTL receipt changed original bytes or note presence")
			}
			var check struct{ CheckID, Status, Output, Notes string }
			if json.Unmarshal(consulAgentServiceMap(t, writer, true)[id], &check) != nil || check.CheckID != id || check.Status != consulAgentTTLStatus(kind) || check.Notes != "original definition Notes 中文" {
				t.Fatal("TTL native state or original definition notes changed", check)
			}
			if stage == "long" {
				if len(check.Output) >= len(output) {
					t.Fatal("native long Output was not truncated")
				}
			} else if check.Output != output {
				t.Fatal("TTL native Output differs from original", check.Output, output)
			}
			consulAgentTTLScreenshot(t, s, d, string(kind)+"-"+stage+"-receipt", "HTTP 原正文", "复制完整操作记录")
			want, err := s.OperationRecord(d)
			if err != nil {
				t.Fatal(err)
			}
			workspaceClick(t, tt, "复制完整操作记录")
			consume(t, s, d)
			if tt.Clipboard() != string(want) {
				t.Fatal("TTL receipt clipboard lost complete evidence")
			}
			s.ExportOperationRecord(d)
			consume(t, s, d)
			if !bytes.Equal(exported, want) {
				t.Fatal("TTL receipt export lost complete evidence")
			}
		}
	}
}

func TestConsulNativePublicAgentTTLOriginalKnownDenialsAndNonTTLUnknown(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	id := "owned-denial-" + t.Name()
	register, err := consul.AgentCheckRegisterRequest([]byte(consulAgentCheckDefinition(t, id, "original definition notes", "critical", "")))
	if err != nil {
		t.Fatal(err)
	}
	consulRenewalDo(t, writer, register)
	nonTTL := id + "-http"
	raw, err := json.Marshal(map[string]any{"Name": nonTTL, "ID": nonTTL, "HTTP": "http://127.0.0.1:1/owned", "Interval": "1h"})
	if err != nil {
		t.Fatal(err)
	}
	register, err = consul.AgentCheckRegisterRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	consulRenewalDo(t, writer, register)
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentCheckPass, contract.KVToolHTTPAgentCheckWarn, contract.KVToolHTTPAgentCheckFail, contract.KVToolHTTPAgentCheckUpdate} {
		for _, stage := range []string{"missing", "non-ttl", "acl-or-boundary", "unsupported-json"} {
			if stage == "unsupported-json" && kind != contract.KVToolHTTPAgentCheckUpdate {
				continue
			}
			credential, target, status := secret, "owned-no-such-ttl", 404
			if stage == "unsupported-json" {
				target, status = id, 400
			}
			if stage == "non-ttl" {
				target, status = nonTTL, 500
			}
			if stage == "acl-or-boundary" {
				credential, target, status = os.Getenv("NYADB_CONSUL_READ_TOKEN"), id, 403
				if credential == "" {
					s, d, _ := consulWorkspaceState(t, p, secret)
					consulAgentTTLScreenshot(t, s, d, string(kind)+"-"+stage, "运行 KV 请求")
					continue
				}
			}
			s, d, _ := consulWorkspaceState(t, p, credential)
			input := consulAgentTTLInput(t, kind, target, "original denied output")
			if stage == "unsupported-json" {
				input.HTTPAgentTTL.DefinitionJSON = ` {"Status":"passing","UnknownNative":18446744073709551615} `
			}
			consulWorkspaceToolInput(t, s, d, kind, input)
			original := d.Request()
			s.Run(d)
			s.Confirm(d, true)
			consume(t, s, d)
			known := status == 403 || status == 404
			receipt := d.last
			if !known {
				receipt = d.unknown
			}
			if receipt == nil || receipt.Authored != original || receipt.Result.HTTP == nil || receipt.Result.HTTP.Status != status || !receipt.Result.HTTP.Captured || !receipt.Result.HTTP.EOF || len(receipt.Result.HTTP.Body) == 0 || receipt.Result.HTTP.Succeeded || receipt.Result.HTTP.Acknowledged != known || receipt.Result.WriteOutcomeUnknown != !known {
				t.Fatal("TTL denial guessed native write outcome", stage, d.Error())
			}
			if known && (d.unknown != nil || !receipt.Result.Completed) {
				t.Fatal("proved TTL denial became unknown")
			}
			if !known {
				retained := d.unknown.Clone()
				consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPAgentChecks, contract.KVRequestToolInput{})
				s.Run(d)
				consume(t, s, d)
				s.Recover(d)
				consume(t, s, d)
				if !reflect.DeepEqual(d.unknown, &retained) {
					t.Fatal("TTL native error evidence replaced by read/recovery")
				}
				consulWorkspaceToolInput(t, s, d, kind, consulAgentTTLInput(t, kind, target, "later denied output"))
				s.Run(d)
				if d.confirmation != nil || d.Busy() {
					t.Fatal("native non-TTL error authorized replay")
				}
			}
			consulAgentTTLScreenshot(t, s, d, string(kind)+"-"+stage, "HTTP 原正文", "复制完整操作记录")
		}
	}
	var check struct{ Status, Output, Notes string }
	if json.Unmarshal(consulAgentServiceMap(t, writer, true)[id], &check) != nil || check.Status != "critical" || check.Output != "" || check.Notes != "original definition notes" {
		t.Fatal("denied TTL write changed original check")
	}
}

func TestConsulNativePublicAgentTTLProcessedWritesParkingACKRecoveryAndNoReplay(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentCheckPass, contract.KVToolHTTPAgentCheckWarn, contract.KVToolHTTPAgentCheckFail, contract.KVToolHTTPAgentCheckUpdate} {
		for _, mode := range []string{"lost", "partial", "http400", "http500", "http408", "invalid", "cancel", "disconnect", "replace", "parked", "close"} {
			t.Run(string(kind)+"/"+mode, func(t *testing.T) {
				id := "owned-" + strings.ReplaceAll(t.Name(), "/", "-")
				definition := consulAgentCheckDefinition(t, id, "original 中文", "critical", "")
				register, err := consul.AgentCheckRegisterRequest([]byte(definition))
				if err != nil {
					t.Fatal(err)
				}
				consulRenewalDo(t, writer, register)
				output := "original first\nsecond\r\nNUL\x00雪"
				input := consulAgentTTLInput(t, kind, id, output)
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
				var check struct{ Status, Output, Notes string }
				if json.Unmarshal(actual[id], &check) != nil || check.Status != consulAgentTTLStatus(kind) || check.Output != output || check.Notes != "original 中文" || writes.Load() != 1 {
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
				consulAgentTTLScreenshot(t, s, d, string(kind)+"-unknown-"+mode, "复制完整操作记录", "HTTP 原正文")
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
				consulAgentTTLScreenshot(t, s, d, string(kind)+"-recovered-"+mode, "运行 KV 请求", "复制完整操作记录")
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
