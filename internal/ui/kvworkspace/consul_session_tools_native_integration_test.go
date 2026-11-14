package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"os"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
)

func consulSessionToolScreenshot(t *testing.T, s *State, d *Document, stage string) {
	t.Helper()
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			labels := []string{"运行 KV 请求", "KV JSON 编辑器"}
			if d.requestTools != nil && d.requestTools.open {
				labels = []string{"请求工具操作", "复制工具输入", "导出工具输入", "返回请求文本", "生成请求文本"}
				if d.requestTools.kind == contract.KVToolHTTPSessionCreate {
					labels = append(labels, "包含 Session 名称", "包含 Session 节点", "TTL", "LockDelay", "Behavior", "NodeChecks JSON（留空省略，[] 为无检查）")
				} else {
					labels = append(labels, "Session UUID")
				}
			} else if d.confirmation != nil {
				labels = []string{"我确认执行此原生写入请求", "复制完整请求", "确认执行 KV 写入"}
			} else if d.last != nil {
				labels = append(labels, "复制完整操作记录", "KV 原生回执")
			}
			assertHTTPSessionRenewalControls(t, tt, size, labels...)
			captureSessionTool(t, tt, language, size, stage)
		}
	}
}

func consulSessionToolCreate(t *testing.T, writer contract.KVRequestLease, name string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"Name": name, "TTL": "10m", "LockDelay": "0s", "NodeChecks": []string{}})
	r := consulRenewalDo(t, writer, consul.Request{Method: "PUT", Path: "/v1/session/create", Body: body})
	if r.HTTP == nil || r.HTTP.SessionID == "" {
		t.Fatal("native create UUID absent")
	}
	id := r.HTTP.SessionID
	t.Cleanup(func() { consulRenewalDo(t, writer, consul.Request{Method: "PUT", Path: "/v1/session/destroy/" + id}) })
	return id
}

