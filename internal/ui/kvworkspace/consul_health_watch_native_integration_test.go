package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
)

func boundedNativeHealthWatch(t *testing.T, d *Document) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(d.Request().JSON), &fields); err != nil {
		t.Fatal(err)
	}
	fields["max_observations"] = json.RawMessage("1")
	fields["wait"] = json.RawMessage(`"100ms"`)
	raw, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	d.Editor.Buffer.Set(string(raw))
}

func TestConsulNativePublicHealthWatchOriginalFormsSnapshotsDetailsAndFiles(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	service := consulHealthBrowserFixture(t, p, secret, writer)
	s, d, m := consulWorkspaceState(t, p, secret)
	s.deps.HTTPWatchProviderFor = m.KVHTTPWatch
	d.httpWatchAvailable = true
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPHealthState, contract.KVToolHTTPHealthNode, contract.KVToolHTTPHealthChecks, contract.KVToolHTTPHealthService} {
		t.Run(string(kind), func(t *testing.T) {
			input := contract.KVRequestToolInput{HTTPHealth: contract.KVHTTPHealthToolInput{Index: "0", Wait: "100ms", IncludeNear: true, Near: "_agent"}}
			switch kind {
			case contract.KVToolHTTPHealthState:
				input.HTTPHealth.State = "any"
				input.HTTPHealth.IncludeFilter, input.HTTPHealth.Filter = true, "ServiceName == `"+service+"`"
			case contract.KVToolHTTPHealthNode:
				input.HTTPHealth.Node = d.scope.HTTP.AgentName
				input.HTTPHealth.IncludeFilter, input.HTTPHealth.Filter = true, "ServiceName == `"+service+"`"
			default:
				input.HTTPHealth.Service = service
			}
			if kind == contract.KVToolHTTPHealthService {
				input.HTTPHealth.Passing = "false"
			}
			s.OpenRequestTool(d, kind)
			s.SetRequestToolInput(d, kind, input)
			consulHealthBrowserScreenshot(t, s, d, string(kind)+"-watch-form", "生成健康 Watch 请求", "生成请求文本", "包含原生 filter", "包含原生 near")
			tt := workspaceTester(s, d, 720, 520)
			workspaceClick(t, tt, "生成健康 Watch 请求")
			if d.method != HTTPWatchMethod || d.Busy() || d.httpWatch.lease != nil && d.httpWatch.run != nil {
				t.Fatal("health generation dispatched", d.Error())
			}
			boundedNativeHealthWatch(t, d)
			request := d.Request()
			file, err := EncodeRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeRequest(file)
			if err != nil || decoded != request {
				t.Fatal("native health Watch request file changed", err)
			}
			consulHealthBrowserScreenshot(t, s, d, string(kind)+"-watch-request", "Watch 原健康目标", "启动 HTTP Watch")
			s.Run(d)
			consume(t, s, d)
			if d.confirmation != nil || !d.HTTPWatchStatus().Ended || d.httpWatch.status.Observations != 1 {
				t.Fatal("native bounded health Watch did not settle", d.Error())
			}
			r := d.httpWatch.healthReceipt
			projection := healthReceiptProjection(r)
			if projection == nil || healthRowCount(projection) != 53 || !projection.Complete || r.Authored != request {
				t.Fatal("selected native health observation lost", d.Error())
			}
			if r.Result.HTTP.Request.Query["index"][0] != "0" || r.Result.HTTP.Request.Query["wait"][0] != "100ms" || r.Result.HTTP.Request.Query["near"][0] != "_agent" {
				t.Fatal("original blocking parameters changed")
			}
			body := bytes.Clone(r.Result.HTTP.Body)
			consulHealthBrowserScreenshot(t, s, d, string(kind)+"-watch-selected", "浏览所选健康观察", "复制完整 Watch 记录")
			tt = workspaceTester(s, d, 720, 520)
			workspaceClick(t, tt, "浏览所选健康观察")
			workspaceClick(t, tt, "下一页")
			b := d.httpHealthBrowser
			if b.page != 1 || len(b.pageIndices()) != 3 {
				t.Fatal("native health snapshot last page missing")
			}
			original := b.pageIndices()[2]
			label := ""
			if projection.Kind == "checks" {
				label = projection.Checks[original].Name
			} else {
				label = projection.Instances[original].Service.ID
			}
			workspaceClick(t, tt, label)
			workspaceClick(t, tt, "复制所选原始 JSON")
			consume(t, s, d)
			want, err := originalHealthRow(body, original)
			if err != nil || tt.Clipboard() != string(want) {
				t.Fatal("Watch snapshot original copy changed", err)
			}
			workspaceClick(t, tt, "查看所选原始详情")
			consulHealthBrowserScreenshot(t, s, d, string(kind)+"-watch-detail", "健康检查或实例原始详情", "返回健康列表")
			workspaceClick(t, tt, "返回原 HTTP 回执")
			record, err := s.HTTPWatchRecord(d)
			if err != nil || !bytes.Contains(record, []byte("original_http")) || !bytes.Contains(record, []byte("captured_request")) || !bytes.Contains(record, []byte(service)) || d.last != nil {
				t.Fatal("Watch evidence promoted to one-shot result or changed", err)
			}
			before := d.httpWatch.run.Snapshot()
			s.ClearHTTPWatch(d)
			if d.httpWatch.run != nil || d.httpWatch.healthReceipt != nil || d.httpHealthBrowser != nil || len(before.Observations) != 1 {
				t.Fatal("explicit clear kept old cache")
			}
		})
	}
}

