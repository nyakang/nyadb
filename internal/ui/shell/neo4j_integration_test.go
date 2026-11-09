package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/neo4j"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
	"github.com/nyakang/nyadb/locales"
)

func TestNeo4jNativePublicChooserReopenNavigationCypherAndLocalizedResults(t *testing.T) {
	if os.Getenv("NYADB_NEO4J_PUBLIC_IT") != "1" {
		t.Skip("requires owned Neo4j workbench fixture")
	}
	raw, e := os.ReadFile(os.Getenv("NYADB_NEO4J_CONFIG_FILE"))
	if e != nil {
		t.Fatal("cannot read private fixture")
	}
	var f struct {
		Config                 connections.Neo4jConfig
		Secret, Nonce, Version string
	}
	if json.Unmarshal(raw, &f) != nil || len(f.Nonce) != 32 || f.Version != "5.26.21" {
		t.Fatal("invalid fixture")
	}
	admin, e := neo4j.Open(context.Background(), f.Config, f.Secret)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	run := func(query string) contract.GraphResult {
		t.Helper()
		p, e := admin.Prepare(neo4j.Request{Query: query})
		if e != nil {
			t.Fatal(e)
		}
		r, e := admin.Execute(context.Background(), p)
		if e != nil || !r.Completed {
			t.Fatal(e)
		}
		return r
	}
	label, relationship := "NyaPublic_"+f.Nonce, "NyaPublic_"+f.Nonce
	defer run("MATCH (n:`" + label + "`) DETACH DELETE n")
	run("CREATE (a:`" + label + "` {name:'原始;节点🐱',exact:9223372036854775807})-[r:`" + relationship + "` {weight:1.5}]->(b:`" + label + "` {name:'end'})")
	u, _ := url.Parse(f.Config.URI)
	u.Path = "/" + f.Config.Database
	if f.Config.Auth == "basic" {
		u.User = url.UserPassword(f.Config.Username, f.Secret)
	}
	q := url.Values{"auth": {f.Config.Auth}}
	for k, v := range map[string]string{"ca_file": f.Config.CAFile, "client_cert_file": f.Config.ClientCertFile, "client_key_file": f.Config.ClientKeyFile} {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	s, _, profiles, path := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	wait := func(done func() bool) {
		t.Helper()
		end := time.Now().Add(60 * time.Second)
		for !done() && time.Now().Before(end) {
			tt.Frame()
			time.Sleep(2 * time.Millisecond)
		}
		if !done() {
			t.Fatal("public Neo4j UI did not settle")
		}
		tt.Frame()
	}
	s.openConnectionDialog()
	tt.Frame()
	if e = tt.Click("搜索连接类型"); e != nil {
		t.Fatal(e)
	}
	tt.Type("Neo4j")
	tt.Frame()
	if e = tt.Click("选择 Neo4j"); e != nil {
		t.Fatal(e)
	}
	if e = tt.Click("下一步"); e != nil {
		t.Fatal(e)
	}
	if s.ConnectionWindowTitle() != "新建连接 (Neo4j)" {
		t.Fatal("chooser lost product")
	}
	if e = s.connectionEditor.ImportURI(u.String()); e != nil {
		t.Fatal("URI import failed")
	}
	s.connectionStep = 1
	s.updateConnectionWindowTitle()
	tt.Frame()
	if f.Config.Auth == "basic" {
		if e = tt.Click("保存密码"); e != nil {
			t.Fatal(e)
		}
	}
	if e = tt.Click("保存连接"); e != nil {
		t.Fatal(e)
	}
	list, e := profiles.List(context.Background())
	if e != nil || len(list) != 1 {
		t.Fatal("native profile not saved", e)
	}
	p := list[0]
	if s.runtime.IsConnected(p.ID) || p.Kind != connections.Neo4j {
		t.Fatal("save connected or changed engine")
	}
	s.CloseMySQLSessions()
	db, e := localdb.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s = NewWithProfiles(connections.NewService(localdb.NewProfileStore(db)))
	s.SetCredentialVault(vault)
	defer s.CloseMySQLSessions()
	tt = ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	s.selectProfileByName(p.Name)
	tt.Frame()
	if e = tt.Click("连接所选数据库"); e != nil {
		t.Fatal(e)
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	w := s.sessions.Current()
	if w.Status != "已连接" {
		t.Fatal(w.Error)
	}
	if w.Namespaces["neo4j"] != (contract.NamespaceRef{ProfileID: p.ID, Catalog: "neo4j"}) {
		t.Fatal("public database identity lost")
	}
	node := navigation.DatabaseNode(p.ID, "neo4j")
	s.submitNavigation(services, node)
	wait(func() bool { return w.Pending == "" && w.Cache["neo4j"] != nil })
	categories := s.navigation.Children(node.Key())
	expected := []string{"图标签", "关系类型", "图索引", "图约束", "图过程", "图函数", "查询"}
	if len(categories) != len(expected) {
		t.Fatal("SQL categories in graph database")
	}
	for i, k := range expected {
		if navigation.Parse(categories[i]).Category != k {
			t.Fatal("native category lost")
		}
	}
	s.openMySQLProgram("neo4j", label, "图标签")
	definition := s.currentTab()
	if definition.object == nil || definition.data != nil || definition.object.Object().Kind != "label" {
		t.Fatal("graph metadata became a table")
	}
	s.openQuery()
	query := s.currentTab()
	if query.query == nil || !strings.HasPrefix(query.title, "Cypher ") {
		t.Fatal("native query entry lost")
	}
	request := "MATCH p=(a:`" + label + "`)-[r:`" + relationship + "`]->(b) RETURN a,r,p,9223372036854775807 AS exact,date('2026-11-09') AS day,point({x:1,y:2}) AS position"
	query.query.Editor.Buffer.Set(request)
	tt.Frame()
	if e = tt.Click("Cypher 编辑器"); e != nil {
		t.Fatal(e)
	}
	query.query.Editor.Select(25, 25)
	tt.Key(ui.Ctrl, ui.KeyEnter)
	wait(func() bool { return !query.query.Loading() })
	result := query.query.Result()
	if query.query.Error() != "" || result == nil || result.Graph == nil || !result.Graph.Completed || len(result.Graph.Rows) != 1 || result.Graph.Rows[0][0].Kind != "node" || result.Graph.Rows[0][1].Kind != "relationship" || result.Graph.Rows[0][2].Kind != "path" || result.Rows[0][3] != "9223372036854775807" {
		t.Fatal("native graph result narrowed", query.query.Error())
	}
	if tt.HasText("格式化 SQL") || tt.HasText("EXPLAIN") || tt.HasText("新建表") {
		t.Fatal("SQL controls in Cypher workbench")
	}
	if output := os.Getenv("NYADB_NEO4J_PUBLIC_IMAGES"); output != "" {
		defer i18n.SetLocaleFunc(nil)
		for _, language := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return language })
			folder := filepath.Join(output, language)
			if e = os.MkdirAll(folder, 0700); e != nil {
				t.Fatal(e)
			}
			for _, size := range [][2]int{{1280, 800}, {720, 520}} {
				for _, panel := range []string{"metadata", "cypher", "native", "parameters"} {
					target := query
					if panel == "metadata" {
						target = definition
					}
					for i, candidate := range s.tabs {
						if candidate.id == target.id {
							s.activeTab = i
						}
					}
					capture := ui.NewTester(s.View, size[0], size[1])
					if panel == "native" {
						if e = capture.Click(locales.Text("原生图结果")); e != nil {
							t.Fatal(e)
						}
					}
					if panel == "parameters" {
						capture.Click(locales.Text("原生图结果"))
						if e = capture.Click(locales.Text("Cypher 参数")); e != nil {
							t.Fatal(e)
						}
					}
					writeDomainCapture(t, folder, fmt.Sprintf("%s-%dx%d", panel, size[0], size[1]), capture)
					if panel == "parameters" {
						capture.Click(locales.Text("Cypher 参数"))
					}
				}
			}
		}
		i18n.SetLocaleFunc(nil)
	}
	var graphDataTabs []*tab
	if os.Getenv("NYADB_NEO4J_GRID_IT") == "1" {
		for _, item := range []struct{ kind, refKind string }{{"图标签", "label"}, {"关系类型", "relationship"}} {
			s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: "neo4j", Category: item.kind, Object: label})
			dataTab := s.currentTab()
			if dataTab.data == nil || dataTab.data.Object() != w.Namespaces["neo4j"].Object(item.refKind, label) {
				t.Fatal("native graph navigation lost same-name kind")
			}
			wait(func() bool { return !dataTab.data.Loading() })
			if dataTab.data.Error() != "" || dataTab.data.Page() == nil {
				t.Fatal("graph navigation grid failed", dataTab.data.Error())
			}
			graphDataTabs = append(graphDataTabs, dataTab)
			count := len(s.tabs)
			s.openMySQLObject(services, "neo4j", label, item.kind)
			if s.currentTab() != dataTab || len(s.tabs) != count {
				t.Fatal("qualified graph opener duplicated document")
			}
			for _, object := range w.Cache["neo4j"] {
				if object.Ref == dataTab.data.Object() {
					s.openSearchObject(services, p.ID, w.Generation, objectworkspace.ObjectSearchEntry{Namespace: contract.Namespace{Ref: object.Ref.Namespace(), Label: "neo4j"}, Object: object})
					if s.currentTab() != dataTab || len(s.tabs) != count {
						t.Fatal("graph search conflated definition or same-name kind")
					}
				}
			}
			if output := os.Getenv("NYADB_NEO4J_SHELL_GRID_IMAGES"); output != "" {
				for _, language := range []string{"zh-Hans", "en"} {
					i18n.SetLocaleFunc(func() string { return language })
					folder := filepath.Join(output, language)
					if e = os.MkdirAll(folder, 0700); e != nil {
						t.Fatal(e)
					}
					for _, size := range [][2]int{{1280, 800}, {720, 520}} {
						capture := ui.NewTester(s.View, size[0], size[1])
						if !capture.HasText(locales.Text("原生游标读取实时数据；翻页和重读期间数据变化可能导致行重复或遗漏。")) {
							t.Fatal("live shared graph grid not shown")
						}
						writeDomainCapture(t, folder, fmt.Sprintf("%s-%dx%d", item.refKind, size[0], size[1]), capture)
					}
				}
				i18n.SetLocaleFunc(nil)
			}
		}
		if graphDataTabs[0].id == graphDataTabs[1].id || graphDataTabs[0].identity() == graphDataTabs[1].identity() {
			t.Fatal("same-name graph types shared a tab")
		}
	}
	for i, candidate := range s.tabs {
		if candidate.id == query.id {
			s.activeTab = i
		}
	}
	// This retained mutation must never run during reconnect or session recovery.
	retained := "CREATE (n:`" + label + "` {name:'never-replay'})"
	query.query.Editor.Buffer.Set(retained)
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	for _, dataTab := range graphDataTabs {
		if dataTab.data.Page() != nil || dataTab.data.Inspector() != nil {
			t.Fatal("disconnect retained original graph caches")
		}
		for _, live := range s.tabs {
			if live == dataTab {
				t.Fatal("disconnect retained scoped graph tab")
			}
		}
	}
	if !query.query.NeedsRecovery() || query.query.Editor.Buffer.String() != retained {
		t.Fatal("disconnect discarded source")
	}
	if e = tt.Click(locales.Text("恢复原连接")); e != nil {
		t.Fatal(e)
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	if e = tt.Click(locales.Text("建立新物理会话（不执行现有 Cypher）")); e != nil {
		t.Fatal(e)
	}
	wait(func() bool { return !query.query.NeedsRecovery() })
	if query.query.Loading() || run("MATCH (n:`" + label + "` {name:'never-replay'}) RETURN count(n)").Rows[0][0].Text != "0" {
		t.Fatal("public recovery replayed mutation")
	}
	snapshot := s.workspaceSnapshot()
	s.CloseMySQLSessions()
	restored := NewWithProfiles(connections.NewService(localdb.NewProfileStore(db)))
	defer restored.CloseMySQLSessions()
	restored.restoreWorkspace(snapshot, false)
	var recovered *tab
	for _, candidate := range restored.tabs {
		if candidate.id == query.id {
			recovered = candidate
		}
	}
	if recovered == nil || recovered.query == nil || !recovered.query.NeedsRecovery() || recovered.query.Editor.Buffer.Language().Name != "Cypher" || recovered.query.Editor.Buffer.String() != retained || restored.runtime.IsConnected(p.ID) {
		t.Fatal("workspace restoration connected or lost Cypher")
	}
}
