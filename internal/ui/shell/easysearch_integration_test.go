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
	"github.com/nyakang/nyadb/internal/services/easysearch"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
)

func TestEasysearchNativePublicChooserURIGridRESTAndRecovery(t *testing.T) {
	if os.Getenv("NYADB_EASYSEARCH_IT") != "1" {
		t.Skip("requires owned disposable Easysearch")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_EASYSEARCH_CONFIG"))
	if err != nil {
		t.Fatal("private probe unavailable")
	}
	var cfg struct {
		Endpoint, Username, Auth string
		CAFile                   string `json:"ca_file"`
		PasswordFile             string `json:"password_file"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		t.Fatal("invalid private probe")
	}
	secret, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		t.Fatal("private credential unavailable")
	}
	ctx, stop := context.WithTimeout(context.Background(), 120*time.Second)
	defer stop()
	admin, err := easysearch.Open(ctx, connections.EasysearchConfig{Endpoint: cfg.Endpoint, Username: cfg.Username, Auth: cfg.Auth, CAFile: cfg.CAFile}, string(secret))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	index := "nyadb_shell_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	alias := index + "_alias"
	stream := index + "_stream"
	template := index + "_template"
	run := func(method, path, body string) {
		t.Helper()
		if _, e := admin.Do(ctx, easysearch.Request{Method: method, Path: path, Body: []byte(body)}); e != nil {
			t.Fatal(e)
		}
	}
	run("PUT", "/"+index, `{"settings":{"number_of_replicas":0},"mappings":{"dynamic":false,"properties":{"name":{"type":"keyword"},"nested":{"properties":{"n":{"type":"long"}}}}}}`)
	run("PUT", "/"+index+"/_alias/"+alias, `{"is_write_index":true}`)
	run("PUT", "/_index_template/"+template, fmt.Sprintf(`{"index_patterns":["%s"],"data_stream":{},"template":{"settings":{"number_of_replicas":0}}}`, stream))
	run("PUT", "/_data_stream/"+stream, "")
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, path := range []string{"/_data_stream/" + stream, "/_index_template/" + template, "/" + index} {
			if _, e := admin.Do(cleanup, easysearch.Request{Method: "DELETE", Path: path}); e != nil {
				t.Error("owned fixture cleanup", e)
			}
		}
	}()
	run("PUT", "/"+index+"/_create/original?refresh=wait_for", `{"name":"原始文档🐱","exact":9007199254740993,"nested":{"n":1}}`)
	s, _, profiles, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	wait := func(done func() bool) {
		t.Helper()
		end := time.Now().Add(25 * time.Second)
		for !done() && time.Now().Before(end) {
			tt.Frame()
			time.Sleep(2 * time.Millisecond)
		}
		if !done() {
			t.Fatal("Easysearch UI operation did not settle")
		}
		tt.Frame()
	}
	s.openConnectionDialog()
	tt.Frame()
	// Search the real chooser, then open the injected editor.
	if err = tt.Click("搜索连接类型"); err != nil {
		t.Fatal(err)
	}
	tt.Type("Easysearch")
	tt.Frame()
	if err = tt.Click("选择 Easysearch"); err != nil {
		t.Fatal(err)
	}
	if err = tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionEditor.Draft().Kind != connections.Easysearch || s.ConnectionWindowTitle() != "新建连接 (Easysearch)" {
		t.Fatal("chooser did not open native Easysearch form")
	}
	endpoint, _ := url.Parse(cfg.Endpoint)
	u := url.URL{Scheme: "easysearch", Host: endpoint.Host}
	opts := url.Values{"auth": {cfg.Auth}, "sslmode": {"verify-full"}}
	if endpoint.Scheme == "http" {
		opts.Set("sslmode", "disable")
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
		t.Fatal("public Easysearch URI import failed")
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
	if p.Kind != connections.Easysearch || s.runtime.IsConnected(p.ID) {
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
	cluster := admin.Identity().ClusterName
	clusterUUID := admin.Identity().ClusterUUID
	if len(w.Databases) != 1 || w.Databases[0] != cluster || w.Namespaces[cluster].Catalog != clusterUUID {
		t.Fatal("namespace lost original UUID or display label")
	}
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, cluster))
	wait(func() bool { return w.Pending == "" && w.Cache[cluster] != nil })
	for _, expected := range []struct{ name, caption, kind string }{{index, "索引", "index"}, {alias, "别名", "alias"}, {stream, "数据流", "data_stream"}} {
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
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: cluster, Category: "索引", Object: index})
	data := s.currentTab()
	if data.data == nil || data.data.Object().Kind != "index" || data.data.Kind() != "索引" {
		t.Fatal("index did not route to shared grid")
	}
	wait(func() bool { return !data.data.Busy() })
	if data.data.Page() == nil || data.data.Error() != "" {
		t.Fatal(data.data.Error())
	}
	source := `{"name":"网格已保存🐱","exact":9007199254740995,"nested":{"n":2}}`
	s.dataWorkspace.EditCell(services, data.data, 0, "document", contract.Cell{Kind: contract.JSON, Text: source})
	tt.Frame()
	if !data.data.HasDraft() {
		t.Fatal("public document edit disabled")
	}
	if err = tt.Click("保存数据更改"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !data.data.Busy() })
	if data.data.HasDraft() || data.data.Error() != "" || !strings.Contains(data.data.Page().Rows[0][3].Text, "9007199254740995") {
		t.Fatal("grid save did not rebuild original PIT", data.data.Error())
	}
	// Both alias and stream use their original native identity in the same grid.
	for _, target := range []struct{ name, caption, kind string }{{alias, "别名", "alias"}, {stream, "数据流", "data_stream"}} {
		s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: cluster, Category: target.caption, Object: target.name})
		tab := s.currentTab()
		wait(func() bool { return !tab.data.Busy() })
		if tab.data == nil || tab.data.Error() != "" || tab.data.Object().Kind != target.kind {
			t.Fatal("native target grid", target.kind)
		}
	}
	s.openMySQLStructure(services, cluster, index)
	mapping := s.currentTab()
	wait(func() bool { return !mapping.object.Loading() })
	if mapping.object.Error() != "" || mapping.object.Data() == nil || !strings.Contains(mapping.object.Data().Definition, "nested") {
		t.Fatal("full native mapping", mapping.object.Error())
	}
	if !tt.HasText("映射（只读）") || tt.HasText("外键") {
		t.Fatal("mapping inherited relational structure")
	}
	s.openQuery()
	query := s.currentTab()
	tt.Frame()
	request := "GET /" + index + "/_doc/original"
	query.query.Editor.Buffer.Set(request)
	tt.Frame()
	if err = tt.Click("运行 Easysearch REST 请求"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, query)
	result := query.query.Result()
	if result == nil || len(result.Rows) != 1 || result.Rows[0][0] != "200" || !strings.Contains(result.Rows[0][1], "9007199254740995") {
		t.Fatal("REST request/full response", query.query.Error())
	}
	// Headless captures show real server data; locale/size changes do not execute.
	if output := os.Getenv("NYADB_EASYSEARCH_WORKBENCH_IMAGES"); output != "" {
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
				}{{"grid", data}, {"mapping", mapping}, {"rest", query}} {
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
	request = "PUT /" + index + "/_create/never-replay\n{\"name\":\"must remain unexecuted\"}"
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
	probe, probeErr := admin.Do(ctx, easysearch.Request{Method: "GET", Path: "/" + index + "/_doc/never-replay"})
	if probeErr == nil || probe.Status != 404 {
		t.Fatal("recovery executed retained write request")
	}
}
