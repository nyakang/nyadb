package rqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func nativeSchemaWriteSnapshot(t *testing.T, d *schemaDocument) contract.SchemaDesignSnapshot {
	t.Helper()
	out, err := d.Operations().Snapshot(context.Background(), d.target)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func retainSchemaWriteOutcome(t *testing.T, name string, out contract.SchemaDesignOutcome) {
	t.Helper()
	if out.Receipt != nil && out.Receipt.NativeJSON != "" {
		retainNativeFileReceipt(t, name+"-native", out.Receipt.NativeJSON)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	retainNativeFileReceipt(t, name+"-outcome", string(raw))
}
func requireSchemaCommit(t *testing.T, name string, out contract.SchemaDesignOutcome) contract.SchemaDesignSnapshot {
	t.Helper()
	retainSchemaWriteOutcome(t, name, out)
	if out.State != "committed" || !out.Verified || out.Error != nil || out.Actual == nil || out.Receipt == nil || out.Receipt.State != "committed" || !out.Receipt.Verified || out.Receipt.Actual == nil || !reflect.DeepEqual(out.Receipt.Actual, out.Actual) || out.Receipt.NativeJSON == "" || len(out.Receipt.Statements) == 0 {
		t.Fatal("original native schema commit not completely verified", out.State, out.Error)
	}
	if strings.Contains(strings.Join(out.Receipt.Statements, ";"), "PRAGMA foreign_keys=") {
		t.Fatal("schema executor mutated writer foreign-key state")
	}
	return out.Actual.Clone()
}
func nativeSchemaDraft(t *testing.T, sql string) schemaddl.Table {
	t.Helper()
	out, err := schemaddl.ParseSQLite(sql)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNativeRQLiteSchemaWritesCreateColumnsDefinitionsRenameAndDropOriginalReceipt(t *testing.T) {
	p, _ := nativeFixtureConfigured(t, "", "", true)
	s := openNative(t, p, "original-fixture-password")
	d := reserveSchemaUnit(t, s, "native-structure", "native_design")
	original := nativeSchemaWriteSnapshot(t, d)
	draft := nativeSchemaDraft(t, `CREATE TABLE native_design(id INTEGER PRIMARY KEY AUTOINCREMENT,v TEXT COLLATE NOCASE UNIQUE DEFAULT 'X''61''',unused BLOB,CONSTRAINT ck CHECK(length(v)>0))`)
	draft.Definitions = []schemaddl.Definition{{Name: "native_design_ix", Kind: "index", SQL: `CREATE INDEX native_design_ix ON native_design(lower(v)) WHERE v IS NOT NULL`}}
	original = requireSchemaCommit(t, "schema-write-create", d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: draft}))
	if len(original.Schema.Table.Definitions) != 1 || d.OriginalOutcome().Receipt.DocumentID != "native-structure" {
		t.Fatal("creation lost original document ownership or definitions")
	}
	l := lease(t, s, 1)
	if got := nativeExecute(t, l, `SELECT count(*) FROM sqlite_sequence`).Sets[0].Rows[0][0].Text; got != "0" {
		t.Fatal("schema preparation initialized an empty autoincrement sequence", got)
	}
	nativeExecute(t, l, `CREATE TABLE native_design_child(id INTEGER PRIMARY KEY,parent_id INTEGER REFERENCES native_design(id) ON DELETE CASCADE);CREATE TABLE native_design_audit(v TEXT);CREATE VIEW native_design_view AS SELECT id,v FROM native_design;CREATE TRIGGER native_design_insert AFTER INSERT ON native_design BEGIN INSERT INTO native_design_audit VALUES(new.v); END;INSERT INTO native_design(id,v,unused) VALUES(9007199254740993,'large',X'00ff'),(9223372036854775807,'max',X'ff00');INSERT INTO native_design_child VALUES(1,9007199254740993)`)
	original = nativeSchemaWriteSnapshot(t, d)
	sequenceBefore := nativeExecute(t, l, `SELECT rowid,name,seq FROM sqlite_sequence ORDER BY rowid`).Sets[0].Rows
	draft = original.Schema.Table.Clone()
	draft.Clauses = append(draft.Clauses, schemaddl.Clause{ID: "new:added", Kind: "column", Name: "added", SQL: "BLOB DEFAULT X'00ff'"})
	original = requireSchemaCommit(t, "schema-write-add", d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: draft}))
	if len(original.Schema.Table.Columns()) != 4 {
		t.Fatal("native add lost column-before-constraint placement")
	}
	draft = original.Schema.Table.Clone()
	for i, c := range draft.Clauses {
		if c.Name == "unused" {
			draft.Clauses = append(draft.Clauses[:i], draft.Clauses[i+1:]...)
			break
		}
	}
	original = requireSchemaCommit(t, "schema-write-drop-column", d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: draft, AllowColumnDrop: true}))
	draft = original.Schema.Table.Clone()
	draft.Definitions = append(draft.Definitions, schemaddl.Definition{Name: "native_design_update", Kind: "trigger", SQL: `CREATE TRIGGER native_design_update AFTER UPDATE OF v ON native_design BEGIN INSERT INTO native_design_audit VALUES(new.v); END`})
	original = requireSchemaCommit(t, "schema-write-definitions", d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: draft}))
	draft = original.Schema.Table.Clone()
	for i, c := range draft.Clauses {
		if c.Name == "v" {
			draft.Clauses[i].Name = "label"
		}
	}
	original = requireSchemaCommit(t, "schema-write-rename-column", d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: draft}))
	beforeRename := original.Clone()
	original = requireSchemaCommit(t, "schema-write-rename-table", d.Operations().Rename(context.Background(), contract.SchemaRenameRequest{Original: original, Destination: "native final table"}))
	if d.target.Object.Name != "native final table" || d.OriginalOutcome().Receipt.Original.Target != beforeRename.Target || d.OriginalOutcome().Receipt.Desired != nil {
		t.Fatal("verified rename relabelled original target or lost stable document ID")
	}
	sequenceAfter := nativeExecute(t, l, `SELECT rowid,name,seq FROM sqlite_sequence ORDER BY rowid`).Sets[0].Rows
	sequenceBefore[0][1].Text = "native final table"
	if !reflect.DeepEqual(sequenceBefore, sequenceAfter) {
		t.Fatal("native schema checks changed the original sequence identity or value", sequenceBefore, sequenceAfter)
	}
	actual := nativeExecute(t, l, `SELECT id,label,added FROM "native final table" ORDER BY id;SELECT id,label FROM native_design_view ORDER BY id;SELECT count(*) FROM native_design_child;UPDATE "native final table" SET label='new' WHERE id=9007199254740993;SELECT count(*) FROM native_design_audit`)
	if actual.Sets[0].Rows[0][0].Text != "9007199254740993" || actual.Sets[0].Rows[1][0].Text != "9223372036854775807" || actual.Sets[0].Rows[0][2].Kind != contract.Binary || !bytes.Equal(actual.Sets[0].Rows[0][2].Bytes, []byte{0, 255}) || actual.Sets[1].Rows[0][0].Text != "9007199254740993" || actual.Sets[2].Rows[0][0].Text != "1" || actual.Sets[3].Rows[0][0].Text != "3" {
		t.Fatal("native DDL changed typed rows, child, trigger or dependent view")
	}
	blocked := d.Operations().Drop(context.Background(), original)
	retainSchemaWriteOutcome(t, "schema-write-drop-inbound-rejected", blocked)
	if blocked.State != "rolled_back" || blocked.Verified || blocked.Receipt == nil || !errors.Is(blocked.Error, schemaddl.ErrConflict) {
		t.Fatal("drop allowed implicit inbound cascade", blocked.State, blocked.Error)
	}
	nativeExecute(t, l, `DROP TABLE native_design_child`)
	original = nativeSchemaWriteSnapshot(t, d)
	blocked = d.Operations().Drop(context.Background(), original)
	retainSchemaWriteOutcome(t, "schema-write-drop-view-rejected", blocked)
	if blocked.State != "rolled_back" || blocked.Verified || blocked.Error == nil {
		t.Fatal("drop left a dangling dependent view", blocked.State, blocked.Error)
	}
	nativeExecute(t, l, `DROP VIEW native_design_view`)
	original = nativeSchemaWriteSnapshot(t, d)
	dropped := requireSchemaCommit(t, "schema-write-drop", d.Operations().Drop(context.Background(), original))
	if dropped.Schema.Exists || dropped.Target != original.Target || dropped.Schema.Table.Name != original.Target.Object.Name {
		t.Fatal("drop receipt lost original table absence")
	}
	if s.SchemaDesign().Apply != nil || !s.Capabilities().DesignTables || !s.Capabilities().Objects {
		t.Fatal("original document capabilities exposed a session executor or disappeared")
	}
}

