package dataworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/rqlite"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// Standalone fixtures exercise injected neutral contracts. The separate public
// workbench and published-import cases validate builtin registration and wiring.
func rqliteGridFixture(t *testing.T) connections.Profile {
	t.Helper()
	if os.Getenv("NYADB_RQLITE_GRID_IT") != "1" {
		t.Skip("requires the pinned owned RQLite shared-grid fixture")
	}
	binary := os.Getenv("NYADB_RQLITE_BINARY")
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal("official RQLite fixture binary unavailable", err)
	}
	digest := sha256.Sum256(raw)
	version := os.Getenv("NYADB_RQLITE_VERSION")
	if version == "" {
		version = "10.7.1"
	}
	artifacts := map[string]string{
		"10.7.1": "37b0be99b2e1d3ca2950c5d2d7432c444be8f3d7526331b2b85b205b8ee6933c",
		"8.43.4": "3b6bd8ad0497777bdece2919b0dc0b7702b6121c0b7db8ebb49f664e59087dff",
	}
	if artifacts[version] == "" || hex.EncodeToString(digest[:]) != artifacts[version] {
		t.Fatal("shared-grid fixture requires the verified original artifact for its explicitly pinned version")
	}
	t.Logf("official native shared-grid fixture version=%s artifact=%s", version, artifacts[version])
	dir := t.TempDir()
	port := func() string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		return l.Addr().String()
	}
	httpAddr, raftAddr := port(), port()
	logPath := filepath.Join(dir, "server.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-node-id", filepath.Base(dir), "-http-addr", httpAddr, "-raft-addr", raftAddr, filepath.Join(dir, "data"))
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	t.Logf("owned native RQLite shared-grid original process pid=%d", cmd.Process.Pid)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("original shared-grid RQLite process did not exit")
		}
		_ = log.Close()
		if output := os.Getenv("NYADB_RQLITE_GRID_EVIDENCE"); output != "" {
			if err := os.MkdirAll(output, 0700); err != nil {
				t.Error(err)
				return
			}
			raw, err := os.ReadFile(logPath)
			if err == nil {
				err = os.WriteFile(filepath.Join(output, fmt.Sprintf("server-%d.log", cmd.Process.Pid)), raw, 0600)
			}
			if err != nil {
				t.Error(err)
			}
		}
	})
	p := rqliteGridProfile(t, "http://"+httpAddr)
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case err := <-done:
			done <- err
			t.Fatal("original shared-grid process exited before readiness", err)
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		s, err := rqlite.OpenSession(ctx, p, "")
		cancel()
		if err == nil {
			_ = s.Close()
			return p
		}
		if time.Now().After(deadline) {
			t.Fatal("original shared-grid server did not become ready", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRQLiteNativeSharedGridCursorHistoryParkedAnalysisAndCompleteExport(t *testing.T) {
	p := rqliteGridFixture(t)
	raw := rqliteGridOpen(t, p)
	rqliteGridSQL(t, raw, "CREATE TABLE native_grid(id INTEGER PRIMARY KEY,note TEXT,payload BLOB,amount INTEGER);INSERT INTO native_grid VALUES(1,CAST(X'610062' AS TEXT),zeroblob(8192),9223372036854775805),(2,'second',X'00FF',9223372036854775807),(3,'last',NULL,NULL)")
	s, active, original := rqliteGridState(t, raw)
	d := rqliteGridDocument(t, s, raw, 1)
	firstPage := d.page
	consume := true
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		if consume {
			s.Consume(c.Services())
		}
		s.View(c, d)
		s.Modals(c)
	}, 1280, 800)
	tt.Frame()
	consume = false
	if err := tt.Click("下一页数据"); err != nil {
		t.Fatal(err)
	}
	active.ProfileID++
	rqliteGridWait(t, func() bool { return len(d.results) > 0 })
	s.Consume(ui.Services{})
	if !d.loading || d.offset != 2 || d.page != firstPage || active.ProfileID == original.ProfileID {
		t.Fatal("another active profile consumed an original parked cursor page")
	}
	*active = *original
	consume = true
	rqliteGridWait(t, func() bool { tt.Frame(); return !d.loading })
	if d.err != "" || len(d.page.Rows) != 1 || d.page.Rows[0][0].Text != "3" || d.after == nil || d.cursorHistory[2] == nil || d.page.PagingMode != "keyset" {
		t.Fatal("native next cursor page lost original boundary", d.err)
	}
	if err := tt.Click("上一页数据"); err != nil {
		t.Fatal(err)
	}
	rqliteGridWait(t, func() bool { tt.Frame(); return !d.loading })
	if d.err != "" || d.offset != 0 || len(d.page.Rows) != 2 || d.page.Rows[0][0].Text != "1" {
		t.Fatal("native previous cursor history changed original rows", d.err)
	}
	if err := tt.Click("下一页数据"); err != nil {
		t.Fatal(err)
	}
	rqliteGridWait(t, func() bool { tt.Frame(); return !d.loading })
	if d.page.Rows[0][0].Text != "3" {
		t.Fatal("repeated next page skipped or repeated an original row")
	}
	if err := tt.Click("上一页数据"); err != nil {
		t.Fatal(err)
	}
	rqliteGridWait(t, func() bool { tt.Frame(); return !d.loading })
	if err := tt.Click("数据分析"); err != nil {
		t.Fatal(err)
	}
	consume = false
	if err := tt.Click("开始分析"); err != nil {
		t.Fatal(err)
	}
	active.ProfileID++
	rqliteGridWait(t, func() bool { return len(d.tools.analysisResults) > 0 })
	s.Consume(ui.Services{})
	if !d.tools.analysisBusy || d.tools.analysis != nil {
		t.Fatal("parked native analysis published under another original profile")
	}
	*active = *original
	consume = true
	rqliteGridWait(t, func() bool { tt.Frame(); return !d.tools.analysisBusy })
	if d.tools.analysisErr != "" || d.tools.analysis == nil || d.tools.analysis.Rows != 3 || d.tools.analysis.Columns[3].Mean != "9223372036854775806" || d.tools.analysis.Columns[3].StdDeviation != "1" {
		t.Fatal("native toolbar did not use the original document's exact analyzer", d.tools.analysisErr)
	}
	rqliteGridCapture(t, tt, "rqlite-exact-native-analysis")
	if err := tt.Click("退出分析"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("导出"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "original-native.csv")
	d.transfer.Scope, d.transfer.Step, d.transfer.ExportPath = "all", 3, path
	tt.Frame()
	consume = false
	if err := tt.Click("导出"); err != nil {
		t.Fatal(err)
	}
	active.ProfileID++
	rqliteGridWait(t, func() bool { return len(d.transfer.Results) > 0 })
	s.Consume(ui.Services{})
	if !d.transfer.Busy || d.transfer.Completed {
		t.Fatal("parked native export changed another active profile")
	}
	*active = *original
	consume = true
	rqliteGridWait(t, func() bool { tt.Frame(); return !d.transfer.Busy })
	if !d.transfer.Completed || d.transfer.Err != "" {
		t.Fatal("complete native toolbar export failed", d.transfer.Err)
	}
	exported, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(exported)).ReadAll()
	if err != nil || len(rows) != 4 || rows[1][1] != "a\x00b" || rows[1][3] != "9223372036854775805" || len(rows[1][2]) < 8192 {
		t.Fatal("native toolbar export used bounded preview or lost types/bytes", err)
	}
	rqliteGridCapture(t, tt, "rqlite-complete-native-export")
	s.closeDataTransfer(d)
	var stream bytes.Buffer
	s.savePath = func(string) (string, error) { return "original-native.jsonl", nil }
	s.writeStream = func(_ string, write func(io.Writer) error) error { return write(&stream) }
	s.ExportJSONL(ui.Services{}, d)
	rqliteGridWait(t, func() bool { s.Consume(ui.Services{}); return !d.export.Busy })
	decoder := json.NewDecoder(&stream)
	var header struct {
		Version int
		Columns []contract.Column
	}
	var cells []contract.Cell
	if err := decoder.Decode(&header); err != nil || header.Version != 1 || len(header.Columns) != 4 {
		t.Fatal("native JSONL toolbar header", err)
	}
	if err := decoder.Decode(&cells); err != nil || cells[1].Text != "a\x00b" || len(cells[2].Bytes) != 8192 || cells[3].Kind != contract.Integer {
		t.Fatal("native JSONL toolbar used preview instead of original typed values", err)
	}
	// A generation change settles the original analysis without replacing its
	// prior complete cache, page, or active profile.
	s.openAnalysis(d)
	s.startDataAnalysis(ui.Services{}, d)
	rqliteGridWait(t, func() bool { s.Consume(ui.Services{}); return !d.tools.analysisBusy })
	cachedAnalysis := d.tools.analysis
	cachedPage := d.page
	s.startDataAnalysis(ui.Services{}, d)
	rqliteGridWait(t, func() bool { return len(d.tools.analysisResults) > 0 })
	original.Generation++
	*active = *original
	s.Consume(ui.Services{})
	if d.tools.analysisBusy || d.tools.analysis != cachedAnalysis || d.page != cachedPage || !strings.Contains(d.tools.analysisErr, "已失效") {
		t.Fatal("stale analysis replaced original page/cache", d.tools.analysisErr)
	}
}

