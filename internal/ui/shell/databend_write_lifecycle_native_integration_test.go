package shell

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

// Withhold the original request or response at one native statement boundary.
// Independent HTTP changes happen on the test's UI thread while it is held.
func databendWorkbenchWriteGate(t *testing.T, f databendWorkbenchFixture, table string, before bool, journals ...string) (*databendReadGate, string) {
	t.Helper()
	journal := "workbench-native-writes.jsonl"
	if len(journals) != 0 {
		journal = journals[0]
	}
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
		match := req.Method == http.MethodPost && strings.Contains(query.SQL, table) &&
			(strings.HasPrefix(query.SQL, "UPDATE ") || strings.HasPrefix(query.SQL, "INSERT INTO ") || strings.HasPrefix(query.SQL, "DELETE FROM "))
		capture := match && g.requests.Add(1) == 1
		hold := func() {
			close(g.held)
			select {
			case <-g.release:
			case <-time.After(60 * time.Second):
				t.Error("original write boundary was not released")
			}
		}
		if capture && before {
			hold()
		}
		code, headers, body := databendWorkbenchForward(t, f, req)
		if match {
			hash := sha256.Sum256(body)
			databendWorkbenchJournal(t, f, journal, map[string]any{"test": t.Name(), "sql": query.SQL, "query_id": req.Header.Get("X-DATABEND-QUERY-ID"), "status": code, "native_bytes": body, "native_sha256": hex.EncodeToString(hash[:])})
		}
		if capture && !before {
			hold()
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
	t.Cleanup(proxy.Close)
	return g, proxy.URL
}

func databendWorkbenchWriteReceipt(t *testing.T, tt *ui.Tester) (string, contract.DataSequentialWriteReceipt) {
	t.Helper()
	databendWorkbenchClick(t, tt, "原生顺序写入回执")
	databendWorkbenchClick(t, tt, "复制完整回执")
	raw := tt.Clipboard()
	var value struct {
		Receipt contract.DataSequentialWriteReceipt
	}
	if err := json.Unmarshal([]byte(raw), &value); err != nil || len(value.Receipt.Statements) == 0 {
		t.Fatal("original sequential receipt missing", err, raw)
	}
	databendWorkbenchClick(t, tt, "关闭")
	return raw, value.Receipt
}

func TestDatabendWorkbenchOriginalSameStatementZeroGuard(t *testing.T) {
	f := databendWorkbenchOriginal(t)
	table := "workbench_zero54"
	databendWorkbenchSQL(t, f, `CREATE TABLE "`+f.Database+`".`+table+` (id UInt64 NOT NULL,txt String NOT NULL)`)
	databendWorkbenchSQL(t, f, `INSERT INTO "`+f.Database+`".`+table+` VALUES (1,'first'),(2,'second')`)
	gate, endpoint := databendWorkbenchWriteGate(t, f, table, true)
	defer gate.unblock()
	s, tt, p, services := databendWorkbenchState(t, f, endpoint)
	d := databendWorkbenchData(t, s, tt, *services, p, table)
	page := d.Page()
	for row := range 2 {
		s.dataWorkspace.EditCell(*services, d, row, "txt", contract.Cell{Kind: contract.Text, Text: "changed"})
	}
	preview := databendWorkbenchPreview(t, s, tt, d)
	databendWorkbenchClick(t, tt, "确认并提交更改")
	databendAwaitReadGate(t, gate.held)
	// All original-row and uniqueness prechecks have already completed. This
	// actual duplicate can only be rejected by the original statement's guard.
	databendWorkbenchSQL(t, f, `INSERT INTO "`+f.Database+`".`+table+` VALUES (1,'first')`)
	gate.unblock()
	databendWorkbenchWait(t, tt, func() bool { return !d.Busy() })
	raw, receipt := databendWorkbenchWriteReceipt(t, tt)
	rows := databendWorkbenchSQL(t, f, `SELECT id,txt FROM "`+f.Database+`".`+table+` ORDER BY id,txt`)
	if !d.HasDraft() || s.dataWorkspace.CanSave(d) || d.Page() != page || gate.requests.Load() != 1 ||
		len(receipt.Statements) != 2 || receipt.Statements[0].State != contract.DataWriteAcknowledged ||
		receipt.Statements[0].AffectedRows != 0 || receipt.Statements[1].State != contract.DataWriteNotSent ||
		len(rows) != 3 || rows[0][1] != "first" || rows[1][1] != "first" || rows[2][1] != "second" {
		t.Fatal("same-statement conflict lost draft/receipt, refreshed success or sent suffix", d.Error(), raw, rows)
	}
	databendWorkbenchCapture(t, f, tt, "zero-guard")
	databendWorkbenchJournal(t, f, "workbench-write-faults.jsonl", map[string]any{"test": t.Name(), "mode": "zero_guard", "preview": json.RawMessage(preview), "receipt": json.RawMessage(raw), "rows": rows, "writes": gate.requests.Load(), "draft_retained": true, "cache_preserved": true, "write_blocked": true})
}

func TestDatabendWorkbenchOriginalRecreatedTableRejectsOldConfirmation(t *testing.T) {
	for _, mode := range []string{"insert", "update", "prefix"} {
		t.Run(mode, func(t *testing.T) {
			f := databendWorkbenchOriginal(t)
			table := "workbench_recreate_" + mode + "54"
			create := `CREATE TABLE "` + f.Database + `".` + table + ` (id UInt64 NOT NULL,txt String NOT NULL)`
			seed := `INSERT INTO "` + f.Database + `".` + table + ` VALUES (1,'first'),(2,'second')`
			databendWorkbenchSQL(t, f, create)
			databendWorkbenchSQL(t, f, seed)
			identitySQL := `SELECT table_id FROM system.tables WHERE database='` + f.Database + `' AND name='` + table + `'`
			before := databendWorkbenchSQL(t, f, identitySQL)
			rowIDSQL := `SELECT id,_row_id FROM "` + f.Database + `".` + table + ` ORDER BY id`
			originalRows := databendWorkbenchSQL(t, f, rowIDSQL)
			ddl := databendWorkbenchSQL(t, f, `SHOW CREATE TABLE "`+f.Database+`".`+table)
			gate, endpoint := databendWorkbenchWriteGate(t, f, table, false)
			defer gate.unblock()
			s, tt, p, services := databendWorkbenchState(t, f, endpoint)
			d := databendWorkbenchData(t, s, tt, *services, p, table)
			page := d.Page()
			if mode == "insert" {
				databendWorkbenchClick(t, tt, "添加记录")
				s.dataWorkspace.EditCell(*services, d, 2, "id", contract.Cell{Kind: contract.Integer, Text: "3"})
				s.dataWorkspace.EditCell(*services, d, 2, "txt", contract.Cell{Kind: contract.Text, Text: "new"})
			} else {
				for row := range 2 {
					s.dataWorkspace.EditCell(*services, d, row, "txt", contract.Cell{Kind: contract.Text, Text: "changed"})
				}
			}
			preview := databendWorkbenchPreview(t, s, tt, d)
			if mode == "prefix" {
				databendWorkbenchClick(t, tt, "确认并提交更改")
				databendAwaitReadGate(t, gate.held)
			}
			databendWorkbenchSQL(t, f, `DROP TABLE "`+f.Database+`".`+table)
			databendWorkbenchSQL(t, f, create)
			databendWorkbenchSQL(t, f, seed)
			after := databendWorkbenchSQL(t, f, identitySQL)
			replacementRows := databendWorkbenchSQL(t, f, rowIDSQL)
			recreatedDDL := databendWorkbenchSQL(t, f, `SHOW CREATE TABLE "`+f.Database+`".`+table)
			if len(before) != 1 || len(after) != 1 || before[0][0] == after[0][0] {
				t.Fatal("actual recreated table identity did not change", before, after)
			}
			if !reflect.DeepEqual(ddl, recreatedDDL) {
				t.Fatal("recreated table definition differs; this would not test incarnation identity", ddl, recreatedDDL)
			}
			databendWorkbenchJournal(t, f, "workbench-table-incarnations.jsonl", map[string]any{"test": t.Name(), "original": before, "replacement": after, "original_ddl": ddl, "replacement_ddl": recreatedDDL, "original_rows": originalRows, "replacement_rows": replacementRows})
			gate.unblock()
			if mode != "prefix" {
				databendWorkbenchClick(t, tt, "确认并提交更改")
			}
			databendWorkbenchWait(t, tt, func() bool { return !d.Busy() })
			rows := databendWorkbenchSQL(t, f, `SELECT id,txt FROM "`+f.Database+`".`+table+` ORDER BY id`)
			wantWrites := int32(0)
			if mode == "prefix" {
				wantWrites = 1
			}
			if !d.HasDraft() || s.dataWorkspace.CanSave(d) || d.Page() != page || gate.requests.Load() != wantWrites || len(rows) != 2 || rows[0][1] != "first" || rows[1][1] != "second" {
				t.Fatal("old immutable confirmation wrote into recreated table or lost evidence", d.Error(), gate.requests.Load(), rows)
			}
			raw, receipt := databendWorkbenchWriteReceipt(t, tt)
			for i, item := range receipt.Statements {
				want := contract.DataWriteNotSent
				if mode == "prefix" && i == 0 {
					want = contract.DataWriteAcknowledged
				}
				if item.State != want {
					t.Fatal("replacement table changed original receipt", raw)
				}
			}
			databendWorkbenchCapture(t, f, tt, "recreated-table")
			databendWorkbenchJournal(t, f, "workbench-write-faults.jsonl", map[string]any{"test": t.Name(), "mode": "recreate_" + mode, "preview": json.RawMessage(preview), "receipt": json.RawMessage(raw), "rows": rows, "writes": gate.requests.Load(), "draft_retained": true, "cache_preserved": true, "write_blocked": true})
		})
	}
}

func TestDatabendWorkbenchOriginalWriteCancellationAndDisconnectGuard(t *testing.T) {
	for _, mode := range []string{"cancel", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			f := databendWorkbenchOriginal(t)
			table := "workbench_write_" + mode + "54"
			databendWorkbenchSQL(t, f, `CREATE TABLE "`+f.Database+`".`+table+` (id UInt64 NOT NULL,txt String NOT NULL)`)
			databendWorkbenchSQL(t, f, `INSERT INTO "`+f.Database+`".`+table+` VALUES (1,'first'),(2,'second')`)
			gate, endpoint := databendWorkbenchWriteGate(t, f, table, false)
			defer gate.unblock()
			acknowledged := make(chan agentbridge.Receipt, 4)
			s, tt, p, services := databendWorkbenchState(t, f, endpoint, func(method string, r agentbridge.Receipt) {
				if method == "data-write" {
					acknowledged <- r
				}
			})
			d := databendWorkbenchData(t, s, tt, *services, p, table)
			tabID, page, generation := s.currentTab().id, d.Page(), s.runtime.Generation(p.ID)
			for row := range 2 {
				s.dataWorkspace.EditCell(*services, d, row, "txt", contract.Cell{Kind: contract.Text, Text: "changed"})
			}
			preview := databendWorkbenchPreview(t, s, tt, d)
			databendWorkbenchClick(t, tt, "确认并提交更改")
			databendAwaitReadGate(t, gate.held)
			if mode == "disconnect" {
				s.disconnectMySQLProfile(p.ID)
				tt.Frame()
				if !s.runtime.IsConnected(p.ID) || s.tabIndex(tabID) < 0 || !d.Busy() || !d.HasDraft() || d.Page() != page {
					t.Fatal("disconnect discarded pending original write")
				}
			}
			databendWorkbenchClick(t, tt, "停止数据操作")
			if !d.Busy() || !d.HasDraft() || d.Page() != page || gate.requests.Load() != 1 {
				t.Fatal("write cancellation settled before original acknowledgement")
			}
			select {
			case <-acknowledged:
				t.Fatal("executor acknowledged before original response returned")
			default:
			}
			gate.unblock()
			databendWorkbenchWait(t, tt, func() bool { return !d.Busy() })
			var original agentbridge.Receipt
			select {
			case original = <-acknowledged:
			case <-time.After(10 * time.Second):
				t.Fatal("original canceled write receipt missing")
			}
			if !original.Dispatched || !original.CancellationRequested || !original.ExecutorAcknowledged || original.ProcessExitConfirmed || len(original.OriginalResponse()) == 0 {
				t.Fatal("canceled write lost original live executor acknowledgement", original)
			}
			raw, receipt := databendWorkbenchWriteReceipt(t, tt)
			rows := databendWorkbenchSQL(t, f, `SELECT id,txt FROM "`+f.Database+`".`+table+` ORDER BY id`)
			if !d.HasDraft() || s.dataWorkspace.CanSave(d) || d.Page() != page || gate.requests.Load() != 1 || len(receipt.Statements) != 2 || receipt.Statements[0].State != contract.DataWriteAcknowledged || receipt.Statements[0].AffectedRows != 1 || receipt.Statements[1].State != contract.DataWriteNotSent || len(rows) != 2 || rows[0][1] != "changed" || rows[1][1] != "second" {
				t.Fatal("canceled original write discarded durable prefix or replayed suffix", d.Error(), raw, rows)
			}
			s.disconnectMySQLProfile(p.ID)
			tt.Frame()
			if !s.runtime.IsConnected(p.ID) || s.runtime.Generation(p.ID) != generation || s.tabIndex(tabID) < 0 || !d.HasDraft() {
				t.Fatal("incomplete original receipt bypassed disconnect guard")
			}
			retained, _ := databendWorkbenchWriteReceipt(t, tt)
			if retained != raw {
				t.Fatal("disconnect attempt rewrote original acknowledgement")
			}
			databendWorkbenchCapture(t, f, tt, "write-acknowledged")
			databendWorkbenchJournal(t, f, "workbench-write-faults.jsonl", map[string]any{"test": t.Name(), "mode": mode, "preview": json.RawMessage(preview), "receipt": json.RawMessage(raw), "rows": rows, "writes": gate.requests.Load(), "draft_retained": true, "cache_preserved": true, "write_blocked": true, "original_generation": generation, "current_generation": s.runtime.Generation(p.ID), "waited_for_original": true, "disconnect_guarded": true})
		})
	}
}
