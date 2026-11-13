package kvworkspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
)

func TestConsulNativeSessionBrowserReadAfterUnknownDestroyKeepsOriginalWriteReceipt(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	id := consulSessionToolCreate(t, writer, t.Name()+"-destroy")
	survivor := consulSessionToolCreate(t, writer, t.Name()+"-survivor")
	t.Cleanup(func() {
		consulRenewalDo(t, writer, consul.Request{Method: "PUT", Path: "/v1/session/destroy/" + survivor})
	})
	proxy, calls, processed := consulSessionToolFault(t, p, "lost")
	s, d, _ := consulWorkspaceState(t, proxy, secret)
	input := contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{ID: id}}
	consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPSessionDestroy, input)
	s.Run(d)
	s.Confirm(d, true)
	await(t, processed)
	consume(t, s, d)
	if d.unknown == nil || !d.unknown.Result.WriteOutcomeUnknown || d.unknown.Result.HTTP.Acknowledged || calls.Load() != 1 {
		t.Fatal("processed destroy lost its independent unknown write receipt")
	}
	original, err := encodeHTTPOperationReceipt(*d.unknown)
	if err != nil {
		t.Fatal(err)
	}
	s.Recover(d)
	consume(t, s, d)
	if d.NeedsRecovery() || calls.Load() != 1 {
		t.Fatal("evidence-only recovery replayed original destroy")
	}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPSessionList, contract.KVToolHTTPNodeSessions} {
		read := contract.KVRequestToolInput{}
		if kind == contract.KVToolHTTPNodeSessions {
			read.HTTPSession.Node = d.scope.HTTP.AgentName
		}
		consulWorkspaceToolInput(t, s, d, kind, read)
		consulWorkspaceRunTool(t, s, d, false)
		tt := workspaceTester(s, d, 720, 520)
		workspaceClick(t, tt, "Session 列表")
		workspaceClick(t, tt, "筛选已返回的 Session")
		tt.Type(survivor)
		tt.Frame()
		if len(d.httpSessionBrowser.indices) != 1 {
			t.Fatal("explicit read lost the original surviving Session")
		}
		workspaceClick(t, tt, survivor)
		workspaceClick(t, tt, "查看所选原始详情")
		nativeSessionBrowserCapture(t, s, d, "native-unknown-write-"+string(kind)+"-detail")
		workspaceClick(t, tt, "返回原 HTTP 回执")
		got, err := encodeHTTPOperationReceipt(*d.unknown)
		if err != nil || !bytes.Equal(original, got) || !d.hasUnknownWrites() || calls.Load() != 1 {
			t.Fatal("local Session browsing changed or acknowledged the original unknown write")
		}
	}
	consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPSessionDestroy, input)
	s.Run(d)
	if d.Busy() || d.confirmation != nil || calls.Load() != 1 {
		t.Fatal("later complete Session reads authorized replay of unknown destroy")
	}
	observed := consulRenewalDo(t, writer, consul.Request{Method: "GET", Path: "/v1/session/info/" + id})
	if !bytes.Equal(bytes.TrimSpace(observed.HTTP.Body), []byte("[]")) {
		t.Fatal("original destroy did not occur on the real server")
	}
}

func nativeSessionBrowserCapture(t *testing.T, s *State, d *Document, stage string) {
	t.Helper()
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			labels := []string{"Session 列表", "复制完整操作记录"}
			if d.requestTools != nil && d.requestTools.open {
				labels = []string{"请求工具操作", "生成请求文本", "复制工具输入"}
			} else if d.httpSessions {
				labels = []string{"返回原 HTTP 回执"}
				if d.httpSessionBrowser != nil && d.httpSessionBrowser.detail != "" {
					labels = append(labels, "返回 Session 列表", "Session 原始详情")
				} else {
					labels = append(labels, "筛选已返回的 Session", "Session TTL 筛选", "查看所选原始详情")
				}
			}
			assertHTTPSessionRenewalControls(t, tt, size, labels...)
			captureSessionBrowser(t, tt, language, size, stage)
		}
	}
}

func TestConsulNativeSessionBrowserCapturedListsNodesDetailsAndOriginalCopies(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	for i := 0; i < 53; i++ {
		consulSessionToolCreate(t, writer, fmt.Sprintf("%s-%02d", t.Name(), i))
	}
	s, d, _ := consulWorkspaceState(t, p, secret)
	if len(d.RequestTools()) != 24 {
		t.Fatal("public Session list and node forms unavailable")
	}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPSessionList, contract.KVToolHTTPNodeSessions} {
		input := contract.KVRequestToolInput{}
		if kind == contract.KVToolHTTPNodeSessions {
			input.HTTPSession.Node = d.scope.HTTP.AgentName
		}
		s.OpenRequestTool(d, kind)
		s.SetRequestToolInput(d, kind, input)
		nativeSessionBrowserCapture(t, s, d, "native-"+string(kind)+"-form")
		tt := workspaceTester(s, d, 720, 520)
		workspaceClick(t, tt, "生成请求文本")
		request := d.Request()
		if d.Busy() || d.confirmation != nil {
			t.Fatal("native list form generated an operation")
		}
		consulWorkspaceRunTool(t, s, d, false)
		original := d.last
		body := bytes.Clone(original.Result.HTTP.Body)
		if original.Result.HTTP.Sessions == nil || len(original.Result.HTTP.Sessions.Rows) < 53 {
			t.Fatal("complete original native Session list missing", d.error)
		}
		workspaceClick(t, tt, "Session 列表")
		workspaceClick(t, tt, "筛选已返回的 Session")
		tt.Type(t.Name())
		tt.Frame()
		if len(d.httpSessionBrowser.indices) != 53 {
			t.Fatal("local filter lost original Session rows or changed server total")
		}
		nativeSessionBrowserCapture(t, s, d, "native-"+string(kind)+"-list")
		tt.Frame()
		workspaceClick(t, tt, "下一页")
		id := original.Result.HTTP.Sessions.Rows[d.httpSessionBrowser.indices[52]].ID
		workspaceClick(t, tt, id)
		workspaceClick(t, tt, "复制所选 Session UUID")
		if tt.Clipboard() != id || d.httpSessionBrowser.receipt != original {
			t.Fatal("native paging or copy changed original owner or UUID")
		}
		workspaceClick(t, tt, "查看所选原始详情")
		var detail map[string]json.RawMessage
		var selectedID string
		if json.Unmarshal([]byte(d.httpSessionBrowser.preview), &detail) != nil || json.Unmarshal(detail["ID"], &selectedID) != nil || selectedID != id {
			t.Fatal("native original detail differs from selected UUID")
		}
		nativeSessionBrowserCapture(t, s, d, "native-"+string(kind)+"-detail")
		tt.Frame()
		workspaceClick(t, tt, "返回原 HTTP 回执")
		workspaceClick(t, tt, "复制完整操作记录")
		consume(t, s, d)
		want, err := s.OperationRecord(d)
		if err != nil || tt.Clipboard() != string(want) || d.Request() != request || d.last != original || !bytes.Equal(d.last.Result.HTTP.Body, body) || d.Busy() || d.unknown != nil {
			t.Fatal("native viewer changed original complete record or request")
		}
	}
	s.Close(d)
	if d.httpSessionBrowser != nil || d.httpSessions || d.last != nil {
		t.Fatal("native document close retained original Session browser cache")
	}
}