func TestNativeRQLiteSchemaWritesRejectScopeDriftAndRollbackProgramFailures(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	nativeExecute(t, l, `CREATE TABLE native_guarded(id INTEGER PRIMARY KEY,v TEXT);CREATE TABLE native_unselected(id INTEGER);INSERT INTO native_guarded VALUES(1,'original')`)
	d := reserveSchemaUnit(t, s, "native-guarded", "native_guarded")
	other := reserveSchemaUnit(t, s, "native-peer", "native_guarded")
	original := nativeSchemaWriteSnapshot(t, d)
	draft := original.Schema.Table.Clone()
	draft.Clauses = append(draft.Clauses, schemaddl.Clause{ID: "new:forbidden", Kind: "column", Name: "forbidden", SQL: "TEXT"})
	foreign := nativeSchemaWriteSnapshot(t, other)
	out := d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: foreign, Desired: draft})
	if out.State != "not_executed" || out.Receipt != nil || !errors.Is(out.Error, schemaddl.ErrConflict) {
		t.Fatal("peer schema proof dispatched a native write", out.Error)
	}
	nativeExecute(t, l, `CREATE INDEX native_unselected_ix ON native_unselected(id)`)
	out = d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: draft})
	retainSchemaWriteOutcome(t, "schema-write-unselected-drift", out)
	if out.State != "rolled_back" || !errors.Is(out.Error, schemaddl.ErrConflict) {
		t.Fatal("unselected schema drift bypassed atomic original guard", out.State, out.Error)
	}
	original = nativeSchemaWriteSnapshot(t, d)
	draft = original.Schema.Table.Clone()
	draft.Clauses = append(draft.Clauses, schemaddl.Clause{ID: "new:forbidden", Kind: "column", Name: "forbidden", SQL: "TEXT"})
	nativeExecute(t, l, `CREATE TEMP TRIGGER native_writer_trigger AFTER UPDATE ON main.native_guarded BEGIN SELECT 1; END`)
	out = d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: draft})
	retainSchemaWriteOutcome(t, "schema-write-writer-temporary-trigger", out)
	if out.State != "rolled_back" || !errors.Is(out.Error, schemaddl.ErrConflict) {
		t.Fatal("writer temporary trigger was ignored", out.State, out.Error)
	}
	nativeExecute(t, l, `DROP TRIGGER temp.native_writer_trigger`)
	for _, bad := range []string{"missing_trigger_table", "unknown_update_column", "required_column"} {
		draft = original.Schema.Table.Clone()
		switch bad {
		case "missing_trigger_table":
			draft.Definitions = []schemaddl.Definition{{Name: "native_bad_trigger", Kind: "trigger", SQL: `CREATE TRIGGER native_bad_trigger AFTER INSERT ON native_guarded BEGIN INSERT INTO missing_destination VALUES(new.v); END`}}
		case "unknown_update_column":
			draft.Definitions = []schemaddl.Definition{{Name: "native_bad_trigger", Kind: "trigger", SQL: `CREATE TRIGGER native_bad_trigger AFTER UPDATE OF missing_field ON native_guarded BEGIN SELECT 1; END`}}
		case "required_column":
			draft.Clauses = append(draft.Clauses, schemaddl.Clause{ID: "new:required", Kind: "column", Name: "required", SQL: "TEXT NOT NULL"})
		}
		out = d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: draft})
		retainSchemaWriteOutcome(t, "schema-write-rejected-"+bad, out)
		if out.Error == nil || out.Verified || out.State != "rolled_back" && out.State != "not_executed" {
			t.Fatal("invalid program/column committed", bad, out.State, out.Error)
		}
		if bad == "unknown_update_column" && (out.State != "not_executed" || out.Receipt != nil) {
			t.Fatal("permissive UPDATE OF header was dispatched")
		}
	}
	draft = original.Schema.Table.Clone()
	draft.Clauses[1].SQL = "BLOB"
	out = d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: draft})
	actualSchema := requireSchemaCommit(t, "schema-write-rebuild-writer-fk-disabled", out)
	if actualSchema.Schema.Table.Clauses[1].SQL != "BLOB" || !strings.Contains(strings.Join(out.Receipt.Statements, "\n"), "__nyadb_rebuild_") {
		t.Fatal("disabled-FK reconstruction lost its complete original request", out.State, out.Error)
	}
	actual := nativeExecute(t, l, `SELECT id,v FROM native_guarded;SELECT count(*) FROM main.sqlite_schema WHERE name='native_bad_trigger';SELECT count(*) FROM pragma_table_xinfo('native_guarded') WHERE name IN ('forbidden','required')`)
	if actual.Sets[0].Rows[0][1].Text != "original" || actual.Sets[1].Rows[0][0].Text != "0" || actual.Sets[2].Rows[0][0].Text != "0" {
		t.Fatal("failed original transaction left schema or data prefix")
	}
}

