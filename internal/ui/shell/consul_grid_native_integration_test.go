package shell

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
)

func consulShellGridProfile(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_CONSUL_IT") != "1" {
		t.Skip("owned native Consul fixture required")
	}
	u, err := url.Parse(os.Getenv("NYADB_CONSUL_ENDPOINT"))
	if err != nil || u.Hostname() == "" {
		t.Fatal("owned endpoint missing")
	}
	p, _ := connections.Defaults(connections.Consul)
	port, _ := strconv.Atoi(u.Port())
	p.Name, p.Host, p.Port = "original Consul grid", u.Hostname(), uint16(port)
	p.ConsulOptions.Auth, p.ConsulOptions.Datacenter, p.ConsulOptions.Prefix = os.Getenv("NYADB_CONSUL_AUTH"), "dc1", "native/shell-grid//"+t.Name()+"/中文/"
	p.ConsulOptions.CAFile, p.ConsulOptions.ClientCertFile, p.ConsulOptions.ClientKeyFile = os.Getenv("NYADB_CONSUL_CA"), os.Getenv("NYADB_CONSUL_CERT"), os.Getenv("NYADB_CONSUL_KEY")
	if u.Scheme == "https" {
		p.TLSMode = "verify-full"
	}
	return p, os.Getenv("NYADB_CONSUL_TOKEN")
}

func consulShellGridWait(t *testing.T, tt *ui.Tester, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !done() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if !done() {
		t.Fatal("original Consul workbench did not settle")
	}
	tt.Frame()
}

func consulShellGridClick(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	tt.Frame()
	if err := tt.Click(locales.Text(label)); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
}

func captureConsulShellGrid(t *testing.T, tt *ui.Tester, stage string) {
	t.Helper()
	dir := os.Getenv("NYADB_CONSUL_GRID_IMAGES")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	defer func() { i18n.SetLocaleFunc(func() string { return "zh-Hans" }); tt.SetSize(1280, 800); tt.Frame() }()
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt.SetSize(size[0], size[1])
			tt.Frame()
			if stage == "grid" || stage == "reopened" {
				if !tt.HasText(locales.Text("原生游标读取实时数据；翻页和重读期间数据变化可能导致行重复或遗漏。")) {
					t.Fatal("public grid live semantics missing", language, size)
				}
			}
			if stage == "structure" {
				for _, label := range []string{"SQL NULL / 未设置", "默认值", "自增", "自动递增", "外键", "索引"} {
					if tt.HasText(locales.Text(label)) {
						t.Fatal("public key structure invented relational metadata", language, size, label)
					}
				}
			}
			f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%dx%d-shell-grid-%s.png", language, size[0], size[1], stage)))
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(f, tt.Image())
			closeErr := f.Close()
			if err != nil || closeErr != nil {
				t.Fatal("public grid image failed")
			}
		}
	}
}

