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
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/consul"
)

func consulCatalogBrowserFixture(t *testing.T, p connections.Profile, secret string, writer contract.KVRequestLease) string {
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
	service := "  owned-catalog-" + t.Name() + " 雪%?#++  "
	for i := range 53 {
		id := fmt.Sprintf("owned-catalog-%s-%03d", t.Name(), i)
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

func consulCatalogBrowserScreenshot(t *testing.T, s *State, d *Document, stage string, labels ...string) {
	t.Helper()
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			assertHTTPSessionRenewalControls(t, tt, size, labels...)
			captureCatalogUnit(t, tt, language, size, stage)
		}
	}
}

func TestConsulNativePublicCatalogBrowserOriginalFormsMapsDetailsAndFiles(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	service := consulCatalogBrowserFixture(t, p, secret, writer)
	self := consulRenewalDo(t, writer, consul.AgentSelfRequest())
	var info struct{ Config struct{ NodeName string } }
	if self.HTTP == nil || json.Unmarshal(self.HTTP.Body, &info) != nil || info.Config.NodeName == "" {
		t.Fatal("original native node identity unavailable")
	}
	node := info.Config.NodeName
	s, d, _ := consulWorkspaceState(t, p, secret)
	if len(d.RequestTools()) != 35 {
		t.Fatal("five original Catalog tools unavailable")
	}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPCatalogDatacenters, contract.KVToolHTTPCatalogNodes, contract.KVToolHTTPCatalogServices, contract.KVToolHTTPCatalogService, contract.KVToolHTTPCatalogNode} {
		t.Run(string(kind), func(t *testing.T) {
			input := contract.KVRequestToolInput{}
			switch kind {
			case contract.KVToolHTTPCatalogNodes:
				input.HTTPCatalog = contract.KVHTTPCatalogToolInput{IncludeFilter: true, Filter: "Node == " + fmt.Sprintf("%q", node), IncludeNear: true, Near: "_agent", Index: "0", Wait: "1ms"}
			case contract.KVToolHTTPCatalogServices:
				input.HTTPCatalog = contract.KVHTTPCatalogToolInput{IncludeFilter: true, Filter: "ServiceName == " + fmt.Sprintf("%q", service), Index: "0", Wait: "1ms"}
			case contract.KVToolHTTPCatalogService:
				input.HTTPCatalog = contract.KVHTTPCatalogToolInput{Service: service, IncludeFilter: true, Filter: "ServiceName == " + fmt.Sprintf("%q", service), IncludeNear: true, Near: "", Index: "0", Wait: "1ms"}
			case contract.KVToolHTTPCatalogNode:
				input.HTTPCatalog = contract.KVHTTPCatalogToolInput{Node: node, IncludeFilter: true, Filter: "Service == " + fmt.Sprintf("%q", service), Index: "0", Wait: "1ms"}
			}
			s.OpenRequestTool(d, kind)
			s.SetRequestToolInput(d, kind, input)
			consulCatalogBrowserScreenshot(t, s, d, string(kind)+"-native-form", "生成请求文本", "复制工具输入", "返回请求文本")
			previous := d.last
			s.GenerateRequestTool(d)
			if d.Busy() || d.last != previous {
				t.Fatal("Catalog generation dispatched")
			}
			request := d.Request()
			s.Run(d)
			if d.confirmation != nil {
				t.Fatal("Catalog read requested write confirmation")
			}
			consume(t, s, d)
			r := d.last
			if r == nil || r.Result.HTTP == nil || !r.Result.Completed || r.Authored != request || r.Result.WriteOutcomeUnknown {
				t.Fatal("original Catalog receipt missing", d.Error())
			}
			projection := catalogReceiptProjection(r)
			if projection == nil || !projection.Complete || r.Result.HTTP.Agent != nil || r.Result.HTTP.Health != nil {
				t.Fatal("original Catalog projection missing or mixed", d.Error())
			}
			if r.Result.HTTP.Request.Query["dc"] != nil || r.Result.HTTP.Request.Query["consistent"] != nil {
				t.Fatal("Catalog form injected hidden routing scope")
			}
			if kind != contract.KVToolHTTPCatalogDatacenters && (r.Result.HTTP.Request.Query["filter"][0] != input.HTTPCatalog.Filter || r.Result.HTTP.Request.Query["index"][0] != "0") {
				t.Fatal("Catalog form changed exact native option presence/text")
			}
			count := catalogRowCount(projection)
			expected := 1
			if kind == contract.KVToolHTTPCatalogService {
				expected = 53
			}
			if kind == contract.KVToolHTTPCatalogNode {
				expected = 54
			}
			if count != expected {
				t.Fatal("original Catalog row count changed", kind, count, expected)
			}
			body := bytes.Clone(r.Result.HTTP.Body)
			consulCatalogBrowserScreenshot(t, s, d, string(kind)+"-native-receipt", "HTTP 原正文", "HTTP 头与元数据")
			tt := workspaceTester(s, d, 720, 520)
			workspaceClick(t, tt, "Catalog 原响应浏览器")
			consulCatalogBrowserScreenshot(t, s, d, string(kind)+"-native-list", "筛选已返回的 Catalog 条目", "下一页", "复制所选 Catalog 原值 JSON")
			b := d.httpCatalogBrowser
			if count > 50 {
				workspaceClick(t, tt, "下一页")
				if b.page != 1 {
					t.Fatal("Catalog native page did not advance")
				}
			}
			indices := b.pageIndices()
			original := indices[len(indices)-1]
			cells := catalogRowCells(projection, original)
			label := cells[1]
			if kind == contract.KVToolHTTPCatalogService || kind == contract.KVToolHTTPCatalogNode {
				label = cells[3]
			}
			workspaceClick(t, tt, label)
			workspaceClick(t, tt, "复制所选 Catalog 原值 JSON")
			consume(t, s, d)
			want, ok := catalogRowRaw(r, original)
			if !ok || tt.Clipboard() != string(want) {
				t.Fatal("original Catalog value clipboard changed")
			}
			var exported []byte
			s.SetFiles(Files{SavePath: func(string) (string, error) { return "owned-original-catalog.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
				var out bytes.Buffer
				err := write(&out)
				exported = out.Bytes()
				return err
			}})
			s.ExportSelectedHTTPCatalogValue(d)
			consume(t, s, d)
			if !bytes.Equal(exported, want) {
				t.Fatal("complete original Catalog export changed")
			}
			workspaceClick(t, tt, "查看所选原始详情")
			if !b.detail || b.preview == "" {
				t.Fatal("Catalog original value detail unavailable")
			}
			consulCatalogBrowserScreenshot(t, s, d, string(kind)+"-native-detail", "Catalog 原值详情", "返回 Catalog 列表")
			workspaceClick(t, tt, "返回原 HTTP 回执")
			if d.Request() != request || !bytes.Equal(d.last.Result.HTTP.Body, body) {
				t.Fatal("Catalog browsing changed original request/receipt")
			}
		})
	}
}

