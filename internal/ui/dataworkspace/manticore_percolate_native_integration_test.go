package dataworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/manticore"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestManticoreNativePercolateReadOnlyGridFieldsParkingAndRecovery(t *testing.T) {
	if os.Getenv("NYADB_MANTICORE_IT") != "1" {
		t.Skip("requires owned real Manticore fixture")
	}
	host, port, e := net.SplitHostPort(os.Getenv("NYADB_MANTICORE_ENDPOINT"))
	if e != nil {
		t.Fatal(e)
	}
	n, e := strconv.Atoi(port)
	if e != nil {
		t.Fatal(e)
	}
	cfg := connections.ManticoreConfig{Host: host, Port: uint16(n), Username: "root", QueryTimeoutSeconds: 20}
	current, e := manticore.Open(context.Background(), 7601, cfg, "")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { current.Close() }()
	table := fmt.Sprintf("nyadb_pqui76_%d", time.Now().UnixNano())
	q, e := current.ReserveQuery(1)
	if e != nil {
		t.Fatal(e)
	}
	defer q.Release()
	run := func(sql string) {
		t.Helper()
		if _, e := q.Execute(context.Background(), "Manticore", sql); e != nil {
			t.Fatal(e)
		}
	}
	run("CREATE TABLE " + table + " (title text, tag string) type='pq'")
	long := " " + strings.Repeat("猫🙂", 1025)
	for start := 1; start <= 2008; start += 250 {
		var values []string
		for id := start; id < start+250 && id <= 2008; id++ {
			tags := fmt.Sprintf("group%d", id%3)
			if id == 1 {
				tags = long
			}
			values = append(values, fmt.Sprintf("(%d,'hello','%s','')", id, tags))
		}
		run("INSERT INTO " + table + " (id,query,tags,filters) VALUES " + strings.Join(values, ","))
	}
	defer func() {
		cleanup, err := current.ReserveQuery(99)
		if err != nil {
			t.Error(err)
			return
		}
		defer cleanup.Release()
		if _, err = cleanup.Execute(context.Background(), "Manticore", "DROP TABLE "+table); err != nil {
			t.Error(err)
		}
	}()
	active := Session{ProfileID: 7601, Generation: 1, Connected: true}
	originalSession := func(id int64) Session {
		return Session{ProfileID: id, Generation: active.Generation, Connected: id == 7601 && current.Alive()}
	}
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: originalSession, RuntimeFor: func(id int64) Runtime {
		if id != 7601 {
			return Runtime{}
		}
		return Runtime{Capabilities: current.DataDocumentCapabilities(), ReserveDocument: current.ReserveDataDocument}
	}, Inspector: InspectorDependencies{Session: func() Session { return active }, Profile: func(id int64) (connections.Profile, bool) {
		return connections.Profile{ID: 7601, Name: "Manticore percolate", Host: host, Port: uint16(n)}, id == 7601
	}}})
	defer s.CloseAll()
	object := current.Namespace("Manticore").Object("TABLE", table)
	d := s.OpenObject(1, object, current.DataDocumentCapabilities())
	d.limit = 7
	wait := func(done func() bool) {
		t.Helper()
		until := time.Now().Add(15 * time.Second)
		for !done() && time.Now().Before(until) {
			s.Consume(ui.Services{})
			time.Sleep(time.Millisecond)
		}
		s.Consume(ui.Services{})
		if !done() {
			t.Fatal("original percolate workspace did not settle")
		}
	}
	s.Load(ui.Services{}, d, false)
	wait(func() bool { return !d.loading })
	if d.err != "" || d.page == nil || len(d.page.Rows) != 7 || !d.page.HasMore || d.allowsDataEdit("insert") || d.allowsDataEdit("delete") || mysqlDataCanEdit(d, 0, 2) || mysqlDataCanEdit(d, 0, 1) || mysqlDataCanEdit(d, 0, 0) {
		t.Fatal("percolate original mode permissions", d.err)
	}

	if d.editable() || !slices.Equal(d.page.Columns, []string{"id", "query", "tags", "filters"}) || !slices.Equal(s.filterColumns(d, d.page.Columns), d.page.Columns) {
		t.Fatal("read-only stored-query mode lost")
	}
	for _, c := range d.columns {
		if !c.Sortable {
			t.Fatal("native string sort disabled", c.ID)
		}
	}
	s.stageMySQLDataRecord(ui.Services{}, d, nil)
	s.deleteMySQLDataRecords(ui.Services{}, d, 0)
	if d.HasDraft() || d.edit.Editor != nil {
		t.Fatal("read-only actions created a draft")
	}
	s.changeMySQLDataPage(ui.Services{}, d, 1000)
	wait(func() bool { return !d.loading })
	if d.err != "" || d.page.Rows[0][0].Text != "1001" {
		t.Fatal(d.err)
	}
	s.changeMySQLDataPage(ui.Services{}, d, 0)
	wait(func() bool { return !d.loading })
	cache := d.page
	s.Load(ui.Services{}, d, true)
	active.ProfileID = 7699
	var originalPage mysqlDataResult
	select {
	case originalPage = <-d.results:
	case <-time.After(15 * time.Second):
		t.Fatal("parked page missing")
	}
	if originalPage.err != nil {
		t.Fatal(originalPage.err)
	}
	parkedPage := make(chan mysqlDataResult, 1)
	parkedPage <- originalPage
	d.results = parkedPage
	s.Consume(ui.Services{})
	if !d.loading || d.page != cache || len(parkedPage) != 1 {
		t.Fatal("page published to foreign profile")
	}
	active.ProfileID = 7601
	s.Consume(ui.Services{})
	if d.loading || d.err != "" || d.page == cache {
		t.Fatal("original page did not publish")
	}
	d.selectedRow, d.grid.Cells.Cursor = 0, "tags"
	panel := s.inspectorPanel(d)
	s.inspector.LoadInfo(panel, nil, false)
	s.inspector.LoadCount(panel, nil)
	wait(func() bool { return !panel.InfoLoading() && !panel.CountLoading() })
	if panel.Count() == nil || *panel.Count() != 2008 {
		t.Fatal("stored-query count missing")
	}

	s.inspector.ShowField(panel)
	var calls atomic.Int32
	original := d.boundRuntime.Chunk
	if original == nil {
		t.Fatal("missing native field reader")
	}
	d.boundRuntime.Chunk = func(ctx context.Context, id int64, database, table, field string, ref contract.RowRef, offset int64, limit int) (*contract.FieldChunk, error) {
		calls.Add(1)
		return original(ctx, id, database, table, field, ref, offset, limit)
	}
	locale := "zh-Hans"
	i18n.SetLocaleFunc(func() string { return locale })
	defer i18n.SetLocaleFunc(nil)
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.Consume(c.Services())
		ui.Row(c).Fill().Children(func() {
			ui.Box(c).Grow(1).MinWidth(0).FillHeight().Children(func() { s.View(c, d) })
			ui.Box(c).Width(320).FillHeight().Children(func() { s.InspectorView(c, d) })
		})
		s.Modals(c)
	}, 1280, 800)
	capture := func(name string) {
		t.Helper()
		tt.Frame()
		if dir := os.Getenv("NYADB_MANTICORE_EVIDENCE"); dir != "" {
			f, e := os.Create(filepath.Join(dir, name+".png"))
			if e != nil {
				t.Fatal(e)
			}
			e = png.Encode(f, tt.Image())
			f.Close()
			if e != nil {
				t.Fatal(e)
			}
		}
	}

	for _, lang := range []string{"zh-Hans", "en"} {
		locale = lang
		capture(lang + "-percolate-field-unread")
		if !tt.HasText(locales.Text("查看片段会读取完整字段并检查是否发生变化；每次显示最多 1024 字符/字节。")) {
			t.Fatal(tt.Texts())
		}
		if !tt.HasText("string") {
			t.Fatal("original native field properties missing", tt.Texts())
		}
		for _, label := range []string{"不是 NULL", "默认值", "自增", "主键", "SQL NULL / 未设置"} {
			if tt.HasText(locales.Text(label)) {
				t.Fatal("native field invented relational attributes", label)
			}
		}
	}
	if calls.Load() != 0 || panel.ChunkLoading() {
		t.Fatal("render implicitly read full field")
	}
	locale = "zh-Hans"
	tt.Frame()
	if e = tt.Click(locales.Text("读取所选字段下一段")); e != nil {
		t.Fatal(e)
	}
	wait(func() bool { return !panel.ChunkLoading() })
	if panel.ChunkNext() != 1024 || panel.ChunkTotal() != 2051 || panel.ChunkText() != string([]rune(long)[:1024]) {
		t.Fatal(panel.ChunkError(), panel.ChunkNext())
	}
	capture("zh-Hans-percolate-field-first")
	if e = tt.Click(locales.Text("复制此片段")); e != nil {
		t.Fatal(e)
	}
	if tt.Clipboard() != panel.ChunkText() {
		t.Fatal("fragment clipboard changed original text")
	}
	s.inspector.LoadChunk(panel, nil)
	channel := panel.chunkResults
	active.ProfileID = 7699
	var result chunkResult
	select {
	case result = <-channel:
	case <-time.After(15 * time.Second):
		t.Fatal("original field did not complete")
	}
	if result.err != nil || result.chunk == nil {
		t.Fatal(result.err)
	}
	parked := make(chan chunkResult, 1)
	parked <- result
	panel.chunkResults = parked
	s.Consume(ui.Services{})
	if len(parked) != 1 || !panel.ChunkLoading() || panel.ChunkNext() != 1024 {
		t.Fatal("parked field published to another profile")
	}
	active.ProfileID = 7601
	s.Consume(ui.Services{})
	if panel.ChunkNext() != 2048 || panel.ChunkText() != string([]rune(long)[1024:2048]) {
		t.Fatal("original parked fragment lost", panel.ChunkError())
	}
	locale = "en"
	capture("en-percolate-field-second")
	if e = tt.Click(locales.Text("读取所选字段下一段")); e != nil {
		t.Fatal(e)
	}
	wait(func() bool { return !panel.ChunkLoading() })
	if panel.ChunkNext() != 2051 || panel.ChunkText() != string([]rune(long)[2048:]) || !panel.chunkDone {
		t.Fatal(panel.ChunkError(), panel.ChunkText())
	}
	capture("en-percolate-field-final")
	// A changed cell retires the field presentation but never launches a read.
	priorCalls := calls.Load()
	d.grid.Cells.Cursor = "query"
	s.Consume(ui.Services{})
	tt.Frame()
	if panel.ChunkText() != "" || panel.ChunkNext() != 0 || calls.Load() != priorCalls {
		t.Fatal("selection reused or implicitly read field")
	}
	s.inspector.LoadChunk(panel, nil)
	wait(func() bool { return !panel.ChunkLoading() })
	if panel.ChunkText() != "hello" {
		t.Fatal(panel.ChunkError(), panel.ChunkText())
	}
	capture("en-percolate-field-scalar")
	// A real old-generation completion remains separate from a fresh connection.
	d.grid.Cells.Cursor = "tags"
	s.Consume(ui.Services{})
	s.inspector.LoadChunk(panel, nil)
	select {
	case result = <-panel.chunkResults:
	case <-time.After(15 * time.Second):
		t.Fatal("original field did not complete")
	}
	if result.err != nil {
		t.Fatal(result.err)
	}
	if e = current.Close(); e != nil {
		t.Fatal(e)
	}
	current, e = manticore.Open(context.Background(), 7601, cfg, "")
	if e != nil {
		t.Fatal(e)
	}
	active.Generation++
	pending := make(chan chunkResult, 1)
	pending <- result
	panel.chunkResults = pending
	s.Consume(ui.Services{})
	if panel.ChunkLoading() || panel.ChunkText() != "" || panel.ChunkError() == "" {
		t.Fatal("retired physical generation published field")
	}
	s.Close(d)
	d = s.OpenObject(2, object, current.DataDocumentCapabilities())
	d.limit = 7
	s.Load(ui.Services{}, d, false)
	wait(func() bool { return !d.loading })
	if d.err != "" {
		t.Fatal(d.err)
	}
	d.selectedRow, d.grid.Cells.Cursor = 0, "tags"
	panel = s.inspectorPanel(d)
	s.inspector.ShowField(panel)
	s.inspector.LoadChunk(panel, nil)
	wait(func() bool { return !panel.ChunkLoading() })
	if panel.ChunkNext() != 1024 || panel.ChunkText() != string([]rune(long)[:1024]) {
		t.Fatal(panel.ChunkError())
	}
	capture("en-percolate-field-recovered")
	if dir := os.Getenv("NYADB_MANTICORE_EVIDENCE"); dir != "" {
		raw, e := json.MarshalIndent(map[string]any{"generation": active.Generation, "next": panel.ChunkNext(), "total": panel.ChunkTotal(), "text": panel.ChunkText()}, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, "native-percolate-field-inspector.json"), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
}