func consulShellGridConnect(t *testing.T, p connections.Profile, secret string) (*State, *ui.Tester, connections.Profile, ui.Services) {
	t.Helper()
	s, _, profiles, _ := profileTestState(t)
	var err error
	p, err = profiles.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	t.Cleanup(s.CloseMySQLSessions)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	s.sessions.Current().Password = secret
	consulShellGridClick(t, tt, "连接所选数据库")
	consulShellGridWait(t, tt, func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	if s.sessions.StatusFor(p.ID) != "已连接" {
		t.Fatal("public connection failed", s.sessions.Current().Error)
	}
	return s, tt, p, services
}

func consulShellGridOpenNamespace(t *testing.T, s *State, tt *ui.Tester, services ui.Services, p connections.Profile) contract.ObjectRef {
	t.Helper()
	node := navigation.DatabaseNode(p.ID, "kv")
	s.selectNavigationNode(services, node.Key())
	tt.Frame()
	if s.sessions.DatabaseOpened(p.ID, "kv") || s.toolbarActionAvailable("键") {
		t.Fatal("selection implicitly opened key namespace")
	}
	s.submitNavigation(services, node)
	consulShellGridWait(t, tt, func() bool { w := s.sessions.Workspace(p.ID); return w.Pending == "" && w.Cache["kv"] != nil })
	w := s.sessions.Workspace(p.ID)
	object := s.runtime.Namespace(p.ID, "kv").Object("keyspace", "keys")
	if len(w.Cache["kv"]) != 1 || w.Cache["kv"][0].Ref != object {
		t.Fatal("original qualified key object missing")
	}
	children := s.navigation.Children(node.Key())
	if len(children) != 1 || navigation.Parse(children[0]).Category != "键" {
		t.Fatal("native navigator exposed relational categories", children)
	}
	for _, child := range s.navigation.Children(navigation.ConnectionKey(p.ID)) {
		if navigation.Parse(child).Category == "查询" {
			t.Fatal("native navigator exposed SQL")
		}
	}
	return object
}

func TestConsulNativePublicShellQualifiedGridNavigationMenusSearchStructureAndDisconnect(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	p, secret := consulShellGridProfile(t)
	s, tt, p, services := consulShellGridConnect(t, p, secret)
	object := consulShellGridOpenNamespace(t, s, tt, services, p)
	for _, action := range []string{"表", "查询", "用户", "备份", "新建查询"} {
		if s.toolbarActionAvailable(action) {
			t.Fatal("unsupported native toolbar", action)
		}
	}
	// Native View/toolbar categories keep HTTP request documents alongside keys.
	consulShellGridClick(t, tt, "查看")
	if err := tt.ChooseMenuItem(locales.Text("数据库对象"), locales.Text("键")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	captureConsulShellGrid(t, tt, "keys")
	provider, err := s.runtime.KVRequests(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := provider.ReserveKV(999)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	value := bytes.Repeat([]byte{0, 255}, 1024)
	put, _ := consul.KeyPutRequest(p.ConsulOptions.Prefix+"key", value, nil, nil)
	request, _ := consul.DocumentRequest(put)
	plan, err := writer.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := writer.Execute(context.Background(), plan)
	if err != nil || result.HTTP == nil || !result.HTTP.Succeeded {
		t.Fatal("owned grid seed failed", err)
	}
	// The central toolbar and context menu both resolve the exact metadata ref.
	consulShellGridClick(t, tt, "keys")
	consulShellGridClick(t, tt, "打开选中键数据")
	grid := s.currentTab()
	d := grid.data
	if d == nil || d.Object() != object || d.Generation() != s.sessions.Current().Generation {
		t.Fatal("toolbar lost original grid scope")
	}
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if d.Error() != "" || d.Page() == nil || len(d.Page().Rows) != 1 || !d.Page().LiveRead || d.HasDraft() {
		t.Fatal("original public shared grid failed", d.Error())
	}
	if d.Page().Rows[0][0].Value.Text != p.ConsulOptions.Prefix+"key" || !d.Page().Rows[0][1].Truncated {
		t.Fatal("public grid changed original prefix/value")
	}
	captureConsulShellGrid(t, tt, "grid")
	tabs := len(s.tabs)
	s.openMySQLData(services, "kv", "keys")
	if len(s.tabs) != tabs {
		t.Fatal("name-only KV data entry guessed identity")
	}
	for _, bad := range []contract.ObjectRef{object.Namespace().Object("table", "keys"), object.Namespace().Object("keyspace", "other"), {ProfileID: p.ID + 1, Catalog: "kv", Kind: "keyspace", Name: "keys"}} {
		s.openQualifiedData(services, s.sessions.Current().Generation, "kv", bad)
		if len(s.tabs) != tabs || s.currentTab() != grid {
			t.Fatal("foreign qualified object opened")
		}
	}
	s.openQualifiedData(services, s.sessions.Current().Generation+1, "kv", object)
	if len(s.tabs) != tabs {
		t.Fatal("foreign generation opened")
	}
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: "kv", Category: "键", Object: "keys"})
	if s.currentTab() != grid || len(s.tabs) != tabs {
		t.Fatal("navigation duplicated original grid")
	}
	// Test actual metadata search and its qualified result dispatch.
	s.openObjectSearch()
	tt.Frame()
	consulShellGridClick(t, tt, "查找对象名称、注释或定义")
	tt.Type("keys")
	consulShellGridClick(t, tt, "开始查找")
	consulShellGridWait(t, tt, func() bool { return tt.HasText(fmt.Sprintf(locales.Text("找到 %d 项"), 1)) })
	consulShellGridClick(t, tt, "keys")
	consulShellGridClick(t, tt, "打开所选对象")
	if s.currentTab() != grid || len(s.tabs) != tabs {
		t.Fatal("search duplicated original grid")
	}
	// Return to the object list for its original context menu and structure tool.
	s.clickToolbar("键")
	tt.Frame()
	if err := tt.RightClick("keys"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.Text("打开键")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if s.currentTab() != grid || len(s.tabs) != tabs {
		t.Fatal("context menu duplicated original grid")
	}
	s.clickToolbar("键")
	tt.Frame()
	consulShellGridClick(t, tt, "keys")
	consulShellGridClick(t, tt, "查看键结构（只读）")
	structure := s.currentTab()
	if structure.object == nil || structure.object.Object() != object {
		t.Fatal("read-only structure lost qualified keyspace")
	}
	consulShellGridWait(t, tt, func() bool { return !structure.object.Loading() })
	if structure.object.Error() != "" || structure.object.Data() == nil || len(structure.object.Data().Columns) != 7 || structure.object.Data().Columns[2].Generated || structure.object.DesignMode() {
		t.Fatal("native read-only structure failed", structure.object.Error())
	}
	captureConsulShellGrid(t, tt, "structure")
	// An inactive scoped close preserves the original active document ID.
	s.selectTab(grid.id)
	tt.Frame()
	s.closeTabID(structure.id)
	tt.Frame()
	if s.currentTab() != grid || structure.object.Data() != nil {
		t.Fatal("inactive close damaged active original grid")
	}
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("retained HTTP workspace unavailable")
	}
	httpDoc := s.currentTab()
	draft := httpDoc.kv.Request()
	s.selectTab(grid.id)
	tt.Frame()
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if d.Page() != nil || s.runtime.IsConnected(p.ID) || httpDoc.kv.Editor == nil || httpDoc.kv.Request() != draft || !httpDoc.kv.NeedsRecovery() {
		t.Fatal("disconnect lost original HTTP draft or retained scoped grid")
	}
	for _, tab := range s.tabs {
		if tab.data == d {
			t.Fatal("disconnect retained scoped grid tab")
		}
	}
}

// Hold only a reply that the original upstream has completely processed.
func consulShellGridRelay(t *testing.T, p connections.Profile) (connections.Profile, *atomic.Bool, *atomic.Int32, <-chan struct{}) {
	t.Helper()
	secure := &tls.Config{MinVersion: tls.VersionTLS12}
	if p.ConsulOptions.CAFile != "" {
		raw, err := os.ReadFile(p.ConsulOptions.CAFile)
		if err != nil {
			t.Fatal(err)
		}
		secure.RootCAs = x509.NewCertPool()
		if !secure.RootCAs.AppendCertsFromPEM(raw) {
			t.Fatal("owned CA invalid")
		}
	}
	if p.ConsulOptions.ClientCertFile != "" {
		pair, err := tls.LoadX509KeyPair(p.ConsulOptions.ClientCertFile, p.ConsulOptions.ClientKeyFile)
		if err != nil {
			t.Fatal(err)
		}
		secure.Certificates = []tls.Certificate{pair}
	}
	transport := &http.Transport{TLSClientConfig: secure, DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	cfg, err := connections.ConsulProfileConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	armed, calls := &atomic.Bool{}, &atomic.Int32{}
	processed := make(chan struct{})
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		var operations []consul.TxnOperation
		treeRead := r.URL.Path == "/v1/txn" && json.Unmarshal(raw, &operations) == nil && len(operations) == 1 && operations[0].KV != nil && operations[0].KV.Verb == "get-tree"
		dataRead := r.URL.Query().Has("recurse") || treeRead
		request, err := http.NewRequestWithContext(r.Context(), r.Method, cfg.Endpoint+r.URL.RequestURI(), bytes.NewReader(raw))
		if err != nil {
			return
		}
		request.Header = r.Header.Clone()
		response, err := client.Do(request)
		if err != nil {
			return
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			return
		}
		if dataRead {
			calls.Add(1)
			if armed.CompareAndSwap(true, false) {
				close(processed)
				<-r.Context().Done()
				return
			}
		}
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.Header().Del("Content-Length")
		w.WriteHeader(response.StatusCode)
		w.Write(body)
	}))
	t.Cleanup(relay.Close)
	u, _ := url.Parse(relay.URL)
	port, _ := strconv.Atoi(u.Port())
	p.Host, p.Port, p.TLSMode = u.Hostname(), uint16(port), "disable"
	p.ConsulOptions.CAFile, p.ConsulOptions.ClientCertFile, p.ConsulOptions.ClientKeyFile = "", "", ""
	return p, armed, calls, processed
}

