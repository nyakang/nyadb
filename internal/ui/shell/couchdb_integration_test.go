package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/couchdb"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
)

func couchdbPublicFixture(t *testing.T, configName string) connections.URIImport {
	t.Helper()
	if os.Getenv("NYADB_COUCHDB_WORKBENCH_IT") != "1" {
		t.Skip("requires owned native CouchDB workbench fixture")
	}
	raw, err := os.ReadFile(os.Getenv(configName))
	if err != nil {
		t.Fatal("private CouchDB fixture config unavailable")
	}
	var cfg struct {
		Endpoint, Auth, Username string
		CAFile                   string `json:"ca_file"`
		ServerName               string `json:"server_name"`
		PasswordFile             string `json:"password_file"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		t.Fatal("invalid private CouchDB config")
	}
	secret := ""
	if cfg.PasswordFile != "" {
		raw, err = os.ReadFile(cfg.PasswordFile)
		if err != nil {
			t.Fatal("private credential unavailable")
		}
		secret = string(raw)
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil {
		t.Fatal("invalid private endpoint")
	}
	uri := url.URL{Scheme: "couchdb", Host: endpoint.Host, Path: endpoint.Path}
	options := url.Values{"auth": {cfg.Auth}, "sslmode": {"disable"}}
	if endpoint.Scheme == "https" {
		options.Set("sslmode", "verify-full")
	}
	if cfg.CAFile != "" {
		options.Set("ca_file", cfg.CAFile)
	}
	if cfg.ServerName != "" {
		options.Set("server_name", cfg.ServerName)
	}
	if cfg.Auth == "basic" {
		uri.User = url.UserPassword(cfg.Username, secret)
	}
	uri.RawQuery = options.Encode()
	parsed, err := connections.ParseURI(uri.String())
	if err != nil {
		t.Fatal("public CouchDB URI rejected", err)
	}
	parsed.Profile.ID, parsed.Profile.Name = 96, "Native CouchDB workbench"
	return parsed
}

func TestCouchDBNativePublicChooserURINavigationDefinitionsRESTAndRecovery(t *testing.T) {
	parsed := couchdbPublicFixture(t, "NYADB_COUCHDB_ADMIN_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	cfg, _ := connections.CouchDBProfileConfig(parsed.Profile)
	admin, err := couchdb.Open(ctx, cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	dbs := []string{"owned_shell_" + strconv.FormatInt(time.Now().UnixNano(), 36), "owned_shell_" + strconv.FormatInt(time.Now().UnixNano(), 36)}
	source := `{"_id":"_design/shared","views":{"exact":{"map":"function(doc) { emit(doc._id, doc.n); }","reduce":"_sum"}},"n":9007199254740993}`
	run := func(method, path, body string) couchdb.Response {
		t.Helper()
		r, err := admin.Do(ctx, couchdb.Request{Method: method, Path: path, Body: []byte(body)})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for _, name := range dbs {
		run("PUT", "/"+name, "")
		run("PUT", "/"+name+"/_design/shared", source)
		defer func() {
			_, err := admin.Do(context.Background(), couchdb.Request{Method: "DELETE", Path: "/" + name})
			if err != nil {
				t.Error("owned database cleanup", err)
			}
		}()
	}
	run("PUT", "/"+dbs[0]+"/original", `{"_id":"original","n":9007199254740993,"text":"原始;文档🐱"}`)
	rows := make([]map[string]any, 0, 100)
	for i := range 100 {
		rows = append(rows, map[string]any{"_id": fmt.Sprintf("row_%03d", i), "n": json.Number("9007199254740993")})
	}
	body, _ := json.Marshal(map[string]any{"docs": rows})
	run("POST", "/"+dbs[0]+"/_bulk_docs", string(body))
	s, _, profiles, path := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	wait := func(done func() bool) {
		t.Helper()
		end := time.Now().Add(90 * time.Second)
		for !done() && time.Now().Before(end) {
			tt.Frame()
			time.Sleep(2 * time.Millisecond)
		}
		if !done() {
			t.Fatal("native CouchDB UI work did not settle")
		}
		tt.Frame()
	}
	s.openConnectionDialog()
	tt.Frame()
	if err = tt.Click("搜索连接类型"); err != nil {
		t.Fatal(err)
	}
	tt.Type("CouchDB")
	tt.Frame()
	if err = tt.Click("选择 Apache CouchDB"); err != nil {
		t.Fatal(err)
	}
	if err = tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionEditor.Draft().Kind != connections.CouchDB || s.ConnectionWindowTitle() != "新建连接 (Apache CouchDB)" {
		t.Fatal("chooser did not open native form")
	}
	endpoint, _ := url.Parse(cfg.Endpoint)
	uri := url.URL{Scheme: "couchdb", Host: endpoint.Host, Path: endpoint.Path, User: url.UserPassword(cfg.Username, parsed.Password)}
	options := url.Values{"auth": {"basic"}, "sslmode": {parsed.Profile.TLSMode}}
	if cfg.CAFile != "" {
		options.Set("ca_file", cfg.CAFile)
	}
	if cfg.ServerName != "" {
		options.Set("server_name", cfg.ServerName)
	}
	uri.RawQuery = options.Encode()
	if err = s.connectionEditor.ImportURI(uri.String()); err != nil {
		t.Fatal(err)
	}
	s.connectionStep = 1
	s.updateConnectionWindowTitle()
	tt.Frame()
	if err = tt.Click("保存凭据"); err != nil {
		t.Fatal(err)
	}
	if err = tt.Click("保存连接"); err != nil {
		t.Fatal(err)
	}
	list, err := profiles.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatal("public saved profile absent", err)
	}
	p := list[0]
	if p.Kind != connections.CouchDB || s.runtime.IsConnected(p.ID) {
		t.Fatal("save changed product or executed connection")
	}
	// A fresh storage handle and host re-open the saved public profile and vault.
	s.CloseMySQLSessions()
	reopened, err := localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	service := connections.NewService(localdb.NewProfileStore(reopened))
	s = NewWithProfiles(service)
	s.SetCredentialVault(vault)
	defer s.CloseMySQLSessions()
	tt = ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
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
	var definitions, documents []*tab
	for _, database := range dbs {
		s.submitNavigation(services, navigation.DatabaseNode(p.ID, database))
		wait(func() bool { return w.Pending == "" && w.Cache[database] != nil })
		found := false
		for _, o := range w.Cache[database] {
			if o.Name == "_design/shared" && o.Kind == "设计文档" && o.Ref == s.runtime.Namespace(p.ID, database).Object("design_document", "_design/shared") {
				found = true
			}
		}
		if !found {
			t.Fatal("qualified public design document missing")
		}
		categories := s.navigation.Children(navigation.DatabaseNode(p.ID, database).Key())
		if len(categories) != 4 || navigation.Parse(categories[0]).Category != "文档" || navigation.Parse(categories[1]).Category != "数据库信息" || navigation.Parse(categories[2]).Category != "设计文档" {
			t.Fatal("native categories changed", categories)
		}
		s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: database, Category: "设计文档", Object: "_design/shared"})
		view := s.currentTab()
		if view.object == nil || view.data != nil {
			t.Fatal("design document did not open native definition")
		}
		wait(func() bool { return !view.object.Loading() })
		if view.object.Error() != "" || view.object.Data() == nil || !strings.Contains(view.object.Data().Definition, "9007199254740993") || !tt.HasText("CouchDB 原生定义（只读）") || tt.HasText("外键") {
			t.Fatal("native JSON definition lost or routed into SQL schema", view.object.Error())
		}
		definitions = append(definitions, view)
		s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: database, Category: "文档", Object: "_all_docs"})
		data := s.currentTab()
		if data.data == nil || data.data.Object() != s.runtime.Namespace(p.ID, database).Object("documents", "_all_docs") {
			t.Fatal("public documents did not open the qualified shared grid")
		}
		wait(func() bool { return !data.data.Busy() })
		if data.data.Error() != "" || data.data.Page() == nil || data.data.Page().Editable == nil || *data.data.Page().Editable || len(data.data.Page().Columns) != 3 {
			t.Fatal("public readonly native document grid", data.data.Error())
		}
		documents = append(documents, data)
	}
	if definitions[0].id == definitions[1].id || definitions[0].object.Database() == definitions[1].object.Database() {
		t.Fatal("cross-database same-name tabs aliased")
	}
	if documents[0].id == documents[1].id || documents[0].data.Object() == documents[1].data.Object() {
		t.Fatal("same-name native document ranges aliased")
	}
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: dbs[0], Category: "文档", Object: "_all_docs"})
	data := documents[0].data
	if s.currentTab().data != data || !data.Page().HasMore {
		t.Fatal("original public document could not be reactivated")
	}
	first := data.Page().Rows[0][0].Text
	if err = tt.Click("下一页数据"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !data.Busy() })
	if data.Error() != "" || data.Offset() != 100 || data.Page().Rows[0][0].Text == first || documents[1].data.Page().Rows[0][0].Text != "_design/shared" {
		t.Fatal("public cursor paging changed original/sibling identity", data.Error())
	}
	if err = tt.Click("上一页数据"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !data.Busy() })
	if data.Page().Rows[0][0].Text != first || data.Offset() != 0 {
		t.Fatal("public back navigation lost original boundary")
	}
	if err = clickMySQLDataToolbar(tt, "Mango 查询"); err != nil {
		t.Fatal("public Mango editor missing", err)
	}
	if err = tt.Click("Mango selector JSON"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type(`{"_id":{"$in":["row_000","row_001"]},"n":{"$eq":9007199254740993}}`)
	if err = tt.Click("应用 Mango 查询"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !data.Busy() })
	if data.Error() != "" || len(data.Page().Rows) != 2 || data.Page().Rows[0][0].Text != "row_000" || !strings.Contains(data.Page().Rows[0][2].Text, "9007199254740993") || !strings.HasPrefix(data.Page().QuerySQL, "POST /") {
		t.Fatal("public Mango selector did not preserve native query/range", data.Error())
	}
	var exported bytes.Buffer
	s.SetEngineFiles(func(name string) (string, error) { return name, nil }, func(_ string, write func(io.Writer) error) error { return write(&exported) })
	if err = clickMySQLDataToolbar(tt, "导出选项"); err != nil {
		t.Fatal(err)
	}
	if err = tt.ChooseMenuItem("整表导出为 JSONL（忽略当前筛选）"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !data.Busy() })
	lines := bytes.Split(bytes.TrimSpace(exported.Bytes()), []byte("\n"))
	if data.Error() != "" || len(lines) != 102 {
		t.Fatal("public native complete export followed preview/filter range", len(lines), data.Error())
	}
	for _, line := range lines {
		var raw map[string]json.RawMessage
		if json.Unmarshal(line, &raw) != nil || string(raw["n"]) != "9007199254740993" {
			t.Fatal("public export wrapped or rounded native documents")
		}
	}
	caps := s.objectCapabilitiesFor(p.ID)
	if caps.EditableTables || caps.Import || caps.Transactions {
		t.Fatal("public document grid inferred unsupported write capabilities")
	}
	// Return to the original database and use the real REST run control.
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, dbs[0]))
	wait(func() bool { return w.Pending == "" })
	s.openQuery()
	query := s.currentTab()
	tt.Frame()
	if query.query.Editor.Buffer.String() != "GET /" || !strings.HasPrefix(query.title, "REST ") {
		t.Fatal("public native request initial text/title")
	}
	request := "GET /" + dbs[0] + "/original"
	query.query.Editor.Buffer.Set(request)
	tt.Frame()
	if err = tt.Click("运行 CouchDB REST 请求"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, query)
	if query.query.Error() != "" || query.query.Result() == nil || !strings.Contains(query.query.Result().Sets[0].Rows[0][1].Text, "9007199254740993") {
		t.Fatal("native exact REST result", query.query.Error())
	}
	if output := os.Getenv("NYADB_COUCHDB_WORKBENCH_IMAGES"); output != "" {
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
				}{{"definition", definitions[0]}, {"rest", query}, {"documents", documents[0]}} {
					for i, candidate := range s.tabs {
						if candidate.id == view.tab.id {
							s.activeTab = i
							break
						}
					}
					capture := ui.NewTester(s.View, size[0], size[1])
					writeDomainCapture(t, folder, fmt.Sprintf("%s-%dx%d", view.name, size[0], size[1]), capture)
					control := "运行 CouchDB REST 请求"
					if view.name == "definition" {
						control = "刷新原生定义"
					} else if view.name == "documents" {
						control = "应用 Mango 查询"
					}
					rect, ok := capture.Find(locales.Text(control))
					if !ok || rect.W <= 0 || rect.H <= 0 || rect.X < 0 || rect.Y < 0 || rect.X+rect.W > float32(size[0])+1 || rect.Y+rect.H > float32(size[1])+1 {
						t.Fatal("native workbench control not visible", language, view.name, rect)
					}
				}
			}
		}
		i18n.SetLocaleFunc(nil)
	}
	for i, candidate := range s.tabs {
		if candidate.id == query.id {
			s.activeTab = i
			break
		}
	}
	tt.Frame()
	request = "PUT /" + dbs[0] + "/never_replayed\n{\"_id\":\"never_replayed\",\"n\":9}"
	query.query.Editor.Buffer.Set(request)
	tt.Frame()
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if !query.query.NeedsRecovery() || query.query.Editor.Buffer.String() != request {
		t.Fatal("disconnect lost retained REST request")
	}
	if err = tt.Click(locales.Text("恢复原连接")); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	if err = tt.Click(locales.Text("建立新会话（不执行现有请求）")); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !query.query.NeedsRecovery() })
	if query.query.Editor.Buffer.String() != request || query.query.Loading() {
		t.Fatal("recovery discarded or executed retained request")
	}
	response, err := admin.Do(ctx, couchdb.Request{Method: "GET", Path: "/" + dbs[0] + "/never_replayed"})
	if err == nil || response.Status != 404 {
		t.Fatal("explicit recovery replayed retained write")
	}
}
