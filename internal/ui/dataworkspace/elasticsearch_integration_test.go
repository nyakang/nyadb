package dataworkspace

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/elasticsearch"
)

func TestElasticsearchNativeSharedGridPagingCreateDeleteParkAndGeneration(t *testing.T) {
	if os.Getenv("NYADB_ELASTICSEARCH_IT") != "1" {
		t.Skip("requires owned disposable Elasticsearch")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_ELASTICSEARCH_CONFIG"))
	if err != nil {
		t.Fatal("private probe unavailable")
	}
	var c struct {
		Endpoint, Username, Auth string
		CAFile                   string `json:"ca_file"`
		PasswordFile             string `json:"password_file"`
	}
	if json.Unmarshal(raw, &c) != nil {
		t.Fatal("invalid private probe")
	}
	secret, err := os.ReadFile(c.PasswordFile)
	if err != nil {
		t.Fatal("private secret unavailable")
	}
	endpoint, _ := url.Parse(c.Endpoint)
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil {
		t.Fatal("invalid fixture port")
	}
	p, _ := connections.Defaults(connections.Elasticsearch)
	p.ID = 73
	p.Name = "Real shared-grid document"
	p.Host = endpoint.Hostname()
	p.Port = uint16(port)
	p.Username = c.Username
	p.ElasticsearchOptions.Auth = c.Auth
	p.ElasticsearchOptions.CAFile = c.CAFile
	if endpoint.Scheme == "http" {
		p.TLSMode = "disable"
	}
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	admin, err := elasticsearch.Open(ctx, connections.ElasticsearchConfig{Endpoint: c.Endpoint, Auth: c.Auth, Username: c.Username, CAFile: c.CAFile}, string(secret))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	index := "nyadb_grid_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	run := func(method, path, body string) {
		t.Helper()
		if _, e := admin.Do(ctx, elasticsearch.Request{Method: method, Path: path, Body: []byte(body)}); e != nil {
			t.Fatal(e)
		}
	}
	run("PUT", "/"+index, `{"settings":{"number_of_replicas":0},"mappings":{"dynamic":false}}`)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, e := admin.Do(cleanup, elasticsearch.Request{Method: "DELETE", Path: "/" + index}); e != nil {
			t.Error("owned grid cleanup", e)
		}
	}()
	for _, id := range []string{"a", "b", "c"} {
		run("PUT", "/"+index+"/_create/"+id+"?refresh=wait_for", `{"exact":9007199254740993}`)
	}
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	defer m.Close()
	handle, err := m.ConnectSession(ctx, p, string(secret))
	if err != nil {
		t.Fatal(err)
	}
	active := Session{ProfileID: p.ID, Generation: handle.Generation(), Connected: true}
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(id int64) Session {
		return Session{ProfileID: id, Generation: m.Generation(id), Connected: m.IsConnected(id)}
	}, RuntimeFor: m.Data})
	defer s.CloseAll()
	object := m.Namespace(p.ID, admin.Identity().ClusterName).Object("index", index)
	a := s.OpenObject(1, object, handle.Capabilities())
	b := s.OpenObject(2, object, handle.Capabilities())
	a.limit, b.limit = 1, 1
	wait := func(d *Document) {
		t.Helper()
		end := time.Now().Add(15 * time.Second)
		for d.Busy() && time.Now().Before(end) {
			s.Consume(ui.Services{})
			time.Sleep(time.Millisecond)
		}
		if d.Busy() || d.err != "" {
			t.Fatal("grid operation", d.err)
		}
	}
	s.Load(ui.Services{}, a, false)
	wait(a)
	s.Load(ui.Services{}, b, false)
	wait(b)
	first := a.page.Rows[0][1].Text
	siblingFirst := b.page.Rows[0][1].Text
	boundary := cloneCursor(a.page.CurrentCursor)
	if len(a.columns) != 4 || a.columns[0].Sortable || a.columns[1].Sortable || a.columns[2].Sortable {
		t.Fatal("unsupported native sort exposed")
	}
	if fields := s.filterColumns(a, a.page.Columns); len(fields) != 2 || fields[0] != "_id" || fields[1] != "_routing" {
		t.Fatal("unsupported native filters exposed")
	}
	s.changeMySQLDataPage(ui.Services{}, a, 1)
	wait(a)
	if a.page.Rows[0][1].Text == first {
		t.Fatal("native next page repeated first hit")
	}
	s.changeMySQLDataPage(ui.Services{}, a, 0)
	wait(a)
	if a.page.Rows[0][1].Text != first {
		t.Fatal("native previous page lost original boundary")
	}
	if b.page.Rows[0][1].Text != siblingFirst || b.page.CurrentCursor == nil || boundary == nil {
		t.Fatal("independent document paging changed")
	}
	// Apply unsupported column rules on the UI thread before any executor runs.
	a.filterColumnDraft = "document"
	a.filterModeDraft = "精确匹配"
	a.filterValueDraft = `{"query":{}}`
	s.applyMySQLDataRules(ui.Services{}, a)
	if a.ruleError == "" || a.loading {
		t.Fatal("unsupported field dispatched")
	}
	a.filterColumnDraft = "选择字段"
	a.ruleError = ""
	a.edit.Batch = true
	s.addMySQLDataRecord(ui.Services{}, a)
	s.finishMySQLCellEdit(a, false)
	inserted := len(a.page.Rows) - 1
	s.EditCell(ui.Services{}, a, inserted, "document", contract.Cell{Kind: contract.JSON, Text: `{"name":"created by shared grid🐱","exact":9007199254740995}`})
	s.Save(ui.Services{}, a)
	wait(a)
	if a.HasDraft() || a.offset != 0 || a.page.CurrentCursor == nil {
		t.Fatal("shared-grid create did not establish fresh PIT", a.edit.Err)
	}
	count, err := s.runtimeFor(a).Count(ctx, p.ID, a.database, index)
	if err != nil || count != 4 {
		t.Fatal("create count", err)
	}
	// Park a completed read; publishing waits until its original profile returns.
	s.Load(ui.Services{}, a, true)
	active.ProfileID = 99
	var read mysqlDataResult
	select {
	case read = <-a.results:
	case <-time.After(15 * time.Second):
		t.Fatal("parked read stalled")
	}
	original := a.page
	ready := make(chan mysqlDataResult, 1)
	ready <- read
	a.results = ready
	s.Consume(ui.Services{})
	if !a.loading || a.page != original {
		t.Fatal("parked read replaced active cache")
	}
	active.ProfileID = p.ID
	s.Consume(ui.Services{})
	if a.loading || a.page == nil {
		t.Fatal("original profile did not consume parked read")
	}
	// Delete only the original signed hit currently selected in a fresh page.
	a.selectedRow = 0
	a.selection.Clear()
	a.selection.Add(0)
	s.deleteMySQLDataRecords(ui.Services{}, a, 0)
	s.Save(ui.Services{}, a)
	wait(a)
	count, err = s.runtimeFor(a).Count(ctx, p.ID, a.database, index)
	if err != nil || count != 3 || a.HasDraft() {
		t.Fatal("shared-grid delete", err, a.edit.Err)
	}
	// Closing one lease leaves the second PIT usable, then a public generation
	// replacement revokes the exact old document instead of following the manager.
	s.Close(a)
	if b.closed {
		t.Fatal("document close damaged sibling")
	}
	s.changeMySQLDataPage(ui.Services{}, b, 1)
	wait(b)
	if !strings.Contains(b.page.Rows[0][3].Text, "exact") {
		t.Fatal("sibling lost actual data")
	}
	newer, err := m.ConnectSession(ctx, p, string(secret))
	if err != nil || newer.Generation() <= active.Generation {
		t.Fatal("replacement generation", err)
	}
	s.Load(ui.Services{}, b, true)
	s.Consume(ui.Services{})
	deadline := time.Now().Add(10 * time.Second)
	for b.loading && time.Now().Before(deadline) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if b.loading {
		t.Fatal("revoked document stayed loading")
	}
	if b.err == "" {
		t.Fatal("old document silently followed replacement")
	}
}