func TestRQLiteNativeSharedGridReadCancellationWaitsAcknowledgementAndAllowsExplicitRefresh(t *testing.T) {
	p := rqliteGridFixture(t)
	admin := rqliteGridOpen(t, p)
	rqliteGridSQL(t, admin, "CREATE TABLE native_grid(id INTEGER PRIMARY KEY,note TEXT);INSERT INTO native_grid VALUES(1,'original'),(2,'second')")
	for _, kind := range []string{"page", "analysis", "export"} {
		t.Run(kind, func(t *testing.T) {
			x := rqliteGridProxy(t, p, "held-"+kind)
			defer x.release.Do(func() { close(x.ack) })
			raw := rqliteGridOpen(t, rqliteGridProfile(t, x.server.URL))
			s, _, _ := rqliteGridState(t, raw)
			d := rqliteGridDocument(t, s, raw, 1)
			cached := d.page
			path := filepath.Join(t.TempDir(), "original.csv")
			if err := os.WriteFile(path, []byte("original destination"), 0600); err != nil {
				t.Fatal(err)
			}
			x.armed.Store(true)
			switch kind {
			case "page":
				s.Refresh(ui.Services{}, d)
			case "analysis":
				s.openAnalysis(d)
				s.startDataAnalysis(ui.Services{}, d)
			case "export":
				s.openDataExport(d)
				d.transfer.Scope, d.transfer.ExportPath = "all", path
				s.startDataExport(ui.Services{}, d)
			}
			rqliteGridWait(t, func() bool {
				select {
				case <-x.started:
					return true
				default:
					return false
				}
			})
			if original := <-x.raw; !json.Valid([]byte(original)) {
				t.Fatal("native UI read discarded its original receipt")
			}
			switch kind {
			case "page":
				s.Cancel(d)
			case "analysis":
				s.cancelDataAnalysis(d)
			case "export":
				s.stopDataTransfer(d)
			}
			s.Consume(ui.Services{})
			if kind == "page" && (!d.loading || !d.readStopping || d.readRetired) || kind == "analysis" && (!d.tools.analysisBusy || !d.tools.analysisCanceling) || kind == "export" && (!d.transfer.Busy || !d.transfer.Canceling) {
				t.Fatal("native UI cancellation discarded pending acknowledgement")
			}
			if count, err := s.runtimeFor(d).Count(context.Background(), raw.ProfileID(), "main", "native_grid"); err != nil || count != 2 {
				t.Fatal("canceled native read revoked the independent count scope", err)
			}
			x.release.Do(func() { close(x.ack) })
			rqliteGridWait(t, func() bool { s.Consume(ui.Services{}); return !d.loading && !d.tools.analysisBusy && !d.transfer.Busy })
			if d.page != cached || d.readRetired || d.tools.analysis != nil || x.reads.Load() != 1 || x.writes.Load() != 0 {
				t.Fatal("canceled native read replaced cached data, retired the stateless scope, or replayed")
			}
			if kind == "export" {
				original, err := os.ReadFile(path)
				if err != nil || string(original) != "original destination" {
					t.Fatal("canceled native export replaced its original destination", err)
				}
			}
			s.closeDataTransfer(d)
			s.Refresh(ui.Services{}, d)
			rqliteGridWait(t, func() bool { s.Consume(ui.Services{}); return !d.loading })
			if d.err != "" || d.page == nil || d.page.Rows[0][1].Text != "original" || d.readRetired {
				t.Fatal("explicit read could not recover the original stateless document after acknowledged cancellation", d.err)
			}
		})
	}
}

