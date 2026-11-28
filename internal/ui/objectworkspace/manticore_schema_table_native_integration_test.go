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
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/manticore"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestManticoreNativeSchemaTableModalsParkingUnknownAndRecovery(t *testing.T) {
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
	control, e := manticore.Open(context.Background(), 8101, cfg, "")
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
	for _, operation := range []string{"truncate", "drop"} {
		t.Run(operation, func(t *testing.T) {
			table := fmt.Sprintf("nyadb_schema_modal81_%d", time.Now().UnixNano())
			run("CREATE TABLE " + table + " (title text, qty integer) min_word_len='2'")
			defer run("DROP TABLE IF EXISTS " + table)
			run("INSERT INTO " + table + " (id,title,qty) VALUES (1,'original',7),(2,'original',9)")
			gate := newSchemaPacketGate(t, os.Getenv("NYADB_MANTICORE_ENDPOINT"))
			h, p, _ := net.SplitHostPort(gate.listener.Addr().String())
			port, _ := strconv.Atoi(p)
			proxied := cfg
			proxied.Host, proxied.Port = h, uint16(port)
			native, e := manticore.Open(context.Background(), 8101, proxied, "")
			if e != nil {
				t.Fatal(e)
			}
			defer func() { native.Close() }()
			active := Session{ProfileID: 8101, Generation: 1, Connected: true}
			events := 0
			s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(id int64) Session {
				return Session{ProfileID: id, Generation: active.Generation, Connected: native.Alive() && id == 8101}
			}, Engine: func(int64) string { return "manticoresearch" }, Capabilities: func(int64) contract.Capabilities { return contract.Capabilities{Metadata: true, Objects: true} },
				Availability: func(_ int64, op string, target contract.SchemaDesignTarget) contract.Availability {
					f := native.SchemaDesignDocumentFeatures()
					return contract.Availability{Enabled: op == "drop_table" && f.CanDrop(target) || op == "truncate_table" && f.CanTruncate(target)}
				},
				SchemaDocumentsFor: func(int64) (contract.SchemaDesignDocumentsProvider, error) { return native, nil }, SchemaDesignFor: func(int64) contract.SchemaDesignOperations { return native.SchemaDesign() }, SchemaTarget: func(id int64, db, name string) contract.SchemaDesignTarget {
					return contract.SchemaDesignTarget{Generation: 1, Object: contract.ObjectRef{ProfileID: id, Catalog: db, Kind: "table", Name: name}}
				},
				SchemaDropped: func(_ ui.Services, event SchemaDropEvent) {
					if event.Original.Object.ProfileID != 8101 || event.Original.Object.Name != table || event.Generation != 1 {
						t.Fatal(event)
					}
					events++
				}, SchemaCleared: func(_ ui.Services, event SchemaClearEvent) {
					if event.Original.Object.ProfileID != 8101 || event.Original.Object.Name != table || event.Generation != 1 {
						t.Fatal(event)
					}
					events++
				},
			})
			defer s.CloseAll()
			locale := "zh-Hans"
			i18n.SetLocaleFunc(func() string { return locale })
			defer i18n.SetLocaleFunc(nil)
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.Modals(c) }, 1280, 800)
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
					t.Fatal("original modal did not settle", s.TableError())
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
			begin := func() {
				if operation == "drop" {
					s.beginSchemaDrop(8101, "Manticore", table)
				} else {
					s.beginSchemaEmpty(8101, "Manticore", table, true)
				}
			}
			closeModal := func() {
				if operation == "drop" {
					s.closeSchemaDrop()
				} else {
					s.closeSchemaClear()
				}
			}
			busy := func() bool {
				if operation == "drop" {
					return s.schemaDrop.busy
				}
				return s.schemaClear.busy
			}
			resultPending := func() bool {
				if operation == "drop" {
					return len(s.schemaDrop.results) == 1
				}
				return len(s.schemaClear.results) == 1
			}
			source := func() *contract.SchemaDesignSnapshot {
				if operation == "drop" {
					return s.schemaDrop.original
				}
				return s.schemaClear.original
			}
			apply := func() {
				if operation == "drop" {
					s.applySchemaDrop(ui.Services{})
				} else {
					s.applySchemaClear(ui.Services{})
				}
			}
			blocked := func() bool {
				if operation == "drop" {
					return s.schemaDrop.blocked && s.schemaDrop.owner.blocked
				}
				return s.schemaClear.blocked && s.schemaClear.owner.blocked
			}
			completed := func() bool {
				if operation == "drop" {
					return s.schemaDrop.completed
				}
				return s.schemaClear.completed
			}
			confirm := "确认截断表"
			if operation == "drop" {
				confirm = "确认删除表"
			}
			begin()
			click("重新读取结构")
			wait(func() bool { return !busy() }, true)
			if source() == nil {
				t.Fatal(s.TableError())
			}
			plan, e := schemaddl.PlanManticoreRTTableOperation(source().Schema, operation)
			if e != nil {
				t.Fatal(e)
			}
			for _, lang := range []string{"zh-Hans", "en"} {
				locale = lang
				capture(lang + "-schema-table-" + operation + "-confirm")
				if !tt.HasText(locales.Text(plan.ScopeHint)) {
					t.Fatal("native scope missing", tt.Texts())
				}
				if tt.HasText(locales.Text("重置自增值")) || tt.HasText(locales.Text("事务清空所有行；保留表结构、索引和触发器。")) {
					t.Fatal("relational promise exposed")
				}
			}
			apply()
			if busy() {
				t.Fatal("unconfirmed operation dispatched")
			}
			click("取消")
			if len(run("SELECT id FROM "+table).Rows) != 2 {
				t.Fatal("cancel mutated table")
			}
			begin()
			click("重新读取结构")
			wait(func() bool { return !busy() }, true)
			click("确认原表名称")
			tt.Type("wrong")
			click(confirm)
			if busy() {
				t.Fatal("wrong original name admitted")
			}
			click("确认原表名称")
			tt.Key(ui.Cmd, ui.KeyA)
			tt.Type(table)
			tt.Frame()
			apply()
			if !busy() {
				t.Fatal("confirmed original operation did not start", s.TableError())
			}
			active.ProfileID = 8999
			wait(resultPending, false)
			s.Consume(ui.Services{})
			if !busy() || events != 0 {
				t.Fatal("foreign profile consumed original table result")
			}
			active.ProfileID = 8101
			wait(func() bool { return !busy() }, true)
			first := s.SchemaTableOriginalOutcome(operation)
			if first == nil || !first.Verified || first.State != "committed" || first.Rows != 0 || !completed() || events != 1 {
				t.Fatal(first, s.TableError())
			}
			capture("en-schema-table-" + operation + "-completed")
			closeModal()
			if operation == "drop" {
				run("CREATE TABLE " + table + " (title text, qty integer) min_word_len='2'")
			}
			run("INSERT INTO " + table + " (id,title,qty) VALUES (3,'uncertain original',12)")
			begin()
			click("重新读取结构")
			wait(func() bool { return !busy() }, true)
			plan, e = schemaddl.PlanManticoreRTTableOperation(source().Schema, operation)
			if e != nil {
				t.Fatal(e)
			}
			observed := gate.arm(plan.SQL[0], "hold")
			click("确认原表名称")
			tt.Type(table)
			click(confirm)
			select {
			case packet := <-observed:
				if len(packet) < 5 || packet[4] != 0 {
					t.Fatal(packet)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("original native OK missing")
			}
			click("取消")
			wait(func() bool { return !busy() }, true)
			unknown := s.SchemaTableOriginalOutcome(operation)
			if unknown == nil || unknown.State != "uncertain" || unknown.Verified || !blocked() || gate.calls.Load() != 1 || events != 2 {
				t.Fatal(unknown, s.TableError())
			}
			gate.mu.Lock()
			gate.query = ""
			close(gate.resume)
			gate.mu.Unlock()
			actual := run("SHOW TABLES")
			if operation == "drop" {
				for _, row := range actual.Rows {
					if row[0] == table {
						t.Fatal("withheld native DROP did not apply")
					}
				}
				run("CREATE TABLE " + table + " (title text, qty integer) min_word_len='2'")
			} else {
				if rows := run("SELECT id FROM " + table); len(rows.Rows) != 0 {
					t.Fatal(rows)
				}
			}
			click("重新读取结构")
			wait(func() bool { return !busy() }, true)
			if !blocked() || gate.calls.Load() != 1 {
				t.Fatal("read repaired original receipt")
			}
			if e = native.Close(); e != nil {
				t.Fatal(e)
			}
			native, e = manticore.Open(context.Background(), 8101, proxied, "")
			if e != nil {
				t.Fatal(e)
			}
			active.Generation++
			s.Consume(ui.Services{})
			apply()
			if busy() || gate.calls.Load() != 1 {
				t.Fatal("reconnect replayed operation")
			}
			capture("en-schema-table-" + operation + "-unknown-receipt")
			before, _ := json.Marshal(unknown.Receipt)
			after, _ := json.Marshal(s.SchemaTableOriginalOutcome(operation).Receipt)
			if string(before) != string(after) {
				t.Fatal("later reads replaced original receipt")
			}
			if dir := os.Getenv("NYADB_MANTICORE_EVIDENCE"); dir != "" {
				b, e := json.MarshalIndent(map[string]any{"first": first.Receipt, "unknown": unknown.Receipt, "calls": gate.calls.Load(), "generation": active.Generation, "events": events, "actual": actual}, "", "  ")
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(dir, "native-schema-table-"+operation+"-workspace.json"), b, 0600); e != nil {
					t.Fatal(e)
				}
			}
			closeModal()
			begin()
			click("重新读取结构")
			wait(func() bool { return !busy() }, true)
			if source() == nil || !strings.Contains(source().Schema.DDL, "min_word_len") {
				t.Fatal("fresh document failed to recover original scope", s.TableError())
			}
			capture("en-schema-table-" + operation + "-recovered")
		})
	}
}
