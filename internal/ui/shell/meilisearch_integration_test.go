package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/meilisearch"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
)

func meilisearchPublicURI(p connections.Profile, secret string) string {
	cfg, _ := connections.MeilisearchProfileConfig(p)
	endpoint, _ := url.Parse(cfg.Endpoint)
	uri := url.URL{Scheme: "meilisearch", Host: endpoint.Host, Path: endpoint.Path}
	opts := url.Values{"auth": {cfg.Auth}, "sslmode": {p.TLSMode}}
	for k, v := range map[string]string{"index_uid": cfg.IndexUID, "ca_file": cfg.CAFile, "server_name": cfg.ServerName, "client_cert_file": cfg.ClientCertFile, "client_key_file": cfg.ClientKeyFile} {
		if v != "" {
			opts.Set(k, v)
		}
	}
	if cfg.Auth == "bearer" {
		uri.User = url.UserPassword("", secret)
	}
	uri.RawQuery = opts.Encode()
	return uri.String()
}

func TestMeilisearchNativePublicChooserURINavigationDefinitionsRESTAndRecovery(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	cfg, _ := connections.MeilisearchProfileConfig(parsed.Profile)
	admin, err := meilisearch.Open(ctx, cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	submit := func(method, path, body string) meilisearch.Receipt {
		t.Helper()
		r, _, err := admin.Submit(ctx, meilisearch.Request{Method: method, Path: path, Body: []byte(body)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = admin.WaitTask(ctx, r, 30*time.Second); err != nil {
			t.Fatal(err)
		}
		return r
	}
	indexes := []string{"owned_shell_a_" + strconv.FormatInt(time.Now().UnixNano(), 36), "owned_shell_b_" + strconv.FormatInt(time.Now().UnixNano(), 36)}
	for _, uid := range indexes {
		submit("POST", "/indexes", `{"uid":"`+uid+`","primaryKey":"id"}`)
		defer submit("DELETE", "/indexes/"+uid, "")
		submit("PATCH", "/indexes/"+uid+"/settings", `{"searchableAttributes":["text"],"filterableAttributes":["integer"],"synonyms":{"猫":["cat","原生🐱"]}}`)
	}
	submit("POST", "/indexes/"+indexes[0]+"/documents", `[{"id":"original","integer":9007199254740993,"text":"原始;文档🐱"}]`)
	rows := make([]map[string]any, 1016)
	for i := range rows {
		rows[i] = map[string]any{"id": fmt.Sprintf("row_%04d", i), "integer": json.Number("9007199254740993")}
	}
	rows[0]["memo"] = strings.Repeat("猫🐱", 1500)
	seed, _ := json.Marshal(rows)
	submit("POST", "/indexes/"+indexes[0]+"/documents", string(seed))
	submit("POST", "/indexes/"+indexes[1]+"/documents", `[{"id":"sibling","integer":9007199254740993}]`)
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
			t.Fatal("native Meilisearch UI did not settle")
		}
		tt.Frame()
	}
	s.openConnectionDialog()
	tt.Frame()
	if err = tt.Click("搜索连接类型"); err != nil {
		t.Fatal(err)
	}
	tt.Type("Meilisearch")
	tt.Frame()
	if err = tt.Click("选择 Meilisearch"); err != nil {
		t.Fatal(err)
	}
	if err = tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionEditor.Draft().Kind != connections.Meilisearch || s.ConnectionWindowTitle() != "新建连接 (Meilisearch)" {
		t.Fatal("native chooser form/title")
	}
	if err = s.connectionEditor.ImportURI(meilisearchPublicURI(parsed.Profile, parsed.Password)); err != nil {
		t.Fatal(err)
	}
	s.connectionStep = 1
	s.updateConnectionWindowTitle()
	tt.Frame()
	if cfg.Auth == "bearer" {
		if err = tt.Click("保存凭据"); err != nil {
			t.Fatal(err)
		}
	}
	if err = tt.Click("保存连接"); err != nil {
		t.Fatal(err)
	}
	list, err := profiles.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatal("saved profile absent", err)
	}
	p := list[0]
	if p.Kind != connections.Meilisearch || s.runtime.IsConnected(p.ID) {
		t.Fatal("save connected or changed product")
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
	if cfg.Auth == "none" && tt.HasText("Meilisearch 会话 API key") {
		t.Fatal("anonymous session asks for key")
	}
	if err = tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	w := s.sessions.Current()
	if w.Status != "已连接" {
		t.Fatal(w.Error)
	}
	var definitions, documents []*tab
	for _, uid := range indexes {
		s.submitNavigation(services, navigation.DatabaseNode(p.ID, uid))
		wait(func() bool { return w.Pending == "" && w.Cache[uid] != nil })
		categories := s.navigation.Children(navigation.DatabaseNode(p.ID, uid).Key())
		if len(categories) != 6 {
			t.Fatal("native categories", categories)
		}
		for i, kind := range []string{"索引信息", "文档", "索引设置", "索引统计", "任务", "查询"} {
			if navigation.Parse(categories[i]).Category != kind {
				t.Fatal("wrong native category", categories)
			}
		}
		for _, category := range []string{"索引信息", "索引设置", "索引统计", "文档"} {
			if !s.toolbarActionAvailable(category) {
				t.Fatal("native category disabled", category)
			}
			s.dispatchToolbar(category, services)
			tt.Frame()
		}
		s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: uid, Category: "索引设置", Object: "settings"})
		view := s.currentTab()
		if view.object == nil || view.data != nil {
			t.Fatal("settings not native definition")
		}
		wait(func() bool { return !view.object.Loading() })
		if view.object.Error() != "" || !view.object.SettingsMode() || !strings.Contains(s.objectWorkspace.SettingsSnapshot(view.object).Raw, "原生🐱") || !tt.HasText("当前设置") || tt.HasText("外键") {
			t.Fatal("full native definition", view.object.Error())
		}
		definitions = append(definitions, view)
		s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: uid, Category: "文档", Object: "documents"})
		data := s.currentTab()
		if data.data == nil || data.data.Object() != s.runtime.Namespace(p.ID, uid).Object("documents", "documents") {
			t.Fatal("native documents did not open the qualified shared grid")
		}
		wait(func() bool { return !data.data.Busy() })
		if data.data.Error() != "" || data.data.Page() == nil || data.data.Page().Editable == nil || !*data.data.Page().Editable || data.data.Page().KeysetAvailable || !data.data.Page().LiveRead {
			t.Fatal("native async JSON / OFFSET grid", data.data.Error())
		}
		documents = append(documents, data)
	}
	if definitions[0].id == definitions[1].id || definitions[0].object.Object() == definitions[1].object.Object() {
		t.Fatal("same-name settings aliased")
	}
	if documents[0].id == documents[1].id || documents[0].data.Object() == documents[1].data.Object() {
		t.Fatal("same-name original document ranges aliased")
	}
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: indexes[0], Category: "文档", Object: "documents"})
	data := documents[0].data
	first := data.Page().Rows[0][0].Text
	if err = tt.Click("下一页数据"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !data.Busy() })
	if data.Error() != "" || data.Offset() != 100 || data.Page().Rows[0][0].Text == first || documents[1].data.Page().Rows[0][0].Text != "sibling" {
		t.Fatal("original offset continuation or sibling changed", data.Error())
	}
	if err = tt.Click("上一页数据"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !data.Busy() })
	if data.Error() != "" || data.Offset() != 0 || data.Page().Rows[0][0].Text != first {
		t.Fatal("original offset back boundary", data.Error())
	}
	panel := data.Inspector()
	wait(func() bool { return !panel.InfoLoading() })
	if err = tt.Click("获取整表行数"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !panel.CountLoading() })
	if panel.CountError() != "" || panel.Count() == nil || *panel.Count() != 1017 {
		t.Fatal("native explicit total", panel.CountError())
	}
	// Selecting an actual JSON cell uses the existing grid and field inspector.
	if err = tt.Click(data.Page().Rows[1][1].Text); err != nil {
		t.Fatal("native JSON cell selection", err)
	}
	s.dataWorkspace.ShowField(data)
	tt.Frame()
	if err = tt.Click("读取所选字段下一段"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !panel.ChunkLoading() })
	if panel.ChunkError() != "" || len([]rune(panel.ChunkText())) != 1024 || panel.ChunkNext() != 1024 || panel.ChunkTotal() <= 1024 {
		t.Fatal("native bounded field tool", panel.ChunkError())
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
	lines := bytes.Split(bytes.TrimSpace(exported.Bytes()), []byte{'\n'})
	if data.Error() != "" || len(lines) != 1017 {
		t.Fatal("complete native export was bounded by page/search", len(lines), data.Error())
	}
	for _, line := range lines {
		var doc struct {
			ID      string
			Integer json.Number
		}
		if json.Unmarshal(line, &doc) != nil || doc.ID == "" || doc.Integer.String() != "9007199254740993" {
			t.Fatal("export wrapped or rounded original JSON")
		}
	}
	if caps := s.objectCapabilitiesFor(p.ID); !caps.EditableTables || !caps.Import || caps.Transactions || caps.Keyset {
		t.Fatal("native async JSON editing/import binding or unsupported transaction semantics")
	}
	beforeImport := len(s.tabs)
	s.openDatabaseImport(p.ID, indexes[0])
	if len(s.tabs) != beforeImport {
		t.Fatal("document capability enabled relational new-table import")
	}
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, indexes[0]))
	wait(func() bool { return w.Pending == "" })
	s.openQuery()
	query := s.currentTab()
	tt.Frame()
	if query.query.Editor.Buffer.String() != "GET /version" || !strings.HasPrefix(query.title, "REST ") {
		t.Fatal("initial REST text/title")
	}
	waitSQLUI(t, s, tt, query)
	request := "GET /indexes/" + indexes[0] + "/documents/original"
	query.query.Editor.Buffer.Set(request)
	tt.Frame()
	if err = tt.Click("运行 Meilisearch REST 请求"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, query)
	if query.query.Error() != "" || query.query.Result() == nil || !strings.Contains(query.query.Result().Sets[0].Rows[0][1].Text, "9007199254740993") || query.query.Result().Steps[0].State != "completed" {
		t.Fatal("synchronous original result", query.query.Error())
	}
	// Selection is a whole native request; semicolons in JSON are never SQL delimiters.
	write := "POST /indexes/" + indexes[0] + "/documents\n[{\"id\":\"keyboard\",\"integer\":9007199254740993,\"text\":\"原始;请求🐱\"}]"
	query.query.Editor.Buffer.Set(write)
	tt.Frame()
	if err = tt.Click("Meilisearch REST 编辑器"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(ui.Ctrl, ui.KeyEnter)
	waitSQLUI(t, s, tt, query)
	if query.query.Error() != "" || query.query.Result() == nil || query.query.Result().Steps[0].State != "enqueued" || !tt.HasText("任务已接收，尚未确认完成") {
		t.Fatal("admission represented as completion", query.query.Error(), s.status)
	}
	raw := query.query.Result().Sets[0].Rows[0][1].Text
	var receipt struct {
		TaskUID          uint32  `json:"taskUid"`
		IndexUID         *string `json:"indexUid"`
		Type, EnqueuedAt string
	}
	if json.Unmarshal([]byte(raw), &receipt) != nil {
		t.Fatal("receipt JSON")
	}
	if _, err = admin.WaitTask(ctx, meilisearch.Receipt{UID: receipt.TaskUID, IndexUID: receipt.IndexUID, Type: receipt.Type, EnqueuedAt: receipt.EnqueuedAt, Raw: []byte(raw)}, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if output := os.Getenv("NYADB_MEILISEARCH_PUBLIC_IMAGES"); output != "" {
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
				}{{"definition", definitions[0]}, {"rest-admission", query}, {"documents", documents[0]}} {
					for i, candidate := range s.tabs {
						if candidate.id == view.tab.id {
							s.activeTab = i
							break
						}
					}
					capture := ui.NewTester(s.View, size[0], size[1])
					writeDomainCapture(t, folder, fmt.Sprintf("%s-%dx%d", view.name, size[0], size[1]), capture)
					control := "运行 Meilisearch REST 请求"
					if view.name == "definition" {
						control = "读取当前设置"
					} else if view.name == "documents" {
						control = "下一页数据"
					}
					rect, ok := capture.Find(locales.Text(control))
					if !ok || rect.W <= 0 || rect.H <= 0 || rect.X < 0 || rect.Y < 0 || rect.X+rect.W > float32(size[0])+1 || rect.Y+rect.H > float32(size[1])+1 {
						t.Fatal("native control outside viewport", language, view.name, rect)
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
	request = "POST /indexes/" + indexes[0] + "/documents\n[{\"id\":\"never_replayed\"}]"
	query.query.Editor.Buffer.Set(request)
	tt.Frame()
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if !query.query.NeedsRecovery() || query.query.Editor.Buffer.String() != request {
		t.Fatal("disconnect lost original request")
	}
	if err = tt.Click("恢复原连接"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	if err = tt.Click("建立新会话（不执行现有请求）"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !query.query.NeedsRecovery() })
	if query.query.Editor.Buffer.String() != request || query.query.Loading() {
		t.Fatal("recovery replayed retained request")
	}
	response, err := admin.Do(ctx, meilisearch.Request{Method: "GET", Path: "/indexes/" + indexes[0] + "/documents/never_replayed"})
	if err == nil || response.Status != 404 {
		t.Fatal("recovery submitted retained write")
	}
}

func TestMeilisearchNativePublicScopedWorkbenchPermissions(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_SCOPED_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cfg, _ := connections.MeilisearchProfileConfig(parsed.Profile)
	original, err := meilisearch.Open(ctx, cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	if original.Identity() != (meilisearch.Identity{}) {
		t.Fatal("scoped key inferred global version")
	}
	var globals atomic.Int32
	var relayRequests sync.Mutex
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" || r.URL.Path == "/indexes" {
			globals.Add(1)
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			w.WriteHeader(502)
			return
		}
		relayRequests.Lock()
		response, err := original.Do(r.Context(), meilisearch.Request{Method: r.Method, Path: r.URL.RequestURI(), Body: body})
		relayRequests.Unlock()
		if err != nil && response.Status == 0 {
			w.WriteHeader(502)
			return
		}
		w.Header().Set("Content-Type", response.ContentType)
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
	}))
	defer relay.Close()
	ca := filepath.Join(t.TempDir(), "relay-ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: relay.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	endpoint, _ := url.Parse(relay.URL)
	port, _ := strconv.Atoi(endpoint.Port())
	p := parsed.Profile
	p.Host, p.Port = endpoint.Hostname(), uint16(port)
	p.TLSMode = "verify-full"
	p.MeilisearchOptions.CAFile, p.MeilisearchOptions.ServerName, p.MeilisearchOptions.ContextPath = ca, "example.com", ""
	p.MeilisearchOptions.ClientCertFile, p.MeilisearchOptions.ClientKeyFile = "", ""
	s, _, profiles, _ := profileTestState(t)
	defer s.CloseMySQLSessions()
	p.ID = 0
	p.Name = "Scoped Meilisearch"
	p, err = profiles.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	s.SetCredentialVault(&memoryCredentialVault{secrets: map[string]string{}})
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	s.sessions.Current().Password = parsed.Password
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
			t.Fatal("scoped UI did not settle")
		}
		tt.Frame()
	}
	if err = tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	w := s.sessions.Current()
	if w.Status != "已连接" {
		t.Fatal(w.Error)
	}
	uid := cfg.IndexUID
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, uid))
	wait(func() bool { return w.Pending == "" && w.Cache[uid] != nil })
	for _, kind := range []string{"索引设置", "索引统计"} {
		name := "settings"
		if kind == "索引统计" {
			name = "stats"
		}
		s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: uid, Category: kind, Object: name})
		view := s.currentTab()
		if view.object == nil {
			t.Fatal("scoped definition missing")
		}
		wait(func() bool { return !view.object.Loading() })
		if view.object.Error() == "" || view.object.Data() != nil {
			t.Fatal("denied native read became empty success")
		}
	}
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: uid, Category: "文档", Object: "documents"})
	data := s.currentTab().data
	if data == nil {
		t.Fatal("scoped shared document grid missing")
	}
	wait(func() bool { return !data.Busy() })
	if data.Error() != "" || len(data.Page().Rows) != 1 || !strings.Contains(data.Page().Rows[0][1].Text, "9007199254740993") || globals.Load() != 0 {
		t.Fatal("scoped documents changed original key or scope", data.Error())
	}
	s.openQuery()
	query := s.currentTab()
	waitSQLUI(t, s, tt, query)
	if query.query.Editor.Buffer.String() != "GET /indexes/"+uid {
		t.Fatal("scoped initial request queried global version")
	}
	query.query.Editor.Buffer.Set("GET /indexes/" + uid + "/documents/one")
	tt.Frame()
	if err = tt.Click("运行 Meilisearch REST 请求"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, query)
	if query.query.Error() != "" || !strings.Contains(query.query.Result().Sets[0].Rows[0][1].Text, "9007199254740993") || globals.Load() != 0 {
		t.Fatal("scoped read/key changed global scope", query.query.Error(), globals.Load())
	}
}
