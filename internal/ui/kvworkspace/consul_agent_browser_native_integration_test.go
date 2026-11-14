package kvworkspace

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
)

func consulAgentBrowserScreenshot(t *testing.T, s *State, d *Document, stage string, labels ...string) {
	t.Helper()
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			assertHTTPSessionRenewalControls(t, tt, size, labels...)
			captureAgentUnit(t, tt, language, size, stage)
		}
	}
}

func TestConsulNativePublicAgentBrowserOriginalFormsMapsDetailsAndFiles(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	service := consulHealthBrowserFixture(t, p, secret, writer)
	s, d, _ := consulWorkspaceState(t, p, secret)
	if len(d.RequestTools()) != 30 {
		t.Fatal("original Agent tools unavailable")
	}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentSelf, contract.KVToolHTTPAgentServices, contract.KVToolHTTPAgentChecks} {
		t.Run(string(kind), func(t *testing.T) {
			input := contract.KVRequestToolInput{}
			if kind != contract.KVToolHTTPAgentSelf {
				field := "Service"
				if kind == contract.KVToolHTTPAgentChecks {
					field = "ServiceName"
				}
				input.HTTPAgent = contract.KVHTTPAgentToolInput{IncludeFilter: true, Filter: field + " == `" + service + "`"}
			}
			s.OpenRequestTool(d, kind)
			s.SetRequestToolInput(d, kind, input)
			consulAgentBrowserScreenshot(t, s, d, string(kind)+"-native-form", "生成请求文本", "复制工具输入", "返回请求文本")
			previous := d.last
			s.GenerateRequestTool(d)
			if d.Busy() || d.last != previous {
				t.Fatal("Agent form generation dispatched")
			}
			request := d.Request()
			s.Run(d)
			if d.confirmation != nil {
				t.Fatal("Agent read requested write confirmation")
			}
			consume(t, s, d)
			r := d.last
			if r == nil || r.Result.HTTP == nil || !r.Result.Completed || r.Authored != request || r.Result.WriteOutcomeUnknown {
				t.Fatal("original Agent HTTP receipt missing", d.Error())
			}
			h := r.Result.HTTP
			if h.Request.Query["dc"] != nil || h.Request.Query["consistent"] != nil || h.Request.Query["index"] != nil {
				t.Fatal("Agent read inherited catalog options")
			}
			consulAgentBrowserScreenshot(t, s, d, string(kind)+"-native-receipt", "HTTP 原正文", "HTTP 头与元数据")
			if kind == contract.KVToolHTTPAgentSelf {
				if h.Agent != nil || !strings.Contains(string(h.Body), "NodeID") {
					t.Fatal("original Agent self identity changed")
				}
				return
			}
			projection := agentReceiptProjection(r)
			if projection == nil || len(projection.Rows) != 53 || !projection.Complete || h.Request.Query["filter"][0] != input.HTTPAgent.Filter {
				t.Fatal("original filtered Agent map missing", d.Error())
			}
			body := bytes.Clone(h.Body)
			tt := workspaceTester(s, d, 720, 520)
			workspaceClick(t, tt, "本机 Agent 服务与检查")
			consulAgentBrowserScreenshot(t, s, d, string(kind)+"-native-list", "筛选已返回的 Agent 条目", "下一页", "复制所选原 map 值 JSON")
			workspaceClick(t, tt, "下一页")
			b := d.httpAgentBrowser
			if b.page != 1 || len(b.pageIndices()) != 3 {
				t.Fatal("original Agent last page missing")
			}
			original := b.pageIndices()[2]
			workspaceClick(t, tt, projection.Rows[original].Key)
			workspaceClick(t, tt, "复制所选原 map 值 JSON")
			consume(t, s, d)
			want := bytes.Clone(projection.Rows[original].Raw)
			if tt.Clipboard() != string(want) {
				t.Fatal("original Agent value clipboard changed")
			}
			var exported []byte
			s.SetFiles(Files{SavePath: func(string) (string, error) { return "owned-original-agent.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
				var out bytes.Buffer
				err := write(&out)
				exported = out.Bytes()
				return err
			}})
			s.ExportSelectedHTTPAgentValue(d)
			consume(t, s, d)
			if !bytes.Equal(exported, want) {
				t.Fatal("complete original Agent value export changed")
			}
			workspaceClick(t, tt, "查看所选原始详情")
			if !strings.Contains(b.preview, projection.Rows[original].ID) {
				t.Fatal("original Agent detail identity missing")
			}
			consulAgentBrowserScreenshot(t, s, d, string(kind)+"-native-detail", "本机 Agent 原 map 值详情", "返回 Agent 列表")
			workspaceClick(t, tt, "返回原 HTTP 回执")
			if d.Request() != request || !bytes.Equal(d.last.Result.HTTP.Body, body) {
				t.Fatal("Agent browsing changed original request/receipt")
			}
		})
	}
}