func rqliteGridProfile(t *testing.T, endpoint string) connections.Profile {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := connections.Defaults(connections.RQLite)
	p.ID, p.Name, p.Host, p.Port = 185, "owned native grid", u.Hostname(), uint16(port)
	return p
}

func rqliteGridOpen(t *testing.T, p connections.Profile) *rqlite.Session {
	t.Helper()
	s, err := rqlite.OpenSession(context.Background(), p, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s.(*rqlite.Session)
}

func rqliteGridSQL(t *testing.T, s contract.Session, sql string) contract.SQLResult {
	t.Helper()
	q, err := contract.Require[contract.QueryProvider](s)
	if err != nil {
		t.Fatal(err)
	}
	l, err := q.ReserveQuery(99)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	r, err := l.Execute(context.Background(), "main", sql)
	if err != nil {
		t.Fatal("owned native grid SQL", err)
	}
	return r
}

func rqliteGridState(t *testing.T, raw contract.Session) (*State, *Session, *Session) {
	t.Helper()
	original := &Session{ProfileID: raw.ProfileID(), Generation: 1, Connected: true}
	active := *original
	runtime := contract.ComposeData(raw)
	s := New(Dependencies{
		Session: func() Session { return active },
		SessionFor: func(id int64) Session {
			if id == original.ProfileID {
				return *original
			}
			return Session{}
		},
		RuntimeFor: func(id int64) Runtime {
			if id == original.ProfileID {
				return runtime
			}
			return Runtime{}
		},
		Inspector: InspectorDependencies{Session: func() Session { return active }},
	})
	t.Cleanup(s.CloseAll)
	return s, &active, original
}

func rqliteGridDocument(t *testing.T, s *State, raw contract.Session, id uint64) *Document {
	t.Helper()
	d := s.OpenObject(id, contract.ObjectRef{ProfileID: raw.ProfileID(), Catalog: "main", Kind: "table", Name: "native_grid"}, raw.Capabilities())
	if d == nil || d.runtimeLease == nil || !d.capabilities.EditableTables || !s.runtimeFor(d).NativeReceipts {
		t.Fatal("original native grid lease/capabilities unavailable")
	}
	d.edit.Batch, d.limit = true, 2
	s.Load(ui.Services{}, d, true)
	rqliteGridWait(t, func() bool { s.Consume(ui.Services{}); return !d.Busy() })
	if d.err != "" || d.page == nil || d.page.RowSnapshots[0] == nil {
		t.Fatal("original native grid page unavailable", d.err)
	}
	return d
}

func rqliteGridWait(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("original native grid operation did not settle")
		}
		time.Sleep(time.Millisecond)
	}
}