type heldSchemaWrite struct {
	base         http.RoundTripper
	marker       string
	started, ack chan struct{}
	release      sync.Once
	requests     atomic.Int32
	allWrites    atomic.Int32
	raw          chan string
	lose         bool
}

func (h *heldSchemaWrite) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	res, err := h.base.RoundTrip(req)
	if err != nil {
		return res, err
	}
	if req.URL.Path == "/db/request" || req.URL.Path == "/db/execute" {
		h.allWrites.Add(1)
	}
	if req.URL.Path != "/db/execute" || !strings.Contains(string(body), h.marker) {
		return res, nil
	}
	raw, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil {
		return nil, err
	}
	res.Body = io.NopCloser(bytes.NewReader(raw))
	if h.requests.Add(1) == 1 {
		h.raw <- string(raw)
		close(h.started)
	}
	<-h.ack
	if h.lose {
		_ = res.Body.Close()
		return nil, errors.New("original complete applied response deliberately lost")
	}
	return res, nil
}

func TestNativeRQLiteSchemaWritesCancelCloseWaitAndRetainOriginalCommit(t *testing.T) {
	for _, mode := range []string{"caller", "document", "session"} {
		t.Run(mode, func(t *testing.T) {
			p := nativeFixture(t, "")
			s := openNative(t, p, "original-fixture-password")
			d := reserveSchemaUnit(t, s, "native-held", "native_held_write")
			original := nativeSchemaWriteSnapshot(t, d)
			draft := nativeSchemaDraft(t, `CREATE TABLE native_held_write(id INTEGER PRIMARY KEY,v TEXT)`)
			h := &heldSchemaWrite{base: s.transport, marker: "native_held_write", started: make(chan struct{}), ack: make(chan struct{}), raw: make(chan string, 1)}
			defer h.release.Do(func() { close(h.ack) })
			s.client.Transport = h
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan contract.SchemaDesignOutcome, 1)
			go func() {
				done <- d.Operations().Apply(ctx, contract.SchemaDesignRequest{Original: original, Desired: draft})
			}()
			select {
			case <-h.started:
			case <-time.After(10 * time.Second):
				t.Fatal("original native write did not reach complete acknowledgement")
			}
			raw := <-h.raw
			retainNativeFileReceipt(t, "schema-write-held-"+mode+"-transport-original", raw)
			cancel()
			closed := make(chan struct{})
			if mode != "caller" {
				go func() {
					if mode == "session" {
						_ = s.Close()
					} else {
						_ = d.Close()
					}
					close(closed)
				}()
			}
			select {
			case <-done:
				t.Fatal("cancellation abandoned original native write acknowledgement")
			case <-closed:
				t.Fatal("close abandoned original executor")
			case <-time.After(40 * time.Millisecond):
			}
			h.release.Do(func() { close(h.ack) })
			select {
			case out := <-done:
				requireSchemaCommit(t, "schema-write-held-"+mode, out)
				if out.Receipt.NativeJSON != raw || out.Receipt.Original.Target != original.Target || out.Receipt.DocumentID != "native-held" {
					t.Fatal("late commit lost complete original identity or native bytes")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("original native schema write did not settle")
			}
			if mode != "caller" {
				select {
				case <-closed:
				case <-time.After(5 * time.Second):
					t.Fatal("close did not settle after acknowledgement")
				}
				if d.OriginalOutcome() != nil {
					t.Fatal("closed document retained original caches")
				}
			}
			if h.requests.Load() != 1 {
				t.Fatal("canceled schema write replayed")
			}
		})
	}
}

