package dataworkspace

import (
	"context"
	"encoding/json"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/solr"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestSolrNativeSharedGridPagingInspectorParkGenerationAndClose(t *testing.T) {
	if os.Getenv("NYADB_SOLR_DATA_IT") != "1" || os.Getenv("NYADB_SOLR_IT") != "1" {
		t.Skip("requires owned native Solr shared-grid fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_SOLR_CONFIG"))
	if err != nil {
		t.Fatal("private probe unavailable")
	}
	var probe struct {
		Endpoint, Username, Auth string
		CAFile                   string `json:"ca_file"`
		ServerName               string `json:"server_name"`
		PasswordFile             string `json:"password_file"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		t.Fatal("invalid private probe")
	}
	secret, err := os.ReadFile(probe.PasswordFile)
	if err != nil {
		t.Fatal("private secret unavailable")
	}
	endpoint, _ := url.Parse(probe.Endpoint)
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := connections.Defaults(connections.Solr)
	p.ID, p.Name, p.Host, p.Port = 73, "Solr shared grid", endpoint.Hostname(), uint16(port)
	p.Username = probe.Username
	p.SolrOptions = connections.SolrOptions{Version: 1, Auth: probe.Auth, ContextPath: endpoint.Path, CAFile: probe.CAFile, ServerName: probe.ServerName}
	if endpoint.Scheme == "http" {
		p.TLSMode = "disable"
	}
	ctx, stop := context.WithTimeout(context.Background(), 180*time.Second)
	defer stop()
	handle, err := solr.OpenSession(ctx, p, string(secret))
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	provider, err := contract.Require[contract.MetadataProvider](handle)
	if err != nil {
		t.Fatal(err)
	}
	namespaces, err := provider.Namespaces(ctx)
	if err != nil || len(namespaces) != 1 {
		t.Fatal(err)
	}
	object := namespaces[0].Ref.Object("core", "nyadb_ui_data")
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
	first, sibling := a.page.Rows[0][2].Text, b.page.Rows[0][2].Text
	if len(a.columns) != 5 || a.page.Editable == nil || *a.page.Editable {
		t.Fatal("native projection became editable")
	}
	for _, column := range a.columns {
		if column.Sortable {
			t.Fatal("unsupported field sort exposed")
		}
	}
	if fields := s.filterColumns(a, a.page.Columns); len(fields) != 1 || fields[0] != "_key" {
		t.Fatal("unsupported filters exposed", fields)
	}
	s.changeMySQLDataPage(ui.Services{}, a, 1)
	wait(a)
	if a.page.Rows[0][2].Text == first {
		t.Fatal("next page repeated first native key")
	}
	s.changeMySQLDataPage(ui.Services{}, a, 0)
	wait(a)
	if a.page.Rows[0][2].Text != first || b.page.Rows[0][2].Text != sibling {
		t.Fatal("original boundaries/sibling lost")
	}
	a.selectedRow, a.grid.Cells.Cursor = 0, "document"
	panel := s.inspectorPanel(a)
	s.inspector.LoadInfo(panel, nil, false)
	s.inspector.LoadCount(panel, nil)
	s.inspector.LoadChunk(panel, nil)
	end := time.Now().Add(30 * time.Second)
	for (panel.InfoLoading() || panel.CountLoading() || panel.ChunkLoading()) && time.Now().Before(end) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if panel.Info() == nil || panel.Info().Engine != "Solr" || panel.Count() == nil || *panel.Count() != 3 || panel.ChunkError() != "" || panel.ChunkNext() != 1024 {
		t.Fatal("original native inspector", panel.Info(), panel.InfoError(), panel.Count(), panel.CountError(), panel.ChunkError(), panel.ChunkNext())
	}
	if !strings.Contains(a.page.Rows[0][4].Text, "9007199254740993") {
		t.Fatal("shared grid rounded native integer")
	}
	if folder := os.Getenv("NYADB_SOLR_DATA_IMAGES"); folder != "" {
		if err = os.MkdirAll(folder, 0700); err != nil {
			t.Fatal(err)
		}
		tester := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.View(c, a) }, 1200, 720)
		tester.Frame()
		file, e := os.Create(filepath.Join(folder, "native-shared-grid.png"))
		if e != nil {
			t.Fatal(e)
		}
		err = png.Encode(file, tester.Image())
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
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
