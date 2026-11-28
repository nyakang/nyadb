package objectworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"reflect"
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

func TestManticoreNativeSchemaCreateFormsParkingUnknownAndRecovery(t *testing.T) {
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
	table := fmt.Sprintf("nyadb_schema_create_ui82_%d", time.Now().UnixNano())
	defer run("DROP TABLE IF EXISTS " + table)
	unknownTable := table + "_unknown"
	defer run("DROP TABLE IF EXISTS " + unknownTable)
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
	documentActive := false
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
	d := s.OpenNewSchemaDesign(1, object, ui.Services{})
	if d == nil {
		t.Fatal("native new designer unavailable")
	}
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
	// The absent original belongs to this profile and document, including while parked.
	documentActive = false
	wait(func() bool { return len(d.schemaDesign.results) == 1 }, false)
	s.Consume(ui.Services{})
	if d.schemaDesign.loaded || !d.schemaDesign.busy {
		t.Fatal("inactive absence was consumed")
	}
	documentActive = true
	wait(func() bool { return !d.schemaDesign.busy }, true)
	x := d.schemaDesign
	if d.err != "" || !x.loaded || x.original.Schema.Exists || !d.DesignDirty() || len(x.draft.Clauses) != 1 || x.draft.Clauses[0].Name != "id" {
		t.Fatal(d.err, x)
	}
	capture("zh-Hans-schema-create-absent")
	click("添加字段")
	click(fmt.Sprintf(locales.Text("RT 字段名称 %d"), 2))
	tt.Type("score")
	click("添加字段")
	click(fmt.Sprintf(locales.Text("RT 字段名称 %d"), 3))
	tt.Type("qty")
	tt.Frame()
	if len(x.draft.Clauses) != 3 || x.draft.Clauses[1].Name != "score" || x.draft.Clauses[2].Name != "qty" {
		t.Fatal(x.draft)
	}
	click("SQL 预览")
	if !x.preview || x.plan.Mode != "native_create" || len(x.plan.SQL) != 1 {
		t.Fatal(d.err, x.plan)
	}
	for _, lang := range []string{"zh-Hans", "en"} {
		locale = lang
		capture(lang + "-schema-create-confirm")
		if !tt.HasText(locales.Text(x.plan.ScopeHint)) {
			t.Fatal("native create scope hidden", tt.Texts())
		}
	}
	click("返回编辑")
	if !d.DesignDirty() {
		t.Fatal("preview cancellation lost draft")
	}
	for _, row := range run("SHOW TABLES").Rows {
		if row[0] == table {
			t.Fatal("preview cancellation created table")
		}
	}
	click("SQL 预览")
	s.executeSchemaDesign(d, ui.Services{})
	active.ProfileID = 8999
	wait(func() bool { return len(x.results) == 1 }, false)
	s.Consume(ui.Services{})
	if !x.busy || !d.DesignDirty() {
		t.Fatal("foreign profile consumed original CREATE")
	}
	active.ProfileID = 8001
	wait(func() bool { return !x.busy && !d.loading }, true)
	created := d.SchemaDesignOriginalOutcome()
	if d.err != "" || d.DesignDirty() || x.isNew || !x.original.Schema.Exists || created == nil || !created.Verified || created.Receipt.Original.Schema.Exists || created.Receipt.Original.Target.Object != object {
		t.Fatal(d.err, created)
	}
	run("INSERT INTO " + table + " (id,score,qty) VALUES (1,7,9)")
	if got := run("SELECT id,score,qty FROM " + table); fmt.Sprint(got.Rows) != "[[1 7 9]]" {
		t.Fatal(got)
	}
	click("原始结构回执")
	capture("en-schema-create-original-receipt")
	s.Close(d)
	// An actual CREATE reaches the original server, but its OK is withheld.
	object = native.Namespace("Manticore").Object("table", unknownTable)
	d = s.OpenNewSchemaDesign(2, object, ui.Services{})
	if d == nil {
		t.Fatal("second native new document unavailable")
	}
	wait(func() bool { return !d.schemaDesign.busy }, true)
	x = d.schemaDesign
	if d.err != "" || x.original.Schema.Exists {
		t.Fatal(d.err)
	}
	click("添加字段")
	click(fmt.Sprintf(locales.Text("RT 字段名称 %d"), 2))
	tt.Type("candidate")
	click("SQL 预览")
	observed := gate.arm(x.plan.SQL[0], "hold")
	s.executeSchemaDesign(d, ui.Services{})
	select {
	case packet := <-observed:
		if len(packet) < 5 || packet[4] != 0 {
			t.Fatal(packet)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("native original CREATE OK missing")
	}
	s.cancelSchemaDesign(d)
	if !x.busy {
		t.Fatal("CREATE cancellation settled before executor")
	}
	wait(func() bool { return !x.busy }, true)
	unknown := d.SchemaDesignOriginalOutcome()
	if !x.blocked || !d.DesignDirty() || unknown == nil || unknown.State != "uncertain" || unknown.Verified || gate.calls.Load() != 1 {
		t.Fatal(d.err, unknown)
	}
	gate.mu.Lock()
	gate.query = ""
	close(gate.resume)
	gate.mu.Unlock()
	if got := run("DESCRIBE " + unknownTable); len(got.Rows) != 2 {
		t.Fatal(got)
	}
	s.loadSchemaDesign(d, ui.Services{})
	wait(func() bool { return !x.busy }, true)
	if !x.blocked || !d.DesignDirty() || !reflect.DeepEqual(unknown.Receipt, d.SchemaDesignOriginalOutcome().Receipt) {
		t.Fatal("observation repaired original CREATE")
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
	if x.busy || !x.blocked || gate.calls.Load() != 1 || !reflect.DeepEqual(unknown.Receipt, d.SchemaDesignOriginalOutcome().Receipt) {
		t.Fatal("reconnect replayed original CREATE")
	}
	click("原始结构回执")
	capture("en-schema-create-unknown-receipt")
	if dir := os.Getenv("NYADB_MANTICORE_EVIDENCE"); dir != "" {
		raw, e := json.MarshalIndent(map[string]any{"created": created.Receipt, "unknown": unknown.Receipt, "generation": active.Generation, "calls": gate.calls.Load(), "actual": run("DESCRIBE " + unknownTable)}, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, "native-schema-create-workspace.json"), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	s.Close(d)
	d = s.OpenQualifiedStructure(3, object, "表")
	s.BeginDesign(d, ui.Services{})
	wait(func() bool { return d.schemaDesign != nil && !d.schemaDesign.busy }, true)
	if d.err != "" || !d.schemaDesign.loaded || d.DesignDirty() || gate.calls.Load() != 1 || !strings.Contains(d.schemaDesign.original.Schema.DDL, "candidate integer") {
		t.Fatal("fresh document did not recover observed native table", d.err)
	}
	capture("en-schema-create-recovered")
}
