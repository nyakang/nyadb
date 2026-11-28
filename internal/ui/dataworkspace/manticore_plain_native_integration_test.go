package dataworkspace

import (
	"context"
	"encoding/json"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"strconv"
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

func TestManticoreNativePlainSharedGridOriginalModeAndRecovery(t *testing.T) {
	if os.Getenv("NYADB_MANTICORE_PLAIN_IT") != "1" {
		t.Skip("requires owned indexer-built plain fixture")
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
	current, e := manticore.Open(context.Background(), 7401, cfg, "")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { current.Close() }()
	table := "nyadb_plain"
	active := Session{ProfileID: 7401, Generation: 1, Connected: true}
	originalSession := func(id int64) Session {
		return Session{ProfileID: id, Generation: active.Generation, Connected: id == 7401 && current.Alive()}
	}
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: originalSession, RuntimeFor: func(id int64) Runtime {
		if id != 7401 {
			return Runtime{}
		}
		return Runtime{Capabilities: current.DataDocumentCapabilities(), ReserveDocument: current.ReserveDataDocument}
	}, Inspector: InspectorDependencies{Session: func() Session { return active }, Profile: func(id int64) (connections.Profile, bool) {
		return connections.Profile{ID: 7401, Name: "Manticore plain", Host: host, Port: uint16(n)}, id == 7401
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
			t.Fatal("original plain workspace did not settle")
		}
	}
	s.Load(ui.Services{}, d, false)
	wait(func() bool { return !d.loading })
	if d.err != "" || d.page == nil || len(d.page.Rows) != 7 || !d.page.HasMore || d.allowsDataEdit("insert") || d.allowsDataEdit("delete") || !mysqlDataCanEdit(d, 0, 2) || mysqlDataCanEdit(d, 0, 1) || mysqlDataCanEdit(d, 0, 0) {
		t.Fatal("plain original mode permissions", d.err)
	}
	rows := len(d.page.Rows)
	s.stageMySQLDataRecord(ui.Services{}, d, nil)
	s.deleteMySQLDataRecords(ui.Services{}, d, 0)
	if len(d.page.Rows) != rows || d.HasDraft() {
		t.Fatal("plain insert/delete created a draft")
	}
	panel := s.inspectorPanel(d)
	s.inspector.LoadInfo(panel, nil, false)
	s.inspector.LoadCount(panel, nil)
	wait(func() bool { return !panel.InfoLoading() && !panel.CountLoading() })
	if panel.Count() == nil || *panel.Count() != 2008 {
		t.Fatal("plain count", panel.Count())
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
		capture(lang + "-plain-grid")
		if !tt.HasText("qty") {
			t.Fatal(tt.Texts())
		}
	}
	d.edit.Batch = true
	draft := s.mysqlRowDraft(d, 1)
	draft.Values["qty"] = contract.DataValue{Kind: contract.Integer, Text: "27"}
	s.saveMySQLData(ui.Services{}, d)
	wait(func() bool { return d.mutation.results == nil })
	if !d.mutation.open || d.mutation.plan == nil {
		t.Fatal("plain confirmation", d.edit.Err)
	}
	for _, lang := range []string{"zh-Hans", "en"} {
		locale = lang
		capture(lang + "-plain-update-confirm")
		if !tt.HasText(locales.Text("确认 Manticore 数据写入")) {
			t.Fatal(tt.Texts())
		}
	}
	s.confirmDataMutation(ui.Services{}, d)
	results := d.edit.Results
	active.ProfileID = 7499
	var original mysqlEditResult
	select {
	case original = <-results:
	case <-time.After(15 * time.Second):
		t.Fatal("plain original write missing")
	}
	if original.Err != nil || !contract.DataWriteComplete(original.Saved, 1) {
		t.Fatal(original.Err)
	}
	parked := make(chan mysqlEditResult, 1)
	parked <- original
	d.edit.Results = parked
	s.Consume(ui.Services{})
	if !d.edit.Busy || len(parked) != 1 || !d.HasDraft() {
		t.Fatal("plain write published to another profile")
	}
	active.ProfileID = 7401
	s.Consume(ui.Services{})
	wait(func() bool { return !d.edit.Busy && !d.loading })
	if d.edit.Err != "" || d.HasDraft() || d.nativeReceipt.JSON == "" || d.page.Rows[1][2].Text != "27" || d.allowsDataEdit("delete") {
		t.Fatal("plain original update", d.edit.Err, d.err)
	}
	q, e := current.ReserveQuery(7402)
	if e != nil {
		t.Fatal(e)
	}
	actual, e := q.Execute(context.Background(), "Manticore", "SELECT qty FROM nyadb_plain WHERE id=2")
	q.Release()
	if e != nil || actual.Rows[0][0] != "27" {
		t.Fatal(actual, e)
	}
	defer func() {
		q, e := current.ReserveQuery(7403)
		if e != nil {
			t.Error(e)
			return
		}
		defer q.Release()
		if _, e = q.Execute(context.Background(), "Manticore", "UPDATE nyadb_plain SET qty=2 WHERE id=2"); e != nil {
			t.Error(e)
		}
	}()
	d.nativeReceipt.Open = true
	capture("en-plain-original-receipt")
	d.nativeReceipt.Open = false
	s.changeMySQLDataPage(ui.Services{}, d, 1000)
	wait(func() bool { return !d.loading })
	if d.err != "" || d.page.Rows[0][0].Text != "1001" {
		t.Fatal(d.err)
	}
	capture("en-plain-beyond-default-page")
	// Park an actual original result across a real reconnect. Its permissions
	// and data must not replace the cached page in the retired generation.
	old := d.page
	s.Load(ui.Services{}, d, true)
	var pageResult mysqlDataResult
	select {
	case pageResult = <-d.results:
	case <-time.After(15 * time.Second):
		t.Fatal("plain read missing")
	}
	if pageResult.err != nil {
		t.Fatal(pageResult.err)
	}
	if e = current.Close(); e != nil {
		t.Fatal(e)
	}
	current, e = manticore.Open(context.Background(), 7401, cfg, "")
	if e != nil {
		t.Fatal(e)
	}
	active.Generation++
	buffer := make(chan mysqlDataResult, 1)
	buffer <- pageResult
	d.results = buffer
	s.Consume(ui.Services{})
	if d.loading || d.page != old || d.err == "" {
		t.Fatal("old plain generation published")
	}
	s.Close(d)
	d = s.OpenObject(2, object, current.DataDocumentCapabilities())
	d.limit = 7
	s.Load(ui.Services{}, d, false)
	wait(func() bool { return !d.loading })
	if d.err != "" || d.page.Rows[1][2].Text != "27" || d.allowsDataEdit("insert") || d.allowsDataEdit("delete") {
		t.Fatal("fresh plain document", d.err)
	}
	if dir := os.Getenv("NYADB_MANTICORE_EVIDENCE"); dir != "" {
		raw, e := json.MarshalIndent(map[string]any{"generation": active.Generation, "page": d.page, "readback": actual}, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, "native-plain-grid.json"), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
}