func TestNativeRQLiteSchemaWritesLostAppliedReceiptBlocksReplayAfterReads(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	d := reserveSchemaUnit(t, s, "native-unknown", "native_unknown_write")
	original := nativeSchemaWriteSnapshot(t, d)
	draft := nativeSchemaDraft(t, `CREATE TABLE native_unknown_write(id INTEGER PRIMARY KEY,v TEXT)`)
	h := &heldSchemaWrite{base: s.transport, marker: "native_unknown_write", started: make(chan struct{}), ack: make(chan struct{}), raw: make(chan string, 1), lose: true}
	defer h.release.Do(func() { close(h.ack) })
	s.client.Transport = h
	done := make(chan contract.SchemaDesignOutcome, 1)
	go func() {
		done <- d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: draft})
	}()
	select {
	case <-h.started:
	case <-time.After(10 * time.Second):
		t.Fatal("lost applied schema fixture did not dispatch")
	}
	raw := <-h.raw
	retainNativeFileReceipt(t, "schema-write-lost-transport-original", raw)
	h.release.Do(func() { close(h.ack) })
	out := <-done
	retainSchemaWriteOutcome(t, "schema-write-lost-original", out)
	if out.State != "uncertain" || out.Verified || out.Receipt == nil || out.Receipt.NativeJSON != "" || !errors.Is(out.Error, contract.ErrSchemaCommitUncertain) {
		t.Fatal("lost applied response became a fabricated rollback", out.State, out.Error)
	}
	captured := d.OriginalOutcome()
	observed := nativeSchemaWriteSnapshot(t, d)
	if !observed.Schema.Exists || !reflect.DeepEqual(captured, d.OriginalOutcome()) {
		t.Fatal("later schema read cleared or relabelled original unknown write")
	}
	draft = observed.Schema.Table.Clone()
	draft.Clauses = append(draft.Clauses, schemaddl.Clause{ID: "new:blocked", Kind: "column", Name: "blocked", SQL: "TEXT"})
	attempts := []contract.SchemaDesignOutcome{d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: observed, Desired: draft}), d.Operations().Drop(context.Background(), observed), d.Operations().Rename(context.Background(), contract.SchemaRenameRequest{Original: observed, Destination: "another-request"})}
	for _, attempt := range attempts {
		if attempt.State != "not_executed" || attempt.Receipt != nil || !errors.Is(attempt.Error, contract.ErrSchemaCommitUncertain) {
			t.Fatal("unknown write was replayed or relabelled", attempt)
		}
	}
	if h.requests.Load() != 1 || d.Operations().CanApply(d.target) || d.Operations().CanDrop(d.target) || d.Operations().CanRename(d.target) {
		t.Fatal("unknown original schema receipt allowed another write")
	}
}

