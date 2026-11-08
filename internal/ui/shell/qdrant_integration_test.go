package shell

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/qdrant"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
)

func qdrantPublicURI(p connections.Profile, secret string) string {
	cfg, _ := connections.QdrantProfileConfig(p)
	u, _ := url.Parse(cfg.Endpoint)
	uri := url.URL{Scheme: "qdrant", Host: u.Host, Path: u.Path}
	options := url.Values{"auth": {cfg.Auth}, "sslmode": {p.TLSMode}, "connect_timeout": {fmt.Sprint(cfg.ConnectTimeoutSeconds)}, "request_timeout": {fmt.Sprint(cfg.RequestTimeoutSeconds)}}
	for key, value := range map[string]string{"ca_file": cfg.CAFile, "server_name": cfg.ServerName, "client_cert_file": cfg.ClientCertFile, "client_key_file": cfg.ClientKeyFile} {
		if value != "" {
			options.Set(key, value)
		}
	}
	if cfg.Auth == "api_key" {
		uri.User = url.UserPassword("", secret)
	}
	uri.RawQuery = options.Encode()
	return uri.String()
}

func TestQdrantNativePublicChooserURINavigationDefinitionsRESTAndRecovery(t *testing.T) {
	parsed := qdrantPublicFixture(t, "NYADB_QDRANT_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cfg, _ := connections.QdrantProfileConfig(parsed.Profile)
	admin, err := qdrant.Open(ctx, cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	run := func(method, path, body string) {
		t.Helper()
		if _, err := admin.Do(ctx, qdrant.Request{Method: method, Path: path, Body: []byte(body)}); err != nil {
			t.Fatal(err)
		}
	}
	names := []string{"owned_ui_a_" + time.Now().Format("150405000000000"), "owned_ui_b_" + time.Now().Format("150405000000000")}
	for _, name := range names {
		run("PUT", "/collections/"+name, `{"vectors":{"dense":{"size":2,"distance":"Dot"}},"sparse_vectors":{"sparse":{}}}`)
		defer run("DELETE", "/collections/"+name, "")
		run("PUT", "/collections/"+name+"/points?wait=true", `{"points":[{"id":18446744073709551615,"vector":{"dense":[1,0]},"payload":{"integer":9007199254740993,"text":"原始;向量🐱"}}]}`)
	}
	alias := names[0] + "_alias"
	run("POST", "/collections/aliases", `{"actions":[{"create_alias":{"collection_name":"`+names[0]+`","alias_name":"`+alias+`"}}]}`)
	s, _, profiles, path := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	wait := func(done func() bool) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for !done() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(2 * time.Millisecond)
		}
		if !done() {
			t.Fatal("native Qdrant UI did not settle")
		}
		tt.Frame()
	}
	s.openConnectionDialog()
	tt.Frame()
	if err := tt.Click("搜索连接类型"); err != nil {
		t.Fatal(err)
	}
	tt.Type("Qdrant")
	tt.Frame()
	if err := tt.Click("选择 Qdrant"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionEditor.Draft().Kind != connections.Qdrant || s.ConnectionWindowTitle() != "新建连接 (Qdrant)" {
		t.Fatal("chooser changed native product")
	}
	if err := s.connectionEditor.ImportURI(qdrantPublicURI(parsed.Profile, parsed.Password)); err != nil {
		t.Fatal(err)
	}
	s.connectionStep = 1
	s.updateConnectionWindowTitle()
	tt.Frame()
	if cfg.Auth == "api_key" {
		if err := tt.Click("保存凭据"); err != nil {
			t.Fatal(err)
		}
	}
	if err := tt.Click("保存连接"); err != nil {
		t.Fatal(err)
	}
	list, err := profiles.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatal("saved profile missing", err)
	}
	p := list[0]
	if p.Kind != connections.Qdrant || p.QdrantOptions != parsed.Profile.QdrantOptions || s.runtime.IsConnected(p.ID) {
		t.Fatal("save changed native settings or auto-connected")
	}
	s.CloseMySQLSessions()
	reopened, err := localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s = NewWithProfiles(connections.NewService(localdb.NewProfileStore(reopened)))
	s.SetCredentialVault(vault)
	defer s.CloseMySQLSessions()
	tt = ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	s.selectProfileByName(p.Name)
	tt.Frame()
	if cfg.Auth == "none" && tt.HasText("Qdrant 会话 API key") {
		t.Fatal("anonymous session asks for key")
	}
	if cfg.Auth == "api_key" && !tt.HasText("Qdrant 会话 API key") {
		t.Fatal("native session key label missing")
	}
	if err := tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	w := s.sessions.Current()
	if w.Status != "已连接" {
		t.Fatal(w.Error)
	}
	for label := range w.Namespaces {
		if label == alias {
			t.Fatal("alias became physical namespace")
		}
	}
	var definitions, aliases, pointQueries, pointData []*tab
	for _, name := range names {
		s.submitNavigation(services, navigation.DatabaseNode(p.ID, name))
		wait(func() bool { return w.Pending == "" && w.Cache[name] != nil })
		categories := s.navigation.Children(navigation.DatabaseNode(p.ID, name).Key())
		if len(categories) != 4 {
			t.Fatal("native categories", categories)
		}
		for i, category := range []string{"集合信息", "向量点", "集合别名", "查询"} {
			if navigation.Parse(categories[i]).Category != category || !s.toolbarActionAvailable(category) {
				t.Fatal("native category disabled", category)
			}
			if category != "查询" {
				s.dispatchToolbar(category, services)
				tt.Frame()
			}
		}
		s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: name, Category: "集合信息", Object: name})
		definition := s.currentTab()
		if definition.object == nil || definition.data != nil {
			t.Fatal("collection config became relational grid")
		}
		wait(func() bool { return !definition.object.Loading() })
		if definition.object.Error() != "" || !tt.HasText("Qdrant 原生定义（只读）") || tt.HasText("外键") {
			t.Fatal("native collection definition", definition.object.Error())
		}
		definitions = append(definitions, definition)
		s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: name, Category: "集合别名", Object: "aliases"})
		view := s.currentTab()
		wait(func() bool { return view.object != nil && !view.object.Loading() })
		if view.object.Error() != "" || view.object.Object().Catalog != name || view.object.Object().Kind != "aliases" {
			t.Fatal("original alias definition lost", view.object.Error())
		}
		aliases = append(aliases, view)
		s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: name, Category: "向量点", Object: "points"})
		dataTab := s.currentTab()
		if dataTab.data == nil || dataTab.query != nil || dataTab.data.Object() != s.runtime.Namespace(p.ID, name).Object("points", "points") {
			t.Fatal("public point entry lost shared original data document")
		}
		wait(func() bool { return !dataTab.data.Busy() })
		if dataTab.data.Error() != "" || dataTab.data.Page() == nil || len(dataTab.data.Page().Columns) != 4 || dataTab.data.Page().Rows[0][0].Text != "18446744073709551615" {
			t.Fatal("public point grid lost exact native result", dataTab.data.Error())
		}
		pointData = append(pointData, dataTab)
		s.openQdrantPoints(p.ID, name)
		query := s.currentTab()
		if query.query == nil || query.data != nil || query.query.Namespace() != s.runtime.Namespace(p.ID, name) || query.query.Loading() || !strings.Contains(query.query.Editor.Buffer.String(), name+"/points/scroll") {
			t.Fatal("point entry did not open an original explicit REST read")
		}
		tt.Frame()
		if err := tt.Click("运行 Qdrant REST 请求"); err != nil {
			t.Fatal(err)
		}
		wait(func() bool { return !query.query.Loading() })
		result := query.query.Result()
		if query.query.Error() != "" || result == nil || len(result.Sets) != 2 || result.Sets[0].Rows[0][0].Text != "18446744073709551615" || !strings.Contains(result.Sets[1].Rows[0][1].Text, "9007199254740993") {
			t.Fatal("native point result not shown", query.query.Error())
		}
		if tt.HasText("EXPLAIN") || tt.HasText("格式化 SQL") {
			t.Fatal("REST editor inherited SQL controls")
		}
		pointQueries = append(pointQueries, query)
	}
	if pointData[0].data.Object() == pointData[1].data.Object() || definitions[0].object.Object() == definitions[1].object.Object() || aliases[0].object.Object() == aliases[1].object.Object() || pointQueries[0].query.Namespace() == pointQueries[1].query.Namespace() {
		t.Fatal("same-name original objects aliased")
	}
	query := pointQueries[0]
	for i, tab := range s.tabs {
		if tab.id == query.id {
			s.activeTab = i
		}
	}
	tt.Frame()
	request := "PUT /collections/" + names[0] + "/points?wait=false\n{\"points\":[{\"id\":42,\"vector\":{\"dense\":[0,1]},\"payload\":{\"text\":\"原始;请求🐱\"}}]}"
	query.query.Editor.Buffer.Set(request)
	tt.Frame()
	if err := tt.Click("Qdrant REST 编辑器"); err != nil {
		t.Fatal(err)
	}
	at := strings.Index(request, "原始;")
	query.query.Editor.Select(at, at)
	tt.Key(ui.Ctrl, ui.KeyEnter)
	wait(func() bool { return !query.query.Loading() })
	if query.query.Error() != "" || query.query.Result() == nil || query.query.Result().Steps[0].State != "enqueued" || !tt.HasText("请求已受理，尚未确认完成") {
		t.Fatal("native admission feedback", query.query.Error(), query.query.Result())
	}
	if output := os.Getenv("NYADB_QDRANT_PUBLIC_IMAGES"); output != "" {
		defer i18n.SetLocaleFunc(nil)
		for _, language := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return language })
			for _, size := range [][2]int{{1280, 800}, {720, 520}} {
				folder := filepath.Join(output, language)
				if err := os.MkdirAll(folder, 0700); err != nil {
					t.Fatal(err)
				}
				for _, panel := range []struct {
					name string
					tab  *tab
				}{{"definition", definitions[0]}, {"aliases", aliases[0]}, {"points", pointQueries[1]}, {"rest-admission", query}} {
					for i, candidate := range s.tabs {
						if candidate.id == panel.tab.id {
							s.activeTab = i
						}
					}
					capture := ui.NewTester(s.View, size[0], size[1])
					writeDomainCapture(t, folder, fmt.Sprintf("%s-%dx%d", panel.name, size[0], size[1]), capture)
					control := "运行 Qdrant REST 请求"
					if panel.tab.object != nil {
						control = "刷新原生定义"
					}
					r, ok := capture.Find(locales.Text(control))
					if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
						t.Fatal("native control outside viewport", language, panel.name, r)
					}
				}
			}
		}
		i18n.SetLocaleFunc(nil)
	}
	for i, tab := range s.tabs {
		if tab.id == query.id {
			s.activeTab = i
		}
	}
	tt.Frame()
	request = "PUT /collections/" + names[0] + "/points?wait=true\n{\"points\":[{\"id\":999,\"vector\":{\"dense\":[1,0]}}]}"
	query.query.Editor.Buffer.Set(request)
	tt.Frame()
	s.disconnectMySQLProfile(p.ID)
	for _, d := range pointData {
		if d.data != nil && d.data.Page() != nil {
			t.Fatal("disconnect retained scoped point data")
		}
	}
	tt.Frame()
	if !query.query.NeedsRecovery() || query.query.Editor.Buffer.String() != request {
		t.Fatal("disconnect lost native text")
	}
	if err := tt.Click("恢复原连接"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	if err := tt.Click("建立新会话（不执行现有请求）"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !query.query.NeedsRecovery() })
	if query.query.Editor.Buffer.String() != request || query.query.Loading() {
		t.Fatal("recovery executed retained request")
	}
	response, err := admin.Do(ctx, qdrant.Request{Method: "GET", Path: "/collections/" + names[0] + "/points/999"})
	if err == nil || response.Status != 404 {
		t.Fatal("recovery submitted retained write")
	}
}
