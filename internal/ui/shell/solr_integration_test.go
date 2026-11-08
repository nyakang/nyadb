package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/solr"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
)

func TestSolrNativePublicChooserURIGridRESTAndRecovery(t *testing.T) {
	if os.Getenv("NYADB_SOLR_WORKBENCH_IT") != "1" {
		t.Skip("requires owned disposable Solr")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_SOLR_CONFIG"))
	if err != nil {
		t.Fatal("private probe unavailable")
	}
	var cfg struct {
		Endpoint, Username, Auth string
		CAFile                   string `json:"ca_file"`
		ServerName               string `json:"server_name"`
		PasswordFile             string `json:"password_file"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		t.Fatal("invalid private probe")
	}
	secret, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		t.Fatal("private credential unavailable")
	}
	ctx, stop := context.WithTimeout(context.Background(), 600*time.Second)
	defer stop()
	admin, err := solr.Open(ctx, connections.SolrConfig{Endpoint: cfg.Endpoint, Username: cfg.Username, Auth: cfg.Auth, CAFile: cfg.CAFile, ServerName: cfg.ServerName}, string(secret))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	index := "nyadb_shell_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	alias := index + "_alias"
	cloud := admin.Identity().Mode == "solrcloud"
	kind, caption := "core", "Core"
	create, remove := "/admin/cores?action=CREATE&configSet=nyadb_edit&name=", "/admin/cores?action=UNLOAD&deleteInstanceDir=true&core="
	if cloud {
		kind, caption = "collection", "集合"
		create, remove = "/admin/collections?action=CREATE&numShards=2&replicationFactor=1&collection.configName=nyadb_edit&name=", "/admin/collections?action=DELETE&name="
	}
	run := func(method, path, body string) {
		t.Helper()
		if _, e := admin.Do(ctx, solr.Request{Method: method, Path: path, Body: []byte(body)}); e != nil {
			t.Fatal(e)
		}
	}
	run("GET", create+index+"&wt=json", "")
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if cloud {
			if _, e := admin.Do(cleanup, solr.Request{Method: "GET", Path: "/admin/collections?action=DELETEALIAS&name=" + alias + "&wt=json"}); e != nil {
				t.Error("owned alias cleanup", e)
			}
		}
		if _, e := admin.Do(cleanup, solr.Request{Method: "GET", Path: remove + index + "&wt=json"}); e != nil {
			t.Error("owned target cleanup", e)
		}
	}()
	if cloud {
		run("GET", "/admin/collections?action=CREATEALIAS&name="+alias+"&collections="+index+"&wt=json", "")
	}
	run("POST", "/"+index+"/update?commit=true&wt=json", `[{"id":"original","text_s":"原始文档🐱","exact_l":9007199254740993}]`)
	s, _, profiles, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	wait := func(done func() bool) {
		t.Helper()
		end := time.Now().Add(120 * time.Second)
		for !done() && time.Now().Before(end) {
			tt.Frame()
			time.Sleep(2 * time.Millisecond)
		}
		if !done() {
			t.Fatal("Solr UI operation did not settle")
		}
		tt.Frame()
	}
	s.openConnectionDialog()
	tt.Frame()
	// Search the real chooser, then open the injected editor.
	if err = tt.Click("搜索连接类型"); err != nil {
		t.Fatal(err)
	}
	tt.Type("Solr")
	tt.Frame()
	if err = tt.Click("选择 Apache Solr"); err != nil {
		t.Fatal(err)
	}
	if err = tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionEditor.Draft().Kind != connections.Solr || s.ConnectionWindowTitle() != "新建连接 (Apache Solr)" {
		t.Fatal("chooser did not open native Solr form")
	}
	endpoint, _ := url.Parse(cfg.Endpoint)
	u := url.URL{Scheme: "solr", Host: endpoint.Host, Path: endpoint.Path}
	opts := url.Values{"auth": {cfg.Auth}, "sslmode": {"verify-full"}}
	if endpoint.Scheme == "http" {
		opts.Set("sslmode", "disable")
	}
	if cfg.ServerName != "" {
		opts.Set("server_name", cfg.ServerName)
	}
	if cfg.CAFile != "" {
		opts.Set("ca_file", cfg.CAFile)
	}
	if cfg.Auth != "none" {
		u.User = url.UserPassword(cfg.Username, string(secret))
	}
	u.RawQuery = opts.Encode()
	// The public editor validates the actual URI and retains its secret separately.
	if err = s.connectionEditor.ImportURI(u.String()); err != nil {
		t.Fatal("public Solr URI import failed")
	}
	s.connectionStep = 1
	s.updateConnectionWindowTitle()
	tt.Frame()
	if cfg.Auth != "none" {
		if err = tt.Click("保存凭据"); err != nil {
			t.Fatal(err)
		}
	}
	if err = tt.Click("保存连接"); err != nil {
		t.Fatal(err)
	}
	list, err := profiles.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatal("saved public profile missing", err)
	}
	p := list[0]
	if p.Kind != connections.Solr || s.runtime.IsConnected(p.ID) {
		t.Fatal("save connected or changed engine")
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	tt.Frame()
	if err = tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	w := s.sessions.Current()
	if w.Status != "已连接" {
		t.Fatal(w.Error)
	}
	cluster := "Solr · " + admin.Identity().Mode
	ns := s.runtime.Namespace(p.ID, cluster)
	if ns.ProfileID != p.ID || !strings.HasPrefix(ns.Catalog, "solr-observation:") || ns.Schema != "" {
		t.Fatal("native Solr namespace lost observation identity")
	}
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, cluster))
	wait(func() bool { return w.Pending == "" && w.Cache[cluster] != nil })
	for _, expected := range []struct{ name, caption, kind string }{{index, caption, kind}} {
		found := false
		for _, o := range w.Cache[cluster] {
			if o.Name == expected.name && o.Kind == expected.caption && o.Ref == s.runtime.Namespace(p.ID, cluster).Object(expected.kind, expected.name) {
				found = true
			}
		}
		if !found {
			t.Fatal("qualified native target missing", expected.kind)
		}
	}
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: cluster, Category: caption, Object: index})
	data := s.currentTab()
	if data.data == nil || data.data.Object().Kind != kind || data.data.Kind() != caption {
		t.Fatal("index did not route to shared grid")
	}
	wait(func() bool { return !data.data.Busy() })
	if data.data.Page() == nil || data.data.Error() != "" {
		t.Fatal(data.data.Error())
	}
	wait(func() bool {
		return data.data.Inspector() != nil && data.data.Inspector().Info() != nil || tt.HasText("表信息读取失败")
	})
	if data.data.Inspector().Info() == nil || tt.HasText("表信息读取失败") || data.data.Inspector().Info().Database != cluster {
		t.Fatal("public native information panel did not retain original label")
	}
	source := strings.Replace(data.data.Page().Rows[0][4].Value.Text, "9007199254740993", "9007199254740995", 1)
	source = strings.Replace(source, "原始文档", "网格已保存", 1)
	s.dataWorkspace.EditCell(services, data.data, 0, "document", contract.Cell{Kind: contract.JSON, Text: source})
	tt.Frame()
	if !data.data.HasDraft() {
		t.Fatal("public document edit disabled")
	}
	if err = tt.Click("保存数据更改"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !data.data.Busy() })
	if data.data.HasDraft() || data.data.Error() != "" || !strings.Contains(data.data.Page().Rows[0][4].Text, "9007199254740995") {
		t.Fatal("grid save did not refresh original target", data.data.Error())
	}
	if cloud {
		s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: cluster, Category: "别名", Object: alias})
		aliasTab := s.currentTab()
		if aliasTab.data == nil {
			t.Fatal("alias did not open shared grid")
		}
		wait(func() bool { return !aliasTab.data.Busy() })
		if aliasTab.data.Error() != "" || aliasTab.data.Object().Kind != "alias" || len(aliasTab.data.Page().Rows) != 1 {
			t.Fatal("qualified alias grid", aliasTab.data.Error())
		}
	}
	s.openMySQLStructure(services, cluster, index)
	mapping := s.currentTab()
	wait(func() bool { return !mapping.object.Loading() })
	if mapping.object.Error() != "" || mapping.object.Data() == nil || len(mapping.object.Data().Definitions) < 2 {
		t.Fatal("full native schema/config", mapping.object.Error())
	}
	if !tt.HasText("Schema（只读）") || tt.HasText("外键") {
		t.Fatal("mapping inherited relational structure")
	}
	s.openQuery()
	query := s.currentTab()
	tt.Frame()
	request := "GET /" + index + "/get?id=original&wt=json"
	query.query.Editor.Buffer.Set(request)
	tt.Frame()
	if err = tt.Click("运行 Solr REST 请求"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, query)
	result := query.query.Result()
	if result == nil || len(result.Rows) != 1 || result.Rows[0][0] != "200" || !strings.Contains(result.Rows[0][1], "9007199254740995") {
		t.Fatal("REST request/full response", query.query.Error())
	}
	// Headless captures show real server data; locale/size changes do not execute.
	if output := os.Getenv("NYADB_SOLR_WORKBENCH_IMAGES"); output != "" {
		defer i18n.SetLocaleFunc(nil)
		for _, language := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return language })
			for _, size := range [][2]int{{1280, 800}, {720, 520}} {
				folder := filepath.Join(output, language)
				if err = os.MkdirAll(folder, 0700); err != nil {
					t.Fatal(err)
				}
				for _, view := range []struct {
					name string
					tab  *tab
				}{{"grid", data}, {"schema", mapping}, {"rest", query}} {
					for i, candidate := range s.tabs {
						if candidate.id == view.tab.id {
							s.activeTab = i
							break
						}
					}
					capture := ui.NewTester(s.View, size[0], size[1])
					if capture.HasText(locales.Text("SQL NULL / 未设置")) {
						t.Fatal("native inspector fabricated SQL defaults")
					}
					writeDomainCapture(t, folder, fmt.Sprintf("%s-%dx%d", view.name, size[0], size[1]), capture)
				}
			}
		}
		i18n.SetLocaleFunc(nil)
	}
	request = "POST /" + index + "/update?wt=json\n[{\"id\":\"never-replay\",\"text_s\":\"must remain unexecuted\"}]"
	query.query.Editor.Buffer.Set(request)
	tt.Frame()
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if !query.query.NeedsRecovery() || query.query.Editor.Buffer.String() != request || data.data.Page() != nil {
		t.Fatal("disconnect lost REST text or retained scoped data")
	}
	if err = tt.Click(locales.Text("恢复原连接")); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	if err = tt.Click(locales.Text("建立新会话（不执行现有请求）")); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !query.query.NeedsRecovery() })
	if query.query.NeedsRecovery() || query.query.Editor.Buffer.String() != request || query.query.Loading() {
		t.Fatal("explicit REST recovery replayed or discarded request")
	}
	probe, probeErr := admin.Do(ctx, solr.Request{Method: "GET", Path: "/" + index + "/get?id=never-replay&wt=json"})
	if probeErr != nil || !strings.Contains(string(probe.Body), `"doc":null`) {
		t.Fatal("recovery executed retained write request")
	}
}