func TestConsulNativePublicSessionToolsFormsOriginalConfirmationUUIDLocksAndFiles(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	other := consulSessionToolCreate(t, writer, t.Name()+"-other")
	s, d, m := consulWorkspaceState(t, p, secret)
	if len(d.RequestTools()) != 30 {
		t.Fatal("public Session tools missing")
	}
	key := "native/workspace/session-tools/中文 %?#"
	id := ""
	t.Cleanup(func() {
		if id != "" {
			consulRenewalDo(t, writer, consul.Request{Method: "PUT", Path: "/v1/session/destroy/" + id})
		}
	})
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPSessionCreate, contract.KVToolHTTPSessionInfo, contract.KVToolHTTPAcquire, contract.KVToolHTTPRelease, contract.KVToolHTTPSessionDestroy} {
		t.Run(string(kind), func(t *testing.T) {
			input := contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{ID: id}}
			switch kind {
			case contract.KVToolHTTPSessionCreate:
				input.HTTPSession = contract.KVHTTPSessionToolInput{IncludeName: true, Name: "", IncludeNode: true, Node: d.scope.HTTP.AgentName, TTL: "10m", LockDelay: "0s", Behavior: "release", NodeChecksJSON: "[]"}
			case contract.KVToolHTTPAcquire:
				input.Key, input.Flags, input.ValueBase64 = key, "18446744073709551615", "AP+A"
			case contract.KVToolHTTPRelease:
				input.Key, input.Flags = key, "0"
			}
			original := d.Request()
			s.OpenRequestTool(d, kind)
			s.SetRequestToolInput(d, kind, input)
			consulSessionToolScreenshot(t, s, d, string(kind)+"-form")
			tt := workspaceTester(s, d, 720, 520)
			tt.Frame()
			workspaceClick(t, tt, "复制工具输入")
			consume(t, s, d)
			raw, err := encodeRequestToolInput(kind, input)
			if err != nil || tt.Clipboard() != string(raw) || d.Request() != original {
				t.Fatal("native tool input copy changed original draft", err)
			}
			var exported []byte
			s.SetFiles(Files{SavePath: func(string) (string, error) { return "original.nyakv-tool.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
				var out bytes.Buffer
				err := write(&out)
				exported = out.Bytes()
				return err
			}})
			s.ExportRequestToolInput(d)
			consume(t, s, d)
			if !bytes.Equal(exported, raw) || d.Request() != original {
				t.Fatal("native tool export changed original input")
			}
			workspaceClick(t, tt, "生成请求文本")
			expected, err := d.requestBuilder.BuildKVRequest(kind, input)
			if err != nil || d.Request() != expected || d.requestTools.open || d.Busy() || d.confirmation != nil {
				t.Fatal("form did not generate exact pure request", d.error)
			}
			saved, err := EncodeRequest(expected)
			decoded, decodeErr := DecodeRequest(saved)
			if err != nil || decodeErr != nil || decoded != expected {
				t.Fatal("generated request file changed exact text")
			}
			consulSessionToolScreenshot(t, s, d, string(kind)+"-generated")
			s.Run(d)
			if kind != contract.KVToolHTTPSessionInfo {
				if d.confirmation == nil || d.Busy() || d.confirmation.authored != expected {
					t.Fatal("new tool bypassed immutable original confirmation")
				}
				consulSessionToolScreenshot(t, s, d, string(kind)+"-confirmation")
				tt.Frame()
				workspaceClick(t, tt, "确认执行 KV 写入")
				if d.Busy() {
					t.Fatal("unchecked native request dispatched")
				}
				workspaceClick(t, tt, "我确认执行此原生写入请求")
				workspaceClick(t, tt, "确认执行 KV 写入")
				s.Confirm(d, true)
			}
			consume(t, s, d)
			if d.last == nil || d.last.Authored != expected || d.last.Result.HTTP == nil || !d.last.Result.HTTP.Succeeded || !d.last.Result.HTTP.Acknowledged || d.unknown != nil {
				t.Fatal("original native receipt missing", d.error)
			}
			h := d.last.Result.HTTP
			switch kind {
			case contract.KVToolHTTPSessionCreate:
				id = h.SessionID
				var body struct{ ID string }
				if json.Unmarshal(h.Body, &body) != nil || id == "" || id != body.ID {
					t.Fatal("UUID was guessed outside complete native create reply")
				}
				var fields map[string]json.RawMessage
				if json.Unmarshal(h.Request.Body, &fields) != nil || string(fields["Name"]) != `""` || string(fields["LockDelay"]) != `"0s"` || string(fields["NodeChecks"]) != `[]` {
					t.Fatal("optional empty/zero fields changed")
				}
			case contract.KVToolHTTPSessionInfo:
				var info []struct {
					ID, Name, Node string
					LockDelay      uint64
					NodeChecks     []string
				}
				if json.Unmarshal(h.Body, &info) != nil || len(info) != 1 || info[0].ID != id || info[0].Name != "" || info[0].Node != d.scope.HTTP.AgentName || info[0].LockDelay != 0 || len(info[0].NodeChecks) != 0 {
					t.Fatal("original Session info changed")
				}
			case contract.KVToolHTTPAcquire, contract.KVToolHTTPRelease:
				if string(bytes.TrimSpace(h.Body)) != "true" || h.Txn != nil {
					t.Fatal("lock replaced native boolean reply")
				}
				read, _ := consul.KeyReadRequest(key)
				result := consulRenewalDo(t, writer, read)
				if result.HTTP == nil || len(result.HTTP.Entries) != 1 {
					t.Fatal("original locked key missing")
				}
				e := result.HTTP.Entries[0]
				if kind == contract.KVToolHTTPAcquire {
					if e.Session != id || e.Flags != math.MaxUint64 || !bytes.Equal(e.Value, []byte{0, 255, 128}) {
						t.Fatal("lock lost original UUID/flags/bytes")
					}
				} else if e.Session != "" || e.Flags != 0 || len(e.Value) != 0 {
					t.Fatal("release lost original empty bytes/zero flags")
				}
			}
			consulSessionToolScreenshot(t, s, d, string(kind)+"-receipt")
			// Failed acquisition and release by another Session keep the native false
			// reply and cannot change this Session's key.
			if kind == contract.KVToolHTTPAcquire {
				input.HTTPSession.ID = other
				for _, failedKind := range []contract.KVRequestToolKind{contract.KVToolHTTPAcquire, contract.KVToolHTTPRelease} {
					consulWorkspaceToolInput(t, s, d, failedKind, input)
					s.Run(d)
					s.Confirm(d, true)
					consume(t, s, d)
					h := d.last.Result.HTTP
					if h == nil || string(bytes.TrimSpace(h.Body)) != "false" || !h.Acknowledged || h.Succeeded || d.unknown != nil {
						t.Fatal("native false promoted or made unknown")
					}
					consulSessionToolScreenshot(t, s, d, string(failedKind)+"-false")
				}
			}
		})
	}
	if id == "" {
		t.Fatal("create stage failed")
	}
	consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPSessionInfo, contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{ID: id}})
	s.Run(d)
	job := d.job
	previous := d.last
	active := false
	s.deps.Active = func(*Document) bool { return active }
	await(t, job.finished)
	s.Consume()
	if d.job != job || d.last != previous {
		t.Fatal("parked Session read admitted")
	}
	active = true
	consume(t, s, d)
	if !bytes.Equal(bytes.TrimSpace(d.last.Result.HTTP.Body), []byte("[]")) {
		t.Fatal("destroyed Session remained")
	}
	original, receipt := d.Request(), d.last.Clone()
	m.Disconnect(p.ID)
	s.DisconnectProfile(p.ID)
	if err := m.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	s.Recover(d)
	consume(t, s, d)
	if d.NeedsRecovery() || d.Request() != original || !bytes.Equal(d.last.Result.HTTP.Body, receipt.Result.HTTP.Body) || d.confirmation != nil {
		t.Fatal("Session recovery changed evidence or executed")
	}
	consulSessionToolScreenshot(t, s, d, "recovered")
	s.Close(d)
	if d.requestTools != nil || d.requestBuilder != nil || d.Editor != nil || d.lease != nil {
		t.Fatal("Session tool close retained original inputs")
	}
}