func TestConsulNativePublicCatalogBrowserParkingUnknownWriteAndOriginalRecovery(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	proxy, calls, committed := consulWorkspaceFault(t, p, "lost")
	s, d, m := consulWorkspaceState(t, proxy, secret)
	put, err := consul.KeyPutRequest("native/workspace/catalog-unknown", []byte("original"), nil, nil)
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
	input := contract.KVRequestToolInput{HTTPCatalog: contract.KVHTTPCatalogToolInput{}}
	s.OpenRequestTool(d, contract.KVToolHTTPCatalogServices)
	s.SetRequestToolInput(d, contract.KVToolHTTPCatalogServices, input)
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
	if catalogReceiptProjection(d.last) == nil || !d.hasUnknownWrites() || !bytes.Equal(d.unknown.Result.HTTP.Body, unknown.Result.HTTP.Body) || calls.Load() != 1 {
		t.Fatal("later health read cleared or replayed original unknown write")
	}
	tt := workspaceTester(s, d, 720, 520)
	workspaceClick(t, tt, "Catalog 原响应浏览器")
	consulCatalogBrowserScreenshot(t, s, d, "native-catalog-after-unknown", "筛选已返回的 Catalog 条目", "返回原 HTTP 回执")
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
	consulCatalogBrowserScreenshot(t, s, d, "native-catalog-recovered", "运行 KV 请求", "复制完整操作记录")
	s.Close(d)
	if d.httpCatalogBrowser != nil || d.httpCatalog || d.last != nil || d.unknown != nil {
		t.Fatal("health close retained receipt or caches")
	}
}

