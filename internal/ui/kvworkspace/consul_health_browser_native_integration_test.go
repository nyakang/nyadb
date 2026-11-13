package kvworkspace

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/consul"
)

// Register only owned test services through the original fixture transport.
// Production Agent mutation capabilities are not extended by these fixtures.
func consulHealthBrowserFixture(t *testing.T, p connections.Profile, secret string, writer contract.KVRequestLease) string {
	t.Helper()
	cfg, err := connections.ConsulProfileConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.Options.CAFile != "" {
		raw, err := os.ReadFile(cfg.Options.CAFile)
		if err != nil {
			t.Fatal(err)
		}
		tc.RootCAs = x509.NewCertPool()
		if !tc.RootCAs.AppendCertsFromPEM(raw) {
			t.Fatal("invalid owned CA")
		}
	}
	if cfg.Options.ClientCertFile != "" {
		pair, err := tls.LoadX509KeyPair(cfg.Options.ClientCertFile, cfg.Options.ClientKeyFile)
		if err != nil {
			t.Fatal(err)
		}
		tc.Certificates = []tls.Certificate{pair}
	}
	tr := &http.Transport{TLSClientConfig: tc, DisableKeepAlives: true}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	put := func(path string, value any) {
		t.Helper()
		var body []byte
		if value != nil {
			body, err = json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
		}
		request, err := http.NewRequestWithContext(context.Background(), "PUT", cfg.Endpoint+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		if cfg.Options.Auth == "token" {
			request.Header.Set("X-Consul-Token", secret)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("owned Agent registration transport failed", err)
		}
		defer response.Body.Close()
		_, err = io.Copy(io.Discard, response.Body)
		if err != nil || response.StatusCode != 200 {
			t.Fatal("owned Agent registration failed", response.StatusCode, err)
		}
	}
	service := "owned-health-browser"
	for i := range 53 {
		id := fmt.Sprintf("%s-%03d", service, i)
		status := "passing"
		if i%2 == 1 {
			status = "warning"
		}
		put("/v1/agent/service/register", map[string]any{"ID": id, "Name": service, "Address": "127.0.0.1", "Port": 8087, "Tags": []string{"中文", "owned"}, "Meta": map[string]string{"original": "原值"}, "Check": map[string]any{"CheckID": id + "-check", "Name": id, "TTL": "10m", "Status": status, "Notes": "original 中文"}})
		t.Cleanup(func() { put("/v1/agent/service/deregister/"+url.PathEscape(id), nil) })
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		request, err := consul.HealthChecksRequest(service)
		if err != nil {
			t.Fatal(err)
		}
		r := consulRenewalDo(t, writer, request)
		if r.HTTP != nil && r.HTTP.Health != nil && len(r.HTTP.Health.Checks) == 53 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned Agent registrations did not reach catalog")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return service
}

func consulHealthBrowserScreenshot(t *testing.T, s *State, d *Document, stage string, labels ...string) {
	t.Helper()
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			assertHTTPSessionRenewalControls(t, tt, size, labels...)
			captureHealthUnit(t, tt, language, size, stage)
		}
	}
}

