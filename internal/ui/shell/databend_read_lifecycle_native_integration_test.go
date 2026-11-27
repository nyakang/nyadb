package shell

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

// Delay only a captured response from the real server. Other request scopes
// continue through their original sessions; no SQL or result is fabricated.
type databendReadGate struct {
	armed                   atomic.Bool
	held, release, returned chan struct{}
	once                    sync.Once
	requests                atomic.Int32
}

func databendWorkbenchReadGate(t *testing.T, f databendWorkbenchFixture, table, scope string) (*databendReadGate, string) {
	t.Helper()
	g := &databendReadGate{held: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Error(err)
			return
		}
		req.Body = io.NopCloser(bytes.NewReader(raw))
		var query struct {
			SQL string `json:"sql"`
		}
		_ = json.Unmarshal(raw, &query)
		match := req.Method == http.MethodPost && strings.Contains(query.SQL, table)
		switch scope {
		case "page":
			match = match && strings.Contains(query.SQL, "__nyadb_value_0") && !strings.Contains(query.SQL, " LIMIT 2")
		case "count":
			match = match && strings.Contains(query.SQL, "__nyadb_count")
		case "field":
			match = match && strings.Contains(query.SQL, "__nyadb_value_0") && strings.Contains(query.SQL, " LIMIT 2")
		}
		capture := match && g.armed.CompareAndSwap(true, false)
		if match {
			g.requests.Add(1)
		}
		code, headers, body := databendWorkbenchForward(t, f, req)
		if capture {
			hash := sha256.Sum256(body)
			databendWorkbenchJournal(t, f, "workbench-held-reads.jsonl", map[string]any{"test": t.Name(), "scope": scope, "sql": query.SQL, "query_id": req.Header.Get("X-DATABEND-QUERY-ID"), "status": code, "native_bytes": body, "native_sha256": hex.EncodeToString(hash[:])})
			close(g.held)
			select {
			case <-g.release:
			case <-time.After(60 * time.Second):
				t.Error("original read response was not released")
			}
		}
		for k, values := range headers {
			w.Header()[k] = values
		}
		w.WriteHeader(code)
		_, _ = w.Write(body)
		if capture {
			close(g.returned)
		}
	}))
	// Cleanup runs after the shell's later-registered cleanup, so also release
	// explicitly with defer in callers before their original leases close.
	t.Cleanup(proxy.Close)
	return g, proxy.URL
}

func (g *databendReadGate) unblock() { g.once.Do(func() { close(g.release) }) }

func databendAwaitReadGate(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(45 * time.Second):
		t.Fatal("original native read did not reach expected boundary")
	}
}

