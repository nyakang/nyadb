package objectworkspace

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

func TestManticoreNativeSchemaSharedFormsParkingCancellationAndRecovery(t *testing.T) {
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
	control, e := manticore.Open(context.Background(), 8001, cfg, "")
	if e != nil {
		t.Fatal(e)
	}
	defer control.Close()
	q, e := control.ReserveQuery(1)
	if e != nil {
		t.Fatal(e)
	}
	defer q.Release()
	run := func(sql string) contract.SQLResult {
		t.Helper()
		v, e := q.Execute(context.Background(), "Manticore", sql)
		if e != nil {
			t.Fatal(sql, e)
		}
		return v
	}
	table := fmt.Sprintf("nyadb_schema_ui80_%d", time.Now().UnixNano())
	run("CREATE TABLE " + table + " (title text, tag string)")
	defer run("DROP TABLE " + table)
	run("INSERT INTO " + table + " (id,title,tag) VALUES (1,'hello','original')")
	gate := newSchemaPacketGate(t, net.JoinHostPort(host, port))
	host, port, _ = net.SplitHostPort(gate.listener.Addr().String())
	n, _ = strconv.Atoi(port)
	cfg.Host, cfg.Port = host, uint16(n)
	native, e := manticore.Open(context.Background(), 8001, cfg, "")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { native.Close() }()
	active := Session{ProfileID: 8001, Generation: 1, Connected: true}
	documentActive := true
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(id int64) Session {
		v := active
		v.ProfileID = id
		v.Connected = native.Alive() && id == 8001
		return v
	}, DocumentActive: func(uint64) bool { return documentActive },
		Engine: func(int64) string { return "manticoresearch" }, Capabilities: func(int64) contract.Capabilities { return contract.Capabilities{Metadata: true, DesignTables: true} }, Availability: func(_ int64, operation string, target contract.SchemaDesignTarget) contract.Availability {
			return contract.Availability{Enabled: operation == "design_table" && native.SchemaDesignDocumentFeatures().CanApply(target)}
		},
		MetadataFor: func(int64) (contract.MetadataProvider, error) { return native, nil }, SchemaDocumentsFor: func(int64) (contract.SchemaDesignDocumentsProvider, error) { return native, nil }, SchemaDesignFor: func(int64) contract.SchemaDesignOperations { return native.SchemaDesign() }, SchemaTarget: func(id int64, database, name string) contract.SchemaDesignTarget {
			return contract.SchemaDesignTarget{Generation: 1, Object: contract.ObjectRef{ProfileID: id, Catalog: database, Kind: "table", Name: name}}
		}})
	defer s.CloseAll()
	object := native.Namespace("Manticore").Object("table", table)
	d := s.OpenQualifiedStructure(1, object, "表")
	locale := "zh-Hans"
	i18n.SetLocaleFunc(func() string { return locale })
	defer i18n.SetLocaleFunc(nil)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.View(c, d) }, 1280, 800)
	wait := func(done func() bool, consume bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !done() && time.Now().Before(deadline) {
			if consume {
				s.Consume(ui.Services{})
			}
			time.Sleep(time.Millisecond)
		}
		if !done() {
			t.Fatal("native schema UI did not settle", d.err)
		}
	}
	click := func(label string) {
		t.Helper()
		tt.Frame()
		if e := tt.Click(locales.Text(label)); e != nil {
			t.Fatal(label, e, tt.Texts())
		}
	}
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
	// Original schema read remains parked until its document is active.
	s.BeginDesign(d, ui.Services{})
	if d.schemaDesign == nil {
		t.Fatal("native designer not available")
	}
	documentActive = false
	wait(func() bool { return len(d.schemaDesign.results) == 1 }, false)
	s.Consume(ui.Services{})
	if d.schemaDesign.loaded || !d.schemaDesign.busy {
		t.Fatal("inactive document consumed original schema")
	}
	documentActive = true
	wait(func() bool { return !d.schemaDesign.busy }, true)
	if d.err != "" || !d.schemaDesign.loaded {
		t.Fatal(d.err)
	}
	capture("zh-Hans-schema-original")
	click("添加字段")
	x := d.schemaDesign
	if len(x.draft.Clauses) != 4 {
		t.Fatal(x.draft)
	}
	click("RT 字段名称 4")
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("added")
	tt.Frame()
	if x.draft.Clauses[3].Name != "added" || !d.DesignDirty() {
		t.Fatal(x.draft)
	}
	click("SQL 预览")
	if !x.preview || len(x.plan.SQL) != 1 {
		t.Fatal(d.err)
	}
	for _, lang := range []string{"zh-Hans", "en"} {
		locale = lang
		capture(lang + "-schema-add-confirm")
		if !tt.HasText(locales.Text(x.plan.ScopeHint)) {
			t.Fatal("native scope hidden", tt.Texts())
		}
	}
	click("返回编辑")
	if !d.DesignDirty() || len(run("DESCRIBE "+table).Rows) != 3 {
		t.Fatal("cancel wrote or lost draft")
	}
	click("SQL 预览")
	s.executeSchemaDesign(d, ui.Services{})
	active.ProfileID = 8999
	wait(func() bool { return len(x.results) == 1 }, false)
	s.Consume(ui.Services{})
	if !x.busy || !d.DesignDirty() {
		t.Fatal("foreign profile consumed original DDL")
	}
	active.ProfileID = 8001
	wait(func() bool { return !x.busy && !d.loading }, true)
	if d.err != "" || d.DesignDirty() || len(x.original.Schema.Table.Clauses) != 4 || d.SchemaDesignOriginalOutcome() == nil || !d.SchemaDesignOriginalOutcome().Verified {
		t.Fatal(d.err)
	}
	addedReceipt := d.SchemaDesignOriginalOutcome()
	capture("en-schema-add-completed")
	// Native DROP requires the original table-name confirmation.
	if e := tt.Click(fmt.Sprintf(locales.Text("删除 RT 字段 %s"), "added")); e != nil {
		t.Fatal(e)
	}
	click("SQL 预览")
	if len(x.plan.Dropped) != 1 {
		t.Fatal(x.plan)
	}
	s.executeSchemaDesign(d, ui.Services{})
	if x.busy {
		t.Fatal("unconfirmed drop dispatched")
	}
	locale = "zh-Hans"
	capture("zh-Hans-schema-drop-confirm")
	locale = "en"
	capture("en-schema-drop-confirm")
	click("确认原表名称")
	tt.Type(table)
	click("确认执行结构 SQL")
	wait(func() bool { return !x.busy && !d.loading }, true)
	if d.err != "" || d.DesignDirty() || len(run("DESCRIBE "+table).Rows) != 3 {
		t.Fatal(d.err)
	}
	dropReceipt := d.SchemaDesignOriginalOutcome()
	click("原始结构回执")
	capture("en-schema-original-drop-receipt")
	click("字段")
	// A real ADD succeeds at the server while its original OK is withheld.
	click("添加字段")
	x.draft.Clauses[3].Name = "uncertain"
	if e = s.PrepareSchemaDesign(d); e != nil {
		t.Fatal(e)
	}
	observed := gate.arm(x.plan.SQL[0], "hold")
	s.executeSchemaDesign(d, ui.Services{})
	select {
	case packet := <-observed:
		if len(packet) < 5 || packet[4] != 0 {
			t.Fatal(packet)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("native original DDL OK missing")
	}
	s.cancelSchemaDesign(d)
	if !x.busy {
		t.Fatal("cancel settled before original executor")
	}
	wait(func() bool { return !x.busy }, true)
	if !x.blocked || !d.DesignDirty() || d.SchemaDesignOriginalOutcome().State != "uncertain" || gate.calls.Load() != 1 {
		t.Fatal(d.err)
	}
	unknown := d.SchemaDesignOriginalOutcome()
	gate.mu.Lock()
	gate.query = ""
	close(gate.resume)
	gate.mu.Unlock()
	// Reads and reconnection cannot repair or replay that original receipt.
	s.loadSchemaDesign(d, ui.Services{})
	wait(func() bool { return !x.busy }, true)
	if !x.blocked || !d.DesignDirty() || gate.calls.Load() != 1 {
		t.Fatal(d.err)
	}
	if e = native.Close(); e != nil {
		t.Fatal(e)
	}
	native, e = manticore.Open(context.Background(), 8001, cfg, "")
	if e != nil {
		t.Fatal(e)
	}
	active.Generation++
	s.executeSchemaDesign(d, ui.Services{})
	if x.busy || !x.blocked || gate.calls.Load() != 1 {
		t.Fatal("recovery replayed original DDL")
	}
	click("原始结构回执")
	capture("en-schema-original-unknown-receipt")
	before, _ := json.Marshal(unknown.Receipt)
	after, _ := json.Marshal(d.SchemaDesignOriginalOutcome().Receipt)
	if string(before) != string(after) {
		t.Fatal("later read repaired receipt")
	}
	if dir := os.Getenv("NYADB_MANTICORE_EVIDENCE"); dir != "" {
		raw, e := json.MarshalIndent(map[string]any{"add": addedReceipt.Receipt, "drop": dropReceipt.Receipt, "unknown": unknown.Receipt, "generation": active.Generation, "calls": gate.calls.Load(), "actual": run("DESCRIBE " + table), "rows": run("SELECT id,title,tag FROM " + table)}, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, "native-schema-workspace.json"), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	s.Close(d)
	d = s.OpenQualifiedStructure(2, object, "表")
	s.BeginDesign(d, ui.Services{})
	wait(func() bool { return d.schemaDesign != nil && !d.schemaDesign.busy }, true)
	if d.err != "" || !d.schemaDesign.loaded || d.DesignDirty() || gate.calls.Load() != 1 {
		t.Fatal(d.err)
	}
	capture("en-schema-explicit-recovered-document")
	if !strings.Contains(d.schemaDesign.original.Schema.DDL, "uncertain integer") {
		t.Fatal("explicit recovery lost actual observed structure")
	}
}
