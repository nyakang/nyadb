package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/neo4j"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNeo4jNativePublicSchemaManagementMenusTypedPreviewReceiptsAndLocalizedViews(t *testing.T) {
	if os.Getenv("NYADB_NEO4J_SCHEMA_UI_IT") != "1" {
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
		folder := os.Getenv("NYADB_NEO4J_SCHEMA_IMAGES")
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
				if stage == "schema-draft" && size[0] == 720 {
					picture.Scroll(float32(size[0]/2), float32(size[1]-100), 0, 1000)
					picture.Frame()
				}
				for _, label := range labels {
					r, ok := picture.Find(locales.Text(label))
					if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
						t.Fatal("schema control missing or clipped", stage, label, lang, size, r)
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
	if !slices.Contains(tt.Menu(), locales.Text("图索引与约束管理")) {
		t.Fatal("native database menu missing schema management")
	}
	if e = tt.ChooseMenuItem(locales.Text("图索引与约束管理")); e != nil {
		t.Fatal(e)
	}
	tt.Frame()
	original := s.currentTab()
	d := original.object
	if d == nil || !d.GraphSchemaManagementMode() || d.Loading() || d.Object() != w.Namespaces["neo4j"].Object("graph_schema", "graph_schema") {
		t.Fatal("menu did not retain qualified original namespace")
	}
	countTabs := len(s.tabs)
	generation := w.Generation
	s.openGraphSchemaManagement(services, profile.ID, generation, "neo4j")
	s.openGraphSchemaManagement(services, profile.ID, generation, "system")
	if len(s.tabs) != countTabs {
		t.Fatal("duplicate or unsupported namespace tab")
	}
	click("工具")
	if !slices.Contains(tt.Menu(), locales.Text("图索引与约束管理")) {
		t.Fatal("tools menu missing qualified schema management")
	}
	if e = tt.ChooseMenuItem(locales.Text("图索引与约束管理")); e != nil {
		t.Fatal(e)
	}
	tt.Frame()
	if len(s.tabs) != countTabs || s.currentTab() != original {
		t.Fatal("tools menu duplicated original document")
	}
	capture("schema-entry", "读取完整图结构目录", "预览图结构操作", "核验原图结构操作")
	click("读取完整图结构目录")
	wait(func() bool { return !d.Loading() })
	if d.Error() != "" {
		t.Fatal(d.Error())
	}
	capture("schema-catalog", "完整图结构目录", "复制完整核验证据")
	click("管理草稿")
	name := "nya_" + f.Nonce + "_ui_schema` 猫"
	label := "SchemaUI_" + f.Nonce + ",猫"
	input("图结构名称", name)
	labels, _ := json.Marshal([]string{label})
	input("标签或关系类型 JSON 数组", string(labels))
	input("有序属性 JSON 数组", `["first","second"]`)
	capture("schema-draft", "有序属性 JSON 数组", "索引选项 JSON")
	click("预览图结构操作")
	if !tt.HasText(locales.Text("图结构操作预览")) {
		t.Fatal("missing immutable schema preview")
	}
	capture("schema-preview", "复制完整图结构预览", "返回编辑", "确认提交图结构操作")
	click("确认提交图结构操作")
	wait(func() bool { return !d.Loading() })
	r := s.objectWorkspace.GraphSchemaResult(d)
	if d.Error() != "" || !r.ReceiptValid || r.State != "completed" || r.HasVerification {
		t.Fatal("original CREATE completion lost", d.Error(), r.State)
	}
	quoted := "`" + strings.ReplaceAll(name, "`", "``") + "`"
	defer run("DROP INDEX "+quoted+" IF EXISTS", nil)
	s.objectWorkspace.SubmitGraphSchema(d, nil)
	tt.Frame()
	if s.objectWorkspace.GraphSchemaResult(d).State != "completed" {
		t.Fatal("completion replayed")
	}
	click("核验原图结构操作")
	wait(func() bool { return !d.Loading() })
	r = s.objectWorkspace.GraphSchemaResult(d)
	if r.State != "verified" || !r.ReceiptValid || !r.Matched || len(r.Verification.Indexes.Columns) != 16 {
		t.Fatal("explicit verification missing", r.State, d.Error())
	}
	capture("schema-receipt", "原生完成回执与当前目录已核验", "复制完整核验证据")
	click("管理草稿")
	form := s.objectWorkspace.GraphSchemaForm(d)
	form.Action = "drop_index"
	if !s.objectWorkspace.SetGraphSchemaForm(d, form) {
		t.Fatal("could not create DROP draft")
	}
	tt.Frame()
	click("读取完整图结构目录")
	wait(func() bool { return !d.Loading() })
	click("预览图结构操作")
	if s.objectWorkspace.CanSubmitGraphSchema(d) {
		t.Fatal("DROP confirmation omitted")
	}
	capture("schema-drop-preview", "接受按当前名称删除及同名替换风险", "输入原名称确认操作", "确认提交图结构操作")
	input("输入原名称确认操作", name)
	if s.objectWorkspace.CanSubmitGraphSchema(d) {
		t.Fatal("DROP risk acknowledgement omitted")
	}
	click("接受按当前名称删除及同名替换风险")
	click("确认提交图结构操作")
	wait(func() bool { return !d.Loading() })
	r = s.objectWorkspace.GraphSchemaResult(d)
	if r.State != "completed" || r.Receipt.Summary.Counters["indexes_removed"] != 1 {
		t.Fatal("original DROP receipt absent", d.Error())
	}
	click("核验原图结构操作")
	wait(func() bool { return !d.Loading() })
	r = s.objectWorkspace.GraphSchemaResult(d)
	if r.State != "verified" || !r.Preview.NonAtomicDrop {
		t.Fatal("DROP verification absent")
	}
	capture("schema-drop-verified", "原生完成回执与当前目录已核验")
	// A replaced public session cannot reuse the old original lease or publish metadata.
	click("工具")
	if e = s.runtime.Connect(context.Background(), profile, f.Secret); e != nil {
		t.Fatal(e)
	}
	w.Generation = s.runtime.Generation(profile.ID)
	tt.Frame()
	if e = tt.ChooseMenuItem(locales.Text("图索引与约束管理")); e != nil {
		t.Fatal(e)
	}
	tt.Frame()
	if len(s.tabs) != countTabs {
		t.Fatal("stale tools menu followed replacement")
	}
	s.objectWorkspace.ReadGraphSchema(d, nil)
	if d.Loading() {
		t.Fatal("expired lease automatically recovered")
	}
	if s.objectWorkspace.GraphSchemaResult(d).Receipt.Summary.Counters["indexes_removed"] != 1 {
		t.Fatal("original receipt erased by replacement")
	}
	capture("schema-original-expired", "复制完整核验证据")
	s.objectWorkspace.Close(d)
	s.objectWorkspace.Close(d)
	if s.runtime.Generation(profile.ID) == generation {
		t.Fatal("test missed replacement")
	}
}