func rqliteGridCapture(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	output := os.Getenv("NYADB_RQLITE_GRID_EVIDENCE")
	if output == "" {
		return
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(output, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(f, tt.Image())
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
}

func TestRQLiteNativeSharedGridBoundedFieldsCompleteEditorAndParkedAtomicSave(t *testing.T) {
	p := rqliteGridFixture(t)
	raw := rqliteGridOpen(t, p)
	rqliteGridSQL(t, raw, "CREATE TABLE native_grid(id INTEGER PRIMARY KEY,note TEXT,payload BLOB); INSERT INTO native_grid VALUES(1,'shared native grid',zeroblob(8192)),(2,NULL,X'00FF'),(3,'last',NULL)")
	s, active, original := rqliteGridState(t, raw)
	d := rqliteGridDocument(t, s, raw, 1)
	sibling := rqliteGridDocument(t, s, raw, 2)
	if len(d.page.Rows) != 2 || !d.page.HasMore || !d.page.Rows[0][2].Truncated || !d.page.KeysetAvailable || d.page.NextCursor == nil {
		t.Fatal("bounded shared-grid data or original cursor capability missing")
	}
	consume := true
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		if consume {
			s.Consume(c.Services())
		}
		ui.Row(c).Fill().Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Children(func() { s.View(c, d) })
			ui.Column(c).Width(280).Children(func() { s.InspectorView(c, d) })
		})
		s.Modals(c)
	}, 1280, 800)
	tt.Frame()
	if !tt.HasText("shared native grid") {
		t.Fatal("mature grid did not render actual RQLite data")
	}
	if err := tt.Click("获取整表行数"); err != nil {
		t.Fatal(err)
	}
	rqliteGridWait(t, func() bool { tt.Frame(); return !d.panel.CountLoading() })
	if d.panel.Count() == nil || *d.panel.Count() != 3 {
		t.Fatal("actual shared-grid table count", d.panel.CountError())
	}
	d.selectedRow, d.grid.Cells = 0, ui.ColumnRange{Cursor: "payload", Anchor: "payload"}
	s.ShowField(d)
	tt.Frame()
	consume = false // Click renders frames; park even an immediate native reply.
	if err := tt.Click("读取所选字段下一段"); err != nil {
		t.Fatal(err)
	}
	active.ProfileID++
	rqliteGridWait(t, func() bool { return len(d.panel.chunkResults) > 0 })
	s.Consume(ui.Services{})
	if !d.panel.ChunkLoading() || d.panel.ChunkNext() != 0 || active.ProfileID == original.ProfileID {
		t.Fatal("another active profile consumed or activated a parked field read")
	}
	*active = *original
	consume = true
	rqliteGridWait(t, func() bool { tt.Frame(); return !d.panel.ChunkLoading() })
	if d.panel.ChunkNext() != 1024 || d.panel.ChunkTotal() != 8192 || d.panel.ChunkError() != "" {
		t.Fatal("actual bounded native field read", d.panel.ChunkError())
	}
	rqliteGridCapture(t, tt, "rqlite-bounded-grid-field")
	s.beginMySQLCellEdit(ui.Services{}, d, 0, 2, true)
	rqliteGridWait(t, func() bool { s.Consume(ui.Services{}); return !d.edit.Busy })
	if d.edit.Editor == nil {
		t.Fatal("complete native editor unavailable", d.edit.Err)
	}
	if len(d.edit.Editor.Original.Bytes) != 8192 {
		t.Fatal("complete editor used bounded field preview")
	}
	d.edit.Editor.Text = "0x00ff01"
	s.finishMySQLCellEdit(d, true)
	s.EditCell(ui.Services{}, d, 0, "note", contract.Cell{Kind: contract.Text, Text: "saved native 猫"})
	before := sibling.page
	s.Save(ui.Services{}, d)
	active.ProfileID++
	rqliteGridWait(t, func() bool { return len(d.edit.Results) > 0 })
	s.Consume(ui.Services{})
	if !d.edit.Busy || !d.HasDraft() || d.nativeReceipt.JSON != "" || active.ProfileID == original.ProfileID {
		t.Fatal("parked save changed another active profile or discarded drafts")
	}
	actual := rqliteGridSQL(t, raw, "SELECT note,hex(payload) FROM native_grid WHERE id=1")
	if actual.Sets[0].Rows[0][0].Text != "saved native 猫" || actual.Sets[0].Rows[0][1].Text != "00FF01" {
		t.Fatal("parked native atomic save did not actually apply")
	}
	*active = *original
	rqliteGridWait(t, func() bool { tt.Frame(); return !d.Busy() })
	if d.HasDraft() || d.edit.Uncertain != nil || d.nativeReceipt.JSON == "" || d.err != "" || d.page.Rows[0][1].Text != "saved native 猫" || !bytes.Equal(d.page.Rows[0][2].Value.Bytes, []byte{0, 255, 1}) || sibling.page != before || sibling.page.Rows[0][1].Text != "shared native grid" {
		t.Fatal("original acknowledged save failed refresh or changed sibling grid", d.err, d.edit.Err)
	}
	rqliteGridCapture(t, tt, "rqlite-atomic-saved-grid")
}

