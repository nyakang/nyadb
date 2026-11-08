package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/cassandra"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCassandraNativePublicChooserURIGridCQLStructureAndRecovery(t *testing.T) {
	if os.Getenv("NYADB_CASSANDRA_WORKBENCH_IT") != "1" {
		t.Skip("requires owned disposable Cassandra workbench")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_CASSANDRA_CONFIG"))
	if err != nil {
		t.Fatal("private probe unavailable")
	}
	var cfg struct {
		Endpoint, Username, Auth, TLSMode string
		CAFile                            string `json:"ca_file"`
		ServerName                        string `json:"server_name"`
		PasswordFile                      string `json:"password_file"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		t.Fatal("invalid private probe")
	}
	secret, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		t.Fatal("private credential unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	admin, err := cassandra.Open(ctx, connections.CassandraConfig{Endpoint: cfg.Endpoint, Username: cfg.Username, Auth: cfg.Auth, TLSMode: cfg.TLSMode, CAFile: cfg.CAFile, ServerName: cfg.ServerName}, string(secret))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	ks := "nyadb_shell_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	other := ks + "_other"
	run := func(q string) *cassandra.Result {
		t.Helper()
		out, e := admin.Execute(ctx, cassandra.Request{Statement: q})
		if e != nil {
			t.Fatal(e)
		}
		return out
	}
	for _, name := range []string{ks, other} {
		run("CREATE KEYSPACE " + name + " WITH replication={'class':'SimpleStrategy','replication_factor':1}")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		for _, name := range []string{ks, other} {
			if _, e := admin.Execute(cleanup, cassandra.Request{Statement: "DROP KEYSPACE " + name}); e != nil {
				t.Error("owned keyspace cleanup", e)
			}
		}
	}()
	run("CREATE TYPE " + ks + ".address(street text, zip int)")
	run("CREATE TABLE " + ks + ".items(id int, ord int, amount decimal, note text static, addr frozen<address>, PRIMARY KEY(id,ord)) WITH CLUSTERING ORDER BY(ord DESC)")
	run("CREATE INDEX amount_idx ON " + ks + ".items(amount)")
	run("CREATE TABLE " + other + ".items(id int PRIMARY KEY,foreign_value text)")
	run("INSERT INTO " + other + ".items(id,foreign_value) VALUES(1,'foreign keyspace')")
	run("INSERT INTO " + ks + ".items(id,ord,amount,note,addr) VALUES(1,1,9007199254740993.123456789,'原生静态列🐱',{street:'猫',zip:123})")
	run("CREATE MATERIALIZED VIEW " + ks + ".items_view AS SELECT id,ord,amount FROM " + ks + ".items WHERE id IS NOT NULL AND ord IS NOT NULL PRIMARY KEY(id,ord)")
	s, _, profiles, _ := profileTestState(t)
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
			t.Fatal("Cassandra UI operation did not settle")
		}
		tt.Frame()
	}
	s.openConnectionDialog()
	tt.Frame()
	if err = tt.Click("搜索连接类型"); err != nil {
		t.Fatal(err)
	}
	tt.Type("Cassandra")
	tt.Frame()
	if err = tt.Click("选择 Apache Cassandra"); err != nil {
		t.Fatal(err)
	}
	if err = tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionEditor.Draft().Kind != connections.Cassandra || s.ConnectionWindowTitle() != "新建连接 (Apache Cassandra)" {
		t.Fatal("chooser native form")
	}
	u := url.URL{Scheme: "cassandra", Host: cfg.Endpoint, Path: "/" + ks}
	opts := url.Values{"auth": {cfg.Auth}, "sslmode": {cfg.TLSMode}}
	if cfg.Auth == "password" {
		u.User = url.UserPassword(cfg.Username, string(secret))
	}
	if cfg.CAFile != "" {
		opts.Set("ca_file", cfg.CAFile)
	}
	if cfg.ServerName != "" {
		opts.Set("server_name", cfg.ServerName)
	}
	u.RawQuery = opts.Encode()
	if err = s.connectionEditor.ImportURI(u.String()); err != nil {
		t.Fatal("public Cassandra import")
	}
	s.connectionStep = 1
	s.updateConnectionWindowTitle()
	tt.Frame()
	if cfg.Auth == "password" {
		if err = tt.Click("保存凭据"); err != nil {
			t.Fatal(err)
		}
	}
	if err = tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		return tt.HasText("Apache Cassandra 连接测试成功（已关闭测试会话）") || s.connectionEditor.Error() != ""
	})
	if s.runtime.IsConnected(1) {
		t.Fatal("public test published session")
	}
	if err = tt.Click("保存连接"); err != nil {
		t.Fatal(err)
	}
	list, err := profiles.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatal("saved public Cassandra profile", err)
	}
	p := list[0]
	if p.Kind != connections.Cassandra || p.Database != ks || s.runtime.IsConnected(p.ID) {
		t.Fatal("save changed native kind or connected")
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
	if s.sessions.EngineCatalog(p.ID) != "" {
		t.Fatal("keyspaces wrapped in SQL catalog")
	}
	ns := s.runtime.Namespace(p.ID, ks)
	if ns != (contract.NamespaceRef{ProfileID: p.ID, Catalog: ks}) {
		t.Fatal("native keyspace identity")
	}
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, ks))
	wait(func() bool { return w.Pending == "" && w.Cache[ks] != nil })
	for _, target := range []struct{ name, kind, caption string }{{"items", "table", "表"}, {"items_view", "view", "视图"}, {"address", "type", "类型"}} {
		found := false
		for _, o := range w.Cache[ks] {
			if o.Name == target.name && o.Kind == target.caption && o.Ref == ns.Object(target.kind, target.name) {
				found = true
			}
		}
		if !found {
			t.Fatal("qualified native metadata", target.kind)
		}
	}
	if !s.toolbarActionAvailable("类型") || s.toolbarActionAvailable("函数") || s.toolbarActionAvailable("用户") || s.toolbarActionAvailable("备份") {
		t.Fatal("native toolbar capabilities")
	}
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: ks, Category: "表", Object: "items"})
	data := s.currentTab()
	if data.data == nil || data.data.Object() != ns.Object("table", "items") {
		t.Fatal("table did not route to shared grid")
	}
	wait(func() bool { return !data.data.Busy() })
	if data.data.Page() == nil || data.data.Error() != "" || len(data.data.Page().Rows) != 1 {
		t.Fatal("public native grid", data.data.Error())
	}
	exact := false
	for _, cell := range data.data.Page().Rows[0] {
		if strings.Contains(cell.Text, "9007199254740993.123456789") {
			exact = true
		}
	}
	if !exact {
		t.Fatal("exact decimal lost in public grid")
	}
	s.openMySQLStructure(services, ks, "items")
	structure := s.currentTab()
	wait(func() bool { return !structure.object.Loading() })
	if structure.object.Error() != "" || structure.object.Data() == nil || !tt.HasText("Cassandra 结构（只读）") || !tt.HasText("partition_key") || !tt.HasText("static") || tt.HasText("外键") {
		t.Fatal("native structure roles", structure.object.Error())
	}
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: ks, Category: "视图", Object: "items_view"})
	viewTab := s.currentTab()
	if viewTab.data == nil || viewTab.data.Object() != ns.Object("view", "items_view") {
		t.Fatal("native view did not route to bound grid")
	}
	wait(func() bool { return !viewTab.data.Busy() })
	if viewTab.data.Page() == nil || viewTab.data.Error() != "" {
		t.Fatal("public native view page", viewTab.data.Error())
	}
	completion := s.sqlCompletionSource(p.ID)
	viewColumns, e := completion.Load(ctx, ks, "items_view")
	if e != nil || len(viewColumns) != 3 {
		t.Fatal("completion lost native view kind", e)
	}
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: ks, Category: "类型", Object: "address"})
	udt := s.currentTab()
	if udt.object == nil {
		t.Fatal("native UDT read-only definition missing")
	}
	s.openQuery()
	query := s.currentTab()
	tt.Frame()
	if !strings.HasPrefix(query.title, "CQL ") || query.query.ProfileID() != p.ID {
		t.Fatal("public CQL tab lost profile")
	}
	batch := "BEGIN UNLOGGED BATCH\nINSERT INTO items(id,ord,amount) VALUES(2,1,2);\nINSERT INTO items(id,ord,amount) VALUES(2,2,3);\nAPPLY BATCH;"
	source := "SELECT * FROM items;\n" + batch + "\nSELECT * FROM items WHERE id=2;"
	query.query.Editor.Buffer.Set(source)
	at := strings.Index(source, "VALUES(2,2")
	query.query.Editor.Select(at, at)
	tt.Frame()
	if err = tt.Click("运行当前或选中 CQL"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, query)
	result := query.query.Result()
	if result == nil || len(result.Rows) != 1 || result.Rows[0][0] != "VOID" || result.AffectedRows != -1 {
		t.Fatal("native BATCH acknowledgement", query.query.Error())
	}
	probe := run("SELECT ord FROM " + ks + ".items WHERE id=2")
	if len(probe.Rows) != 2 {
		t.Fatal("caret executed incomplete BATCH")
	}
	query.query.Editor.Buffer.Set("INSERT INTO items(id,ord,amount) VALUES(1,1,0) IF NOT EXISTS;")
	tt.Frame()
	s.sqlWorkspace.RunAll(nil, query.query)
	waitSQLUI(t, s, tt, query)
	if result = query.query.Result(); result == nil || result.Rows[0][0] != "false" {
		t.Fatal("LWT false response lost")
	}
	query.query.Editor.Buffer.Set("SELECT amount,note,addr FROM items WHERE id=1;")
	tt.Frame()
	s.sqlWorkspace.RunAll(nil, query.query)
	waitSQLUI(t, s, tt, query)
	if result = query.query.Result(); result == nil || len(result.Rows) != 1 || result.Rows[0][0] != "9007199254740993.123456789" {
		t.Fatal("native CQL exact results", query.query.Error())
	}
	// Execute a whole CQL script and explicitly refresh the original keyspace.
	query.query.Editor.Buffer.Set("CREATE TABLE marker(id int PRIMARY KEY); INSERT INTO marker(id) VALUES(7); SELECT id FROM marker;")
	tt.Frame()
	s.sqlWorkspace.RunAll(nil, query.query)
	waitSQLUI(t, s, tt, query)
	if result = query.query.Result(); result == nil || len(result.Sets) != 3 || result.Sets[2].Rows[0][0].Text != "7" {
		t.Fatal("whole CQL script results", query.query.Error())
	}
	s.sessions.LoadObjects(ks, true, services.Invalidate)
	wait(func() bool { return w.Pending == "" && w.Cache[ks] != nil })
	marker := false
	for _, o := range w.Cache[ks] {
		if o.Ref == ns.Object("table", "marker") {
			marker = true
		}
	}
	if !marker {
		t.Fatal("CQL DDL refresh lost original keyspace")
	}
	query.query.Editor.Buffer.Set("SELECT amount,note,addr FROM items WHERE id=1;")
	tt.Frame()
	s.sqlWorkspace.RunAll(nil, query.query)
	waitSQLUI(t, s, tt, query)
	// The other keyspace has a same-named table. Completion and grid keep qualified identity.
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, other))
	wait(func() bool { return w.Pending == "" && w.Cache[other] != nil })
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: other, Category: "表", Object: "items"})
	foreign := s.currentTab()
	wait(func() bool { return !foreign.data.Busy() })
	if foreign.data.Object().Catalog != other || foreign.data.Page() == nil || foreign.data.Page().Columns[1] != "foreign_value" {
		t.Fatal("same table crossed keyspace", foreign.data.Error())
	}
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, ks))
	tt.Frame()
	// Captures use actual native data and definitions and never execute on locale changes.
	if output := os.Getenv("NYADB_CASSANDRA_WORKBENCH_IMAGES"); output != "" {
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
				}{{"grid", data}, {"schema", structure}, {"cql", query}, {"udt", udt}} {
					for i, candidate := range s.tabs {
						if candidate.id == view.tab.id {
							s.activeTab = i
							break
						}
					}
					capture := ui.NewTester(s.View, size[0], size[1])
					writeDomainCapture(t, folder, fmt.Sprintf("%s-%dx%d", view.name, size[0], size[1]), capture)
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
	request := "INSERT INTO items(id,ord,amount) VALUES(99,1,99);"
	query.query.Editor.Buffer.Set(request)
	tt.Frame()
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if !query.query.NeedsRecovery() || query.query.Editor.Buffer.String() != request || data.data.Page() != nil {
		t.Fatal("disconnect lost CQL or retained scoped grid")
	}
	if err = tt.Click(locales.Text("恢复原连接")); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	if err = tt.Click(locales.Text("建立新物理会话（不执行现有 CQL）")); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !query.query.NeedsRecovery() })
	if query.query.Loading() || query.query.Editor.Buffer.String() != request {
		t.Fatal("CQL recovery replay or text loss")
	}
	if probe = run("SELECT id FROM " + ks + ".items WHERE id=99"); len(probe.Rows) != 0 {
		t.Fatal("recovery replayed retained write")
	}
	s.deleteProfileID(p.ID)
	remaining, e := profiles.List(ctx)
	if e != nil || len(remaining) != 0 || s.runtime.IsConnected(p.ID) {
		t.Fatal("public profile deletion did not release session", e)
	}
	if _, exists, e := vault.Get(p.ID, ""); e != nil || exists {
		t.Fatal("public profile deletion retained credentials", e)
	}
}
