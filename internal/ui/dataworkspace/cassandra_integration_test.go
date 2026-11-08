package dataworkspace

import (
	"context"
	"encoding/json"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/cassandra"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestCassandraNativeSharedGridPagingInspectorParkGenerationAndClose(t *testing.T) {
	if os.Getenv("NYADB_CASSANDRA_DATA_IT") != "1" || os.Getenv("NYADB_CASSANDRA_IT") != "1" {
		t.Skip("requires owned native Cassandra shared-grid fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_CASSANDRA_CONFIG"))
	if err != nil {
		t.Fatal("private probe unavailable")
	}
	var probe struct {
		Endpoint, Username, Auth, TLSMode string
		CAFile                            string `json:"ca_file"`
		ServerName                        string `json:"server_name"`
		PasswordFile                      string `json:"password_file"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		t.Fatal("invalid private probe")
	}
	secret, err := os.ReadFile(probe.PasswordFile)
	if err != nil {
		t.Fatal("private secret unavailable")
	}
	host, portText, _ := net.SplitHostPort(probe.Endpoint)
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	cfg := connections.CassandraConfig{Endpoint: probe.Endpoint, Username: probe.Username, Auth: probe.Auth, TLSMode: probe.TLSMode, CAFile: probe.CAFile, ServerName: probe.ServerName}
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	admin, err := cassandra.Open(ctx, cfg, string(secret))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	ks := "nyadb_ui_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	run := func(statement string, params ...cassandra.Parameter) {
		t.Helper()
		if _, err := admin.Execute(ctx, cassandra.Request{Statement: statement, Parameters: params}); err != nil {
			t.Fatal(err)
		}
	}
	run("CREATE KEYSPACE " + ks + " WITH replication={'class':'SimpleStrategy','replication_factor':1}")
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := admin.Execute(cleanup, cassandra.Request{Statement: "DROP KEYSPACE " + ks}); err != nil {
			t.Error("owned grid cleanup", err)
		}
	}()
	run("CREATE TABLE " + ks + ".rows (id int PRIMARY KEY,amount decimal,memo text)")
	for _, id := range []string{"1", "2", "3"} {
		run("INSERT INTO "+ks+".rows(id,amount,memo) VALUES ("+id+",9007199254740993.123456789,?)", cassandra.Parameter{Bytes: []byte(strings.Repeat("猫", 2000))})
	}
	p, _ := connections.Defaults(connections.Cassandra)
	p.ID, p.Name, p.Host, p.Port, p.Database = 73, "Cassandra shared grid", host, uint16(port), ks
	p.Username, p.TLSMode = probe.Username, probe.TLSMode
	p.CassandraOptions = connections.CassandraOptions{Version: 1, Auth: probe.Auth, CAFile: probe.CAFile, ServerName: probe.ServerName}
	handle, err := cassandra.OpenSession(ctx, p, string(secret))
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	object := (contract.NamespaceRef{ProfileID: p.ID, Catalog: ks}).Object("table", "rows")
	active := Session{ProfileID: p.ID, Generation: handle.Generation(), Connected: true}
	runtime := contract.ComposeData(handle)
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(id int64) Session { return Session{ProfileID: id, Generation: active.Generation, Connected: true} }, RuntimeFor: func(id int64) Runtime { return runtime }, Inspector: InspectorDependencies{Session: func() Session { return active }}})
	defer s.CloseAll()
	a, b := s.OpenObject(1, object, handle.Capabilities()), s.OpenObject(2, object, handle.Capabilities())
	a.limit, b.limit = 1, 1
	wait := func(d *Document) {
		t.Helper()
		end := time.Now().Add(30 * time.Second)
		for d.Busy() && time.Now().Before(end) {
			s.Consume(ui.Services{})
			time.Sleep(time.Millisecond)
		}
		if d.Busy() || d.err != "" {
			t.Fatal("native shared grid operation", d.err)
		}
	}
	s.Load(ui.Services{}, a, false)
	wait(a)
	s.Load(ui.Services{}, b, false)
	wait(b)
	first, sibling := a.page.Rows[0][0].Text, b.page.Rows[0][0].Text
	if len(a.columns) != 3 || a.page.Editable == nil || *a.page.Editable {
		t.Fatal("native projection became editable")
	}
	for _, column := range a.columns {
		if column.Sortable {
			t.Fatal("unsupported field sort exposed")
		}
	}
	if fields := s.filterColumns(a, a.page.Columns); len(fields) != 0 {
		t.Fatal("unsupported filters exposed", fields)
	}
	s.changeMySQLDataPage(ui.Services{}, a, 1)
	wait(a)
	if a.page.Rows[0][0].Text == first {
		t.Fatal("next page repeated first native key")
	}
	s.changeMySQLDataPage(ui.Services{}, a, 0)
	wait(a)
	if a.page.Rows[0][0].Text != first || b.page.Rows[0][0].Text != sibling {
		t.Fatal("original boundaries/sibling lost")
	}
	a.selectedRow, a.grid.Cells.Cursor = 0, "memo"
	panel := s.inspectorPanel(a)
	s.inspector.LoadInfo(panel, nil, false)
	s.inspector.LoadCount(panel, nil)
	s.inspector.LoadChunk(panel, nil)
	end := time.Now().Add(30 * time.Second)
	for (panel.InfoLoading() || panel.CountLoading() || panel.ChunkLoading()) && time.Now().Before(end) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if panel.Info() == nil || panel.Info().Engine != "Cassandra" || panel.Count() == nil || *panel.Count() != 3 || panel.ChunkError() != "" || panel.ChunkNext() != 1024 {
		t.Fatal("original native inspector", panel.Info(), panel.InfoError(), panel.Count(), panel.CountError(), panel.ChunkError(), panel.ChunkNext())
	}
	if !strings.Contains(a.page.Rows[0][slices.Index(a.page.Columns, "amount")].Text, "9007199254740993") {
		t.Fatal("shared grid rounded native integer")
	}
	if folder := os.Getenv("NYADB_CASSANDRA_DATA_IMAGES"); folder != "" {
		if err = os.MkdirAll(folder, 0700); err != nil {
			t.Fatal(err)
		}
		defer i18n.SetLocaleFunc(nil)
		for _, lang := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return lang })
			for _, width := range []int{1200, 900} {
				tester := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.View(c, a) }, width, 720)
				tester.Frame()
				if _, ok := tester.Find(locales.TextIn(lang, "原生游标读取实时数据；翻页和重读期间数据变化可能导致行重复或遗漏。")); !ok {
					t.Fatal("native live warning missing", lang)
				}
				if mysqlDataPageMethod(a.page) != locales.TextIn(lang, "原生游标") {
					t.Fatal("native method mislabeled")
				}
				file, e := os.Create(filepath.Join(folder, lang+"-"+strconv.Itoa(width)+"-native-shared-grid.png"))
				if e != nil {
					t.Fatal(e)
				}
				e = png.Encode(file, tester.Image())
				file.Close()
				if e != nil {
					t.Fatal(e)
				}
			}
		}
	}

	s.Load(ui.Services{}, a, true)
	active.ProfileID = 99
	var parked mysqlDataResult
	select {
	case parked = <-a.results:
	case <-time.After(30 * time.Second):
		t.Fatal("native parked page stalled")
	}
	original := a.page
	ready := make(chan mysqlDataResult, 1)
	ready <- parked
	a.results = ready
	s.Consume(ui.Services{})
	if !a.loading || a.page != original {
		t.Fatal("parked native page replaced cache")
	}
	active.ProfileID = p.ID
	s.Consume(ui.Services{})
	wait(a)
	s.Close(a)
	s.changeMySQLDataPage(ui.Services{}, b, 1)
	wait(b)
	active.Generation++
	s.Load(ui.Services{}, b, true)
	s.Consume(ui.Services{})
	end = time.Now().Add(30 * time.Second)
	for b.loading && time.Now().Before(end) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if b.loading || b.err == "" {
		t.Fatal("original document followed new generation")
	}
}