type rqliteGridRelay struct {
	server       *httptest.Server
	writes       atomic.Int32
	reads        atomic.Int32
	armed        atomic.Bool
	started, ack chan struct{}
	raw          chan string
	release      sync.Once
}

func rqliteGridProxy(t *testing.T, p connections.Profile, mode string) *rqliteGridRelay {
	t.Helper()
	c, err := connections.RQLiteProfileConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	x := &rqliteGridRelay{started: make(chan struct{}), ack: make(chan struct{}), raw: make(chan string, 1)}
	x.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		forward, err := http.NewRequestWithContext(r.Context(), r.Method, c.Endpoint+r.URL.RequestURI(), bytes.NewReader(requestBody))
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		forward.Header = r.Header.Clone()
		res, err := http.DefaultClient.Do(forward)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if r.URL.Path == "/db/request" {
			if x.writes.Add(1) == 1 {
				x.raw <- string(body)
				close(x.started)
			}
			if mode == "held" {
				<-x.ack
			} else {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
				return
			}
		}
		marker := map[string]string{"held-analysis": "_nyadb_numeric", "held-export": "_nyadb_export_rows", "held-page": "_nyadb_cursor_values"}[mode]
		if x.armed.Load() && marker != "" && r.URL.Path == "/db/query" && strings.Contains(string(requestBody), marker) {
			if !r.URL.Query().Has("transaction") || r.URL.Query().Get("retries") != "0" {
				t.Error("native UI read lost its original transaction or allowed replay")
			}
			if x.reads.Add(1) == 1 {
				x.raw <- string(body)
				close(x.started)
			}
			<-x.ack
		}
		for k, v := range res.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(res.StatusCode)
		_, _ = w.Write(body)
	}))
	t.Cleanup(func() { x.release.Do(func() { close(x.ack) }); x.server.Close() })
	return x
}