func TestConsulNativePublicCatalogOriginalNullInvalidOptionsAndReadCancellation(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	s, d, _ := consulWorkspaceState(t, p, secret)
	s.OpenRequestTool(d, contract.KVToolHTTPCatalogNode)
	s.SetRequestToolInput(d, contract.KVToolHTTPCatalogNode, contract.KVRequestToolInput{HTTPCatalog: contract.KVHTTPCatalogToolInput{Node: "owned-no-such-catalog-node"}})
	s.GenerateRequestTool(d)
	s.Run(d)
	consume(t, s, d)
	r := d.last
	if r == nil || catalogReceiptProjection(r) == nil || !r.Result.HTTP.Catalog.Null || !bytes.Equal(bytes.TrimSpace(r.Result.HTTP.Body), []byte("null")) {
		t.Fatal("native Catalog node null lost its original boundary", d.Error())
	}
	tt := workspaceTester(s, d, 720, 520)
	workspaceClick(t, tt, "Catalog 原响应浏览器")
	consulCatalogBrowserScreenshot(t, s, d, "native-catalog-node-null", "原节点查询返回 null；此观察不提供写入权限", "返回原 HTTP 回执")
	workspaceClick(t, tt, "返回原 HTTP 回执")
	original := d.Request()
	previous := d.last
	s.OpenRequestTool(d, contract.KVToolHTTPCatalogServices)
	s.SetRequestToolInput(d, contract.KVToolHTTPCatalogServices, contract.KVRequestToolInput{HTTPCatalog: contract.KVHTTPCatalogToolInput{IncludeNear: true, Near: "_agent"}})
	s.GenerateRequestTool(d)
	if d.Request() != original || d.last != previous || d.Busy() || d.Error() == "" {
		t.Fatal("ignored native Catalog near option dispatched or replaced text")
	}
	d.requestTools.open = false
	for _, mode := range []string{"lost", "partial", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			proxy, _, calls, processed := consulOriginalToolFault(t, p, mode, func(r *http.Request) bool { return r.Method == "GET" && r.URL.Path == "/v1/catalog/services" })
			state, doc, _ := consulWorkspaceState(t, proxy, secret)
			suppress := doc.last
			state.OpenRequestTool(doc, contract.KVToolHTTPCatalogServices)
			state.GenerateRequestTool(doc)
			state.Run(doc)
			job := doc.job
			if job == nil {
				t.Fatal("original Catalog read fault did not start", doc.Error())
			}
			await(t, processed)
			if mode == "cancel" {
				state.RequestCancel(doc)
			}
			consume(t, state, doc)
			if doc.last == nil || doc.last == suppress || doc.last.Result.HTTP == nil || doc.last.Result.HTTP.Catalog != nil || doc.last.Result.Completed || doc.last.Result.WriteOutcomeUnknown || calls.Load() != 1 {
				t.Fatal("Catalog read fault promoted, lost or retried", mode, doc.Error())
			}
			consulCatalogBrowserScreenshot(t, state, doc, "native-catalog-read-"+mode, "HTTP 原正文", "复制完整操作记录")
		})
	}
}