func TestNativeRQLiteSchemaWrites2000Columns200IndexesPreserveNativeTypedRows(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	d := reserveSchemaUnit(t, s, "native-wide-write", "native_wide_write")
	original := nativeSchemaWriteSnapshot(t, d)
	columns := make([]string, 2000)
	columns[0] = "c0000 INTEGER PRIMARY KEY"
	for i := 1; i < len(columns); i++ {
		columns[i] = fmt.Sprintf("c%04d TEXT DEFAULT 'X''61'''", i)
	}
	draft := nativeSchemaDraft(t, "CREATE TABLE native_wide_write("+strings.Join(columns, ",")+",CONSTRAINT native_wide_positive CHECK(c0000>0))")
	for i := range 200 {
		draft.Definitions = append(draft.Definitions, schemaddl.Definition{Name: fmt.Sprintf("native_wide_i%03d", i), Kind: "index", SQL: fmt.Sprintf("CREATE INDEX native_wide_i%03d ON native_wide_write(c%04d)", i, i+1)})
	}
	original = requireSchemaCommit(t, "schema-write-wide-create", d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: draft}))
	if len(original.Schema.Table.Columns()) != 2000 || len(original.Schema.Table.Clauses) != 2001 || len(original.Schema.Table.Definitions) != 200 {
		t.Fatal("native executor narrowed structural capacity")
	}
	l := lease(t, s, 1)
	nativeExecute(t, l, `INSERT INTO native_wide_write(c0000,c1999) VALUES(9007199254740993,'retained')`)
	draft = original.Schema.Table.Clone()
	draft.Clauses[1999].Name = "complete_last"
	original = requireSchemaCommit(t, "schema-write-wide-rename", d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: draft}))
	draft = original.Schema.Table.Clone()
	for i, clause := range draft.Clauses {
		if clause.Kind == "column" && clause.Name == "complete_last" {
			draft.Clauses = append(draft.Clauses[:i], draft.Clauses[i+1:]...)
			break
		}
	}
	draft.Clauses = append(draft.Clauses, schemaddl.Clause{ID: "new:replacement", Kind: "column", Name: "new_last", SQL: "BLOB DEFAULT X'00ff'"})
	original = requireSchemaCommit(t, "schema-write-wide-drop-add", d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: draft, AllowColumnDrop: true}))
	if len(original.Schema.Table.Columns()) != 2000 || len(original.Schema.Table.Clauses) != 2001 || len(original.Schema.Table.Definitions) != 200 {
		t.Fatal("native column edits lost complete structural payload")
	}
	actual := nativeExecute(t, l, `SELECT c0000,c0001,new_last FROM native_wide_write`)
	if actual.Sets[0].Rows[0][0].Text != "9007199254740993" || actual.Sets[0].Rows[0][1].Text != "X'61'" || actual.Sets[0].Rows[0][2].Kind != contract.Binary || !bytes.Equal(actual.Sets[0].Rows[0][2].Bytes, []byte{0, 255}) {
		t.Fatal("wide native DDL changed retained typed data or typed defaults")
	}
}