func TestRQLiteNativeSharedGridUnknownAppliedWriteRetainsDraftAndExplicitRecoveryNeverReplays(t *testing.T) {
	p := rqliteGridFixture(t)
	admin := rqliteGridOpen(t, p)
	rqliteGridSQL(t, admin, "CREATE TABLE native_grid(id INTEGER PRIMARY KEY,note TEXT,payload BLOB); INSERT INTO native_grid VALUES(1,'original',zeroblob(8192))")
	x := rqliteGridProxy(t, p, "lost")
	raw := rqliteGridOpen(t, rqliteGridProfile(t, x.server.URL))
	s, _, _ := rqliteGridState(t, raw)
	d := rqliteGridDocument(t, s, raw, 1)
	s.EditCell(ui.Services{}, d, 0, "note", contract.Cell{Kind: contract.Text, Text: "applied once"})
	s.Save(ui.Services{}, d)
	rqliteGridWait(t, func() bool { s.Consume(ui.Services{}); return !d.edit.Busy })
	if d.edit.Uncertain == nil || !d.edit.Uncertain.CommitUncertain || !d.edit.Uncertain.Incomplete || !d.HasDraft() || s.CanSave(d) {
		t.Fatal("unknown native write discarded drafts or allowed replay", d.edit.Err)
	}
	unknown := cloneEditResult(d.edit.Uncertain)
	page := d.page
	s.Save(ui.Services{}, d)
	s.Load(ui.Services{}, d, true)
	if d.Busy() || d.page != page || x.writes.Load() != 1 {
		t.Fatal("unknown native write was replayed or reloaded")
	}
	actual := rqliteGridSQL(t, admin, "SELECT note FROM native_grid WHERE id=1")
	if actual.Sets[0].Rows[0][0].Text != "applied once" {
		t.Fatal("lost-response fixture write was not actually applied")
	}
	d.selectedRow, d.grid.Cells = 0, ui.ColumnRange{Cursor: "payload", Anchor: "payload"}
	s.ShowField(d)
	s.inspector.LoadChunk(d.panel, nil)
	s.inspector.LoadCount(d.panel, nil)
	rqliteGridWait(t, func() bool { s.Consume(ui.Services{}); return !d.panel.ChunkLoading() && !d.panel.CountLoading() })
	if d.panel.ChunkNext() != 1024 || d.panel.ChunkError() != "" || d.panel.Count() == nil || *d.panel.Count() != 1 {
		t.Fatal("independent reads after unknown native write failed")
	}
	s.VerifySave(ui.Services{}, d)
	rqliteGridWait(t, func() bool { s.Consume(ui.Services{}); return !d.edit.Busy })
	if !reflect.DeepEqual(d.edit.Uncertain, unknown) || !d.HasDraft() || s.CanSave(d) || x.writes.Load() != 1 {
		t.Fatal("later count/field/verification settled original unknown write")
	}
	oldRuntime := s.runtimeFor(d)
	s.Close(d)
	s.Close(d)
	if _, err := oldRuntime.Count(context.Background(), raw.ProfileID(), "main", "native_grid"); err == nil {
		t.Fatal("closed original native data lease admitted another read")
	}
	fresh := rqliteGridDocument(t, s, raw, 2)
	if fresh.HasDraft() || fresh.page.Rows[0][1].Text != "applied once" || x.writes.Load() != 1 || !unknown.CommitUncertain {
		t.Fatal("explicit new reservation replayed original drafts or settled old receipt")
	}
}