func TestConsulNativePublicShellProcessedGridCancelParkingOriginalReopenAndInactiveClose(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	p, secret := consulShellGridProfile(t)
	p.ConsulOptions.Prefix = "native/shell-grid-fault/" + t.Name()
	p, armed, calls, processed := consulShellGridRelay(t, p)
	s, tt, p, services := consulShellGridConnect(t, p, secret)
	object := consulShellGridOpenNamespace(t, s, tt, services, p)
	s.openQualifiedData(services, s.sessions.Current().Generation, "kv", object)
	grid := s.currentTab()
	d := grid.data
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if d.Page() == nil || d.Error() != "" {
		t.Fatal("original live page unavailable", d.Error())
	}
	cached := d.Page()
	generation := d.Generation()
	parkedProfile := p
	parkedProfile.ID, parkedProfile.Name = 0, "parked Consul sibling"
	parkedProfile, err := s.profileService.Create(context.Background(), parkedProfile)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	// A complete page stays buffered while a different original profile is active.
	beforePark := calls.Load()
	s.dataWorkspace.Load(services, d, true)
	s.selectProfileByName(parkedProfile.Name)
	deadline := time.Now().Add(20 * time.Second)
	for calls.Load() == beforePark && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() == beforePark {
		t.Fatal("parked original page was not processed")
	}
	time.Sleep(100 * time.Millisecond)
	tt.Frame()
	if !d.Busy() || d.Page() != cached {
		t.Fatal("parked original page completion consumed")
	}
	s.selectTab(grid.id)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	cached = d.Page()
	before := calls.Load()
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("original parking document unavailable")
	}
	other := s.currentTab()
	s.selectTab(grid.id)
	tt.Frame()
	armed.Store(true)
	s.dataWorkspace.Load(services, d, true)
	select {
	case <-processed:
	case <-time.After(20 * time.Second):
		t.Fatal("native read not processed")
	}
	consulShellGridClick(t, tt, "取消读取数据")
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if d.Page() != cached || d.Error() == "" || calls.Load() != before+1 || !s.dataWorkspace.CanReopen(d) {
		t.Fatalf("cancel state: same page=%v calls=%d expected=%d can reopen=%v original profile=%d active profile=%d generation=%d active generation=%d error=%s", d.Page() == cached, calls.Load(), before+1, s.dataWorkspace.CanReopen(d), p.ID, s.sessions.Current().ProfileID, d.Generation(), s.sessions.Current().Generation, d.Error())
	}
	s.dataWorkspace.Load(services, d, true)
	tt.Frame()
	if d.Busy() || calls.Load() != before+1 {
		t.Fatal("retired original read repeated")
	}
	consulShellGridClick(t, tt, "重新打开数据")
	fresh := grid.data
	consulShellGridWait(t, tt, func() bool { return !fresh.Busy() })
	if fresh == d || d.Page() != nil || fresh.Object() != object || fresh.Generation() != generation || grid.id != s.currentTab().id || fresh.Error() != "" || calls.Load() != before+2 {
		t.Fatal("explicit original reopen changed identity or replayed", fresh.Error())
	}
	captureConsulShellGrid(t, tt, "reopened")
	// Close the reopened original grid while its retained HTTP sibling is active.
	s.selectTab(other.id)
	tt.Frame()
	s.closeTabID(grid.id)
	tt.Frame()
	if s.currentTab() != other || other.kv.Editor == nil || fresh.Page() != nil {
		t.Fatal("inactive original close damaged active HTTP document")
	}
}
