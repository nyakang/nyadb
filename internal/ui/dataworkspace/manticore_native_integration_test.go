package dataworkspace

import (
	"context"
	"fmt"
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
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/manticore"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// This exercises the sole shared State/Document against real native sessions.
// The provider is injected privately; it does not claim public chooser/runtime
// publication or a desktop compositor capture.
func TestManticoreNativeSharedGridPermissionsPagingParkingAndRecovery(t *testing.T) {
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
	current, e := manticore.Open(context.Background(), 7001, cfg, "")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { current.Close() }()
	if !strings.HasPrefix(current.Version(), os.Getenv("NYADB_MANTICORE_VERSION")+" ") {
		t.Fatal("original version", current.Version())
	}
	q, e := current.ReserveQuery(1)
	if e != nil {
		t.Fatal(e)
	}
	defer q.Release()
	table := fmt.Sprintf("nyadb_grid70_%d", time.Now().UnixNano())
	run := func(source string) {
		t.Helper()
		if _, e := q.Execute(context.Background(), "Manticore", source); e != nil {
			t.Fatal(e)
		}
	}
	run("CREATE TABLE " + table + " (title text, qty integer, tag string, metadata json)")
	run("INSERT INTO " + table + " (id,title,qty,tag,metadata) VALUES (1,'猫 原始行',7,'a','{\"original\":true}'),(2,'" + strings.Repeat("猫", 2049) + "',8,'b','{\"n\":2}'),(9223372036854775807,'末行',9,'c','{\"n\":3}')")
	defer func() {
		lease, e := current.ReserveQuery(99)
		if e == nil {
			defer lease.Release()
			if _, e = lease.Execute(context.Background(), "Manticore", "DROP TABLE "+table); e != nil {
				t.Error(e)
			}
		}
	}()
	active := Session{ProfileID: 7001, Generation: 1, Connected: true}
	originalSession := func(id int64) Session {
		return Session{ProfileID: id, Generation: active.Generation, Connected: current.Alive() && id == 7001}
	}
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: originalSession, RuntimeFor: func(id int64) Runtime {
		if id != 7001 {
			return Runtime{}
		}
		return Runtime{Capabilities: current.DataDocumentCapabilities(), ReserveDocument: current.ReserveDataDocument}
	}, Inspector: InspectorDependencies{Session: func() Session { return active }, Profile: func(id int64) (connections.Profile, bool) {
		return connections.Profile{ID: 7001, Name: "Manticore original grid", Host: host, Port: uint16(n)}, id == 7001
	}}})
	defer s.CloseAll()
	object := contract.ObjectRef{ProfileID: 7001, Catalog: "Manticore", Kind: "TABLE", Name: table}
	d := s.OpenObject(1, object, current.DataDocumentCapabilities())
	if d == nil || d.runtimeLease == nil {
		t.Fatal("missing original data lease")
	}
	d.limit = 2
	wait := func(done func() bool) {
		t.Helper()
		until := time.Now().Add(20 * time.Second)
		for !done() && time.Now().Before(until) {
			s.Consume(ui.Services{})
			time.Sleep(time.Millisecond)
		}
		s.Consume(ui.Services{})
		if !done() {
			t.Fatal("original UI request did not settle")
		}
	}
	s.Load(ui.Services{}, d, false)
	wait(func() bool { return !d.loading })
	if d.err != "" || d.page == nil || len(d.page.Rows) != 2 || !d.page.HasMore || d.page.Rows[0][1].Text != "猫 原始行" || !d.page.Rows[1][1].Truncated {
		t.Fatal("native shared page", d.err)
	}
	if d.editable() || d.capabilities.EditableTables || d.boundRuntime.Chunk != nil {
		t.Fatal("unaccepted tools enabled")
	}
	if !slices.Equal(s.filterColumns(d, d.page.Columns), []string{"id", "qty", "tag"}) {
		t.Fatal("column permission lost", s.filterColumns(d, d.page.Columns))
	}
	if dataRuleProblem(d, contract.DataFilterRule{Column: "title", Mode: "equal", Value: "猫"}) == "" || dataRuleProblem(d, contract.DataFilterRule{Column: "qty", Mode: "equal", Value: "7"}) != "" {
		t.Fatal("native filter eligibility lost")
	}
	for _, c := range d.columns {
		if (c.ID == "title" || c.ID == "metadata") && c.Sortable {
			t.Fatal("unsupported sortable field", c.ID)
		}
	}
	panel := s.inspectorPanel(d)
	s.inspector.LoadInfo(panel, nil, false)
	s.inspector.LoadCount(panel, nil)
	wait(func() bool { return !panel.InfoLoading() && !panel.CountLoading() })
	if panel.Count() == nil || *panel.Count() != 3 {
		t.Fatal("original count", panel.Count())
	}
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.Consume(c.Services())
		ui.Row(c).Fill().Children(func() {
			ui.Box(c).Grow(1).MinWidth(0).FillHeight().Children(func() { s.View(c, d) })
			ui.Box(c).Width(320).FillHeight().Children(func() { s.InspectorView(c, d) })
		})
	}, 1280, 800)
	tt.Frame()
	if !tt.HasText("猫 原始行") || !tt.HasText("qty") {
		t.Fatal("original grid not rendered", tt.Texts())
	}
	capture := func(locale string) {
		t.Helper()
		i18n.SetLocaleFunc(func() string { return locale })
		tt.Frame()
		if !tt.HasText(locales.Text(d.boundRuntime.ReadScopeHint)) {
			t.Fatal("localized original scope hint absent", locale)
		}
		if directory := os.Getenv("NYADB_MANTICORE_EVIDENCE"); directory != "" {
			file, e := os.Create(filepath.Join(directory, locale+"-shared-grid.png"))
			if e != nil {
				t.Fatal(e)
			}
			e = png.Encode(file, tt.Image())
			closeErr := file.Close()
			if e != nil || closeErr != nil {
				t.Fatal(e, closeErr)
			}
		}
	}
	capture("zh-Hans")
	capture("en")
	s.changeMySQLDataPage(ui.Services{}, d, 2)
	wait(func() bool { return !d.loading })
	if d.err != "" || len(d.page.Rows) != 1 || d.page.HasMore || d.page.Rows[0][0].Text != "9223372036854775807" {
		t.Fatal("native next page", d.err)
	}
	s.changeMySQLDataPage(ui.Services{}, d, 0)
	wait(func() bool { return !d.loading })
	// An actual completed page remains buffered while another profile is active.
	oldPage := d.page
	s.Load(ui.Services{}, d, true)
	results := d.results
	active.ProfileID = 7999
	var original mysqlDataResult
	select {
	case original = <-results:
	case <-time.After(20 * time.Second):
		t.Fatal("original page did not return")
	}
	if original.err != nil || original.page == nil {
		t.Fatal(original.err)
	}
	buffered := make(chan mysqlDataResult, 1)
	buffered <- original
	d.results = buffered
	s.Consume(ui.Services{})
	if !d.loading || d.page != oldPage || len(buffered) != 1 {
		t.Fatal("parked page published to another profile")
	}
	active.ProfileID = 7001
	s.Consume(ui.Services{})
	if d.loading || d.page == oldPage || d.err != "" {
		t.Fatal("original parked page not published", d.err)
	}
	// Real disconnect/reopen changes the injected host generation. The original
	// buffered page and document must not attach themselves to the fresh session.
	oldPage = d.page
	s.Load(ui.Services{}, d, true)
	results = d.results
	select {
	case original = <-results:
	case <-time.After(20 * time.Second):
		t.Fatal("late original page missing")
	}
	if original.err != nil {
		t.Fatal(original.err)
	}
	if e = current.Close(); e != nil {
		t.Fatal(e)
	}
	current, e = manticore.Open(context.Background(), 7001, cfg, "")
	if e != nil {
		t.Fatal(e)
	}
	active.Generation++
	buffered = make(chan mysqlDataResult, 1)
	buffered <- original
	d.results = buffered
	s.Consume(ui.Services{})
	if d.loading || d.page != oldPage || d.err == "" {
		t.Fatal("old generation replaced cached data")
	}
	s.Load(ui.Services{}, d, true)
	if d.loading {
		t.Fatal("old document silently recovered")
	}
	s.Close(d)
	fresh := s.OpenObject(2, object, current.DataDocumentCapabilities())
	fresh.limit = 2
	s.Load(ui.Services{}, fresh, false)
	wait(func() bool { return !fresh.loading })
	if fresh.err != "" || fresh.page == nil || fresh.page.Rows[0][1].Text != "猫 原始行" {
		t.Fatal("explicit fresh document", fresh.err)
	}
	s.Close(fresh)
	if fresh.runtimeLease != nil || fresh.boundRuntime != nil || fresh.page != nil {
		t.Fatal("disposed shared document retained resources")
	}
	t.Log("original shared State/Document, native types, column permissions, paging, count, two locales, parked completion and actual session replacement verified")
}