func TestRQLiteNativeSharedGridCancellationWaitsOriginalReceiptAndStaleGenerationRetainsGrid(t *testing.T) {
	p := rqliteGridFixture(t)
	admin := rqliteGridOpen(t, p)
	rqliteGridSQL(t, admin, "CREATE TABLE native_grid(id INTEGER PRIMARY KEY,note TEXT); INSERT INTO native_grid VALUES(1,'original')")
	x := rqliteGridProxy(t, p, "held")
	defer x.release.Do(func() { close(x.ack) })
	raw := rqliteGridOpen(t, rqliteGridProfile(t, x.server.URL))
	s, active, original := rqliteGridState(t, raw)
	d := rqliteGridDocument(t, s, raw, 1)
	s.EditCell(ui.Services{}, d, 0, "note", contract.Cell{Kind: contract.Text, Text: "held original"})
	page := d.page
	s.Save(ui.Services{}, d)
	rqliteGridWait(t, func() bool {
		select {
		case <-x.started:
			return true
		default:
			return false
		}
	})
	receipt := <-x.raw
	s.stopMySQLDataEdit(d)
	s.Consume(ui.Services{})
	if !d.edit.Busy || !d.HasDraft() || len(d.edit.Results) != 0 {
		t.Fatal("cancellation discarded original pending native receipt")
	}
	original.Generation++
	*active = *original
	x.release.Do(func() { close(x.ack) })
	rqliteGridWait(t, func() bool { s.Consume(ui.Services{}); return !d.edit.Busy })
	if d.page != page || !d.HasDraft() || d.edit.Uncertain == nil || !d.edit.Uncertain.CommitUncertain || !d.edit.Uncertain.CommitConfirmed || d.nativeReceipt.JSON != receipt || s.CanSave(d) || x.writes.Load() != 1 {
		t.Fatal("stale original acknowledgement replaced grid, lost receipt or allowed replay")
	}
	actual := rqliteGridSQL(t, admin, "SELECT note FROM native_grid WHERE id=1")
	if actual.Sets[0].Rows[0][0].Text != "held original" {
		t.Fatal("held native write did not actually apply")
	}
	s.Close(d)
	if !raw.Alive() || !admin.Alive() || d.page != nil || d.runtimeLease != nil || len(s.documents) != 0 {
		t.Fatal("original document disposal leaked caches or closed another session")
	}
}