func TestConsulNativePublicAgentBrowserParkingUnknownWriteAndOriginalRecovery(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	proxy, calls, committed := consulWorkspaceFault(t, p, "lost")
	s, d, m := consulWorkspaceState(t, proxy, secret)
	put, err := consul.KeyPutRequest("native/workspace/agent-unknown", []byte("original"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	consulWorkspaceInput(t, s, d, put)
	s.Run(d)
	s.Confirm(d, true)
	await(t, committed)
	consume(t, s, d)
	if d.unknown == nil || !d.unknown.Result.WriteOutcomeUnknown || calls.Load() != 1 {
		t.Fatal("original write fault lost unknown evidence")
	}
	unknown := d.unknown.Clone()
	s.Recover(d)
	consume(t, s, d)
	if d.NeedsRecovery() || calls.Load() != 1 {
		t.Fatal("recovery executed original write")
	}
	input := contract.KVRequestToolInput{HTTPAgent: contract.KVHTTPAgentToolInput{}}
	s.OpenRequestTool(d, contract.KVToolHTTPAgentServices)
	s.SetRequestToolInput(d, contract.KVToolHTTPAgentServices, input)
	s.GenerateRequestTool(d)
	request := d.Request()
	previous := d.last
	s.Run(d)
	job := d.job
	if job == nil {
		t.Fatal("explicit health read did not start", d.Error())
	}
	active := false
	s.deps.Active = func(*Document) bool { return active }
	await(t, job.finished)
	s.Consume()
	if d.job != job || d.last != previous {
		t.Fatal("parked health completion consumed")
	}
	active = true
	consume(t, s, d)
	if agentReceiptProjection(d.last) == nil || !d.hasUnknownWrites() || !bytes.Equal(d.unknown.Result.HTTP.Body, unknown.Result.HTTP.Body) || calls.Load() != 1 {
		t.Fatal("later health read cleared or replayed original unknown write")
	}
	tt := workspaceTester(s, d, 720, 520)
	workspaceClick(t, tt, "本机 Agent 服务与检查")
	consulAgentBrowserScreenshot(t, s, d, "native-agent-after-unknown", "筛选已返回的 Agent 条目", "返回原 HTTP 回执")
	workspaceClick(t, tt, "返回原 HTTP 回执")
	record, err := s.OperationRecord(d)
	if err != nil {
		t.Fatal(err)
	}
	scope := d.scope.Clone()
	m.Disconnect(p.ID)
	s.Consume()
	if !d.NeedsRecovery() || d.Request() != request {
		t.Fatal("disconnect discarded authored health read")
	}
	if err = m.Connect(context.Background(), proxy, secret); err != nil {
		t.Fatal(err)
	}
	s.Recover(d)
	consume(t, s, d)
	got, err := s.OperationRecord(d)
	if err != nil || !bytes.Equal(got, record) || d.scope.Generation == scope.Generation || !d.hasUnknownWrites() || calls.Load() != 1 {
		t.Fatal("pure recovery changed original health/unknown evidence", err)
	}
	consulAgentBrowserScreenshot(t, s, d, "native-agent-recovered", "运行 KV 请求", "复制完整操作记录")
	s.Close(d)
	if d.httpAgentBrowser != nil || d.httpAgent || d.last != nil || d.unknown != nil {
		t.Fatal("health close retained receipt or caches")
	}
}