func TestDatabendWorkbenchOriginalIndependentReadCancellation(t *testing.T) {
	for _, scope := range []string{"page", "count", "field"} {
		t.Run(scope, func(t *testing.T) {
			f := databendWorkbenchOriginal(t)
			table := "workbench_read_" + scope + "53"
			databendWorkbenchSQL(t, f, `CREATE TABLE "`+f.Database+`".`+table+` (id UInt64 NOT NULL,txt String NOT NULL)`)
			databendWorkbenchSQL(t, f, `INSERT INTO "`+f.Database+`".`+table+` VALUES (1,'original field')`)
			gate, endpoint := databendWorkbenchReadGate(t, f, table, scope)
			defer gate.unblock()
			acknowledged := make(chan agentbridge.Receipt, 8)
			s, tt, p, services := databendWorkbenchState(t, f, endpoint, func(_ string, r agentbridge.Receipt) {
				if r.CancellationRequested {
					acknowledged <- r
				}
			})
			d := databendWorkbenchData(t, s, tt, *services, p, table)
			page := d.Page()
			databendWorkbenchClick(t, tt, "original field")
			databendWorkbenchClick(t, tt, "获取整表行数")
			databendWorkbenchWait(t, tt, func() bool { return !d.Inspector().CountLoading() })
			if n := d.Inspector().Count(); n == nil || *n != 1 {
				t.Fatal("initial native count missing")
			}
			gate.armed.Store(true)
			switch scope {
			case "page":
				s.dataWorkspace.Load(*services, d, true)
				databendAwaitReadGate(t, gate.held)
				databendWorkbenchClick(t, tt, "取消读取数据")
				if !d.Loading() || d.Page() != page {
					t.Fatal("page cancellation discarded cache or settled before executor")
				}
				// The original count and field scopes must still be usable while
				// the canceled page executor has not returned.
				databendWorkbenchClick(t, tt, "获取整表行数")
				databendWorkbenchWait(t, tt, func() bool { return !d.Inspector().CountLoading() })
				databendWorkbenchClick(t, tt, "字段信息")
				databendWorkbenchClick(t, tt, "读取所选字段下一段")
				databendWorkbenchWait(t, tt, func() bool { return !d.Inspector().ChunkLoading() })
				if d.Inspector().ChunkText() != "original field" {
					t.Fatal("page cancel interrupted independent field read", d.Inspector().ChunkError())
				}
				gate.unblock()
				databendWorkbenchWait(t, tt, func() bool { return !d.Loading() })
				if d.Page() != page || d.Error() != "" {
					t.Fatal("canceled page replaced cache", d.Error())
				}
			case "count":
				databendWorkbenchClick(t, tt, "获取整表行数")
				databendAwaitReadGate(t, gate.held)
				databendWorkbenchClick(t, tt, "取消计数")
				if n := d.Inspector().Count(); n == nil || *n != 1 {
					t.Fatal("cancel removed cached count")
				}
				databendWorkbenchClick(t, tt, "字段信息")
				databendWorkbenchClick(t, tt, "读取所选字段下一段")
				if !d.Inspector().ChunkLoading() {
					t.Fatal("independent field was not queued")
				}
				// Default metadata reserve is one. Keep the independent reader
				// queued, then acknowledge the canceled owner and free its slot.
				gate.unblock()
				databendWorkbenchWait(t, tt, func() bool { return !d.Inspector().ChunkLoading() })
				if d.Inspector().ChunkText() != "original field" {
					t.Fatal("count cancel interrupted independent field", d.Inspector().ChunkError())
				}
				// An explicit new count must survive the canceled result delivery.
				databendWorkbenchSQL(t, f, `INSERT INTO "`+f.Database+`".`+table+` VALUES (2,'second field')`)
				databendWorkbenchClick(t, tt, "表信息")
				databendWorkbenchClick(t, tt, "获取整表行数")
				databendWorkbenchWait(t, tt, func() bool { return !d.Inspector().CountLoading() })
				if n := d.Inspector().Count(); n == nil || *n != 2 {
					t.Fatal("replacement native count missing")
				}
				gate.unblock()
			case "field":
				databendWorkbenchClick(t, tt, "字段信息")
				databendWorkbenchClick(t, tt, "读取所选字段下一段")
				databendAwaitReadGate(t, gate.held)
				databendWorkbenchClick(t, tt, "表信息")
				if d.Inspector().ChunkLoading() {
					t.Fatal("leaving field mode did not revoke original read")
				}
				databendWorkbenchClick(t, tt, "获取整表行数")
				if !d.Inspector().CountLoading() {
					t.Fatal("independent count was not queued")
				}
				gate.unblock()
				databendWorkbenchWait(t, tt, func() bool { return !d.Inspector().CountLoading() })
				databendWorkbenchClick(t, tt, "字段信息")
				databendWorkbenchClick(t, tt, "读取所选字段下一段")
				databendWorkbenchWait(t, tt, func() bool { return !d.Inspector().ChunkLoading() })
				if d.Inspector().ChunkText() != "original field" {
					t.Fatal("explicit field retry did not finish", d.Inspector().ChunkError())
				}
				gate.unblock()
			}
			databendAwaitReadGate(t, gate.returned)
			var receipt agentbridge.Receipt
			select {
			case receipt = <-acknowledged:
			case <-time.After(30 * time.Second):
				t.Fatal("canceled read lacks original executor receipt")
			}
			if !receipt.Dispatched || !receipt.ExecutorAcknowledged || receipt.ProcessExitConfirmed || len(receipt.OriginalResponse()) == 0 {
				t.Fatal("read cancellation did not acknowledge its original live executor", receipt)
			}
			tt.Frame()
			if scope == "field" && d.Inspector().ChunkText() != "original field" {
				t.Fatal("late field reply replaced explicit retry")
			}
			// A fresh page traverses the original executor after the late reply.
			// The cached count remains the new observation, and the document
			// explicitly recovers its page without replaying the canceled read.
			s.dataWorkspace.Load(*services, d, true)
			databendWorkbenchWait(t, tt, func() bool { return !d.Loading() })
			want := int64(1)
			if scope == "count" {
				want = 2
			}
			if n := d.Inspector().Count(); n == nil || *n != want || d.Error() != "" || int64(len(d.Page().Rows)) != want {
				t.Fatal("late canceled reader changed new count/page", d.Error())
			}
			databendWorkbenchCapture(t, f, tt, "read-recovered")
			databendWorkbenchJournal(t, f, "workbench-read-lifecycle.jsonl", map[string]any{"test": t.Name(), "scope": scope, "object": d.Object(), "count": want, "cache_preserved": true, "independent_read_survived": true, "late_result_rejected": true, "explicit_refresh": true, "matched_requests": gate.requests.Load(), "public_provider_registered": false})
		})
	}
}

