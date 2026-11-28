package dataworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net"
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
	"github.com/nyakang/nyadb/internal/services/manticore"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestManticoreNativeRTReplaceCompleteDraftParkingUnknownAndRecovery(t *testing.T) {
	if os.Getenv("NYADB_MANTICORE_IT") != "1" {
		t.Skip("requires owned real Manticore fixture")
	}
	host, port, err := net.SplitHostPort(os.Getenv("NYADB_MANTICORE_ENDPOINT"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	cfg := connections.ManticoreConfig{Host: host, Port: uint16(n), Username: "root", QueryTimeoutSeconds: 20}
	control, err := manticore.Open(context.Background(), 7901, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	q, err := control.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Release()
	table := fmt.Sprintf("nyadb_rtui79_%d", time.Now().UnixNano())
	run := func(sql string) contract.SQLResult {
		t.Helper()
		v, e := q.Execute(context.Background(), "Manticore", sql)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	run("CREATE TABLE " + table + " (title text, tag string)")
	defer run("DROP TABLE " + table)
	originalTags := " " + strings.Repeat("猫🙂", 1025)
	run("INSERT INTO " + table + " (id,title,tag) VALUES (1,'hello','" + originalTags + "')")
	gate := newPercolatePacketGate(t, net.JoinHostPort(host, port))
	host, port, _ = net.SplitHostPort(gate.listener.Addr().String())
	n, _ = strconv.Atoi(port)
	cfg.Host, cfg.Port = host, uint16(n)
	native, err := manticore.Open(context.Background(), 7901, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { native.Close() }()
	active := Session{ProfileID: 7901, Generation: 1, Connected: true}
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(id int64) Session {
		return Session{ProfileID: id, Generation: active.Generation, Connected: native.Alive() && id == 7901}
	}, RuntimeFor: func(id int64) Runtime {
		if id != 7901 {
			return Runtime{}
		}
		return Runtime{Capabilities: native.DataDocumentCapabilities(), ReserveDocument: native.ReserveDataDocument}
	}})
	defer s.CloseAll()
	object := native.Namespace("Manticore").Object("TABLE", table)
	d := s.OpenObject(1, object, native.DataDocumentCapabilities())
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
			t.Fatal("original RT replacement UI work did not settle")
		}
	}
	s.Load(ui.Services{}, d, false)
	wait(func() bool { return !d.loading })
	if d.err != "" || d.page == nil || len(d.page.Rows) != 1 {
		t.Fatal(d.err)
	}
	if mysqlDataCanEdit(d, 0, 1) || !d.allowsDataEdit("insert") || !d.allowsDataEdit("delete") {
		t.Fatal("RT full text acquired ordinary UPDATE")
	}
	locale := "zh-Hans"
	i18n.SetLocaleFunc(func() string { return locale })
	defer i18n.SetLocaleFunc(nil)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.View(c, d); s.Modals(c) }, 1280, 800)
	capture := func(name string) {
		t.Helper()
		tt.Frame()
		if dir := os.Getenv("NYADB_MANTICORE_EVIDENCE"); dir != "" {
			f, e := os.Create(filepath.Join(dir, name+".png"))
			if e != nil {
				t.Fatal(e)
			}
			e = png.Encode(f, tt.Image())
			ce := f.Close()
			if e != nil || ce != nil {
				t.Fatal(e, ce)
			}
		}
	}
	click := func(label string) {
		t.Helper()
		tt.Frame()
		if e := tt.Click(locales.Text(label)); e != nil {
			t.Fatal(e)
		}
	}
	capture("zh-Hans-rt-replace-original")
	click("记录表单")
	// Cancellation waits for the original complete-row executor and creates no draft.
	source := "SELECT `id`,`title`,`tag` FROM `Manticore`.`" + table + "` WHERE id=1 LIMIT 2 OPTION max_matches=2"
	observed := gate.arm(source, "hold")
	s.ReplaceRecord(ui.Services{}, d, 0)
	select {
	case <-observed:
	case <-time.After(15 * time.Second):
		t.Fatal("original complete row missing")
	}
	s.stopMySQLDataEdit(d)
	if !d.edit.Busy {
		t.Fatal("read cancellation settled early")
	}
	wait(func() bool { return !d.edit.Busy })
	if d.HasDraft() || d.edit.Err == "" {
		t.Fatal("cancelled source staged replacement")
	}
	gate.mu.Lock()
	gate.query = ""
	close(gate.resume)
	gate.mu.Unlock()
	// The ready full record is parked until its original profile is active.
	// Dispatch without an extra tester frame consuming a fast real read before
	// the profile switch. The actual desktop exercises the visible action.
	s.ReplaceRecord(ui.Services{}, d, 0)
	active.ProfileID = 7999
	until := time.Now().Add(15 * time.Second)
	for len(d.edit.Results) == 0 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if len(d.edit.Results) != 1 {
		t.Fatal("original replacement source missing")
	}
	s.Consume(ui.Services{})
	if d.HasDraft() || !d.edit.Busy || len(d.edit.Results) != 1 {
		t.Fatal("foreign profile consumed original row")
	}
	active.ProfileID = 7901
	wait(func() bool { return !d.edit.Busy })
	draft := mysqlDataDraftForRow(d, 0)
	if draft == nil || draft.Kind != "replace" || len(draft.Values) != 3 || draft.Values["tag"].Text != originalTags || mysqlDataCanEdit(d, 0, 0) || !mysqlDataCanEdit(d, 0, 1) || !d.recordFormMode {
		t.Fatal("incomplete or mutable identity draft", d.edit.Err)
	}
	capture("zh-Hans-rt-replace-complete-form")
	if !s.undoDraft(d, false) || d.HasDraft() || mysqlDataCanEdit(d, 0, 1) || !s.undoDraft(d, true) {
		t.Fatal("replacement undo/redo lost explicit operation")
	}
	if restored := mysqlDataDraftForRow(d, 0); restored == nil || restored.Kind != "replace" || restored.Values["tag"].Text != originalTags || mysqlDataCanEdit(d, 0, 0) {
		t.Fatal("replacement redo lost complete source or locked identity")
	}
	click("编辑记录字段 title")
	if d.edit.Editor == nil {
		t.Fatal("replacement editor missing")
	}
	d.edit.Editor.Text = "world original replacement"
	click("应用字段")
	s.Save(ui.Services{}, d)
	wait(func() bool { return d.mutation.results == nil })
	for _, lang := range []string{"zh-Hans", "en"} {
		locale = lang
		capture(lang + "-rt-replace-confirm")
		if !tt.HasText(locales.Text("确认 Manticore RT 整条替换")) || !tt.HasText(locales.Text(d.mutation.presentation.Hint)) {
			t.Fatal(tt.Texts())
		}
		if lang == "en" && !strings.Contains(strings.Join(tt.Texts(), " "), "recreated") {
			t.Fatal("native recreation scope hidden")
		}
	}
	click("取消")
	actual := run("SELECT id,title,tag FROM " + table)
	if !d.HasDraft() || d.edit.Busy || actual.Rows[0][1] != "hello" {
		t.Fatal("cancel lost draft or wrote", actual)
	}
	s.Save(ui.Services{}, d)
	wait(func() bool { return d.mutation.results == nil })
	s.confirmDataMutation(ui.Services{}, d)
	active.ProfileID = 7999
	var written mysqlEditResult
	select {
	case written = <-d.edit.Results:
	case <-time.After(15 * time.Second):
		t.Fatal("original replacement missing")
	}
	if written.Err != nil || !contract.DataWriteComplete(written.Saved, 1) {
		t.Fatal(written.Err)
	}
	parked := make(chan mysqlEditResult, 1)
	parked <- written
	d.edit.Results = parked
	s.Consume(ui.Services{})
	if !d.edit.Busy || !d.HasDraft() || len(parked) != 1 {
		t.Fatal("foreign profile consumed original replacement")
	}
	active.ProfileID = 7901
	s.Consume(ui.Services{})
	wait(func() bool { return !d.edit.Busy && !d.loading })
	if d.HasDraft() || d.edit.Err != "" || len(d.page.Rows) != 1 || d.page.Rows[0][1].Text != "world original replacement" || d.nativeReceipt.JSON == "" {
		t.Fatal(d.edit.Err)
	}
	replaceReceipt := d.nativeReceipt.JSON
	actual = run("SELECT id,title,tag FROM " + table)
	if actual.Rows[0][2] != originalTags {
		t.Fatal("unedited full fields lost")
	}
	capture("en-rt-replace-acknowledged")
	// A second original native REPLACE succeeds server-side; its withheld OK stays unknown.
	s.ReplaceRecord(ui.Services{}, d, 0)
	wait(func() bool { return !d.edit.Busy })
	s.setMySQLDataCell(ui.Services{}, d, 0, 2, contract.DataValue{Kind: contract.Text, Text: "unknown replacement"})
	s.Save(ui.Services{}, d)
	wait(func() bool { return d.mutation.results == nil })
	var preview struct{ Statements []struct{ SQL string } }
	if err = json.Unmarshal([]byte(d.mutation.snapshot.NativeJSON), &preview); err != nil || len(preview.Statements) != 1 {
		t.Fatal(err)
	}
	gate.calls.Store(0)
	observed = gate.arm(preview.Statements[0].SQL, "hold")
	s.confirmDataMutation(ui.Services{}, d)
	select {
	case packet := <-observed:
		if len(packet) < 6 || packet[4] != 0 {
			t.Fatal(packet)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("native replacement OK missing")
	}
	s.stopMySQLDataEdit(d)
	if !d.edit.Busy {
		t.Fatal("cancel settled before original executor")
	}
	wait(func() bool { return !d.edit.Busy })
	if !d.HasDraft() || d.edit.Uncertain == nil || gate.calls.Load() != 1 {
		t.Fatal(d.edit.Err)
	}
	unknownReceipt := d.nativeReceipt.JSON
	d.nativeReceipt.Open = true
	capture("en-rt-replace-original-unknown-receipt")
	d.nativeReceipt.Open = false
	s.Save(ui.Services{}, d)
	s.Load(ui.Services{}, d, true)
	if d.edit.Busy || d.loading || gate.calls.Load() != 1 || d.nativeReceipt.JSON != unknownReceipt {
		t.Fatal("original replacement replayed")
	}
	actual = run("SELECT id,title,tag FROM " + table)
	if len(actual.Rows) != 1 || actual.Rows[0][2] != "unknown replacement" {
		t.Fatal(actual)
	}
	if err = native.Close(); err != nil {
		t.Fatal(err)
	}
	native, err = manticore.Open(context.Background(), 7901, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	active.Generation++
	s.Consume(ui.Services{})
	s.Save(ui.Services{}, d)
	if !d.HasDraft() || d.nativeReceipt.JSON != unknownReceipt || gate.calls.Load() != 1 || d.edit.Busy {
		t.Fatal("reconnect replayed or repaired original receipt")
	}
	if dir := os.Getenv("NYADB_MANTICORE_EVIDENCE"); dir != "" {
		raw, e := json.MarshalIndent(map[string]any{"replace": json.RawMessage(replaceReceipt), "unknown": json.RawMessage(unknownReceipt), "independent_readback": actual, "generation": active.Generation, "original_calls": gate.calls.Load()}, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, "native-rt-replace-workspace.json"), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	s.Close(d)
	d = s.OpenObject(2, object, native.DataDocumentCapabilities())
	s.Load(ui.Services{}, d, false)
	wait(func() bool { return !d.loading })
	if d.err != "" || len(d.page.Rows) != 1 || d.HasDraft() || gate.calls.Load() != 1 {
		t.Fatal(d.err)
	}
	capture("en-rt-replace-explicit-recovered-grid")
}