func TestNativeRQLiteSchemaWritesDatabaseTimeoutRetainsOriginalRollback(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	nativeExecute(t, l, `CREATE TABLE native_schema_timeout(id INTEGER PRIMARY KEY,v TEXT);WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<200000) INSERT INTO native_schema_timeout SELECT x,printf('original_%d',x) FROM n`)
	d := reserveSchemaUnit(t, s, "native-timeout", "native_schema_timeout")
	original := nativeSchemaWriteSnapshot(t, d)
	draft := original.Schema.Table.Clone()
	draft.Definitions = []schemaddl.Definition{{Name: "native_timeout_ix", Kind: "index", SQL: `CREATE INDEX native_timeout_ix ON native_schema_timeout(lower(v)||printf('%1000s',v))`}}
	s.config.Options.DatabaseTimeoutMilliseconds = 50
	out := d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: draft})
	retainSchemaWriteOutcome(t, "schema-write-database-timeout", out)
	s.config.Options.DatabaseTimeoutMilliseconds = 30000
	if out.State != "rolled_back" || out.Verified || out.Receipt == nil || !errors.Is(out.Error, context.DeadlineExceeded) {
		t.Fatal("native DDL timeout did not retain acknowledged rollback", out.State, out.Error)
	}
	var native receipt
	_ = json.Unmarshal([]byte(out.Receipt.NativeJSON), &native)
	failedDDL := false
	for i, result := range native.Results {
		if result.failed() && strings.HasPrefix(out.Receipt.Statements[i], "CREATE INDEX") {
			failedDDL = true
		}
	}
	if !failedDDL {
		t.Fatal("timeout probe did not exercise the original native DDL executor")
	}
	actual := nativeExecute(t, l, `SELECT count(*) FROM native_schema_timeout;SELECT count(*) FROM main.sqlite_schema WHERE name='native_timeout_ix'`)
	if actual.Sets[0].Rows[0][0].Text != "200000" || actual.Sets[1].Rows[0][0].Text != "0" {
		t.Fatal("native timed-out DDL left a committed prefix")
	}
	draft = original.Schema.Table.Clone()
	draft.Clauses = append(draft.Clauses, schemaddl.Clause{ID: "new:reusable", Kind: "column", Name: "reusable", SQL: "TEXT"})
	requireSchemaCommit(t, "schema-write-after-timeout", d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: draft}))
}

type schemaCapturedContext struct {
	context.Context
	captured chan struct{}
	once     sync.Once
}

func (c *schemaCapturedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.captured) })
	return c.Context.Done()
}

func TestNativeRQLiteSchemaWritesQueuedRequestOwnsOriginalDraftAndCatalog(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	nativeExecute(t, l, `CREATE TABLE native_schema_queue(id INTEGER PRIMARY KEY,v TEXT);INSERT INTO native_schema_queue VALUES(1,'duplicate'),(2,'duplicate')`)
	d := reserveSchemaUnit(t, s, "native-queue", "native_schema_queue")
	original := nativeSchemaWriteSnapshot(t, d)
	firstDraft := original.Schema.Table.Clone()
	firstDraft.Definitions = []schemaddl.Definition{{Name: "native_queue_ix", Kind: "index", SQL: `CREATE UNIQUE INDEX native_queue_ix ON native_schema_queue(v)`}}
	h := &heldSchemaWrite{base: s.transport, marker: "native_queue_ix", started: make(chan struct{}), ack: make(chan struct{}), raw: make(chan string, 1)}
	defer h.release.Do(func() { close(h.ack) })
	s.client.Transport = h
	first := make(chan contract.SchemaDesignOutcome, 1)
	go func() {
		first <- d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: firstDraft})
	}()
	select {
	case <-h.started:
	case <-time.After(10 * time.Second):
		t.Fatal("original rollback write did not reach acknowledgement")
	}
	raw := <-h.raw
	retainNativeFileReceipt(t, "schema-write-queued-first-transport", raw)
	queued := contract.SchemaDesignRequest{Original: original.Clone(), Desired: original.Schema.Table.Clone()}
	queued.Desired.Clauses = append(queued.Desired.Clauses, schemaddl.Clause{ID: "new:captured", Kind: "column", Name: "captured", SQL: "TEXT DEFAULT 'original draft'"})
	ctx := &schemaCapturedContext{Context: context.Background(), captured: make(chan struct{})}
	second := make(chan contract.SchemaDesignOutcome, 1)
	go func() { second <- d.Operations().Apply(ctx, queued) }()
	select {
	case <-ctx.captured:
	case <-time.After(5 * time.Second):
		t.Fatal("queued schema request did not capture admission")
	}
	queued.Original.Schema.SQLite.Entries[0].SQL = "edited caller catalog"
	queued.Desired.Clauses[2].SQL = "BLOB DEFAULT X'ff'"
	h.release.Do(func() { close(h.ack) })
	firstOut := <-first
	retainSchemaWriteOutcome(t, "schema-write-queued-first-rollback", firstOut)
	if firstOut.State != "rolled_back" || firstOut.Receipt.NativeJSON != raw {
		t.Fatal("original rollback receipt lost", firstOut.State, firstOut.Error)
	}
	secondOut := <-second
	requireSchemaCommit(t, "schema-write-queued-captured", secondOut)
	if secondOut.Receipt.Sequence != 2 || secondOut.Receipt.Desired.Clauses[2].SQL != "TEXT DEFAULT 'original draft'" || secondOut.Receipt.Original.Schema.SQLite.Entries[0].SQL == "edited caller catalog" || h.allWrites.Load() != 2 {
		t.Fatal("queued native schema request borrowed caller-owned memory or replayed")
	}
	actual := nativeExecute(t, l, `SELECT captured FROM native_schema_queue ORDER BY id`)
	if actual.Sets[0].Rows[0][0].Text != "original draft" || actual.Sets[0].Rows[1][0].Text != "original draft" {
		t.Fatal("queued schema write changed its reviewed immutable draft")
	}
}

