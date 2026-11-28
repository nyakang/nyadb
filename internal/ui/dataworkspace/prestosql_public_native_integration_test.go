package dataworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
	"github.com/nyakang/nyadb/internal/services/prestosql"
	"github.com/nyakang/nyadb/internal/testsupport/prestosqlfixture"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// This barrier delays an actual acknowledged original-plugin field response.
// It never synthesizes database values and excludes process version probes.
type prestoPublicFieldBarrier struct {
	arrived, resume chan struct{}
	once            sync.Once
	method          string
	original        []byte
}

func (b *prestoPublicFieldBarrier) release() { b.once.Do(func() { close(b.resume) }) }

func TestPrestoSQLNativePublicInspectorOriginalFieldsParkingCancellationAndGeneration(t *testing.T) {
	f := prestosqlfixture.Read(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	directory := filepath.Join(f.Directory, t.Name(), "store")
	store, err := driverstore.New(directory)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := store.Import(ctx, driverstore.Product{Key: "prestosql", Runtime: "java", MainClass: "app.dbx.jdbc.DbxJdbcPlugin"}, f.Archive)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.RemovePinned(context.Background(), driver.Key, driver.Version, driver.SHA256); err != nil {
			t.Error("original package remained leased", err)
		}
	})
	parsed, err := connections.ParseURI(f.URI(t))
	if err != nil {
		t.Fatal(err)
	}
	p := parsed.Profile
	p.ID, p.Name = 6801, "PrestoSQL original inspector"
	p.PrestoSQLOptions.DriverVersion, p.PrestoSQLOptions.DriverSHA256, p.PrestoSQLOptions.DriverDirectory = driver.Version, driver.SHA256, driver.Directory
	var pending atomic.Pointer[prestoPublicFieldBarrier]
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	// Instrument the same public Provider.Open route; public generations and
	// feature binding are still supplied by dbruntime.
	m.Register(connections.PrestoSQL, prestosql.Provider{Observe: func(method string, r jdbcplugin.Receipt) {
		b := pending.Load()
		if b == nil || !strings.HasPrefix(method, "data-field-") || !strings.HasSuffix(method, "/executeQuery") || !r.ExecutorAcknowledged || r.RemoteFailure {
			return
		}
		var result struct {
			Columns []string            `json:"columns"`
			Rows    [][]json.RawMessage `json:"rows"`
		}
		if json.Unmarshal(r.Result, &result) != nil || len(result.Columns) != 1 || result.Columns[0] != "__nyadb_field" || len(result.Rows) != 1 {
			return
		}
		if pending.CompareAndSwap(b, nil) {
			b.method = method
			b.original = r.OriginalResponse()
			close(b.arrived)
			<-b.resume
		}
	}})
	t.Cleanup(func() { m.Close() })
	ctx = driverstore.WithRuntimeObserver(ctx, f.Observe(directory))
	handle, err := m.ConnectSession(ctx, p, f.Secret(t))
	if err != nil {
		t.Fatal("public provider", err)
	}
	originalGeneration := handle.Generation()
	qp, err := contract.Require[contract.QueryProvider](handle)
	if err != nil {
		t.Fatal(err)
	}
	q, err := qp.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Release)
	label := `"memory"."default"`
	run := func(sql string) {
		t.Helper()
		if _, err := q.Execute(ctx, label, sql); err != nil {
			t.Fatal("original fixture SQL", err)
		}
	}
	run(`CREATE TABLE memory.default.inspector_public68 AS SELECT BIGINT '9223372036854775807' AS id,rpad('',2049,'猫') AS note,from_hex(rpad('',4098,'00ff80')) AS payload,CAST(NULL AS varchar) AS absent,CAST('' AS varchar) AS empty_text`)
	active := Session{ProfileID: p.ID, Generation: handle.Generation(), Connected: true}
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(id int64) Session {
		current, e := m.Session(id)
		if e != nil {
			return Session{ProfileID: id}
		}
		return Session{ProfileID: id, Generation: current.Generation(), Connected: m.IsConnected(id)}
	}, RuntimeFor: func(id int64) Runtime {
		current, e := m.Session(id)
		if e != nil {
			return Runtime{}
		}
		return contract.ComposeData(current)
	}, NamespaceLabel: func(n contract.NamespaceRef) string { return `"` + n.Catalog + `"."` + n.Schema + `"` }, Inspector: InspectorDependencies{Session: func() Session { return active }, Profile: func(id int64) (connections.Profile, bool) { return p, id == p.ID }}})
	t.Cleanup(s.CloseAll)
	object := (contract.NamespaceRef{ProfileID: p.ID, Catalog: "memory", Schema: "default"}).Object("table", "inspector_public68")
	d := s.OpenObject(1, object, handle.Capabilities())
	if d == nil || d.boundRuntime == nil || d.runtimeLease == nil || d.err != "" {
		t.Fatal("public document unavailable")
	}
	wait := func(done func() bool) {
		t.Helper()
		end := time.Now().Add(90 * time.Second)
		for !done() && time.Now().Before(end) {
			s.Consume(ui.Services{})
			time.Sleep(5 * time.Millisecond)
		}
		s.Consume(ui.Services{})
		if !done() {
			t.Fatal("original public request did not settle")
		}
	}
	s.Load(ui.Services{}, d, false)
	wait(func() bool { return !d.loading })
	if d.err != "" || d.page == nil || len(d.page.Rows) != 1 {
		t.Fatal("original grid", d.err)
	}
	if d.page.Rows[0][0].Text != "9223372036854775807" {
		t.Fatal("integer rounded")
	}
	d.selectedRow, d.grid.Cells.Cursor = 0, "note"
	panel := s.inspectorPanel(d)
	s.inspector.LoadInfo(panel, nil, false)
	s.inspector.LoadCount(panel, nil)
	wait(func() bool { return !panel.InfoLoading() && !panel.CountLoading() })
	if panel.Info() == nil || panel.Info().Engine != object.Catalog || panel.Info().Database != label || panel.Info().Name != object.Name || panel.Count() == nil || *panel.Count() != 1 {
		t.Fatal("original inspector metadata/count", panel.Info(), panel.InfoError(), panel.Count(), panel.CountError())
	}
	read := func() {
		t.Helper()
		s.inspector.LoadChunk(panel, nil)
		wait(func() bool { return !panel.ChunkLoading() })
		if panel.ChunkError() != "" {
			t.Fatal("original field", panel.ChunkError())
		}
	}
	read()
	if panel.ChunkText() != strings.Repeat("猫", 1024) || panel.ChunkNext() != 1024 || panel.ChunkTotal() != 2049 {
		t.Fatal("first field segment changed")
	}
	firstText := panel.ChunkText()
	read()
	if panel.ChunkText() != firstText || panel.ChunkNext() != 2048 {
		t.Fatal("second field segment changed")
	}
	read()
	if panel.ChunkText() != "猫" || panel.ChunkNext() != 2049 || !panel.chunkDone {
		t.Fatal("final field segment changed")
	}
	calls := f.Starts.Load()
	s.inspector.LoadChunk(panel, nil)
	if panel.ChunkLoading() || f.Starts.Load() != calls {
		t.Fatal("completed field implicitly reread")
	}
	// The UI dispatches the selected original field and displays its own result.
	s.inspector.CancelChunk(panel)
	d.grid.Cells.Cursor = "payload"
	read()
	if panel.ChunkNext() != 1024 || panel.ChunkTotal() != 2049 || panel.ChunkText() != "0x"+strings.Repeat("00ff80", 341)+"00" {
		t.Fatal("binary segment lost bytes", panel.ChunkNext(), panel.ChunkTotal())
	}
	binaryText := panel.ChunkText()
	s.inspector.CancelChunk(panel)
	d.grid.Cells.Cursor = "absent"
	calls = f.Starts.Load()
	s.inspector.LoadChunk(panel, nil)
	if panel.ChunkLoading() || f.Starts.Load() != calls {
		t.Fatal("NULL triggered field query")
	}
	d.grid.Cells.Cursor = "empty_text"
	read()
	if panel.ChunkText() != "" || panel.ChunkTotal() != 0 || !panel.chunkDone {
		t.Fatal("empty text became NULL or unread")
	}
	s.inspector.CancelChunk(panel)
	d.grid.Cells.Cursor = "note"
	s.inspector.ShowField(panel)
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	t.Cleanup(func() { i18n.SetLocaleFunc(nil) })
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.InspectorView(c, d) }, 360, 720)
	if err := tt.Click(locales.Text("读取所选字段下一段")); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !panel.ChunkLoading() })
	tt.Frame()
	if panel.ChunkText() != firstText {
		t.Fatal("native-backed inspector button lost field")
	}
	f.Screenshot(t, tt, "zh-Hans-public-field-inspector")
	i18n.SetLocaleFunc(func() string { return "en" })
	tt.Frame()
	f.Screenshot(t, tt, "en-public-field-inspector")
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	// Completed original fields remain buffered while another profile is active.
	s.inspector.CancelChunk(panel)
	s.inspector.LoadChunk(panel, nil)
	originalResult := panel.chunkResults
	active.ProfileID = 9999
	var parked chunkResult
	select {
	case parked = <-originalResult:
	case <-time.After(90 * time.Second):
		t.Fatal("parked original field did not finish")
	}
	buffered := make(chan chunkResult, 1)
	buffered <- parked
	panel.chunkResults = buffered
	s.Consume(ui.Services{})
	if !panel.ChunkLoading() || panel.ChunkText() != "" || len(buffered) != 1 {
		t.Fatal("parked field published on another profile")
	}
	active.ProfileID = p.ID
	s.Consume(ui.Services{})
	if panel.ChunkLoading() || panel.ChunkText() != firstText {
		t.Fatal("parked original field lost")
	}
	var proofs []map[string]any
	arm := func() *prestoPublicFieldBarrier {
		t.Helper()
		b := &prestoPublicFieldBarrier{arrived: make(chan struct{}), resume: make(chan struct{})}
		t.Cleanup(b.release)
		pending.Store(b)
		return b
	}
	arrived := func(b *prestoPublicFieldBarrier) {
		t.Helper()
		select {
		case <-b.arrived:
		case <-time.After(90 * time.Second):
			t.Fatal("actual field acknowledgement did not arrive")
		}
		sum := sha256.Sum256(b.original)
		proofs = append(proofs, map[string]any{"method": b.method, "original_response": string(b.original), "sha256": hex.EncodeToString(sum[:])})
	}
	// Cancellation after the actual response ACK must not publish that response.
	s.inspector.CancelChunk(panel)
	b := arm()
	s.inspector.LoadChunk(panel, nil)
	canceledResults := panel.chunkResults
	arrived(b)
	s.inspector.CancelChunk(panel)
	b.release()
	select {
	case result := <-canceledResults:
		if result.err == nil {
			t.Fatal("cancelled executor returned successful field")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("cancelled field executor did not settle")
	}
	s.Consume(ui.Services{})
	if panel.ChunkLoading() || panel.ChunkText() != "" || d.readRetired {
		t.Fatal("field cancellation published or retired page")
	}
	// Refresh replaces the observation grant while the old field ACK is held.
	b = arm()
	s.inspector.LoadChunk(panel, nil)
	lateResults := panel.chunkResults
	arrived(b)
	oldPage := d.page
	s.Load(ui.Services{}, d, true)
	wait(func() bool { return !d.loading })
	b.release()
	select {
	case result := <-lateResults:
		if result.err == nil {
			t.Fatal("replaced page grant accepted late field")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("late field executor did not settle")
	}
	s.Consume(ui.Services{})
	if d.err != "" || d.page == oldPage || panel.ChunkText() != "" || panel.ChunkLoading() {
		t.Fatal("page replacement retained old field", d.err)
	}
	// A completed field from the old public generation cannot enter a recovered
	// profile, and cached old data is retained until explicit document disposal.
	d.selectedRow, d.grid.Cells.Cursor = 0, "note"
	s.inspector.LoadChunk(panel, nil)
	var stale chunkResult
	select {
	case stale = <-panel.chunkResults:
	case <-time.After(90 * time.Second):
		t.Fatal("old generation field did not finish")
	}
	buffered = make(chan chunkResult, 1)
	buffered <- stale
	panel.chunkResults = buffered
	cached := d.page
	m.Disconnect(p.ID)
	replacement, err := m.ConnectSession(ctx, p, f.Secret(t))
	if err != nil {
		t.Fatal("explicit original profile recovery", err)
	}
	active.Generation = replacement.Generation()
	if active.Generation <= originalGeneration {
		t.Fatal("public generation did not advance")
	}
	s.Consume(ui.Services{})
	if panel.ChunkLoading() || panel.ChunkText() != "" || panel.ChunkError() == "" || d.page != cached {
		t.Fatal("late generation replaced cached data")
	}
	calls = f.Starts.Load()
	s.Load(ui.Services{}, d, true)
	if d.loading || d.page != cached || f.Starts.Load() != calls {
		t.Fatal("old document silently recovered")
	}
	s.Close(d)
	s.Close(d)
	if len(s.documents) != 0 || len(s.inspector.panels) != 0 || d.page != nil || d.boundRuntime != nil || d.runtimeLease != nil || panel.selection != nil {
		t.Fatal("close retained document ownership")
	}
	fresh := s.OpenObject(2, object, replacement.Capabilities())
	s.Load(ui.Services{}, fresh, false)
	wait(func() bool { return !fresh.loading })
	if fresh.err != "" || fresh.page == nil || len(fresh.page.Rows) != 1 || fresh.page.Rows[0][0].Text != "9223372036854775807" {
		t.Fatal("explicit reopen lost original data", fresh.err)
	}
	f.Journal(t, "public-inspector.jsonl", map[string]any{"test": t.Name(), "object": object, "original_generation": originalGeneration, "replacement_generation": replacement.Generation(), "text_units": 2049, "segment_units": 1024, "binary_total_bytes": 2049, "binary_first_segment": binaryText, "null_did_not_read": true, "empty_preserved": true, "parked_until_original_profile": true, "cancelled_original_ack_not_published": true, "page_replacement_rejected_late_field": true, "stale_generation_rejected": true, "cached_page_preserved": true, "explicit_reopen_no_replay": true, "closed_caches_released": true, "actual_field_acknowledgements": proofs, "starts": f.Starts.Load()})
	s.CloseAll()
	m.Close()
}
