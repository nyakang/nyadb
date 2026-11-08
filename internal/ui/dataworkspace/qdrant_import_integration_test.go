package dataworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/qdrant"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func capturePointImport(t *testing.T, view func(*ui.Context), phase, action string) {
	t.Helper()
	folder := os.Getenv("NYADB_QDRANT_IMPORT_IMAGES")
	if folder == "" {
		return
	}
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(view, size[0], size[1])
			tt.Frame()
			r, ok := tt.Find(locales.Text(action))
			if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
				t.Fatal("original point import action clipped", phase, language, action, r)
			}
			if err := os.MkdirAll(folder, 0700); err != nil {
				t.Fatal(err)
			}
			f, e := os.Create(filepath.Join(folder, language+"-"+strconv.Itoa(size[0])+"x"+strconv.Itoa(size[1])+"-import-"+phase+".png"))
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
}
func TestQdrantNativeDataPublicPointImportWizardJSONJSONLModesEvidenceParkAndGeneration(t *testing.T) {
	parsed := qdrantDataFixture(t, "NYADB_QDRANT_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cfg, _ := connections.QdrantProfileConfig(parsed.Profile)
	admin, e := qdrant.Open(ctx, cfg, parsed.Password)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	name := "owned_import_ui_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	run := func(method, path, body string) {
		t.Helper()
		if _, e := admin.Do(ctx, qdrant.Request{Method: method, Path: path, Body: []byte(body)}); e != nil {
			t.Fatal(e)
		}
	}
	run("PUT", "/collections/"+name, `{"vectors":{"size":2,"distance":"Dot"}}`)
	defer run("DELETE", "/collections/"+name, "")
	manager := dbruntime.New()
	dbruntime.RegisterBuiltins(manager, nil)
	defer manager.Close()
	connected, e := manager.ConnectSession(ctx, parsed.Profile, parsed.Password)
	if e != nil {
		t.Fatal(e)
	}
	active := Session{ProfileID: parsed.Profile.ID, Generation: connected.Generation(), Connected: true}
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(id int64) Session { return Session{ProfileID: id, Generation: active.Generation, Connected: true} }, RuntimeFor: manager.Data})
	defer s.CloseAll()
	d := s.OpenObject(1, manager.Namespace(parsed.Profile.ID, name).Object("points", "points"), connected.Capabilities())
	view := func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.View(c, d); s.Modals(c) }
	tt := ui.NewTester(view, 1280, 800)
	wait := func() {
		t.Helper()
		end := time.Now().Add(30 * time.Second)
		for d.Busy() && time.Now().Before(end) {
			tt.Frame()
			time.Sleep(time.Millisecond)
		}
		tt.Frame()
		if d.Busy() || d.err != "" {
			t.Fatal("original import did not settle", d.err, d.transfer.Err)
		}
	}
	click := func(label string) {
		t.Helper()
		if e := tt.Click(locales.Text(label)); e != nil {
			t.Fatal(e)
		}
		tt.Frame()
	}
	s.Load(ui.Services{}, d, true)
	wait()
	if !d.capabilities.Import || !s.runtimeFor(d).DocumentImport || s.documentImportColumn(d) != "point" {
		t.Fatal("public original point import contract absent")
	}
	var docs []string
	for i := 0; i < 251; i++ {
		docs = append(docs, fmt.Sprintf(`{"id":%d,"vector":[1,0],"payload":{"old":true,"n":9007199254740993}}`, i))
	}
	path := filepath.Join(t.TempDir(), "points.json")
	if e := os.WriteFile(path, []byte("["+strings.Join(docs, ",")+"]"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := clickDataToolbar(tt, locales.Text("导入")); e != nil {
		t.Fatal(e)
	}
	d.transfer.OpenDialog = func(mygo.OpenDialogOptions) ([]string, error) { return []string{path}, nil }
	capturePointImport(t, view, "source", "选择文件")
	click("选择文件")
	wait()
	if d.transfer.TotalRows != 251 || d.transfer.Input.Fields[0] != "point" || d.transfer.Mode != "insert_only" {
		t.Fatal("source mapped as relational document")
	}
	click("下一步")
	capturePointImport(t, view, "mode", "下一步")
	if !tt.HasText(locales.Text("仅插入新 ID 的点")) {
		t.Fatal("native mode chooser unavailable")
	}
	click("下一步")
	if e := tt.Click(locales.Text("输入目标名称以确认导入")); e != nil {
		t.Fatal(e)
	}
	tt.Type(name)
	tt.Frame()
	capturePointImport(t, view, "review", "确认导入")
	click("确认导入")
	wait()
	if !d.transfer.Uncertain || d.transfer.Completed || d.edit.Uncertain == nil || d.edit.LastAsync == nil || !d.transfer.Submitted || d.transfer.Input == nil {
		t.Fatal("receipt prematurely cleared source", d.transfer.Err)
	}
	capturePointImport(t, view, "receipt", "核验原点结果")
	click("批次写入证据")
	capturePointImport(t, view, "evidence", "复制批次写入回执")
	click("复制批次写入回执")
	if !strings.Contains(d.edit.LastAsync.ReceiptJSON, "operation_id") {
		t.Fatal("original batch receipt missing")
	}
	click("关闭")
	if e := clickDataToolbar(tt, locales.Text("核验原点结果")); e != nil {
		t.Fatal(e)
	}
	wait()
	if !d.transfer.Completed || d.transfer.Uncertain || d.edit.Uncertain != nil || d.edit.LastAsync.TaskJSON == "" || d.transfer.Input == nil {
		t.Fatal("whole original point source not verified", d.transfer.Err)
	}
	d.transfer.Open = true
	tt.Frame()
	capturePointImport(t, view, "verified", "关闭")
	click("关闭")
	if n, _, e := admin.Count(ctx, name, nil); e != nil || n != 251 {
		t.Fatal("native source count", n, e)
	}
	// The second source goes through the same picker with an explicit upsert mode.
	path = filepath.Join(t.TempDir(), "points.jsonl")
	if e := os.WriteFile(path, []byte("{\"id\":0,\"vector\":[0,1]}\n{\"id\":18446744073709551615,\"vector\":[1,0],\"payload\":{\"n\":9007199254740993}}\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := clickDataToolbar(tt, locales.Text("导入")); e != nil {
		t.Fatal(e)
	}
	d.transfer.Format = "JSONL"
	d.transfer.OpenDialog = func(mygo.OpenDialogOptions) ([]string, error) { return []string{path}, nil }
	click("选择文件")
	wait()
	click("下一步")
	d.transfer.Mode = "upsert"
	click("下一步")
	d.transfer.Confirmation = name
	importer := d.boundRuntime.Importer
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	d.boundRuntime.Importer = func(ctx context.Context, id int64, db, table string, r contract.DataImportRequest) (*contract.DataImportResult, error) {
		result, e := importer(ctx, id, db, table, r)
		close(entered)
		<-release
		return result, e
	}
	s.startDocumentImport(ui.Services{}, d)
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("native import not admitted")
	}
	active.ProfileID = 999
	close(release)
	var parked mysqlDataTransferResult
	select {
	case parked = <-d.transfer.Results:
	case <-time.After(30 * time.Second):
		t.Fatal("parked native source stalled")
	}
	ready := make(chan mysqlDataTransferResult, 1)
	ready <- parked
	d.transfer.Results = ready
	s.Consume(ui.Services{})
	if !d.transfer.Busy || d.transfer.Input == nil || d.transfer.Completed {
		t.Fatal("parked source applied on another profile")
	}
	active.ProfileID = parsed.Profile.ID
	s.Consume(ui.Services{})
	if !d.transfer.Uncertain || d.transfer.Input == nil {
		t.Fatal("original parked receipt lost")
	}
	s.VerifySave(ui.Services{}, d)
	wait()
	if !d.transfer.Completed {
		t.Fatal("JSONL original upsert not verified", d.transfer.Err)
	}
	actual, _, e := admin.ReadPoint(ctx, name, json.RawMessage(`0`))
	if e != nil || string(actual.Payload) != "{}" {
		t.Fatal("JSONL whole point replacement retained old fields", e)
	}
	d.boundRuntime.Importer = importer
	d.transfer.Open = false
	s.closeDataTransfer(d)
	// A real completed native result from another published generation retains its source.
	s.openDataImport(d)
	d.transfer.Input, _ = parseDocumentImport(strings.NewReader(`{"id":9999,"vector":[1,0]}`), "JSON")
	d.transfer.TotalRows = 1
	d.transfer.Mode = "upsert"
	d.transfer.Confirmation = name
	s.startDocumentImport(ui.Services{}, d)
	select {
	case parked = <-d.transfer.Results:
	case <-time.After(30 * time.Second):
		t.Fatal("native generation import stalled")
	}
	replacement, err := manager.ConnectSession(ctx, parsed.Profile, parsed.Password)
	if err != nil || replacement.Generation() <= active.Generation {
		t.Fatal("published replacement generation", err)
	}
	active.Generation = replacement.Generation()
	ready = make(chan mysqlDataTransferResult, 1)
	ready <- parked
	d.transfer.Results = ready
	s.Consume(ui.Services{})
	if d.transfer.Busy || !d.transfer.Uncertain || d.transfer.Input == nil || d.edit.Uncertain == nil || d.transfer.Completed {
		t.Fatal("stale generation cleared original source")
	}
	s.startDocumentImport(ui.Services{}, d)
	if d.transfer.Busy {
		t.Fatal("stale source replayed")
	}
	s.Close(d)
	if d.transfer.Input != nil || d.runtimeLease != nil || d.edit.LastAsync != nil {
		t.Fatal("close retained source or original lease")
	}
	t.Log("Published original provider, actual JSON/JSONL picker, native mode/review/confirm, exact IDs, original batch receipt, bounded whole-source readback, localized evidence, parked and generation source retention verified")
}
