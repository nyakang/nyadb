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
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
)

func consulAgentMaintenanceScreenshot(t *testing.T, s *State, d *Document, stage string, labels ...string) {
	t.Helper()
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			assertHTTPSessionRenewalControls(t, tt, size, labels...)
			if dir := os.Getenv("NYADB_CONSUL_AGENT_MAINTENANCE_IMAGES"); dir != "" {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				file, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%dx%d-workspace-agent-maintenance-%s.png", language, size[0], size[1], stage)))
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

func consulAgentMaintenanceInput(kind contract.KVRequestToolKind, id, enable string, reason *string) contract.KVRequestToolInput {
	a := contract.KVHTTPAgentMaintenanceToolInput{Enable: enable}
	if kind == contract.KVToolHTTPAgentServiceMaintenance {
		a.ID = id
	}
	if reason != nil {
		raw, err := json.Marshal(*reason)
		if err != nil {
			panic(err)
		}
		a.IncludeReason, a.ReasonJSON = true, string(raw)
	}
	return contract.KVRequestToolInput{HTTPAgentMaintenance: a}
}

func consulAgentMaintenanceNative(t *testing.T, writer contract.KVRequestLease, kind contract.KVRequestToolKind, id string, enable bool, reason *string) {
	t.Helper()
	r, err := consul.AgentNodeMaintenanceRequest(enable, reason)
	if kind == contract.KVToolHTTPAgentServiceMaintenance {
		r, err = consul.AgentServiceMaintenanceRequest(id, enable, reason)
	}
	if err != nil {
		t.Fatal(err)
	}
	consulRenewalDo(t, writer, r)
}

func consulAgentMaintenanceIdentity(kind contract.KVRequestToolKind, id string) string {
	if kind == contract.KVToolHTTPAgentNodeMaintenance {
		return "_node_maintenance"
	}
	return "_service_maintenance:" + id
}

func consulAgentMaintenanceObserve(t *testing.T, writer contract.KVRequestLease, kind contract.KVRequestToolKind, id string, present bool, reason string) {
	t.Helper()
	identity := consulAgentMaintenanceIdentity(kind, id)
	raw, exists := consulAgentServiceMap(t, writer, true)[identity]
	if exists != present {
		t.Fatal("original local maintenance presence differs", kind, present)
	}
	if !present {
		return
	}
	var check struct{ CheckID, ServiceID, Status, Type, Notes, Output string }
	wantService := ""
	if kind == contract.KVToolHTTPAgentServiceMaintenance {
		wantService = id
	}
	if json.Unmarshal(raw, &check) != nil || check.CheckID != identity || check.ServiceID != wantService || check.Status != "critical" || check.Type != "maintenance" || check.Notes != reason || check.Output != "" {
		t.Fatal("maintenance changed original native state or complete Notes", kind)
	}
}

func consulAgentMaintenanceService(t *testing.T, writer contract.KVRequestLease, id string) {
	t.Helper()
	definition, err := json.Marshal(map[string]any{"Name": id, "ID": id, "Tags": []string{"original", "中文"}, "Meta": map[string]string{"original": "18446744073709551615"}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := consul.AgentServiceRegisterRequest(definition, nil)
	if err != nil {
		t.Fatal(err)
	}
	consulRenewalDo(t, writer, r)
	definition, err = json.Marshal(map[string]any{"Name": id + "-ordinary", "ID": id + "-ordinary", "ServiceID": id, "TTL": "10m", "Status": "passing"})
	if err != nil {
		t.Fatal(err)
	}
	r, err = consul.AgentCheckRegisterRequest(definition)
	if err != nil {
		t.Fatal(err)
	}
	consulRenewalDo(t, writer, r)
}

func TestConsulNativePublicAgentMaintenanceFormsOriginalReasonsLocalACKAndFiles(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	s, d, _ := consulWorkspaceState(t, p, secret)
	id := "owned-" + t.Name() + "-雪%?#++"
	consulAgentMaintenanceService(t, writer, id)
	originalService := bytes.Clone(consulAgentServiceMap(t, writer, false)[id])
	originalCheck := bytes.Clone(consulAgentServiceMap(t, writer, true)[id+"-ordinary"])
	var exported []byte
	s.SetFiles(Files{SavePath: func(string) (string, error) { return "owned-maintenance.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var out bytes.Buffer
		err := write(&out)
		exported = bytes.Clone(out.Bytes())
		return err
	}})
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentNodeMaintenance, contract.KVToolHTTPAgentServiceMaintenance} {
		defer consulAgentMaintenanceNative(t, writer, kind, id, false, nil)
		for _, enable := range []bool{true, false} {
			for _, stage := range []string{"original", "omitted", "empty", "long"} {
				consulAgentMaintenanceNative(t, writer, kind, id, false, nil)
				seed := "original seed reason"
				if !enable {
					consulAgentMaintenanceNative(t, writer, kind, id, true, &seed)
				}
				reason := "  original first\nsecond\r\nNUL\x00雪 %?#++  "
				if stage == "long" {
					reason = strings.Repeat("雪", 4096)
				}
				var optional *string = &reason
				if stage == "omitted" {
					optional = nil
				}
				if stage == "empty" {
					reason = ""
				}
				lexical := strconv.FormatBool(enable)
				if stage == "original" {
					lexical = strings.ToUpper(lexical)
				}
				input := consulAgentMaintenanceInput(kind, id, lexical, optional)
				s.OpenRequestTool(d, kind)
				tt := workspaceTester(s, d, 720, 520)
				if stage == "original" {
					s.SetRequestToolInput(d, kind, contract.KVRequestToolInput{HTTPAgentMaintenance: contract.KVHTTPAgentMaintenanceToolInput{Enable: "true"}})
					tt.Frame()
					if kind == contract.KVToolHTTPAgentServiceMaintenance {
						workspaceClick(t, tt, "输入原生服务 ID")
						tt.Type(id)
						tt.Frame()
					}
					workspaceClick(t, tt, "原生 enable 布尔值")
					tt.Key(ui.Ctrl, ui.KeyA)
					tt.Type(lexical)
					tt.Frame()
					workspaceClick(t, tt, "显式包含 reason（省略或空值使用原生默认原因）")
					workspaceClick(t, tt, "原始 reason JSON 字符串")
					tt.Type(input.HTTPAgentMaintenance.ReasonJSON)
					tt.Frame()
					actual, _ := d.RequestToolInput(kind)
					if actual != input {
						t.Fatal("native maintenance form changed original inputs")
					}
					consulAgentMaintenanceScreenshot(t, s, d, string(kind)+"-"+strconv.FormatBool(enable)+"-form", "原始 reason JSON 字符串", "生成请求文本", "复制工具输入")
				} else {
					s.SetRequestToolInput(d, kind, input)
				}
				workspaceClick(t, tt, "复制工具输入")
				consume(t, s, d)
				encodedInput, err := encodeRequestToolInput(kind, input)
				if err != nil || tt.Clipboard() != string(encodedInput) {
					t.Fatal("maintenance clipboard changed original inputs", err)
				}
				s.ExportRequestToolInput(d)
				consume(t, s, d)
				if !bytes.Equal(exported, encodedInput) {
					t.Fatal("maintenance input export lost original values")
				}
				previous := d.last
				workspaceClick(t, tt, "生成请求文本")
				original := d.Request()
				if d.Busy() || d.confirmation != nil || d.last != previous {
					t.Fatal("maintenance generation acquired execution authority")
				}
				encoded, err := EncodeRequest(original)
				decoded, decodeErr := DecodeRequest(encoded)
				if err != nil || decodeErr != nil || decoded != original {
					t.Fatal("maintenance request file rewrote original values")
				}
				s.Run(d)
				if d.confirmation == nil || d.confirmation.authored != original || d.Busy() {
					t.Fatal("maintenance bypassed immutable original confirmation", d.Error())
				}
				prefix := string(kind) + "-" + strconv.FormatBool(enable) + "-" + stage
				consulAgentMaintenanceScreenshot(t, s, d, prefix+"-confirmation", "复制完整请求", "我确认执行此原生写入请求", "确认执行 KV 写入")
				tt.Frame()
				workspaceClick(t, tt, "复制完整请求")
				prepared := d.confirmation.plan.Snapshot()
				if tt.Clipboard() != prepared.Method+"\n"+prepared.JSON {
					t.Fatal("maintenance confirmation copy lost original request")
				}
				workspaceClick(t, tt, "确认执行 KV 写入")
				if d.Busy() {
					t.Fatal("unchecked maintenance executed")
				}
				workspaceClick(t, tt, "我确认执行此原生写入请求")
				workspaceClick(t, tt, "确认执行 KV 写入")
				s.Confirm(d, true)
				consume(t, s, d)
				if d.last == nil || d.last.Authored != original || !d.last.Result.Completed || d.unknown != nil || d.last.Result.HTTP == nil {
					t.Fatal("maintenance original receipt missing", d.Error())
				}
				h := d.last.Result.HTTP
				if !h.Captured || !h.EOF || !h.Acknowledged || !h.Succeeded || len(h.Body) != 0 || len(h.Request.Body) != 0 || url.Values(h.Request.Query).Get("enable") != lexical || url.Values(h.Request.Query).Has("reason") != input.HTTPAgentMaintenance.IncludeReason {
					t.Fatal("maintenance local ACK changed original request")
				}
				wantReason := ""
				if optional != nil {
					wantReason = *optional
				}
				if url.Values(h.Request.Query).Get("reason") != wantReason {
					t.Fatal("maintenance receipt changed complete reason")
				}
				if enable && wantReason == "" {
					target := "node"
					if kind == contract.KVToolHTTPAgentServiceMaintenance {
						target = "service"
					}
					wantReason = "Maintenance mode is enabled for this " + target + ", but no reason was provided. This is a default message."
				}
				consulAgentMaintenanceObserve(t, writer, kind, id, enable, wantReason)
				if !bytes.Equal(consulAgentServiceMap(t, writer, false)[id], originalService) || !bytes.Equal(consulAgentServiceMap(t, writer, true)[id+"-ordinary"], originalCheck) {
					t.Fatal("maintenance changed original service or ordinary check")
				}
				consulAgentMaintenanceScreenshot(t, s, d, prefix+"-receipt", "HTTP 原正文", "复制完整操作记录")
				want, err := s.OperationRecord(d)
				if err != nil {
					t.Fatal(err)
				}
				workspaceClick(t, tt, "复制完整操作记录")
				consume(t, s, d)
				if tt.Clipboard() != string(want) {
					t.Fatal("maintenance operation clipboard lost original evidence")
				}
				s.ExportOperationRecord(d)
				consume(t, s, d)
				if !bytes.Equal(exported, want) {
					t.Fatal("maintenance operation export lost original evidence")
				}
			}
		}
		originalReason, later := "original repeated enable reason", "later reason must remain separate"
		consulAgentMaintenanceNative(t, writer, kind, id, false, nil)
		consulAgentMaintenanceNative(t, writer, kind, id, true, &originalReason)
		consulWorkspaceToolInput(t, s, d, kind, consulAgentMaintenanceInput(kind, id, "true", &later))
		s.Run(d)
		consulAgentMaintenanceScreenshot(t, s, d, string(kind)+"-repeated-confirmation", "复制完整请求", "确认执行 KV 写入")
		s.Confirm(d, true)
		consume(t, s, d)
		if d.last == nil || !d.last.Result.Completed || url.Values(d.last.Result.HTTP.Request.Query).Get("reason") != later {
			t.Fatal("repeated enable lost original authored request")
		}
		consulAgentMaintenanceObserve(t, writer, kind, id, true, originalReason)
		consulAgentMaintenanceScreenshot(t, s, d, string(kind)+"-repeated-receipt", "HTTP 原正文", "复制完整操作记录")
	}
}

func TestConsulNativePublicAgentMaintenanceKnownDenialsPreserveOriginalState(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	id := "owned-" + t.Name()
	consulAgentMaintenanceService(t, writer, id)
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentNodeMaintenance, contract.KVToolHTTPAgentServiceMaintenance} {
		for _, stage := range []string{"missing", "acl-or-boundary"} {
			if stage == "missing" && kind == contract.KVToolHTTPAgentNodeMaintenance {
				continue
			}
			credential, target, status := secret, "owned-no-such-maintenance-service", 404
			if stage == "acl-or-boundary" {
				credential, target, status = os.Getenv("NYADB_CONSUL_READ_TOKEN"), id, 403
				if credential == "" {
					s, d, _ := consulWorkspaceState(t, p, secret)
					consulAgentMaintenanceScreenshot(t, s, d, string(kind)+"-"+stage, "运行 KV 请求")
					continue
				}
			}
			s, d, _ := consulWorkspaceState(t, p, credential)
			reason := "original denied reason 中文\n\r\x00"
			consulWorkspaceToolInput(t, s, d, kind, consulAgentMaintenanceInput(kind, target, "true", &reason))
			original := d.Request()
			s.Run(d)
			s.Confirm(d, true)
			consume(t, s, d)
			if d.last == nil || d.last.Authored != original || d.unknown != nil || !d.last.Result.Completed || d.last.Result.HTTP == nil || d.last.Result.HTTP.Status != status || !d.last.Result.HTTP.Acknowledged || d.last.Result.HTTP.Succeeded || len(d.last.Result.HTTP.Body) == 0 {
				t.Fatal("proved maintenance denial guessed an unknown outcome", stage, d.Error())
			}
			consulAgentMaintenanceObserve(t, writer, kind, id, false, "")
			consulAgentMaintenanceScreenshot(t, s, d, string(kind)+"-"+stage, "HTTP 原正文", "复制完整操作记录")
		}
	}
}

func TestConsulNativePublicAgentMaintenanceProcessedWritesParkingRecoveryAndNoReplay(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentNodeMaintenance, contract.KVToolHTTPAgentServiceMaintenance} {
		for _, enable := range []bool{true, false} {
			for _, mode := range []string{"lost", "partial", "http400", "http500", "http408", "invalid", "cancel", "disconnect", "replace", "parked", "close"} {
				t.Run(string(kind)+"/"+strconv.FormatBool(enable)+"/"+mode, func(t *testing.T) {
					id := "owned-" + strings.ReplaceAll(t.Name(), "/", "-")
					consulAgentMaintenanceService(t, writer, id)
					consulAgentMaintenanceNative(t, writer, kind, id, false, nil)
					defer consulAgentMaintenanceNative(t, writer, kind, id, false, nil)
					seed := "original seed"
					if !enable {
						consulAgentMaintenanceNative(t, writer, kind, id, true, &seed)
					}
					reason := "original first\nsecond\r\nNUL\x00雪"
					input := consulAgentMaintenanceInput(kind, id, strconv.FormatBool(enable), &reason)
					proxy, calls, writes, processed := consulOriginalToolFault(t, p, mode, func(r *http.Request) bool {
						return r.Method == "PUT" && (r.URL.Path == "/v1/agent/maintenance" || strings.HasPrefix(r.URL.Path, "/v1/agent/service/maintenance/"))
					})
					s, d, manager := consulWorkspaceState(t, proxy, secret)
					before := calls.Load()
					consulWorkspaceToolInput(t, s, d, kind, input)
					original := d.Request()
					s.Run(d)
					if d.confirmation == nil || d.confirmation.authored != original || calls.Load() != before || writes.Load() != 0 {
						t.Fatal("maintenance preparation dispatched")
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
							t.Fatal("maintenance cancellation settled before executor ACK")
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
					consulAgentMaintenanceObserve(t, writer, kind, id, enable, reason)
					if writes.Load() != 1 {
						t.Fatal("maintenance fault did not follow exactly one original native mutation")
					}
					if mode == "close" {
						if lease.CheckKVLease() == nil || d.Editor != nil || d.unknown != nil || d.requestTools != nil {
							t.Fatal("maintenance close retained original resources")
						}
						return
					}
					s.Consume()
					if d.job != job || d.unknown != nil || !d.Busy() {
						t.Fatal("parked maintenance completion consumed")
					}
					active = true
					s.Consume()
					if d.unknown == nil || d.unknown.Authored != original || !d.unknown.Result.WriteOutcomeUnknown || d.unknown.Result.HTTP.Acknowledged || !d.unknown.Result.HTTP.Ended || writes.Load() != 1 {
						t.Fatal("processed maintenance mutation promoted or replayed", d.Error())
					}
					unknown := d.unknown.Clone()
					if url.Values(unknown.Result.HTTP.Request.Query).Get("enable") != input.HTTPAgentMaintenance.Enable || url.Values(unknown.Result.HTTP.Request.Query).Get("reason") != reason {
						t.Fatal("unknown maintenance receipt lost original request values")
					}
					prefix := string(kind) + "-" + strconv.FormatBool(enable)
					consulAgentMaintenanceScreenshot(t, s, d, prefix+"-unknown-"+mode, "HTTP 原正文", "复制完整操作记录")
					if mode == "disconnect" {
						if err := manager.Connect(context.Background(), proxy, secret); err != nil {
							t.Fatal(err)
						}
					}
					before = calls.Load()
					s.Recover(d)
					consume(t, s, d)
					if d.NeedsRecovery() || calls.Load() != before || d.Request() != original || !reflect.DeepEqual(d.unknown, &unknown) {
						t.Fatal("maintenance recovery dispatched or lost original evidence")
					}
					consulAgentMaintenanceScreenshot(t, s, d, prefix+"-recovered-"+mode, "运行 KV 请求", "复制完整操作记录")
					s.Run(d)
					if d.confirmation != nil || d.Busy() || calls.Load() != before {
						t.Fatal("unknown maintenance authorized replay")
					}
					consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPAgentChecks, contract.KVRequestToolInput{})
					s.Run(d)
					consume(t, s, d)
					if agentReceiptProjection(d.last) == nil || !reflect.DeepEqual(d.unknown, &unknown) || writes.Load() != 1 {
						t.Fatal("later Agent read confirmed unknown maintenance")
					}
					for _, blocked := range []struct {
						kind  contract.KVRequestToolKind
						input contract.KVRequestToolInput
					}{{kind, input}, {contract.KVToolPut, contract.KVRequestToolInput{Key: "native/maintenance/later", ValueBase64: "AP8="}}} {
						before = calls.Load()
						consulWorkspaceToolInput(t, s, d, blocked.kind, blocked.input)
						s.Run(d)
						if d.confirmation != nil || d.Busy() || calls.Load() != before || writes.Load() != 1 {
							t.Fatal("unknown maintenance barrier cleared by a later read")
						}
					}
				})
			}
		}
	}
}