func TestConsulNativePublicSessionToolsOriginalACLFilteredReadsAndDeniedWrites(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	if p.ConsulOptions.Auth == "none" {
		return
	}
	writer := consulRenewalWriter(t, p, secret)
	id := consulSessionToolCreate(t, writer, t.Name())
	for _, variable := range []string{"NYADB_CONSUL_READ_TOKEN", "NYADB_CONSUL_SESSION_READ_TOKEN"} {
		token := os.Getenv(variable)
		if token == "" {
			t.Fatal("original restricted token missing")
		}
		s, d, _ := consulWorkspaceState(t, p, token)
		consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPSessionInfo, contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{ID: id}})
		consulWorkspaceRunTool(t, s, d, false)
		var info []struct{ ID string }
		h := d.last.Result.HTTP
		if json.Unmarshal(h.Body, &info) != nil || h.Status != 200 || variable == "NYADB_CONSUL_READ_TOKEN" && len(info) != 0 || variable == "NYADB_CONSUL_SESSION_READ_TOKEN" && (len(info) != 1 || info[0].ID != id) {
			t.Fatal("original ACL-filtered read changed")
		}
		for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPSessionCreate, contract.KVToolHTTPSessionDestroy, contract.KVToolHTTPAcquire, contract.KVToolHTTPRelease} {
			input := contract.KVRequestToolInput{}
			if kind != contract.KVToolHTTPSessionCreate {
				input.HTTPSession.ID = id
			}
			if kind == contract.KVToolHTTPAcquire || kind == contract.KVToolHTTPRelease {
				input.Key = "allowed/session-tool-denied"
			}
			consulWorkspaceToolInput(t, s, d, kind, input)
			s.Run(d)
			s.Confirm(d, true)
			consume(t, s, d)
			h := d.last.Result.HTTP
			if h == nil || h.Status != 403 || !h.Acknowledged || h.Succeeded || d.unknown != nil {
				t.Fatal("new form bypassed native ACL denial")
			}
		}
	}
}
