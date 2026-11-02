package dataworkspace

import (
	"context"
	"image/png"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestPortableLargeDataSharedGrid(t *testing.T) {
	for _, kind := range []connections.Kind{connections.SQLite, connections.PostgreSQL} {
		t.Run(string(kind), func(t *testing.T) {
			profile := connections.Profile{ID: 102, Name: "Large data fixture", Kind: kind}
			schema := "main"
			catalog := "main"
			payload := "zeroblob(8192)"
			binary := "BLOB"
			if kind == connections.SQLite {
				profile.Database = filepath.Join(t.TempDir(), "large.sqlite")
			} else {
				if os.Getenv("NYADB_POSTGRES_IT") != "1" {
					t.Skip("requires owned PostgreSQL fixture")
				}
				port, err := strconv.ParseUint(os.Getenv("NYADB_POSTGRES_PORT"), 10, 16)
				if err != nil {
					t.Fatal(err)
				}
				profile.Host, profile.Port, profile.Username, profile.Database, profile.TLSMode = "127.0.0.1", uint16(port), "nyadb_it", "nyadb_it", "disable"
				schema, catalog, payload, binary = "public", profile.Database, "decode(repeat('00',8192),'hex')", "BYTEA"
			}
			m := dbruntime.New()
			dbruntime.RegisterBuiltins(m, nil)
			defer m.Close()
			ctx := context.Background()
			original, err := m.ConnectSession(ctx, profile, "")
			if err != nil {
				t.Fatal(err)
			}
			query, err := contract.Require[contract.QueryProvider](original)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := query.ReserveQuery(1)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			table := "nyadb_large_grid_" + strconv.FormatInt(time.Now().UnixNano(), 10)
			target := `"` + schema + `"."` + table + `"`
			_, err = lease.Execute(ctx, schema, "CREATE TABLE "+target+"(id BIGINT NOT NULL PRIMARY KEY,n BIGINT,note TEXT,payload "+binary+"); INSERT INTO "+target+" VALUES(1,9223372036854775805,'first',"+payload+"),(2,9223372036854775807,NULL,"+payload+"),(3,NULL,'last',NULL)")
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Execute(ctx, schema, "DROP TABLE "+target)
			session := func() Session {
				return Session{ProfileID: profile.ID, Generation: m.Generation(profile.ID), Connected: m.IsConnected(profile.ID)}
			}
			state := New(Dependencies{Session: session, Runtime: func() Runtime { return m.Data(profile.ID) }, Inspector: InspectorDependencies{Session: session, InfoLoader: func() InfoLoader { return m.Data(profile.ID).TableInfo }, CountLoader: func() CountLoader { return m.Data(profile.ID).Count }, ChunkLoader: func() ChunkLoader { return m.Data(profile.ID).Chunk }}})
			defer state.CloseAll()
			doc := state.OpenObject(3, contract.ObjectRef{ProfileID: profile.ID, Catalog: catalog, Schema: schema, Kind: "table", Name: table}, original.Capabilities())
			doc.limit = 2
			var services ui.Services
			tester := ui.NewTester(func(c *ui.Context) {
				services = c.Services()
				theme.Apply(c)
				state.Consume(services)
				ui.Row(c).Fill().Children(func() {
					ui.Box(c).Grow(1).MinWidth(0).Children(func() { state.View(c, doc) })
					ui.Box(c).Width(260).Children(func() { state.InspectorView(c, doc) })
				})
				state.Modals(c)
			}, 1280, 800)
			state.Load(services, doc, true)
			waitEngine(t, state, doc, tester)
			if doc.err != "" || !doc.page.KeysetAvailable || !doc.page.Rows[0][3].Truncated || doc.page.RowRefs[0] == nil {
				t.Fatalf("shared grid unavailable: %s", doc.err)
			}
			if err = tester.Click("下一页数据"); err != nil {
				t.Fatal(err)
			}
			waitEngine(t, state, doc, tester)
			if doc.page.Rows[0][0].Text != "3" || doc.after == nil {
				t.Fatal("next did not use real seek")
			}
			if err = tester.Click("上一页数据"); err != nil {
				t.Fatal(err)
			}
			waitEngine(t, state, doc, tester)
			if doc.page.Rows[0][0].Text != "1" {
				t.Fatal("previous did not restore cursor history")
			}
			if err = tester.Click("获取整表行数"); err != nil {
				t.Fatal(err)
			}
			waitTableInfo(t, tester, doc.panel.CountLoading)
			if doc.panel.Count() == nil || *doc.panel.Count() != 3 {
				t.Fatalf("real count failed: %s", doc.panel.CountError())
			}
			doc.selectedRow = 0
			doc.grid.Cells = ui.ColumnRange{Cursor: "payload", Anchor: "payload"}
			state.ShowField(doc)
			tester.Frame()
			if err = tester.Click("读取所选字段下一段"); err != nil {
				t.Fatal(err)
			}
			waitTableInfo(t, tester, doc.panel.ChunkLoading)
			if doc.panel.ChunkNext() != 1024 || doc.panel.ChunkTotal() != 8192 || doc.panel.ChunkError() != "" {
				t.Fatalf("real chunk failed: %s", doc.panel.ChunkError())
			}
			if err = clickDataToolbar(tester, "数据分析"); err != nil {
				t.Fatal(err)
			}
			tester.Frame()
			if err = tester.Click("开始分析"); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for doc.tools.analysisBusy && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
				tester.Frame()
			}
			tester.Frame()
			if doc.tools.analysisErr != "" || doc.tools.analysis == nil || doc.tools.analysis.Rows != 3 || !largeDataMeanEquals(doc.tools.analysis.Columns[1].Mean, "9223372036854775806") {
				t.Fatalf("real analysis failed: %s %+v", doc.tools.analysisErr, doc.tools.analysis)
			}
			if dir := os.Getenv("NYADB_LARGE_DATA_CAPTURE_DIR"); dir != "" {
				if err = os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				file, err := os.Create(filepath.Join(dir, string(kind)+"-tester.png"))
				if err != nil {
					t.Fatal(err)
				}
				err = png.Encode(file, tester.Image())
				file.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestInspectorChunkOwnsTypedAndEmptyBinaryIdentity(t *testing.T) {
	selection := fieldSelection()
	state, panel, _ := tableInfoTestState(selection)
	ref := selection.Page.RowRefs[selection.Row]
	ref.TypedValues = []contract.Cell{{Kind: contract.Binary, Bytes: []byte{0, 255}}, {Kind: contract.Binary, Bytes: []byte{}}}
	ref.Values = [][]byte{{0, 255}, {}}
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var captured contract.RowRef
	state.ChunkLoader = func(_ context.Context, _ int64, _, _, _ string, row contract.RowRef, _ int64, _ int) (*contract.FieldChunk, error) {
		close(started)
		<-release
		captured = row
		return &contract.FieldChunk{Text: "owned"}, nil
	}
	state.LoadChunk(panel, func() { close(done) })
	<-started
	ref.TypedValues[0].Bytes[0] = 7
	ref.Values[0][0] = 7
	close(release)
	<-done
	if captured.TypedValues[0].Bytes[0] != 0 || captured.Values[0][0] != 0 || captured.TypedValues[1].Bytes == nil || captured.Values[1] == nil {
		t.Fatal("chunk worker retained mutable or conflated empty binary identity")
	}
	state.CloseAll()
}

func largeDataMeanEquals(got, want string) bool {
	a, aok := new(big.Rat).SetString(got)
	b, bok := new(big.Rat).SetString(want)
	return aok && bok && a.Cmp(b) == 0
}
