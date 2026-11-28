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

func TestManticoreNativePercolateWriteConfirmationParkingUnknownAndRecovery(t *testing.T) {
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
	control, err := manticore.Open(context.Background(), 7701, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	q, err := control.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Release()
	table := fmt.Sprintf("nyadb_pqui77_%d", time.Now().UnixNano())
	run := func(sql string) contract.SQLResult {
		t.Helper()
		v, e := q.Execute(context.Background(), "Manticore", sql)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	run("CREATE TABLE " + table + " (title text, tag string) type='pq'")
	defer run("DROP TABLE " + table)
	run("INSERT INTO " + table + " (id,query,tags) VALUES (1,'hello','original')")
	gate := newPercolatePacketGate(t, net.JoinHostPort(host, port))
	host, port, _ = net.SplitHostPort(gate.listener.Addr().String())
	n, _ = strconv.Atoi(port)
	cfg.Host, cfg.Port = host, uint16(n)
	native, err := manticore.Open(context.Background(), 7701, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { native.Close() }()
	active := Session{ProfileID: 7701, Generation: 1, Connected: true}
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(id int64) Session {
		return Session{ProfileID: id, Generation: active.Generation, Connected: native.Alive() && id == 7701}
	}, RuntimeFor: func(id int64) Runtime {
		if id != 7701 {
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
			t.Fatal("original percolate UI work did not settle")
		}
	}
	s.Load(ui.Services{}, d, false)
	wait(func() bool { return !d.loading })
	if d.err != "" || d.page == nil || len(d.page.Rows) != 1 {
		t.Fatal(d.err)
	}
	if mysqlDataCanEdit(d, 0, 1) || !d.allowsDataEdit("insert") || !d.allowsDataEdit("delete") {
		t.Fatal("percolate acquired UPDATE")
	}
	d.edit.Batch = true
	stage := func(id int, tags string) {
		t.Helper()
		s.stageMySQLDataRecord(ui.Services{}, d, nil)
		d.edit.Editor = nil
		for _, draft := range d.edit.Drafts {
			if draft.Kind == "insert" {
				draft.Values = map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: strconv.Itoa(id)}, "query": {Kind: contract.Text, Text: "hello"}, "tags": {Kind: contract.Text, Text: tags}}
			}
		}
	}
	stage(2, "猫 original insert")
	s.Save(ui.Services{}, d)
	// A ready original plan remains parked until the original profile returns.
	active.ProfileID = 7799
	until := time.Now().Add(15 * time.Second)
	for len(d.mutation.results) == 0 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if len(d.mutation.results) != 1 {
		t.Fatal("original preview missing")
	}
	s.Consume(ui.Services{})
	if d.mutation.open || !d.edit.Busy || len(d.mutation.results) != 1 {
		t.Fatal("foreign profile consumed confirmation")
	}
	active.ProfileID = 7701
	wait(func() bool { return d.mutation.results == nil })
	if !d.mutation.open || d.mutation.presentation == nil || d.mutation.snapshot.Presentation == nil {
		t.Fatal(d.edit.Err)
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
	checkConfirmation := func(kind string) {
		t.Helper()
		for _, lang := range []string{"zh-Hans", "en"} {
			locale = lang
			capture(lang + "-percolate-" + kind + "-confirm")
			if !tt.HasText(locales.Text("确认 Manticore 存储查询写入")) || !tt.HasText(locales.Text(d.mutation.presentation.Hint)) {
				t.Fatal(tt.Texts())
			}
			if lang == "en" && !strings.Contains(strings.Join(tt.Texts(), " "), "without atomic compare-and-delete") {
				t.Fatal("native limitation hidden")
			}
		}
	}
	checkConfirmation("insert")
	if err = tt.Click(locales.Text("取消")); err != nil {
		t.Fatal(err)
	}
	if !d.HasDraft() || d.edit.Busy || d.mutation.open || len(run("SELECT id FROM "+table).Rows) != 1 {
		t.Fatal("cancel submitted or discarded draft")
	}
	s.Save(ui.Services{}, d)
	wait(func() bool { return d.mutation.results == nil })
	s.confirmDataMutation(ui.Services{}, d)
	active.ProfileID = 7799
	var written mysqlEditResult
	select {
	case written = <-d.edit.Results:
	case <-time.After(15 * time.Second):
		t.Fatal("original write missing")
	}
	if written.Err != nil || !contract.DataWriteComplete(written.Saved, 1) {
		t.Fatal(written.Err)
	}
	parked := make(chan mysqlEditResult, 1)
	parked <- written
	d.edit.Results = parked
	s.Consume(ui.Services{})
	if !d.edit.Busy || !d.HasDraft() || len(parked) != 1 {
		t.Fatal("foreign profile consumed original write")
	}
	active.ProfileID = 7701
	s.Consume(ui.Services{})
	wait(func() bool { return !d.edit.Busy && !d.loading })
	if d.HasDraft() || d.edit.Err != "" || len(d.page.Rows) != 2 || d.page.Rows[1][2].Text != "猫 original insert" || d.nativeReceipt.JSON == "" {
		t.Fatal(d.edit.Err)
	}
	insertReceipt := d.nativeReceipt.JSON
	s.deleteMySQLDataRecords(ui.Services{}, d, 1)
	s.Save(ui.Services{}, d)
	wait(func() bool { return d.mutation.results == nil })
	checkConfirmation("delete")
	s.confirmDataMutation(ui.Services{}, d)
	wait(func() bool { return !d.edit.Busy && !d.loading })
	if d.HasDraft() || d.edit.Err != "" || len(d.page.Rows) != 1 || len(run("SELECT id FROM "+table+" WHERE id=2").Rows) != 0 {
		t.Fatal(d.edit.Err)
	}
	deleteReceipt := d.nativeReceipt.JSON
	stage(3, "unknown original")
	s.Save(ui.Services{}, d)
	wait(func() bool { return d.mutation.results == nil })
	var preview struct{ Statements []struct{ SQL string } }
	if err = json.Unmarshal([]byte(d.mutation.snapshot.NativeJSON), &preview); err != nil || len(preview.Statements) != 1 {
		t.Fatal(err)
	}
	observed := gate.arm(preview.Statements[0].SQL, "hold")
	s.confirmDataMutation(ui.Services{}, d)
	select {
	case packet := <-observed:
		if len(packet) < 6 || packet[4] != 0 {
			t.Fatal("not original native OK", packet)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("original native OK not observed")
	}
	s.stopMySQLDataEdit(d)
	if !d.edit.Busy {
		t.Fatal("cancel settled without original executor acknowledgement")
	}
	wait(func() bool { return !d.edit.Busy })
	if !d.HasDraft() || d.edit.Uncertain == nil || d.nativeReceipt.JSON == "" || gate.calls.Load() != 1 {
		t.Fatal("unknown outcome discarded", d.edit.Err)
	}
	unknownReceipt := d.nativeReceipt.JSON
	d.nativeReceipt.Open = true
	capture("en-percolate-original-unknown-receipt")
	if !tt.HasText(locales.Text("原生顺序写入回执")) {
		t.Fatal(tt.Texts())
	}
	d.nativeReceipt.Open = false
	s.Save(ui.Services{}, d)
	s.Load(ui.Services{}, d, true)
	if d.edit.Busy || d.loading || gate.calls.Load() != 1 || d.nativeReceipt.JSON != unknownReceipt {
		t.Fatal("uncertain write replayed")
	}
	actual := run("SELECT id,query,tags FROM " + table + " ORDER BY id ASC")
	if len(actual.Rows) != 2 || actual.Rows[1][0] != "3" || actual.Rows[1][2] != "unknown original" || d.nativeReceipt.JSON != unknownReceipt {
		t.Fatal(actual)
	}
	if err = native.Close(); err != nil {
		t.Fatal(err)
	}
	native, err = manticore.Open(context.Background(), 7701, cfg, "")
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
		raw, e := json.MarshalIndent(map[string]any{"insert": json.RawMessage(insertReceipt), "delete": json.RawMessage(deleteReceipt), "unknown": json.RawMessage(unknownReceipt), "independent_readback": actual, "generation": active.Generation, "original_calls": gate.calls.Load()}, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, "native-percolate-write-workspace.json"), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	s.Close(d)
	d = s.OpenObject(2, object, native.DataDocumentCapabilities())
	s.Load(ui.Services{}, d, false)
	wait(func() bool { return !d.loading })
	if d.err != "" || len(d.page.Rows) != 2 || d.HasDraft() || gate.calls.Load() != 1 {
		t.Fatal("explicit fresh document", d.err)
	}
	capture("en-percolate-explicit-recovered-grid")
}