func TestNativeRQLiteSchemaWritesQueryOnlyCredentialsRetainOriginalRejection(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	nativeExecute(t, lease(t, s, 1), `CREATE TABLE native_auth_schema(id INTEGER PRIMARY KEY,v TEXT)`)
	p.ID, p.Username = 82, "reader"
	p.RQLiteOptions.ReadOnly = false
	reader := openNative(t, p, "reader-password")
	d := reserveSchemaUnit(t, reader, "native-query-only", "native_auth_schema")
	original := nativeSchemaWriteSnapshot(t, d)
	draft := original.Schema.Table.Clone()
	draft.Clauses = append(draft.Clauses, schemaddl.Clause{ID: "new:forbidden", Kind: "column", Name: "forbidden", SQL: "TEXT"})
	out := d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: draft})
	retainSchemaWriteOutcome(t, "schema-write-query-only-original-rejection", out)
	if out.State != "not_executed" || out.Verified || out.Error == nil || out.Receipt == nil || out.Receipt.NativeJSON == "" || out.Receipt.Original.Target.Object.ProfileID != 82 {
		t.Fatal("authentication rejection lost original complete receipt", out.State, out.Error)
	}
	captured := d.OriginalOutcome()
	after := nativeSchemaWriteSnapshot(t, d)
	if len(after.Schema.Table.Columns()) != 2 || !reflect.DeepEqual(captured, d.OriginalOutcome()) {
		t.Fatal("query-only rejection mutated structure or was relabelled by a later read")
	}
}

func TestNativeRQLiteSchemaWritesPreparationPreservesEmptySequencesGeneratedRowsAndTriggers(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	nativeExecute(t, l, `CREATE TABLE native_sequence(id INTEGER PRIMARY KEY AUTOINCREMENT,v TEXT DEFAULT 'original',generated TEXT GENERATED ALWAYS AS (v||' generated') VIRTUAL);CREATE TABLE native_sequence_audit(id INTEGER,v TEXT);CREATE TRIGGER native_sequence_insert AFTER INSERT ON native_sequence BEGIN INSERT INTO native_sequence_audit VALUES(new.id,new.v); END`)
	d := reserveSchemaUnit(t, s, "native-sequence", "native_sequence")
	original := nativeSchemaWriteSnapshot(t, d)
	draft := original.Schema.Table.Clone()
	draft.Clauses = append(draft.Clauses, schemaddl.Clause{ID: "new:added", Kind: "column", Name: "added", SQL: "TEXT"})
	original = requireSchemaCommit(t, "schema-write-sequence-empty-add", d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: draft}))
	rows := nativeExecute(t, l, `SELECT count(*) FROM sqlite_sequence;SELECT count(*) FROM native_sequence;SELECT count(*) FROM native_sequence_audit`)
	for _, set := range rows.Sets {
		if set.Rows[0][0].Text != "0" {
			t.Fatal("EXPLAIN executed a row, trigger, default or sequence initialization")
		}
	}
	nativeExecute(t, l, `INSERT INTO native_sequence DEFAULT VALUES`)
	before := nativeExecute(t, l, `SELECT rowid,name,seq FROM sqlite_sequence;SELECT rowid,id,v,generated,added FROM native_sequence;SELECT rowid,id,v FROM native_sequence_audit`).Sets
	if before[0].Rows[0][2].Text != "1" || before[1].Rows[0][3].Text != "original generated" || len(before[2].Rows) != 1 {
		t.Fatal("preparation changed the first real insert or generated column")
	}
	original = nativeSchemaWriteSnapshot(t, d)
	draft = original.Schema.Table.Clone()
	draft.Definitions = append(draft.Definitions, schemaddl.Definition{Name: "native_sequence_bad", Kind: "trigger", SQL: `CREATE TRIGGER native_sequence_bad AFTER INSERT ON native_sequence BEGIN INSERT INTO missing_sequence_destination VALUES(new.id); END`})
	out := d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: draft})
	retainSchemaWriteOutcome(t, "schema-write-sequence-program-rollback", out)
	if out.State != "rolled_back" || out.Error == nil || out.Receipt == nil {
		t.Fatal("invalid trigger preparation did not abort the original transaction", out.State, out.Error)
	}
	after := nativeExecute(t, l, `SELECT rowid,name,seq FROM sqlite_sequence;SELECT rowid,id,v,generated,added FROM native_sequence;SELECT rowid,id,v FROM native_sequence_audit`).Sets
	for i := range before {
		if !reflect.DeepEqual(before[i].Rows, after[i].Rows) {
			t.Fatal("rejected schema transaction changed sequence, typed row or trigger receipt")
		}
	}
}