func TestConsulNativePublicHealthBrowserOriginalFormsPagesDetailsAndExport(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	service := consulHealthBrowserFixture(t, p, secret, writer)
	s, d, _ := consulWorkspaceState(t, p, secret)
	if len(d.RequestTools()) != 17 {
		t.Fatal("original four health read tools missing")
	}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPHealthState, contract.KVToolHTTPHealthNode, contract.KVToolHTTPHealthChecks, contract.KVToolHTTPHealthService} {
		t.Run(string(kind), func(t *testing.T) {
			input := contract.KVRequestToolInput{HTTPHealth: contract.KVHTTPHealthToolInput{Index: "0", Wait: "1ms", IncludeNear: true, Near: "_agent"}}
			switch kind {
			case contract.KVToolHTTPHealthState:
				input.HTTPHealth.State = "any"
				input.HTTPHealth.IncludeFilter = true
				input.HTTPHealth.Filter = "ServiceName == `" + service + "`"
			case contract.KVToolHTTPHealthNode:
				input.HTTPHealth.Node = d.scope.HTTP.AgentName
				input.HTTPHealth.IncludeFilter = true
				input.HTTPHealth.Filter = "ServiceName == `" + service + "`"
			default:
				input.HTTPHealth.Service = service
			}
			if kind == contract.KVToolHTTPHealthService {
				input.HTTPHealth.Passing = "false"
			}
			s.OpenRequestTool(d, kind)
			s.SetRequestToolInput(d, kind, input)
			consulHealthBrowserScreenshot(t, s, d, string(kind)+"-native-form", "生成请求文本", "复制工具输入", "包含原生 filter", "包含原生 near")
			s.GenerateRequestTool(d)
			request := d.Request()
			s.Run(d)
			if d.confirmation != nil {
				t.Fatal("health read acquired write confirmation")
			}
			consume(t, s, d)
			r := d.last
			projection := healthReceiptProjection(r)
			if projection == nil || healthRowCount(projection) != 53 || r.Authored != request || !projection.Complete {
				t.Fatal("original health response missing", d.Error())
			}
			body := bytes.Clone(r.Result.HTTP.Body)
			if r.Result.HTTP.Request.Query["index"][0] != "0" || r.Result.HTTP.Request.Query["wait"][0] != "1ms" || r.Result.HTTP.Request.Query["near"][0] != "_agent" {
				t.Fatal("captured original options changed")
			}
			tt := workspaceTester(s, d, 720, 520)
			workspaceClick(t, tt, "健康检查与服务实例")
			consulHealthBrowserScreenshot(t, s, d, string(kind)+"-native-list", "筛选已返回的健康观察", "检查状态筛选", "下一页", "复制所选原始 JSON")
			workspaceClick(t, tt, "下一页")
			b := d.httpHealthBrowser
			if b.page != 1 || len(b.pageIndices()) != 3 {
				t.Fatal("native last page missing")
			}
			original := b.pageIndices()[2]
			label := projection.Checks
			rowLabel := ""
			if projection.Kind == "checks" {
				rowLabel = label[original].Name
			} else {
				rowLabel = projection.Instances[original].Service.ID
			}
			workspaceClick(t, tt, rowLabel)
			workspaceClick(t, tt, "复制所选原始 JSON")
			consume(t, s, d)
			want, err := originalHealthRow(body, original)
			if err != nil || tt.Clipboard() != string(want) {
				t.Fatal("native selected JSON changed", err)
			}
			var exported []byte
			s.SetFiles(Files{SavePath: func(string) (string, error) { return "owned-original-health.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
				var out bytes.Buffer
				err := write(&out)
				exported = out.Bytes()
				return err
			}})
			s.ExportSelectedHTTPHealthRow(d)
			consume(t, s, d)
			if !bytes.Equal(exported, want) {
				t.Fatal("native original row export changed")
			}
			workspaceClick(t, tt, "查看所选原始详情")
			if !strings.Contains(b.preview, "original") && !strings.Contains(b.preview, "原值") {
				t.Fatal("native details omitted original fields")
			}
			consulHealthBrowserScreenshot(t, s, d, string(kind)+"-native-detail", "健康检查或实例原始详情", "返回健康列表")
			workspaceClick(t, tt, "返回原 HTTP 回执")
			if d.Request() != request || !bytes.Equal(d.last.Result.HTTP.Body, body) {
				t.Fatal("local browser rewrote original receipt/request")
			}
		})
	}
}

func TestConsulNativePublicHealthBrowserParkedReadPreservesUnknownWriteAndRecovery(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	proxy, calls, committed := consulWorkspaceFault(t, p, "lost")
	s, d, m := consulWorkspaceState(t, proxy, secret)
	put, err := consul.KeyPutRequest("native/workspace/health-unknown", []byte("original"), nil, nil)
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
	input := contract.KVRequestToolInput{HTTPHealth: contract.KVHTTPHealthToolInput{State: "any"}}
	s.OpenRequestTool(d, contract.KVToolHTTPHealthState)
	s.SetRequestToolInput(d, contract.KVToolHTTPHealthState, input)
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
	if healthReceiptProjection(d.last) == nil || !d.hasUnknownWrites() || !bytes.Equal(d.unknown.Result.HTTP.Body, unknown.Result.HTTP.Body) || calls.Load() != 1 {
		t.Fatal("later health read cleared or replayed original unknown write")
	}
	tt := workspaceTester(s, d, 720, 520)
	workspaceClick(t, tt, "健康检查与服务实例")
	consulHealthBrowserScreenshot(t, s, d, "native-health-after-unknown", "筛选已返回的健康观察", "返回原 HTTP 回执")
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
	consulHealthBrowserScreenshot(t, s, d, "native-health-recovered", "运行 KV 请求", "复制完整操作记录")
	s.Close(d)
	if d.httpHealthBrowser != nil || d.httpHealth || d.last != nil || d.unknown != nil {
		t.Fatal("health close retained receipt or caches")
	}
}
