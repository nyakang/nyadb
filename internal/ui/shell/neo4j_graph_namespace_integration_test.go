package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphimport"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/neo4j"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
)

func TestNeo4jNativePublicGraphNamespaceEmptyDatabaseEntryImportExchangeTabsAndOriginalGeneration(t *testing.T) {
	if os.Getenv("NYADB_NEO4J_PUBLIC_IT") != "1" {
		t.Skip("requires fresh owned graph namespace fixture")
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
	run := func(query string, params map[string]any) contract.GraphResult {
		t.Helper()
		p, e := admin.Prepare(neo4j.Request{Query: query, Parameters: params})
		if e != nil {
			t.Fatal(e)
		}
		r, e := admin.Execute(context.Background(), p)
		if e != nil || !r.Completed {
			t.Fatal(e)
		}
		return r
	}
	if run("MATCH (n) RETURN count(n)", nil).Rows[0][0].Text != "0" {
		t.Fatal("namespace UI acceptance needs its original empty owned database")
	}
	u, _ := url.Parse(f.Config.URI)
	u.Path = "/neo4j"
	if f.Config.Auth == "basic" {
		u.User = url.User(f.Config.Username)
	}
	q := url.Values{"auth": {f.Config.Auth}}
	for k, v := range map[string]string{"ca_file": f.Config.CAFile, "client_cert_file": f.Config.ClientCertFile, "client_key_file": f.Config.ClientKeyFile} {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	parsed, e := connections.ParseURI(u.String())
	if e != nil {
		t.Fatal("invalid fixture URI")
	}
	parsed.Profile.Name = "Empty graph namespace"
	s, _, profiles, _ := profileTestState(t)
	defer s.CloseMySQLSessions()
	profile, e := profiles.Create(context.Background(), parsed.Profile)
	if e != nil {
		t.Fatal(e)
	}
	s.loadProfiles()
	s.selectProfileByName(profile.Name)
	if e = s.runtime.Connect(context.Background(), profile, f.Secret); e != nil {
		t.Fatal(e)
	}
	w := s.sessions.Current()
	w.ProfileID = profile.ID
	w.Kind = connections.Neo4j
	w.Status = "已连接"
	w.Generation = s.runtime.Generation(profile.ID)
	w.Databases, e = runtimeNamespaceNames(context.Background(), s, profile.ID)
	if e != nil {
		t.Fatal(e)
	}
	w.Namespaces = map[string]contract.NamespaceRef{"neo4j": s.runtime.Namespace(profile.ID, "neo4j"), "system": s.runtime.Namespace(profile.ID, "system")}
	s.navigation.Tree.Open.Add(navigation.ConnectionKey(profile.ID))
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
			t.Fatal("original namespace UI did not settle")
		}
		tt.Frame()
	}
	click := func(label string) {
		t.Helper()
		if e := tt.Click(locales.Text(label)); e != nil {
			t.Fatal(label, e)
		}
		tt.Frame()
	}
	input := func(label, value string) {
		t.Helper()
		click(label)
		tt.Command("selectAll")
		tt.Type(value)
		tt.Frame()
	}
	capture := func(stage string, labels ...string) {
		t.Helper()
		folder := os.Getenv("NYADB_NEO4J_NAMESPACE_IMAGES")
		if folder == "" {
			return
		}
		if e := os.MkdirAll(folder, 0700); e != nil {
			t.Fatal(e)
		}
		defer i18n.SetLocaleFunc(nil)
		for _, lang := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return lang })
			for _, size := range [][2]int{{1280, 800}, {720, 520}} {
				picture := ui.NewTester(s.View, size[0], size[1])
				picture.Frame()
				for _, label := range labels {
					r, ok := picture.Find(locales.Text(label))
					if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
						t.Fatal("namespace control missing or clipped", stage, label, lang, size, r)
					}
				}
				p, e := os.Create(filepath.Join(folder, fmt.Sprintf("%s-%dx%d-%s.png", lang, size[0], size[1], stage)))
				if e != nil {
					t.Fatal(e)
				}
				e = png.Encode(p, picture.Image())
				p.Close()
				if e != nil {
					t.Fatal(e)
				}
			}
		}
	}
	if e = tt.RightClick("数据库：neo4j"); e != nil {
		t.Fatal(e)
	}
	if !slices.Contains(tt.Menu(), locales.Text("完整图交换")) {
		t.Fatal("empty database menu lacks graph entry", tt.Menu())
	}
	if e = tt.ChooseMenuItem(locales.Text("完整图交换")); e != nil {
		t.Fatal(e)
	}
	tt.Frame()
	tab := s.currentTab()
	d := tab.data
	if d == nil || !d.GraphExchange() || d.Object() != w.Namespaces["neo4j"].Object(contract.GraphNamespaceKind, "") || d.Page() != nil || d.Loading() || d.Table() != "" {
		t.Fatal("empty database entry fabricated a label or grid")
	}
	if tt.HasText(locales.Text("批量编辑")) || tt.HasText(locales.Text("没有已加载的数据页")) {
		t.Fatal("namespace entry showed an empty table")
	}
	capture("namespace-entry", "导入向导…", "导出向导…")
	if s.canOpenGraphExchange(profile.ID, w.Generation, "system") {
		t.Fatal("system database admitted graph transfer")
	}
	countTabs := len(s.tabs)
	s.openGraphExchange(services, profile.ID, "system", "")
	if len(s.tabs) != countTabs {
		t.Fatal("system graph transfer created tab")
	}
	s.openGraphExchange(services, profile.ID, "neo4j", "")
	if len(s.tabs) != countTabs || s.currentTab() != tab {
		t.Fatal("namespace entry duplicated original document")
	}
	export := func(path, format, stage string) contract.GraphImportBundle {
		t.Helper()
		click("导出向导…")
		if format == "JSONL" {
			click("文件格式")
			if e := tt.Click("JSONL"); e != nil {
				t.Fatal(e)
			}
			tt.Frame()
		}
		input("目标文件路径", path)
		if stage != "" {
			capture(stage, "目标文件路径", "取消", "导出完整数据库图")
		}
		click("导出完整数据库图")
		wait(func() bool { return !d.Busy() })
		file, e := os.Open(path)
		if e != nil {
			t.Fatal(e)
		}
		b, e := graphimport.Parse(file, format)
		file.Close()
		if e != nil {
			t.Fatal("namespace exported invalid exchange", e)
		}
		click("关闭")
		return b
	}
	for _, format := range []string{"JSON", "JSONL"} {
		stage := ""
		if format == "JSON" {
			stage = "namespace-empty-export"
		}
		b := export(filepath.Join(t.TempDir(), "empty.graph"), format, stage)
		if len(b.Nodes) != 0 || len(b.Relationships) != 0 || b.Source != d.Object().Namespace() {
			t.Fatal("empty database exchange fabricated graph")
		}
	}
	props := run("RETURN {exact:9223372036854775807,bytes:$bytes,days:[date({year:10000,month:2,day:29})],point:point({srid:4326,x:181,y:-90}),point_list:[point({x:1,y:2})],empty:'',flag:false}", map[string]any{"bytes": []byte{0, 255}}).Rows[0][0].Properties
	label := "NyaNamespacePublic_" + f.Nonce
	bundle := contract.GraphImportBundle{Format: graphimport.Format, Nodes: []contract.GraphNode{{ElementID: "first", Labels: []string{label}, Properties: props}, {ElementID: "second", Properties: props}}, Relationships: []contract.GraphRelationship{{ElementID: "forward", StartElementID: "first", EndElementID: "second", Type: label, Properties: props}, {ElementID: "self", StartElementID: "second", EndElementID: "second", Type: label, Properties: props}}}
	source := filepath.Join(t.TempDir(), "source.graph.json")
	file, e := os.Create(source)
	if e != nil {
		t.Fatal(e)
	}
	e = graphimport.Write(file, bundle, "JSON")
	file.Close()
	if e != nil {
		t.Fatal(e)
	}
	s.openDatabaseImport(profile.ID, "neo4j")
	tt.Frame()
	if s.currentTab() != tab || !tt.HasText(locales.Text("选择图交换文件")) && !tt.HasText(locales.Text("读取图交换文件")) {
		t.Fatal("database import did not reuse namespace wizard")
	}
	input("源文件路径", source)
	click("读取图交换文件")
	wait(func() bool { return !d.Busy() })
	capture("namespace-import-source", "源文件路径", "读取图交换文件", "下一步")
	click("下一步")
	click("预览图导入")
	wait(func() bool { return !d.Busy() })
	capture("namespace-import-preview", "输入目标数据库名以确认", "复制完整预览", "确认导入完整图", "取消")
	if run("MATCH (n) RETURN count(n)", nil).Rows[0][0].Text != "0" {
		t.Fatal("empty database wizard preview wrote graph")
	}
	input("输入目标数据库名以确认", "neo4j")
	click("确认导入完整图")
	wait(func() bool { return !d.Busy() })
	if d.Page() != nil || d.Loading() || !tt.HasText(locales.Text("原生事务回执")) {
		t.Fatal("namespace completion fabricated grid or lost receipt")
	}
	generated := run("MATCH (n) RETURN n", nil)
	if len(generated.Rows) != 2 {
		t.Fatal("namespace import did not commit both nodes")
	}
	ids := []string{}
	for _, row := range generated.Rows {
		ids = append(ids, row[0].Node.ElementID)
	}
	defer run("MATCH (n) WHERE elementId(n) IN $ids DETACH DELETE n", map[string]any{"ids": ids})
	if run("MATCH ()-[r]->() RETURN count(r)", nil).Rows[0][0].Text != "2" {
		t.Fatal("namespace import lost relationships")
	}
	click("关闭")
	click("原生事务回执")
	capture("namespace-import-receipt", "复制完整回执", "导出完整回执", "关闭")
	click("关闭")
	exported := export(filepath.Join(t.TempDir(), "complete.graph.jsonl"), "JSONL", "namespace-export")
	if len(exported.Nodes) != 2 || len(exported.Relationships) != 2 || len(graphimport.ExternalEndpoints(exported)) != 0 {
		t.Fatal("namespace complete exchange lost graph")
	}
	for _, n := range exported.Nodes {
		if !reflect.DeepEqual(n.Properties, props) {
			t.Fatal("namespace exchange lost exact native types")
		}
	}
	for _, r := range exported.Relationships {
		if !reflect.DeepEqual(r.Properties, props) {
			t.Fatal("namespace exchange lost exact relationship types")
		}
	}
	s.openMySQLDatabase(services, "neo4j", true)
	wait(func() bool { return w.Pending == "" && w.Cache["neo4j"] != nil })
	s.openQualifiedData(services, w.Generation, "neo4j", w.Namespaces["neo4j"].Object("label", label))
	nodes := s.currentTab()
	wait(func() bool { return nodes.data != nil && !nodes.data.Busy() })
	s.openQualifiedData(services, w.Generation, "neo4j", w.Namespaces["neo4j"].Object("relationship", label))
	edges := s.currentTab()
	wait(func() bool { return edges.data != nil && !edges.data.Busy() })
	if nodes == edges || nodes == tab || edges == tab || nodes.data.Object().Kind != "label" || edges.data.Object().Kind != "relationship" {
		t.Fatal("namespace identity collided with same-name graph tabs")
	}
	s.selectTab(tab.id)
	snapshot := s.workspaceSnapshot()
	s.switchWorkspace(snapshot)
	wait(func() bool {
		i := s.tabIndex(tab.id)
		return i >= 0 && s.tabs[i].placeholder != nil
	})
	restored := s.tabs[s.tabIndex(tab.id)]
	if restored.placeholder == nil || restored.data != nil {
		t.Fatal("workspace restoration replayed graph transfer")
	}
	s.materializeRestored(services, restored)
	d = restored.data
	tab = restored
	if d == nil || !d.GraphExchange() || d.Page() != nil || d.Busy() {
		t.Fatal("explicit namespace reopening restored requests or grid")
	}
	tt.Frame()
	capture("namespace-restored", "导入向导…", "导出向导…")
	replacement, e := s.runtime.ConnectSession(context.Background(), profile, f.Secret)
	if e != nil {
		t.Fatal(e)
	}
	w = s.sessions.Current()
	w.Generation = replacement.Generation()
	tt.Frame()
	capture("namespace-stale", "原数据库会话已失效，请关闭文档后重新打开")
	s.openGraphExchange(services, profile.ID, "neo4j", "import")
	tt.Frame()
	if d.Busy() || tt.HasText(locales.Text("读取图交换文件")) {
		t.Fatal("replacement reopened original namespace request")
	}
	s.closeTabID(tab.id)
	if !s.runtime.IsConnected(profile.ID) || s.runtime.Generation(profile.ID) != replacement.Generation() {
		t.Fatal("original namespace close disconnected replacement")
	}
}
