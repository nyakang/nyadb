package dataworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"io"
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
	"github.com/nyakang/nyadb/internal/services/qdrant"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestQdrantNativeDataSharedGridPagingSelectorInspectorParkGenerationAndClose(t *testing.T) {
	parsed := qdrantDataFixture(t, "NYADB_QDRANT_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cfg, _ := connections.QdrantProfileConfig(parsed.Profile)
	admin, err := qdrant.Open(ctx, cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := "owned_grid_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	run := func(method, path string, body any) {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err = admin.Do(ctx, qdrant.Request{Method: method, Path: path, Body: raw}); err != nil {
			t.Fatal(err)
		}
	}
	run("PUT", "/collections/"+name, map[string]any{"vectors": map[string]any{"dense": map[string]any{"size": 2, "distance": "Dot"}}, "sparse_vectors": map[string]any{"sparse": map[string]any{}}})
	defer run("DELETE", "/collections/"+name, nil)
	var points []any
	for _, id := range []uint64{0, 9007199254740993, 18446744073709551615} {
		points = append(points, map[string]any{"id": id, "vector": map[string]any{"dense": []float64{1, 0}}, "payload": map[string]any{"n": json.Number("9007199254740993"), "memo": strings.Repeat("猫", 2000), "selected": id != 0, "null": nil}})
	}
	run("PUT", "/collections/"+name+"/points?wait=true", map[string]any{"points": points})
	handle, err := qdrant.OpenSession(ctx, parsed.Profile, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	object := (contract.NamespaceRef{ProfileID: parsed.Profile.ID, Catalog: name}).Object("points", "points")
	active := Session{ProfileID: parsed.Profile.ID, Generation: handle.Generation(), Connected: true}
	runtime := contract.ComposeData(handle)
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(id int64) Session { return Session{ProfileID: id, Generation: active.Generation, Connected: true} }, RuntimeFor: func(int64) Runtime { return runtime }, Inspector: InspectorDependencies{Session: func() Session { return active }}})
	defer s.CloseAll()
	a, b := s.OpenObject(1, object, handle.Capabilities()), s.OpenObject(2, object, handle.Capabilities())
	a.limit, b.limit = 1, 1
	wait := func(d *Document) {
		t.Helper()
		end := time.Now().Add(30 * time.Second)
		for d.Busy() && time.Now().Before(end) {
			s.Consume(ui.Services{})
			time.Sleep(time.Millisecond)
		}
		if d.Busy() || d.err != "" {
			t.Fatal("original native grid did not settle", d.err)
		}
	}
	s.Load(ui.Services{}, a, false)
	wait(a)
	s.Load(ui.Services{}, b, false)
	wait(b)
	if a.objectKind != "向量点" || a.page.Rows[0][0].Text != "0" || len(a.columns) != 4 || a.page.Rows[0][0].Value.Kind != contract.Integer || a.page.Editable == nil || *a.page.Editable || !a.page.LiveRead || a.page.PagingMode != "cursor" {
		t.Fatal("shared point projection wrong")
	}
	for _, column := range a.columns {
		if column.Sortable {
			t.Fatal("unsupported point sort exposed")
		}
	}
	if len(s.filterColumns(a, a.page.Columns)) != 0 || slices.Contains(a.exportFormats, "sql") || a.capabilities.Import || a.capabilities.EditableTables {
		t.Fatal("unsupported relational tools exposed")
	}
	if _, err = mysqlDataSQLSelection(a, false); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("point grid fabricated INSERT SQL", err)
	}
	s.changeMySQLDataPage(ui.Services{}, a, 1)
	wait(a)
	if a.page.Rows[0][0].Text != "9007199254740993" || b.page.Rows[0][0].Text != "0" {
		t.Fatal("point cursor/sibling lost")
	}
	s.changeMySQLDataPage(ui.Services{}, a, 0)
	wait(a)
	a.selectedRow, a.grid.Cells.Cursor = 0, "payload"
	panel := s.inspectorPanel(a)
	s.inspector.LoadInfo(panel, nil, false)
	s.inspector.LoadCount(panel, nil)
	s.inspector.LoadChunk(panel, nil)
	end := time.Now().Add(30 * time.Second)
	for (panel.InfoLoading() || panel.CountLoading() || panel.ChunkLoading()) && time.Now().Before(end) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if panel.Info() == nil || panel.Info().Engine != "Qdrant" || panel.Count() == nil || *panel.Count() != 3 || panel.ChunkError() != "" || panel.ChunkNext() != 1024 {
		t.Fatal("original native inspector", panel.InfoError(), panel.CountError(), panel.ChunkError())
	}
	tester := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.View(c, a) }, 1280, 800)
	if err = clickDataToolbar(tester, locales.Text("Qdrant 过滤")); err != nil {
		t.Fatal("native point filter unavailable", err)
	}
	input := func(raw string) {
		t.Helper()
		if err := tester.Click(locales.Text("Qdrant filter JSON")); err != nil {
			t.Fatal(err)
		}
		tester.Key(ui.Cmd, ui.KeyA)
		tester.Type(raw)
		tester.Frame()
	}
	page, request := a.page, a.request
	input(`{"must":[],"must":[]}`)
	if err = tester.Click(locales.Text("应用 Qdrant 过滤")); err != nil {
		t.Fatal(err)
	}
	if a.selectorError == "" || a.page != page || a.request != request || a.loading {
		t.Fatal("invalid native filter changed cache")
	}
	const filter = `{"must":[{"key":"selected","match":{"value":true}},{"key":"n","match":{"value":9007199254740993}}]}`
	input(filter)
	if err = tester.Click(locales.Text("应用 Qdrant 过滤")); err != nil {
		t.Fatal(err)
	}
	wait(a)
	if a.page.Rows[0][0].Text != "9007199254740993" || a.filterMode != "qdrant_filter" || a.page.QuerySQL != "POST /collections/"+name+"/points/scroll" || !strings.Contains(mysqlDataFilterStatus(a), "Qdrant") {
		t.Fatal("native filter/endpoint mislabeled")
	}
	history := a.tools.history[0]
	page, request = a.page, a.request
	s.restoreDataHistory(a, history)
	if a.loading || a.page != page || a.request != request || a.selectorDraft != filter {
		t.Fatal("filter history replayed")
	}
	s.applyDataSelector(ui.Services{}, a)
	wait(a)
	s.changeMySQLDataPage(ui.Services{}, a, 1)
	wait(a)
	if a.page.Rows[0][0].Text != "18446744073709551615" {
		t.Fatal("filtered cursor lost exact ID")
	}
	path := filepath.Join(t.TempDir(), "points.jsonl")
	s.SetFiles(func(string) (string, error) { return path, nil }, func(name string, write func(io.Writer) error) error {
		f, err := os.Create(name)
		if err != nil {
			return err
		}
		defer f.Close()
		return write(f)
	})
	s.ExportJSONL(ui.Services{}, a)
	wait(a)
	raw, err := os.ReadFile(path)
	if err != nil || bytes.Count(raw, []byte{'\n'}) != 3 || !bytes.Contains(raw, []byte("9007199254740993")) {
		t.Fatal("full native JSONL inherited filter/page", err)
	}
	if folder := os.Getenv("NYADB_QDRANT_DATA_IMAGES"); folder != "" {
		if err = os.MkdirAll(folder, 0700); err != nil {
			t.Fatal(err)
		}
		defer i18n.SetLocaleFunc(nil)
		for _, language := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return language })
			for _, size := range [][2]int{{1280, 800}, {720, 520}} {
				for _, view := range []string{"grid", "filter"} {
					a.selectorOpen = view == "filter"
					tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.View(c, a) }, size[0], size[1])
					tt.Frame()
					if !tt.HasText(locales.Text("原生游标读取实时数据；翻页和重读期间数据变化可能导致行重复或遗漏。")) {
						t.Fatal("live point pagination warning missing", language)
					}
					if view == "filter" {
						for _, label := range []string{"Qdrant filter JSON", "应用 Qdrant 过滤", "清除 Qdrant 过滤"} {
							rect, ok := tt.Find(locales.Text(label))
							if !ok || rect.X < 0 || rect.Y < 0 || rect.X+rect.W > float32(size[0])+1 || rect.Y+rect.H > float32(size[1])+1 {
								t.Fatal("native filter control clipped", language, size, label, rect)
							}
						}
					}
					f, err := os.Create(filepath.Join(folder, language+"-"+strconv.Itoa(size[0])+"x"+strconv.Itoa(size[1])+"-"+view+".png"))
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
	}
	s.Load(ui.Services{}, a, true)
	active.ProfileID = 999
	var parked mysqlDataResult
	select {
	case parked = <-a.results:
	case <-time.After(30 * time.Second):
		t.Fatal("native parked read stalled")
	}
	cached := a.page
	ready := make(chan mysqlDataResult, 1)
	ready <- parked
	a.results = ready
	s.Consume(ui.Services{})
	if !a.loading || a.page != cached {
		t.Fatal("parked read replaced original cache")
	}
	active.ProfileID = parsed.Profile.ID
	s.Consume(ui.Services{})
	wait(a)
	s.Close(a)
	if a.page != nil || a.runtimeLease != nil || panel.chunkLoader != nil {
		t.Fatal("point close retained caches or lease")
	}
	s.Load(ui.Services{}, b, true)
	select {
	case parked = <-b.results:
	case <-time.After(30 * time.Second):
		t.Fatal("generation read stalled")
	}
	cached = b.page
	active.Generation++
	ready = make(chan mysqlDataResult, 1)
	ready <- parked
	b.results = ready
	s.Consume(ui.Services{})
	if b.loading || b.page != cached || b.err == "" {
		t.Fatal("old-generation point read replaced cache")
	}
}