func TestConsulNativePublicHealthWatchParkingUnknownWriteAndOriginalRecovery(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	proxy, calls, committed := consulWorkspaceFault(t, p, "lost")
	s, d, m := consulWorkspaceState(t, proxy, secret)
	s.deps.HTTPWatchProviderFor = m.KVHTTPWatch
	d.httpWatchAvailable = true
	put, err := consul.KeyPutRequest("native/workspace/health-watch-unknown", []byte("original"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	consulWorkspaceInput(t, s, d, put)
	s.Run(d)
	s.Confirm(d, true)
	await(t, committed)
	consume(t, s, d)
	if d.unknown == nil || !d.unknown.Result.WriteOutcomeUnknown || calls.Load() != 1 {
		t.Fatal("original processed write fault absent")
	}
	unknown := d.unknown.Clone()
	s.Recover(d)
	consume(t, s, d)
	s.OpenRequestTool(d, contract.KVToolHTTPHealthState)
	s.SetRequestToolInput(d, contract.KVToolHTTPHealthState, contract.KVRequestToolInput{HTTPHealth: contract.KVHTTPHealthToolInput{State: "any"}})
	s.GenerateHTTPHealthWatch(d)
	boundedNativeHealthWatch(t, d)
	request := d.Request()
	s.Run(d)
	job := d.httpWatch.job
	if job == nil {
		t.Fatal("explicit health Watch did not start", d.Error())
	}
	active := false
	s.deps.Active = func(*Document) bool { return active }
	await(t, job.finished)
	s.Consume()
	if d.httpWatch.job != job || d.httpWatch.healthReceipt != nil {
		t.Fatal("parked original start completion consumed")
	}
	active = true
	consume(t, s, d)
	if !d.HTTPWatchStatus().Ended || healthReceiptProjection(d.httpWatch.healthReceipt) == nil || !d.hasUnknownWrites() || calls.Load() != 1 {
		t.Fatal("health Watch cleared or replayed original unknown write")
	}
	consulHealthBrowserScreenshot(t, s, d, "health-watch-after-unknown", "浏览所选健康观察", "复制完整 Watch 记录")
	record, err := s.HTTPWatchRecord(d)
	if err != nil {
		t.Fatal(err)
	}
	scope := d.Scope()
	m.Disconnect(p.ID)
	s.Consume()
	if !d.NeedsRecovery() || d.Request() != request {
		t.Fatal("retirement discarded original request")
	}
	if err := m.Connect(context.Background(), proxy, secret); err != nil {
		t.Fatal(err)
	}
	s.Recover(d)
	consume(t, s, d)
	got, err := s.HTTPWatchRecord(d)
	if err != nil || !bytes.Equal(got, record) || d.Scope().Generation <= scope.Generation || !d.hasUnknownWrites() || calls.Load() != 1 || !bytes.Equal(d.unknown.Result.HTTP.Body, unknown.Result.HTTP.Body) {
		t.Fatal("health Watch recovery changed original archives or replayed", err)
	}
	consulHealthBrowserScreenshot(t, s, d, "health-watch-recovered", "浏览所选健康观察", "启动 HTTP Watch")
	consulWorkspaceInput(t, s, d, put)
	s.Run(d)
	if d.confirmation != nil || !strings.Contains(d.Error(), "unknown") && !d.hasUnknownWrites() || calls.Load() != 1 {
		t.Fatal("health observation removed unknown write barrier")
	}
	s.Close(d)
	if d.httpWatch != nil || d.httpHealthBrowser != nil || d.unknown != nil {
		t.Fatal("close retained original Watch caches")
	}
}
