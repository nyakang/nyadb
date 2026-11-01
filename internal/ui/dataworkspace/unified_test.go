package dataworkspace

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestUnifiedSnapshotsCloneRawValuesAndWriteEvidence(t *testing.T) {
	raw := []byte{0, 255}
	c := contract.Cell{Kind: contract.Binary, Bytes: raw}
	original := &contract.RowSnapshot{Mode: "values", Original: []contract.Cell{c}, Ref: contract.RowRef{TypedValues: []contract.Cell{c}}}
	result := &contract.DataEditResult{Incomplete: true, Saved: []contract.Edit{{Identity: contract.RowIdentity{Values: []contract.Cell{c}}, Original: []contract.Cell{c}, Values: map[string]contract.Cell{"raw": c}}}, Outcomes: []contract.DataEditOutcome{{Saved: original, Previews: []contract.DataCell{{Value: &c}}}}}
	owned := cloneEditResult(result)
	raw[0] = 7
	if owned.Saved[0].Identity.Values[0].Bytes[0] != 0 || owned.Saved[0].Original[0].Bytes[0] != 0 || owned.Saved[0].Values["raw"].Bytes[0] != 0 || owned.Outcomes[0].Saved.Original[0].Bytes[0] != 0 || owned.Outcomes[0].Saved.Ref.TypedValues[0].Bytes[0] != 0 || owned.Outcomes[0].Previews[0].Value.Bytes[0] != 0 {
		t.Fatal("worker retained UI-owned raw bytes")
	}
	hash := "original"
	snapshot := cloneSnapshot(&contract.RowSnapshot{Mode: "fingerprints", Fingerprints: []*string{&hash, nil}})
	hash = "mutated"
	if *snapshot.Fingerprints[0] != "original" || snapshot.Fingerprints[1] != nil {
		t.Fatal("fingerprint payload changed")
	}
}
func TestUnifiedLayoutsKeepBothStoredKeysAndFormats(t *testing.T) {
	for _, portable := range []bool{false, true} {
		s := New(Dependencies{})
		d := s.Open(9, 12, "main", "records")
		d.page = &contract.DataPage{Columns: []string{"id", "note"}}
		prefix := "mysql_data_layout:"
		var identity []byte
		if portable {
			d.object = contract.ObjectRef{ProfileID: 12, Catalog: "main", Schema: "main", Kind: "table", Name: "records"}
			prefix = "engine_data_layout:"
			identity, _ = json.Marshal(d.object)
		} else {
			identity, _ = json.Marshal([]any{int64(12), "main", "records"})
		}
		key := prefix + base64.RawURLEncoding.EncodeToString(identity)
		stored := `{"version":1,"order":["note","id"],"widths":{"note":240}}`
		s.SetDataLayoutPreferences(func(got string) (string, error) {
			if got != key {
				t.Fatalf("legacy layout key changed: %s", got)
			}
			return stored, nil
		}, nil)
		s.restoreMySQLDataLayout(d)
		if !reflect.DeepEqual(d.grid.Columns.Order, []string{"note", "id"}) || d.grid.Columns.Widths["note"] != 240 {
			t.Fatal("old layout not restored")
		}
		if portable && mysqlDataLayoutSnapshot(d) != stored {
			t.Fatalf("portable format changed: %s", mysqlDataLayoutSnapshot(d))
		}
	}
}
func TestUnifiedGridRepresentative(t *testing.T) {
	for _, kind := range []connections.Kind{connections.MySQL, connections.PostgreSQL, connections.SQLite} {
		t.Run(string(kind), func(t *testing.T) {
			p := connections.Profile{ID: 77, Name: "Unified grid fixture", Kind: kind}
			catalog, schema := "main", "main"
			quote := func(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
			switch kind {
			case connections.MySQL:
				if os.Getenv("NYADB_MYSQL_CORE_IT") != "1" {
					t.Skip("requires a new explicitly enabled disposable MySQL fixture")
				}
				port, e := strconv.Atoi(os.Getenv("NYADB_MYSQL_CORE_PORT"))
				if e != nil || port < 1024 {
					t.Fatal("invalid fixture port")
				}
				p.Host, p.Port, p.Username, p.Database = "127.0.0.1", uint16(port), "nyadb_core", "nyadb_core_it"
				p.MySQLAdvanced = connections.DefaultMySQLAdvanced()
				catalog, schema = p.Database, ""
				quote = func(v string) string { return "`" + strings.ReplaceAll(v, "`", "``") + "`" }
			case connections.PostgreSQL:
				if os.Getenv("NYADB_POSTGRES_IT") != "1" {
					t.Skip("requires a new explicitly enabled disposable PostgreSQL fixture")
				}
				port, e := strconv.Atoi(os.Getenv("NYADB_POSTGRES_PORT"))
				if e != nil || port < 1024 {
					t.Fatal("invalid fixture port")
				}
				p.Host, p.Port, p.Username, p.Database, p.TLSMode = "127.0.0.1", uint16(port), "nyadb_it", "nyadb_it", "disable"
				catalog, schema = p.Database, "public"
			case connections.SQLite:
				p.Database = filepath.Join(t.TempDir(), "representative.sqlite")
			}
			m := dbruntime.New()
			dbruntime.RegisterBuiltins(m, nil)
			t.Cleanup(func() { m.Close() })
			ctx := context.Background()
			original, e := m.ConnectSession(ctx, p, "")
			if e != nil {
				t.Fatal(e)
			}
			qp, e := contract.Require[contract.QueryProvider](original)
			if e != nil {
				t.Fatal(e)
			}
			lease, e := qp.ReserveQuery(8)
			if e != nil {
				t.Fatal(e)
			}
			defer lease.Release()
			table := "nyadb_unified_" + strconv.FormatInt(time.Now().UnixNano(), 10)
			object := contract.ObjectRef{ProfileID: p.ID, Catalog: catalog, Schema: schema, Kind: "table", Name: table}
			namespace := schema
			if namespace == "" {
				namespace = catalog
			}
			target := quote(namespace) + "." + quote(table)
			binaryType, binaryValue := "BLOB", "x'00ff'"
			numberType := "TEXT"
			if kind == connections.PostgreSQL {
				binaryType, binaryValue, numberType = "BYTEA", `decode('00ff','hex')`, "NUMERIC(38,18)"
			}
			if kind == connections.MySQL {
				numberType = "DECIMAL(38,18)"
			}
			sql := "CREATE TABLE " + target + " (id BIGINT NOT NULL PRIMARY KEY, note VARCHAR(120), amount " + numberType + ", payload " + binaryType + "); INSERT INTO " + target + " VALUES (1,'共享网格 · NULL 文本','12345678901234567890.123456789012345678'," + binaryValue + "),(2,NULL,'0',NULL);"
			if _, e = lease.Execute(ctx, namespace, sql); e != nil {
				t.Fatal(e)
			}
			defer lease.Execute(ctx, namespace, "DROP TABLE "+target)
			session := func() Session {
				return Session{ProfileID: p.ID, Generation: m.Generation(p.ID), Connected: m.IsConnected(p.ID)}
			}
			state := New(Dependencies{Session: session, Runtime: func() Runtime { return m.Data(p.ID) }, Inspector: InspectorDependencies{Session: session, InfoLoader: func() InfoLoader { return m.Data(p.ID).TableInfo }, CountLoader: func() CountLoader { return m.Data(p.ID).Count }, ChunkLoader: func() ChunkLoader { return m.Data(p.ID).Chunk }}})
			defer state.CloseAll()
			doc := state.OpenObject(3, object, original.Capabilities())
			var services ui.Services
			tt := ui.NewTester(func(c *ui.Context) {
				services = c.Services()
				theme.Apply(c)
				state.Consume(services)
				ui.Row(c).Fill().Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Children(func() { state.View(c, doc) })
					ui.Column(c).Width(260).Children(func() { state.InspectorView(c, doc) })
				})
				state.Modals(c)
			}, 1280, 800)
			state.Load(services, doc, true)
			waitEngine(t, state, doc, tt)
			if doc.Page() == nil || doc.Error() != "" || !tt.HasText("共享网格 · NULL 文本") {
				t.Fatalf("shared grid failed: %s", doc.Error())
			}
			if doc.portable() && (tt.HasText("数据分析") || tt.HasText("导入") || tt.HasText("获取整表行数")) {
				t.Fatal("unimplemented capability rendered")
			}
			if doc.Page().Rows[0][2].Text != "12345678901234567890.123456789012345678" {
				t.Fatal("exact number changed")
			}
			if dir := os.Getenv("NYADB_UNIFIED_GRID_CAPTURE_DIR"); dir != "" {
				if e = os.MkdirAll(dir, 0755); e != nil {
					t.Fatal(e)
				}
				f, e := os.Create(filepath.Join(dir, string(kind)+".png"))
				if e != nil {
					t.Fatal(e)
				}
				if e = png.Encode(f, tt.Image()); e != nil {
					t.Fatal(e)
				}
				if e = f.Close(); e != nil {
					t.Fatal(e)
				}
			}
			state.EditCell(services, doc, 0, "note", contract.Cell{Kind: contract.Text, Text: "统一写入"})
			state.Save(services, doc)
			waitEngine(t, state, doc, tt)
			if doc.HasDraft() || doc.edit.Err != "" {
				t.Fatalf("shared atomic save failed: %s", doc.edit.Err)
			}
			state.Begin(services, doc)
			waitEngine(t, state, doc, tt)
			if !doc.HasTransaction() {
				t.Fatalf("shared transaction not retained: %s", doc.transaction.err)
			}
			state.EditCell(services, doc, 1, "note", contract.Cell{Kind: contract.Text, Text: "事务写入"})
			state.Save(services, doc)
			waitEngine(t, state, doc, tt)
			state.Commit(services, doc)
			waitEngine(t, state, doc, tt)
			if doc.HasDraft() || doc.HasTransaction() || doc.edit.Uncertain != nil {
				t.Fatalf("shared commit did not settle: %s %s", doc.edit.Err, doc.transaction.err)
			}
			result, err := lease.Execute(ctx, namespace, "SELECT note FROM "+target+" WHERE id=2")
			if err != nil || len(result.Sets) != 1 || len(result.Sets[0].Rows) != 1 || result.Sets[0].Rows[0][0].Text != "事务写入" {
				t.Fatalf("shared write missing from database: %v", err)
			}
			if _, err = lease.Execute(ctx, namespace, "UPDATE "+target+" SET note='external change' WHERE id=1"); err != nil {
				t.Fatal(err)
			}
			state.EditCell(services, doc, 0, "note", contract.Cell{Kind: contract.Text, Text: "conflicting write"})
			state.Save(services, doc)
			waitEngine(t, state, doc, tt)
			if !doc.HasDraft() || doc.edit.Err == "" {
				t.Fatal("shared grid replayed a conflicting original row")
			}
			state.discardMySQLData(doc)
			if kind == connections.PostgreSQL {
				pidResult, err := lease.Execute(ctx, namespace, "SELECT pg_backend_pid()")
				if err != nil {
					t.Fatal(err)
				}
				pid, err := strconv.Atoi(pidResult.Sets[0].Rows[0][0].Text)
				if err != nil {
					t.Fatal(err)
				}
				killer, err := qp.ReserveQuery(19)
				if err != nil {
					t.Fatal(err)
				}
				defer killer.Release()
				if _, err = killer.Execute(ctx, namespace, "SELECT pg_terminate_backend("+strconv.Itoa(pid)+")"); err != nil {
					t.Fatal(err)
				}
				if _, err = lease.Execute(ctx, namespace, "SELECT 1"); !errors.Is(err, contract.ErrSQLSessionLost) {
					t.Fatalf("schema selection did not revoke lost pinned connection: %v", err)
				}
			}

		})
	}
}