func TestNativeRQLiteTransactionPreparationErrorsRetainAmbiguousOriginalReceiptWithoutReplay(t *testing.T) {
	for _, kind := range []string{"middle_query", "terminal_query", "terminal_insert", "terminal_ddl"} {
		t.Run(kind, func(t *testing.T) {
			p := nativeFixture(t, "")
			p.RQLiteOptions.BatchTransaction = true
			s := openNative(t, p, "original-fixture-password")
			observer := lease(t, s, 1)
			nativeExecute(t, observer, `CREATE TABLE native_preparation(id INTEGER PRIMARY KEY)`)
			counter := &heldSchemaWrite{base: s.client.Transport, marker: "unused_schema_marker"}
			s.client.Transport = counter
			source := `INSERT INTO native_preparation VALUES(1);`
			switch kind {
			case "middle_query":
				source += `SELECT * FROM missing_native_preparation;INSERT INTO native_preparation VALUES(2)`
			case "terminal_query":
				source += `SELECT * FROM missing_native_preparation`
			case "terminal_insert":
				source += `INSERT INTO missing_native_preparation VALUES(2)`
			case "terminal_ddl":
				source += `CREATE INDEX missing_native_ix ON missing_native_preparation(id)`
			}
			l := lease(t, s, 2)
			out, err := l.Execute(context.Background(), "main", source)
			retainNativeFileReceipt(t, "transaction-preparation-"+kind+"-native", out.NativeJSON)
			if out.NativeJSON == "" || err == nil || out.AffectedRows != 0 {
				t.Fatal("preparation failure lost the original native error receipt", err)
			}
			legacy := strings.TrimPrefix(s.version, "v") == "8.43.4"
			ambiguous := kind != "middle_query" || legacy
			if ambiguous && (!errors.Is(err, contract.ErrSQLSessionLost) || l.CheckQueryLease() == nil) {
				t.Fatal("ambiguous original native receipt allowed replay", err)
			}
			observed := nativeExecute(t, observer, `SELECT count(*) FROM native_preparation`).Sets[0].Rows[0][0].Text
			want := "0"
			if legacy {
				want = "1"
				if kind == "middle_query" {
					want = "2"
				}
			}
			if observed != want {
				t.Fatal("official native preparation/transaction behavior changed", observed, want)
			}
			writes := counter.allWrites.Load()
			if ambiguous {
				for _, step := range out.Steps {
					if step.State != "uncertain" {
						t.Fatal("unknown native outcome was declared rolled back", step.State)
					}
				}
				if _, err := l.Execute(context.Background(), "main", source); !errors.Is(err, contract.ErrSQLSessionLost) || counter.allWrites.Load() != writes {
					t.Fatal("observation replayed the original ambiguous write", err)
				}
				if _, err := l.Replace(); err != nil || counter.allWrites.Load() != writes {
					t.Fatal("explicit recovery performed SQL", err)
				}
			} else if l.CheckQueryLease() != nil || out.Steps[0].State != "rolled_back" || out.Steps[2].State != "not_executed" {
				t.Fatal("v10 original acknowledged abort was not retained", out)
			}
		})
	}
}