func TestDatabendWorkbenchOriginalLateReadCannotReplaceNewGeneration(t *testing.T) {
	f := databendWorkbenchOriginal(t)
	table := "workbench_generation53"
	databendWorkbenchSQL(t, f, `CREATE TABLE "`+f.Database+`".`+table+` (id UInt64 NOT NULL,txt String NOT NULL)`)
	databendWorkbenchSQL(t, f, `INSERT INTO "`+f.Database+`".`+table+` VALUES (1,'original generation')`)
	gate, endpoint := databendWorkbenchReadGate(t, f, table, "page")
	defer gate.unblock()
	s, tt, p, services := databendWorkbenchState(t, f, endpoint)
	d := databendWorkbenchData(t, s, tt, *services, p, table)
	originalTab, page, generation := s.currentTab().id, d.Page(), s.runtime.Generation(p.ID)
	gate.armed.Store(true)
	s.dataWorkspace.Load(*services, d, true)
	databendAwaitReadGate(t, gate.held)
	secret, err := os.ReadFile(f.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	// A runtime replacement is an external lifecycle event. Only its immutable
	// request and private buffered completion leave this UI thread.
	finished := make(chan error, 1)
	go func() { finished <- s.runtime.Connect(context.Background(), p, string(secret)) }()
	// Hold UI consumption across this externally initiated replacement. A frame
	// during its unpublished interval would correctly interpret it as a lost
	// connection and revoke the replacement before its host notification.
	deadline := time.Now().Add(10 * time.Second)
	for s.runtime.IsConnected(p.ID) {
		if time.Now().After(deadline) {
			t.Fatal("replacement did not revoke the old public session")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case err := <-finished:
		t.Fatal("replacement returned before the old reader settled", err)
	default:
	}
	gate.unblock()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal("original profile replacement failed", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("original runtime replacement did not settle")
	}
	replacement := s.runtime.Generation(p.ID)
	if replacement <= generation {
		t.Fatal("replacement reused original generation")
	}
	s.sessions.Current().Generation = replacement
	databendWorkbenchWait(t, tt, func() bool { return !d.Loading() })
	if d.Page() != page || d.Error() == "" {
		t.Fatal("late old-generation read replaced cached data")
	}
	before := gate.requests.Load()
	s.dataWorkspace.Load(*services, d, true)
	tt.Frame()
	if d.Loading() || gate.requests.Load() != before {
		t.Fatal("revoked document borrowed replacement session")
	}
	// Disposing the old document must close only its original captured handle.
	s.closeTabID(originalTab)
	if !s.runtime.IsConnected(p.ID) || s.runtime.Generation(p.ID) != replacement {
		t.Fatal("old document closed the new session")
	}
	databendWorkbenchSQL(t, f, `INSERT INTO "`+f.Database+`".`+table+` VALUES (2,'replacement generation')`)
	current := databendWorkbenchData(t, s, tt, *services, p, table)
	if current == d || len(current.Page().Rows) != 2 || current.Error() != "" {
		t.Fatal("explicit replacement document failed", current.Error())
	}
	databendWorkbenchCapture(t, f, tt, "generation-recovered")
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if s.runtime.IsConnected(p.ID) || current.Page() != nil {
		t.Fatal("disconnect retained scoped data resources")
	}
	databendWorkbenchJournal(t, f, "workbench-generation-lifecycle.jsonl", map[string]any{"test": t.Name(), "object": current.Object(), "original_generation": generation, "replacement_generation": replacement, "old_cache_preserved": true, "old_document_revoked": true, "replacement_survived_old_close": true, "replacement_rows": 2, "disconnect_disposed_data": true, "public_provider_registered": false})
}

func TestDatabendWorkbenchOriginalDisconnectWaitsForReadAndNeverReplaysSQL(t *testing.T) {
	f := databendWorkbenchOriginal(t)
	table := "workbench_disconnect53"
	databendWorkbenchSQL(t, f, `CREATE TABLE "`+f.Database+`".`+table+` (id UInt64 NOT NULL,txt String NOT NULL)`)
	databendWorkbenchSQL(t, f, `INSERT INTO "`+f.Database+`".`+table+` VALUES (1,'retained row')`)
	gate, endpoint := databendWorkbenchReadGate(t, f, table, "page")
	defer gate.unblock()
	acknowledged := make(chan agentbridge.Receipt, 8)
	s, tt, p, services := databendWorkbenchState(t, f, endpoint, func(_ string, r agentbridge.Receipt) {
		if r.CancellationRequested {
			acknowledged <- r
		}
	})
	d := databendWorkbenchData(t, s, tt, *services, p, table)
	dataID := s.currentTab().id
	s.openQuery()
	queryID, q := s.currentTab().id, s.currentTab().query
	databendWorkbenchWait(t, tt, func() bool { return !q.Preparing() && !q.Loading() })
	source := `INSERT INTO "` + table + `" VALUES (999,'must not replay')`
	q.Editor.Buffer.Set(source)
	s.selectTab(dataID)
	gate.armed.Store(true)
	s.dataWorkspace.Load(*services, d, true)
	databendAwaitReadGate(t, gate.held)
	generation := s.runtime.Generation(p.ID)
	// Disconnect is a UI-thread operation. The independent proxy releases only
	// its original bytes after a controlled delay, never touching workbench state.
	go func() { time.Sleep(200 * time.Millisecond); gate.unblock() }()
	s.disconnectMySQLProfile(p.ID)
	select {
	case <-gate.returned:
	default:
		t.Fatal("disconnect returned before its original response settled")
	}
	select {
	case r := <-acknowledged:
		if !r.Dispatched || !r.ExecutorAcknowledged || r.ProcessExitConfirmed || len(r.OriginalResponse()) == 0 {
			t.Fatal("disconnect lost original read acknowledgement", r)
		}
	default:
		t.Fatal("disconnect discarded the pending original executor")
	}
	tt.Frame()
	if s.runtime.IsConnected(p.ID) || d.Page() != nil || s.tabIndex(dataID) >= 0 || !q.NeedsRecovery() || q.Editor.Buffer.String() != source {
		t.Fatal("disconnect lost SQL text or retained scoped data")
	}
	secret, err := os.ReadFile(f.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	s.sessions.Current().Password = string(secret)
	s.sessions.Connect(p, services.Invalidate)
	databendWorkbenchWait(t, tt, func() bool { return s.runtime.IsConnected(p.ID) && s.sessions.StatusFor(p.ID) == "已连接" })
	s.selectTab(queryID)
	databendWorkbenchClick(t, tt, "建立新物理会话（不执行现有 SQL）")
	databendWorkbenchWait(t, tt, func() bool { return !q.NeedsRecovery() && !q.Preparing() && !q.Loading() })
	rows := databendWorkbenchSQL(t, f, `SELECT id FROM "`+f.Database+`".`+table+` ORDER BY id`)
	if q.Editor.Buffer.String() != source || q.Result() != nil || len(rows) != 1 || rows[0][0] != "1" || gate.requests.Load() != 2 || s.runtime.Generation(p.ID) <= generation {
		t.Fatal("explicit recovery replayed canceled data or unexecuted SQL", rows)
	}
	databendWorkbenchCapture(t, f, tt, "disconnect-recovered")
	databendWorkbenchJournal(t, f, "workbench-disconnect-lifecycle.jsonl", map[string]any{"test": t.Name(), "database": f.Database, "original_generation": generation, "replacement_generation": s.runtime.Generation(p.ID), "matched_requests": gate.requests.Load(), "sql_text_retained": true, "original_executor_acknowledged": true, "data_disposed": true, "recovery_replayed": false, "rows": rows, "public_provider_registered": false})
}
