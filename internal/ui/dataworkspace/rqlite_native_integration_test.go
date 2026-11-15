package dataworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// This fixture exercises the unpublished kernel through injected neutral
// contracts. It is not acceptance of the public chooser/provider/workbench.
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
	if hex.EncodeToString(digest[:]) != "37b0be99b2e1d3ca2950c5d2d7432c444be8f3d7526331b2b85b205b8ee6933c" {
		t.Fatal("shared-grid fixture requires the verified official v10.7.1 server")
	}
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
	if len(d.page.Rows) != 2 || !d.page.HasMore || !d.page.Rows[0][2].Truncated || d.page.KeysetAvailable {
		t.Fatal("bounded shared-grid data or unsupported cursor capability changed")
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
		forward, err := http.NewRequestWithContext(r.Context(), r.Method, c.Endpoint+r.URL.RequestURI(), r.Body)
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
