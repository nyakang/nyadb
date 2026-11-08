package dataworkspace

import (
	"context"
	"encoding/json"
	"image/png"
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
	"github.com/nyakang/nyadb/internal/services/qdrant"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestQdrantNativeDataSharedGridFullPointCRUDParkedSaveVerificationEvidenceAndGeneration(t *testing.T) {
	parsed := qdrantDataFixture(t, "NYADB_QDRANT_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cfg, _ := connections.QdrantProfileConfig(parsed.Profile)
	admin, e := qdrant.Open(ctx, cfg, parsed.Password)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	name := "owned_grid_write_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	run := func(method, path string, body any) {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, e = json.Marshal(body)
			if e != nil {
				t.Fatal(e)
			}
		}
		if _, e = admin.Do(ctx, qdrant.Request{Method: method, Path: path, Body: raw}); e != nil {
			t.Fatal(e)
		}
	}
	run("PUT", "/collections/"+name, map[string]any{"vectors": map[string]any{"size": 2, "distance": "Cosine"}})
	defer run("DELETE", "/collections/"+name, nil)
	run("PUT", "/collections/"+name+"/points?wait=true", map[string]any{"points": []any{map[string]any{"id": 1, "vector": []float64{1, 0}, "payload": map[string]any{"memo": strings.Repeat("猫", 2000)}}, map[string]any{"id": 2, "vector": []float64{0, 1}}}})
	handle, e := qdrant.OpenSession(ctx, parsed.Profile, parsed.Password)
	if e != nil {
		t.Fatal(e)
	}
	defer handle.Close()
	active := Session{ProfileID: parsed.Profile.ID, Generation: handle.Generation(), Connected: true}
	runtime := contract.ComposeData(handle)
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(id int64) Session { return Session{ProfileID: id, Generation: active.Generation, Connected: true} }, RuntimeFor: func(int64) Runtime { return runtime }})
	defer s.CloseAll()
	object := contract.NamespaceRef{ProfileID: parsed.Profile.ID, Catalog: name}.Object("points", "points")
	d := s.OpenObject(1, object, handle.Capabilities())
	s.deps.CloseDocument = func(_ ui.Services, original *Document) { s.Close(original) }
	wait := func() {
		t.Helper()
		end := time.Now().Add(30 * time.Second)
		for d.Busy() && time.Now().Before(end) {
			s.Consume(ui.Services{})
			time.Sleep(time.Millisecond)
		}
		if d.Busy() || d.err != "" {
			t.Fatal("original point grid stalled", d.err, d.edit.Err)
		}
	}
	s.Load(ui.Services{}, d, false)
	wait()
	if !d.edit.Batch || !d.page.Rows[0][3].Truncated {
		t.Fatal("native batch/full point preview missing")
	}
	for col := 0; col < 3; col++ {
		s.beginMySQLCellEdit(ui.Services{}, d, 0, col, false)
		if d.edit.Editor != nil {
			t.Fatal("derived projection became editable")
		}
	}
	s.beginMySQLCellEdit(ui.Services{}, d, 0, 3, true)
	wait()
	if d.edit.Editor == nil || len(d.edit.Editor.Text) <= 1024 || !strings.Contains(d.edit.Editor.Text, strings.Repeat("猫", 2000)) || d.edit.Err != "" {
		t.Fatal("actual complete point edit read rejected snapshot", d.edit.Err)
	}
	d.edit.Editor.Text = `{"id":1,"vector":[3,4],"payload":{"n":9007199254740993}}`
	s.finishMySQLCellEdit(d, true)
	s.addMySQLDataRecord(ui.Services{}, d)
	if d.edit.Editor == nil || d.edit.Editor.Field != "point" || typedKind(d.edit.Editor.Kind) != contract.JSON {
		t.Fatal("new point did not focus JSON field")
	}
	d.edit.Editor.Text = `{"id":18446744073709551615,"vector":[1,0],"payload":null}`
	s.finishMySQLCellEdit(d, true)
	d.selection.Clear()
	d.selection.Add(1)
	s.deleteMySQLDataRecords(ui.Services{}, d, 1)
	if len(d.edit.Drafts) != 3 || !s.CanSave(d) {
		t.Fatal("mixed point drafts unavailable")
	}
	cached := d.page
	s.Save(ui.Services{}, d)
	var parked mysqlEditResult
	select {
	case parked = <-d.edit.Results:
	case <-time.After(30 * time.Second):
		t.Fatal("point batch stalled")
	}
	active.ProfileID = 999
	ready := make(chan mysqlEditResult, 1)
	ready <- parked
	d.edit.Results = ready
	s.Consume(ui.Services{})
	if !d.edit.Busy || d.page != cached || len(d.edit.Drafts) != 3 {
		t.Fatal("parked write changed original draft/grid")
	}
	active.ProfileID = parsed.Profile.ID
	s.Consume(ui.Services{})
	if d.edit.Uncertain == nil || d.edit.LastAsync == nil || d.edit.LastAsync.Status != "completed" || !d.HasDraft() || s.CanSave(d) {
		t.Fatal("native receipt prematurely accepted draft", d.edit.Err)
	}
	s.Save(ui.Services{}, d)
	s.Load(ui.Services{}, d, true)
	s.discardMySQLData(d)
	if d.Busy() || len(d.edit.Drafts) != 3 || d.page != cached {
		t.Fatal("pending batch replayed/refreshed/discarded")
	}
	if folder := os.Getenv("NYADB_QDRANT_DATA_IMAGES"); folder != "" {
		defer i18n.SetLocaleFunc(nil)
		for _, language := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return language })
			for _, size := range [][2]int{{1280, 800}, {720, 520}} {
				for _, view := range []string{"write", "write-evidence"} {
					d.edit.AsyncEvidenceOpen = view == "write-evidence"
					tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.View(c, d); s.Modals(c) }, size[0], size[1])
					tt.Frame()
					if view == "write-evidence" {
						for _, key := range []string{"批次写入证据", "原批次写入回执 JSON", "复制批次写入回执"} {
							rect, ok := tt.Find(locales.Text(key))
							if !ok || rect.X < 0 || rect.Y < 0 || rect.X+rect.W > float32(size[0])+1 || rect.Y+rect.H > float32(size[1])+1 {
								t.Fatal("point evidence clipped", language, size, key, rect)
							}
						}
					}
					f, e := os.Create(filepath.Join(folder, language+"-"+strconv.Itoa(size[0])+"x"+strconv.Itoa(size[1])+"-"+view+".png"))
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
	}
	i18n.SetLocaleFunc(nil)
	d.edit.AsyncEvidenceOpen = false
	tester := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.View(c, d); s.Modals(c) }, 1280, 800)
	if e = clickDataToolbar(tester, locales.Text("核验原点结果")); e != nil {
		t.Fatal("explicit original point verification unavailable", e)
	}
	wait()
	if d.HasDraft() || d.edit.Uncertain != nil || d.edit.LastAsync == nil || d.edit.LastAsync.TaskJSON == "" || len(d.page.Rows) != 2 {
		t.Fatal("verified point outcome/refresh failed", d.edit.Err)
	}
	actual, _, e := admin.ReadPoint(ctx, name, []byte(`1`))
	if e != nil || !strings.Contains(string(actual.Payload), "9007199254740993") || strings.Contains(string(actual.Payload), "memo") {
		t.Fatal("native full point replacement", e)
	}
	s.EditCell(ui.Services{}, d, 0, "point", contract.Cell{Kind: contract.JSON, Text: `{"id":1,"vector":[1,0]}`})
	s.Save(ui.Services{}, d)
	select {
	case parked = <-d.edit.Results:
	case <-time.After(30 * time.Second):
		t.Fatal("generation write stalled")
	}
	active.Generation++
	ready = make(chan mysqlEditResult, 1)
	ready <- parked
	d.edit.Results = ready
	s.Consume(ui.Services{})
	if d.edit.Busy || d.edit.Uncertain == nil || !d.edit.Uncertain.CommitUncertain || !d.HasDraft() || d.edit.LastAsync == nil {
		t.Fatal("stale native write not retained")
	}
	s.Close(d)
	if d.edit.LastAsync != nil || d.runtimeLease != nil || d.page != nil {
		t.Fatal("close retained write evidence/lease")
	}
	t.Log("Shared grid: actual long full-JSON editor, derived readonly columns, add/delete/update, parked native receipt, explicit toolbar verification, localized evidence, refresh and generation draft retention verified")
}
