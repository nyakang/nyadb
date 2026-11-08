package dataworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/couchdb"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestCouchDBNativeSharedGridPagingInspectorParkGenerationAndClose(t *testing.T) {
	if os.Getenv("NYADB_COUCHDB_DATA_IT") != "1" || os.Getenv("NYADB_COUCHDB_IT") != "1" {
		t.Skip("requires owned native CouchDB shared-grid fixture")
	}
	readConfig := func(name string) (connections.CouchDBConfig, string) {
		t.Helper()
		raw, err := os.ReadFile(os.Getenv(name))
		if err != nil {
			t.Fatal("private probe unavailable")
		}
		var probe struct {
			Endpoint, Auth, Username string
			CAFile                   string `json:"ca_file"`
			ServerName               string `json:"server_name"`
			PasswordFile             string `json:"password_file"`
		}
		if json.Unmarshal(raw, &probe) != nil {
			t.Fatal("invalid private probe")
		}
		var secret []byte
		if probe.PasswordFile != "" {
			secret, err = os.ReadFile(probe.PasswordFile)
			if err != nil {
				t.Fatal("private secret unavailable")
			}
		}
		return connections.CouchDBConfig{Endpoint: probe.Endpoint, Auth: probe.Auth, Username: probe.Username, CAFile: probe.CAFile, ServerName: probe.ServerName}, string(secret)
	}
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	adminConfig, adminSecret := readConfig("NYADB_COUCHDB_ADMIN_CONFIG")
	admin, err := couchdb.Open(ctx, adminConfig, adminSecret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	db := "nyadb_ui_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	run := func(method, path string, body any) {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		response, err := admin.Do(ctx, couchdb.Request{Method: method, Path: path, Body: raw})
		if err != nil || response.Status >= 400 {
			t.Fatal(err)
		}
	}
	run("PUT", "/"+db, nil)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		response, err := admin.Do(cleanup, couchdb.Request{Method: "DELETE", Path: "/" + db})
		if err != nil || response.Status >= 400 {
			t.Error("owned grid cleanup", err)
		}
	}()
	run("PUT", "/"+db+"/_security", map[string]any{"admins": map[string]any{"names": []string{}, "roles": []string{}}, "members": map[string]any{"names": []string{}, "roles": []string{}}})
	for _, id := range []string{"row_1", "row_2", "row_3"} {
		run("PUT", "/"+db+"/"+id, map[string]any{"_id": id, "amount": json.Number("9007199254740993"), "memo": strings.Repeat("猫", 2000), "null": nil, "empty": ""})
	}
	cfg, secret := readConfig("NYADB_COUCHDB_CONFIG")
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	p, err := connections.Defaults(connections.CouchDB)
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Name, p.Host, p.Port, p.Username = 73, "CouchDB shared grid", u.Hostname(), uint16(port), cfg.Username
	if u.Scheme == "https" {
		p.TLSMode = "verify-full"
	}
	p.CouchDBOptions = connections.CouchDBOptions{Version: 1, Auth: cfg.Auth, ContextPath: u.Path, CAFile: cfg.CAFile, ServerName: cfg.ServerName}
	handle, err := couchdb.OpenSession(ctx, p, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	object := (contract.NamespaceRef{ProfileID: p.ID, Catalog: db}).Object("documents", "_all_docs")
	active := Session{ProfileID: p.ID, Generation: handle.Generation(), Connected: true}
	caps := handle.Capabilities()
	runtime := contract.ComposeData(handle)
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(id int64) Session { return Session{ProfileID: id, Generation: active.Generation, Connected: true} }, RuntimeFor: func(id int64) Runtime { return runtime }, Inspector: InspectorDependencies{Session: func() Session { return active }}})
	defer s.CloseAll()
	a, b := s.OpenObject(1, object, caps), s.OpenObject(2, object, caps)
	a.limit, b.limit = 1, 1
	wait := func(d *Document) {
		t.Helper()
		end := time.Now().Add(30 * time.Second)
		for d.Busy() && time.Now().Before(end) {
			s.Consume(ui.Services{})
			time.Sleep(time.Millisecond)
		}
		if d.Busy() || d.err != "" {
			t.Fatal("native shared grid operation", d.err)
		}
	}
	s.Load(ui.Services{}, a, false)
	wait(a)
	s.Load(ui.Services{}, b, false)
	wait(b)
	first, sibling := a.page.Rows[0][0].Text, b.page.Rows[0][0].Text
	if len(a.columns) != 3 || a.page.Editable == nil || *a.page.Editable {
		t.Fatal("native projection became editable")
	}
	for _, column := range a.columns {
		if column.Sortable != (column.ID == "_id") {
			t.Fatal("unsupported document field sort exposed", column.ID)
		}
	}
	if fields := s.filterColumns(a, a.page.Columns); !slices.Equal(fields, []string{"_id", "_rev"}) {
		t.Fatal("unsupported filters exposed", fields)
	}
	if _, err := mysqlDataSQLSelection(a, false); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("document data produced inferred INSERT SQL", err)
	}
	if slices.Contains(a.exportFormats, "sql") || a.capabilities.Import || a.capabilities.Transactions {
		t.Fatal("unsupported SQL export/import/transactions exposed")
	}
	s.changeMySQLDataPage(ui.Services{}, a, 1)
	wait(a)
	if a.page.Rows[0][0].Text == first {
		t.Fatal("next page repeated first native key")
	}
	s.changeMySQLDataPage(ui.Services{}, a, 0)
	wait(a)
	if a.page.Rows[0][0].Text != first || b.page.Rows[0][0].Text != sibling {
		t.Fatal("original boundaries/sibling lost")
	}
	a.selectedRow, a.grid.Cells.Cursor = 0, "document"
	panel := s.inspectorPanel(a)
	s.inspector.LoadInfo(panel, nil, false)
	s.inspector.LoadCount(panel, nil)
	s.inspector.LoadChunk(panel, nil)
	end := time.Now().Add(30 * time.Second)
	for (panel.InfoLoading() || panel.CountLoading() || panel.ChunkLoading()) && time.Now().Before(end) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if panel.Info() == nil || panel.Info().Engine != "Apache CouchDB" || panel.Count() == nil || *panel.Count() != 3 || panel.ChunkError() != "" || panel.ChunkNext() != 1024 {
		t.Fatal("original native inspector", panel.Info(), panel.InfoError(), panel.Count(), panel.CountError(), panel.ChunkError(), panel.ChunkNext())
	}
	if !strings.Contains(a.page.Rows[0][2].Text, "9007199254740993") || !a.page.Rows[0][2].Truncated {
		t.Fatal("shared grid lost exact integer/bounded original JSON")
	}
	editor := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.View(c, a) }, 1200, 720)
	if err := clickDataToolbar(editor, locales.Text("Mango 查询")); err != nil {
		t.Fatal("public Mango editor unavailable", err)
	}
	input := func(raw string) {
		t.Helper()
		if err := editor.Click(locales.Text("Mango selector JSON")); err != nil {
			t.Fatal(err)
		}
		editor.Key(ui.Cmd, ui.KeyA)
		editor.Type(raw)
		editor.Frame()
	}
	originalPage, request := a.page, a.request
	input(`{"_id":1,"_id":2}`)
	if err := editor.Click(locales.Text("应用 Mango 查询")); err != nil {
		t.Fatal(err)
	}
	if a.selectorError == "" || a.page != originalPage || a.request != request || a.loading {
		t.Fatal("invalid native selector dispatched or discarded cached page")
	}
	input(`{"_id":{"$unsupported":1}}`)
	if err := editor.Click(locales.Text("应用 Mango 查询")); err != nil {
		t.Fatal(err)
	}
	end = time.Now().Add(30 * time.Second)
	for a.loading && time.Now().Before(end) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if a.loading || a.err != "CouchDB HTTP 400" || a.page != nil || b.page.Rows[0][0].Text != sibling {
		t.Fatal("complete Mango semantic error claimed interruption or published invalid results", a.err)
	}
	const selector = `{"_id":{"$in":["row_1","row_2"]},"amount":{"$eq":9007199254740993}}`
	input(selector)
	if err := editor.Click(locales.Text("应用 Mango 查询")); err != nil {
		t.Fatal(err)
	}
	wait(a)
	if len(a.page.Rows) != 1 || a.page.Rows[0][0].Text != "row_1" || a.page.QuerySQL != "POST /"+db+"/_find" || a.selectorError != "" {
		t.Fatal("public native selector lost exact JSON/range", a.err)
	}
	history := a.tools.history[0]
	if history.SQL != a.page.QuerySQL || history.Summary {
		t.Fatal("native selector history inferred SQL")
	}
	s.changeMySQLDataPage(ui.Services{}, a, 1)
	wait(a)
	if a.page.Rows[0][0].Text != "row_2" || b.page.Rows[0][0].Text != sibling {
		t.Fatal("Mango bookmark lost query or sibling identity")
	}
	page, request := a.page, a.request
	s.restoreDataHistory(a, history)
	if a.page != page || a.request != request || a.loading || a.selectorDraft != selector {
		t.Fatal("Mango history replayed a request")
	}
	s.applyDataSelector(ui.Services{}, a)
	wait(a)
	if a.offset != 0 || a.page.Rows[0][0].Text != "row_1" {
		t.Fatal("explicit Mango history application reused an old bookmark")
	}
	if folder := os.Getenv("NYADB_COUCHDB_DATA_IMAGES"); folder != "" {
		if err = os.MkdirAll(folder, 0700); err != nil {
			t.Fatal(err)
		}
		defer i18n.SetLocaleFunc(nil)
		for _, lang := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return lang })
			for _, width := range []int{1200, 900} {
				for _, selectorOpen := range []bool{false, true} {
					a.selectorOpen = selectorOpen
					tester := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.View(c, a) }, width, 720)
					tester.Frame()
					if _, ok := tester.Find(locales.TextIn(lang, "原生游标读取实时数据；翻页和重读期间数据变化可能导致行重复或遗漏。")); !ok {
						t.Fatal("native live warning missing", lang)
					}
					if mysqlDataPageMethod(a.page) != locales.TextIn(lang, "原生游标") {
						t.Fatal("native method mislabeled")
					}
					if err := tester.Click(locales.Text("查询信息操作")); err != nil {
						t.Fatal(err)
					}
					if err := tester.ChooseMenuItem(locales.Text("复制查询信息")); err != nil || tester.Clipboard() != a.page.QuerySQL {
						t.Fatal("native endpoint query information", err)
					}
					view := "native-shared-grid"
					if selectorOpen {
						view = "native-mango-grid"
						if rect, ok := tester.Find(locales.Text("应用 Mango 查询")); !ok || rect.Y+rect.H > 720 || rect.X+rect.W > float32(width)+1 {
							t.Fatal("localized Mango editor action clipped", lang, width, rect)
						}
					}
					file, e := os.Create(filepath.Join(folder, lang+"-"+strconv.Itoa(width)+"-"+view+".png"))
					if e != nil {
						t.Fatal(e)
					}
					e = png.Encode(file, tester.Image())
					file.Close()
					if e != nil {
						t.Fatal(e)
					}
				}
			}
		}
	}
	s.clearMySQLDataFilter(ui.Services{}, a)
	wait(a)
	if a.page.QuerySQL != "GET /"+db+"/_all_docs" || a.filterMode != "" {
		t.Fatal("clearing Mango query retained original selector/bookmark")
	}
	s.Load(ui.Services{}, a, true)
	active.ProfileID = 99
	var parked mysqlDataResult
	select {
	case parked = <-a.results:
	case <-time.After(30 * time.Second):
		t.Fatal("native parked page stalled")
	}
	original := a.page
	ready := make(chan mysqlDataResult, 1)
	ready <- parked
	a.results = ready
	s.Consume(ui.Services{})
	if !a.loading || a.page != original {
		t.Fatal("parked native page replaced cache")
	}
	active.ProfileID = p.ID
	s.Consume(ui.Services{})
	wait(a)
	s.Close(a)
	s.changeMySQLDataPage(ui.Services{}, b, 1)
	wait(b)
	active.Generation++
	s.Load(ui.Services{}, b, true)
	s.Consume(ui.Services{})
	end = time.Now().Add(30 * time.Second)
	for b.loading && time.Now().Before(end) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if b.loading || b.err == "" {
		t.Fatal("original document followed new generation")
	}
}
