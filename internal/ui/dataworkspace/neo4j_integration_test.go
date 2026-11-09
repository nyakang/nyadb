package dataworkspace

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"image/png"
	"io"
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
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/neo4j"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type neo4jDataFixture struct {
	Config                 connections.Neo4jConfig
	Secret, Nonce, Version string
}

func neo4jDataFixtureTest(t *testing.T) neo4jDataFixture {
	t.Helper()
	if os.Getenv("NYADB_NEO4J_GRID_IT") != "1" {
		t.Skip("requires owned public graph grid fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_NEO4J_CONFIG_FILE"))
	if err != nil {
		t.Fatal("cannot read private fixture")
	}
	var f neo4jDataFixture
	if json.Unmarshal(raw, &f) != nil || len(f.Nonce) != 32 || f.Version != "5.26.21" {
		t.Fatal("invalid fixture")
	}
	return f
}
func neo4jDataProfile(t *testing.T, f neo4jDataFixture) connections.Profile {
	t.Helper()
	u, err := url.Parse(f.Config.URI)
	if err != nil {
		t.Fatal("invalid fixture URI")
	}
	u.Path = "/" + f.Config.Database
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
	parsed, err := connections.ParseURI(u.String())
	if err != nil {
		t.Fatal("invalid fixture profile")
	}
	parsed.Profile.ID = 86
	return parsed.Profile
}
func neo4jDataRun(t *testing.T, c *neo4j.Client, q string, params map[string]any) contract.GraphResult {
	t.Helper()
	request, err := c.Prepare(neo4j.Request{Query: q, Parameters: params})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Execute(context.Background(), request)
	if err != nil || !result.Completed {
		t.Fatal("native graph observation failed", err)
	}
	return result
}
func waitNeo4jData(t *testing.T, s *State, d *Document) {
	t.Helper()
	end := time.Now().Add(30 * time.Second)
	for d.Busy() && time.Now().Before(end) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if d.Busy() {
		t.Fatal("original graph data executor did not acknowledge")
	}
}
func parkNeo4jData(t *testing.T, d *Document) {
	t.Helper()
	select {
	case result := <-d.results:
		out := make(chan mysqlDataResult, 1)
		out <- result
		d.results = out
	case <-time.After(30 * time.Second):
		t.Fatal("original graph page stalled")
	}
}
func captureNeo4jData(t *testing.T, s *State, d *Document, name string) {
	t.Helper()
	output := os.Getenv("NYADB_NEO4J_GRID_IMAGES")
	if output == "" {
		return
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.View(c, d) }, size[0], size[1])
			tt.Frame()
			if tt.HasText(locales.Text("批量编辑")) {
				t.Fatal("read-only graph grid exposed batch editing")
			}
			if !d.readRetired && !tt.HasText(locales.Text("原生游标读取实时数据；翻页和重读期间数据变化可能导致行重复或遗漏。")) {
				t.Fatal("live graph cursor notice missing", language)
			}
			if d.readRetired {
				rect, ok := tt.Find(locales.Text("重新打开数据"))
				if !ok || rect.X < 0 || rect.Y < 0 || rect.X+rect.W > float32(size[0])+1 || rect.Y+rect.H > float32(size[1])+1 {
					t.Fatal("explicit reopen control clipped", language, size, rect)
				}
			}
			f, err := os.Create(filepath.Join(output, language+"-"+strconv.Itoa(size[0])+"x"+strconv.Itoa(size[1])+"-"+name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(f, tt.Image())
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestNeo4jNativePublicDataSharedGridPagingInspectorExportParkGenerationAndClose(t *testing.T) {
	f := neo4jDataFixtureTest(t)
	admin, err := neo4j.Open(context.Background(), f.Config, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := "NyaGrid_" + f.Nonce + "_猫`\\u0060"
	params := map[string]any{"name": name, "exact": int64(9223372036854775807), "memo": strings.Repeat("猫", 2000)}
	defer neo4jDataRun(t, admin, "MATCH (n) WHERE $name IN labels(n) DETACH DELETE n", map[string]any{"name": name})
	neo4jDataRun(t, admin, "UNWIND range(0,1204) AS i CREATE (n:$($name) {i:i,exact:$exact,memo:CASE i % 10 WHEN 0 THEN $memo ELSE '' END}) RETURN count(n)", params)
	neo4jDataRun(t, admin, "MATCH (a),(b) WHERE $name IN labels(a) AND $name IN labels(b) AND a.i=0 AND b.i=1 CREATE (a)-[r:$($name) {exact:$exact}]->(b) RETURN r", params)
	profile := neo4jDataProfile(t, f)
	manager := dbruntime.New()
	dbruntime.RegisterBuiltins(manager, nil)
	defer manager.Close()
	handle, err := manager.ConnectSession(context.Background(), profile, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	active := Session{ProfileID: profile.ID, Generation: handle.Generation(), Connected: true}
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(id int64) Session { return Session{ProfileID: id, Generation: active.Generation, Connected: true} }, RuntimeFor: manager.Data, Inspector: InspectorDependencies{Session: func() Session { return active }, Profile: func(int64) (connections.Profile, bool) { return profile, true }}})
	defer s.CloseAll()
	namespace := manager.Namespace(profile.ID, "neo4j")
	a := s.OpenObject(1, namespace.Object("label", name), handle.Capabilities())
	b := s.OpenObject(2, namespace.Object("relationship", name), handle.Capabilities())
	a.limit, b.limit = 1000, 1
	s.Load(ui.Services{}, a, false)
	waitNeo4jData(t, s, a)
	s.Load(ui.Services{}, b, false)
	waitNeo4jData(t, s, b)
	if a.err != "" || b.err != "" {
		t.Fatal("native shared graph grid failed", a.err, b.err)
	}
	if a.Kind() != "图标签" || b.Kind() != "关系类型" || len(a.page.Rows) != 1000 || !a.page.HasMore || len(b.page.Rows) != 1 || !a.capabilities.Documents || !a.capabilities.JSONL || a.capabilities.EditableTables || a.capabilities.Import || a.capabilities.Transactions || !a.boundRuntime.PageFailureRequiresReopen {
		t.Fatal("public graph projection/capabilities wrong")
	}
	if !slices.Equal(b.page.Columns, []string{"element_id", "type", "start_element_id", "end_element_id", "properties", "relationship"}) || b.page.Rows[0][1].Text != name || b.page.Rows[0][2].Text == "" || b.page.Rows[0][3].Text == "" {
		t.Fatal("relationship lost type or endpoints")
	}
	for _, d := range []*Document{a, b} {
		if d.page.Editable == nil || *d.page.Editable || len(s.filterColumns(d, d.page.Columns)) != 0 || slices.Contains(d.exportFormats, "sql") {
			t.Fatal("unsupported relational graph tools exposed")
		}
		for _, column := range d.columns {
			if column.Sortable {
				t.Fatal("unsupported graph sort exposed")
			}
		}
		if _, err := mysqlDataSQLSelection(d, false); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal("graph grid fabricated SQL")
		}
	}
	first := a.page.Rows[0][0].Text
	s.changeMySQLDataPage(ui.Services{}, a, 1000)
	waitNeo4jData(t, s, a)
	if a.err != "" || len(a.page.Rows) != 205 || a.page.HasMore || b.page.Rows[0][1].Text != name {
		t.Fatal("graph cursor crossed kind or lost final page", a.err)
	}
	s.changeMySQLDataPage(ui.Services{}, a, 0)
	waitNeo4jData(t, s, a)
	if a.err != "" || a.page.Rows[0][0].Text != first {
		t.Fatal("graph back boundary failed", a.err)
	}
	selected := -1
	for i, row := range a.page.Rows {
		if row[2].Truncated {
			selected = i
			break
		}
	}
	if selected < 0 {
		t.Fatal("long graph property preview missing")
	}
	a.selectedRow, a.grid.Cells.Cursor = selected, "properties"
	panel := s.inspectorPanel(a)
	s.inspector.LoadInfo(panel, nil, false)
	s.inspector.LoadCount(panel, nil)
	s.inspector.LoadChunk(panel, nil)
	end := time.Now().Add(30 * time.Second)
	for (panel.InfoLoading() || panel.CountLoading() || panel.ChunkLoading()) && time.Now().Before(end) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if panel.Info() == nil || panel.Info().Kind != "label" || panel.Count() == nil || *panel.Count() != 1205 || panel.ChunkError() != "" || panel.ChunkNext() != 1024 {
		t.Fatal("native graph inspector failed", panel.InfoError(), panel.CountError(), panel.ChunkError())
	}
	complete, err := s.runtimeFor(a).RowReader(context.Background(), profile.ID, "neo4j", name, *a.page.RowRefs[selected])
	if err != nil || complete.Values["properties"].Kind != contract.JSON || !strings.Contains(complete.Values["properties"].Text, "9223372036854775807") {
		t.Fatal("typed graph property narrowed", err)
	}
	output := filepath.Join(t.TempDir(), "nodes.jsonl")
	s.SetFiles(func(string) (string, error) { return output, nil }, func(path string, write func(io.Writer) error) error {
		return mysqlDataAtomicFile(context.Background(), path, write)
	})
	s.ExportJSONL(ui.Services{}, a)
	waitNeo4jData(t, s, a)
	raw, err := os.ReadFile(output)
	if err != nil || bytes.Count(raw, []byte{'\n'}) != 1205 || !bytes.Contains(raw, []byte("9223372036854775807")) {
		t.Fatal("complete graph JSONL truncated", err)
	}
	s.openDataExport(a)
	a.transfer.Scope, a.transfer.ExportPath = "all", filepath.Join(t.TempDir(), "nodes.csv")
	path := a.transfer.ExportPath
	s.startDataExport(ui.Services{}, a)
	waitNeo4jData(t, s, a)
	table, err := os.Open(path)
	if err != nil {
		t.Fatal("tabular graph export failed", err, a.transfer.Err)
	}
	records, err := csv.NewReader(table).ReadAll()
	table.Close()
	if err != nil || len(records) != 1206 || !strings.Contains(records[1][2], "9223372036854775807") {
		t.Fatal("complete tabular graph export narrowed", err)
	}
	captureNeo4jData(t, s, a, "nodes")
	captureNeo4jData(t, s, b, "relationships")
	s.Load(ui.Services{}, a, true)
	active.ProfileID = 999
	parkNeo4jData(t, a)
	cached := a.page
	s.Consume(ui.Services{})
	if !a.loading || a.page != cached {
		t.Fatal("parked graph result published")
	}
	active.ProfileID = profile.ID
	s.Consume(ui.Services{})
	waitNeo4jData(t, s, a)
	s.Load(ui.Services{}, a, true)
	parkNeo4jData(t, a)
	cached = a.page
	replacement, err := manager.ConnectSession(context.Background(), profile, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	active.Generation = replacement.Generation()
	s.Consume(ui.Services{})
	if a.loading || a.page != cached || a.err == "" {
		t.Fatal("replaced public generation overwrote original graph page")
	}
	s.Close(a)
	s.Close(b)
	handle.Close()
	if a.page != nil || a.runtimeLease != nil || panel.chunkLoader != nil || !manager.IsConnected(profile.ID) || manager.Generation(profile.ID) != replacement.Generation() {
		t.Fatal("old graph documents retained resources or closed replacement")
	}
}
