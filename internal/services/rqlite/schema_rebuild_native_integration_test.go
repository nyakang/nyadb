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
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

type rebuildReceiptMutation struct {
	base              http.RoundTripper
	mode              string
	original, emitted string
	writes            int
	fkCaptureIndex    int
}

func (m *rebuildReceiptMutation) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := m.base.RoundTrip(r)
	if err != nil || r.URL.Path != "/db/execute" {
		return response, err
	}
	m.writes++
	raw, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return nil, err
	}
	m.original = string(raw)
	var payload map[string]json.RawMessage
	if err = json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	var results []map[string]json.RawMessage
	if err = json.Unmarshal(payload["results"], &results); err != nil {
		return nil, err
	}
	for resultIndex, result := range results {
		var columns []string
		_ = json.Unmarshal(result["columns"], &columns)
		if m.mode == "fk-affected" && resultIndex == m.fkCaptureIndex {
			result["rows_affected"] = json.RawMessage("3")
		}
		if len(columns) == 6 && columns[0] == "fk_phase" && strings.HasPrefix(m.mode, "fk-") {
			var rows [][]json.RawMessage
			_ = json.Unmarshal(result["values"], &rows)
			if m.mode == "fk-label" {
				columns[2] = "other_table_hex"
				result["columns"], _ = json.Marshal(columns)
			}
			for i, row := range rows {
				if string(row[0]) != `"before"` {
					continue
				}
				if string(row[1]) != "null" {
					if m.mode == "fk-count" {
						rows[i][1] = json.RawMessage("3")
					}
					continue
				}
				if m.mode == "fk-count" {
					continue
				}
				switch m.mode {
				case "fk-missing":
					rows = append(rows[:i], rows[i+1:]...)
				case "fk-null-rowid":
					rows[i][3] = json.RawMessage("0")
				case "fk-valid-frame":
					rows[i][4] = json.RawMessage(`"6F746865725F706172656E74"`)
				case "fk-duplicate":
					rows = append(rows, append([]json.RawMessage(nil), row...))
				}
				break
			}
			result["values"], _ = json.Marshal(rows)
		}
		if len(columns) == 7 && columns[0] == "identity_phase" && m.mode == "identity-label" {
			columns[3] = "foreign_name_type"
			result["columns"], _ = json.Marshal(columns)
		}
		if len(columns) == 7 && columns[0] == "identity_phase" && m.mode == "valid-frame-change" {
			var rows [][]json.RawMessage
			_ = json.Unmarshal(result["values"], &rows)
			for i, row := range rows {
				if string(row[0]) == `"before"` && string(row[1]) == "null" {
					rows[i][6] = json.RawMessage(`"3939"`)
					break
				}
			}
			result["values"], _ = json.Marshal(rows)
		}
		if len(columns) == 8 && columns[0] == "original_rows" && m.mode == "writer-label" {
			columns[1] = "foreign_writer"
			result["columns"], _ = json.Marshal(columns)
		}
		if len(columns) == 8 && columns[0] == "original_rows" && m.mode == "writer-frame" {
			var rows [][]json.RawMessage
			_ = json.Unmarshal(result["values"], &rows)
			if len(rows) == 1 {
				if string(rows[0][1]) == "1" {
					rows[0][1] = json.RawMessage("0")
				} else {
					rows[0][1] = json.RawMessage("1")
				}
			}
			result["values"], _ = json.Marshal(rows)
		}
		if len(columns) == 8 && columns[0] == "original_rows" && m.mode == "row-count-frame" {
			var rows [][]json.RawMessage
			_ = json.Unmarshal(result["values"], &rows)
			if len(rows) == 1 {
				rows[0][0] = json.RawMessage("2")
			}
			result["values"], _ = json.Marshal(rows)
		}
		if len(columns) == 6 && columns[0] == "type_hex" && m.mode == "missing-implicit-index" {
			var rows [][]json.RawMessage
			_ = json.Unmarshal(result["values"], &rows)
			for i, row := range rows {
				if string(row[5]) == "1" {
					rows = append(rows[:i], rows[i+1:]...)
					break
				}
			}
			result["values"], _ = json.Marshal(rows)
		}
	}
	payload["results"], _ = json.Marshal(results)
	changed, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	m.emitted = string(changed)
	response.Body = io.NopCloser(bytes.NewReader(changed))
	response.ContentLength = int64(len(changed))
	response.Header.Del("Content-Length")
	return response, nil
}

func TestNativeRQLiteSchemaRebuildIncompleteCatalogWriterOrIdentityBlocksReplay(t *testing.T) {
	profile, _ := nativeFixtureConfigured(t, "", "", true)
	s := openNative(t, profile, "original-fixture-password")
	l := lease(t, s, 728)
	for _, mode := range []string{"identity-label", "writer-label", "writer-frame", "row-count-frame", "missing-implicit-index", "valid-frame-change"} {
		t.Run(mode, func(t *testing.T) {
			table := "rebuild_fault_" + strings.ReplaceAll(mode, "-", "_")
			nativeExecute(t, l, "CREATE TABLE "+schemaddl.Quote(table)+"(id INTEGER PRIMARY KEY AUTOINCREMENT,v BLOB UNIQUE);INSERT INTO "+schemaddl.Quote(table)+" VALUES(42,X'00FF')")
			d := reserveSchemaUnit(t, s, "original-rebuild-fault-"+mode, table)
			original := nativeSchemaWriteSnapshot(t, d)
			desired := original.Schema.Table.Clone()
			desired.Clauses[0], desired.Clauses[1] = desired.Clauses[1], desired.Clauses[0]
			r := contract.SchemaDesignRequest{Original: original, Desired: desired}
			base := s.client.Transport
			mutation := &rebuildReceiptMutation{base: base, mode: mode}
			s.client.Transport = mutation
			out := d.Operations().Apply(context.Background(), r)
			s.client.Transport = base
			retainNativeFileReceipt(t, "schema-rebuild-fault-"+mode+"-transport-original", mutation.original)
			retainSchemaWriteOutcome(t, "schema-rebuild-fault-"+mode, out)
			if out.State != "committed" || out.Verified || out.Actual == nil || out.Receipt == nil || out.Receipt.NativeJSON != mutation.emitted || !errors.Is(out.Error, contract.ErrSchemaCommitUncertain) {
				t.Fatal("incomplete original reconstruction receipt was accepted or discarded", out.State, out.Error)
			}
			saved := d.OriginalOutcome()
			_ = nativeSchemaWriteSnapshot(t, d)
			if next := d.Operations().Apply(context.Background(), r); next.State != "not_executed" || mutation.writes != 1 || !reflect.DeepEqual(saved, d.OriginalOutcome()) {
				t.Fatal("read erased incomplete original acknowledgement or replayed reconstruction")
			}
		})
	}
}

func TestNativeRQLiteSchemaRebuildAutoIncrementTransitionsPreserveOtherCompleteSequences(t *testing.T) {
	for _, mode := range []string{"add-first", "add-existing", "remove"} {
		t.Run(mode, func(t *testing.T) {
			profile, _ := nativeFixtureConfigured(t, "", "", true)
			s := openNative(t, profile, "original-fixture-password")
			l := lease(t, s, 729)
			declaration := "INTEGER PRIMARY KEY"
			if mode == "remove" {
				declaration += " AUTOINCREMENT"
			}
			nativeExecute(t, l, "CREATE TABLE target(id "+declaration+",v BLOB);INSERT INTO target VALUES(42,X'00FF')")
			if mode != "add-first" {
				nativeExecute(t, l, "CREATE TABLE other_sequence(id INTEGER PRIMARY KEY AUTOINCREMENT,v BLOB);INSERT INTO other_sequence VALUES(7,X'00AB');UPDATE sqlite_sequence SET seq=5000 WHERE name='other_sequence';INSERT INTO sqlite_sequence(rowid,name,seq) VALUES(33,NULL,X'00FE'),(34,'opaque',CAST(X'80FF' AS TEXT))")
			}
			var before [][]contract.Cell
			if mode != "add-first" {
				before = nativeExecute(t, l, "SELECT rowid,typeof(name),hex(name),typeof(seq),hex(seq) FROM sqlite_sequence WHERE name IS NULL OR name<>'target' ORDER BY rowid").Sets[0].Rows
			}
			d := reserveSchemaUnit(t, s, "original-rebuild-auto-"+mode, "target")
			original := nativeSchemaWriteSnapshot(t, d)
			desired := original.Schema.Table.Clone()
			if mode == "remove" {
				desired.Clauses[0].SQL = "INTEGER PRIMARY KEY"
			} else {
				desired.Clauses[0].SQL = "INTEGER PRIMARY KEY AUTOINCREMENT"
			}
			out := d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: desired})
			requireSchemaCommit(t, "schema-rebuild-auto-"+mode, out)
			if mode != "add-first" && !reflect.DeepEqual(before, nativeExecute(t, l, "SELECT rowid,typeof(name),hex(name),typeof(seq),hex(seq) FROM sqlite_sequence WHERE name IS NULL OR name<>'target' ORDER BY rowid").Sets[0].Rows) {
				t.Fatal("selected identity transition changed an unselected original sequence")
			}
			rows := nativeExecute(t, l, "SELECT seq FROM sqlite_sequence WHERE name='target'").Sets[0].Rows
			if mode == "remove" && len(rows) != 0 || mode != "remove" && (len(rows) != 1 || rows[0][0].Text != "42") {
				t.Fatal("native selected sequence transition did not retain declared semantics", rows)
			}
		})
	}
}

func nativeSchemaRebuildSetup(t *testing.T, s *Session, l *queryLease, c rebuildProtocolCase) {
	t.Helper()
	if c.name == "wide_hidden_rowid" {
		var columns, values []string
		for i := 0; i < 2000; i++ {
			columns = append(columns, fmt.Sprintf("c%04d", i))
			value := "X'00FF'"
			if i > 1 {
				value = []string{"NULL", "9223372036854775807", "1.0000000000000002", "CAST(X'610080FF' AS TEXT)", "X'00FF'"}[i%5]
			}
			values = append(values, value)
		}
		nativeExecute(t, l, "CREATE TABLE target("+strings.Join(columns, ",")+");INSERT INTO target(rowid,"+strings.Join(columns, ",")+") VALUES(-9,"+strings.Join(values, ",")+");INSERT INTO target(rowid,"+strings.Join(columns, ",")+") VALUES(9007199254740993,"+strings.Join(values, ",")+")")
		return
	}
	if strings.Contains(c.name, "cyclic") || c.name == "immediate_cycle_rollback" {
		var sql []any
		for _, statement := range strings.Split(c.setup, ";") {
			if strings.TrimSpace(statement) != "" {
				sql = append(sql, statement)
			}
		}
		r, err := s.request(context.Background(), "/db/execute", sql, true, true)
		retainNativeFileReceipt(t, "schema-rebuild-"+c.name+"-setup", r.Raw)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	nativeExecute(t, l, c.setup)
}

func nativeSchemaRebuildDraft(original contract.SchemaDesignSnapshot, c rebuildProtocolCase) schemaddl.Table {
	desired := original.Schema.Table.Clone()
	desired.Clauses[0], desired.Clauses[1] = desired.Clauses[1], desired.Clauses[0]
	for i, clause := range desired.Clauses {
		if clause.Name == "v" && c.replacement != "" {
			desired.Clauses[i].SQL = c.replacement
		}
		if clause.Name == "r" && c.name == "typed_generated" {
			desired.Clauses[i].SQL = "REAL NOT NULL DEFAULT (1.25)"
		}
		if clause.Name == "v" && c.name == "recursive_cascade" {
			desired.Clauses[i].SQL = "BLOB DEFAULT X'00FA'"
		}
		if clause.Name == "v" && c.name == "duplicates_and_aliases" {
			desired.Clauses[i].SQL = "BLOB COLLATE NOCASE DEFAULT X'00AA'"
		}
		if clause.Name == "id" && c.name == "foreign_key_rollback" {
			desired.Clauses[i].SQL = "INTEGER"
		}
	}
	if c.name == "wide_hidden_rowid" {
		desired.Clauses[0].SQL = "BLOB DEFAULT X'00AB'"
	} else {
		desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:default", Kind: "column", Name: "added", SQL: "BLOB DEFAULT X'00AB'"})
	}
	if !c.rollback {
		check := "CHECK(id>0)"
		switch c.name {
		case "without_rowid":
			check = "CHECK(length(k)>0)"
		case "duplicates_and_aliases":
			check = "CHECK(n>0)"
		case "wide_hidden_rowid":
			check = "CHECK(length(c0000)=2)"
		}
		desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:constraint", Kind: "constraint", SQL: check})
	}
	return desired
}

func TestNativeRQLiteSchemaRebuildOriginalDocumentsRetainTypedDatabaseAndReceipts(t *testing.T) {
	for _, c := range rebuildProtocolCases() {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "immediate_cycle_rollback" {
				c.rollback = false // Native action suspension now settles this cycle.
			}
			profile, _ := nativeFixtureConfigured(t, "", "", true)
			s := openNative(t, profile, "original-fixture-password")
			l := lease(t, s, 726)
			nativeSchemaRebuildSetup(t, s, l, c)
			d := reserveSchemaUnit(t, s, "original-rebuild-"+c.name, "target")
			original := nativeSchemaWriteSnapshot(t, d)
			desired := nativeSchemaRebuildDraft(original, c)
			r := contract.SchemaDesignRequest{Original: original, Desired: desired}
			sequence := d.sequence
			plan, err := d.Operations().Plan(r)
			if err != nil || plan.Mode != "rebuild" || len(plan.SQL) == 0 || d.sequence != sequence || d.OriginalOutcome() != nil {
				t.Fatal("original document has no pure guarded reconstruction preview", plan.Mode, err)
			}
			tables := rebuildProtocolTables(t, s, original)
			before := rebuildProtocolFingerprints(t, l, tables)
			writerBefore := rebuildProtocolWriterState(t, s, "schema-rebuild-"+c.name+"-before")
			out := d.Operations().Apply(context.Background(), r)
			retainSchemaWriteOutcome(t, "schema-rebuild-"+c.name, out)
			if out.Receipt == nil || out.Receipt.Operation != "apply" || out.Receipt.Original.Target != original.Target || out.Receipt.DocumentID != d.id || out.Receipt.Sequence != 1 || !reflect.DeepEqual(out.Receipt.Desired, &desired) {
				t.Fatal("reconstruction lost the original immutable request or document")
			}
			if c.rollback {
				if out.State != "rolled_back" || out.Verified || out.Error == nil || !reflect.DeepEqual(original, nativeSchemaWriteSnapshot(t, d)) {
					t.Fatal("reconstruction failure did not preserve the original snapshot", out.State, out.Error)
				}
				if c.name == "affinity_rollback" && !errors.Is(out.Error, schemaddl.ErrDataChanged) {
					t.Fatal("retained storage-type conversion was not reported explicitly", out.Error)
				}
			} else {
				actual := requireSchemaCommit(t, "schema-rebuild-"+c.name, out)
				if out.Rows < 1 || !schemaddl.SQLiteNativeTableMatches(actual.Schema.Table, desired, "main") {
					t.Fatal("original reconstruction did not retain its final complete schema")
				}
				if strings.Contains(strings.Join(out.Receipt.Statements, ";"), "PRAGMA legacy_alter_table=") || strings.Contains(strings.Join(out.Receipt.Statements, ";"), "ALTER TABLE") {
					t.Fatal("reconstruction changed writer settings or used an unverified shadow rename")
				}
				if c.name != "wide_hidden_rowid" && nativeExecute(t, l, "SELECT count(*) FROM target WHERE typeof(added)<>'blob' OR hex(added)<>'00AB'").Sets[0].Rows[0][0].Text != "0" {
					t.Fatal("new typed default did not populate original rows")
				}
			}
			after := rebuildProtocolFingerprints(t, l, tables)
			writerAfter := rebuildProtocolWriterState(t, s, "schema-rebuild-"+c.name+"-after")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(writerBefore, writerAfter) {
				t.Fatal("original complete per-table values, multiplicity, rowids, sequences or writer state changed")
			}
			saved := d.OriginalOutcome()
			_ = nativeSchemaWriteSnapshot(t, d)
			if !reflect.DeepEqual(saved, d.OriginalOutcome()) {
				t.Fatal("later metadata read replaced original reconstruction acknowledgement")
			}
			proof, err := json.Marshal(struct {
				Before, After             map[string][][]contract.Cell
				WriterBefore, WriterAfter []contract.Cell
				OriginalOutcome           *contract.SchemaDesignOutcome
			}{before, after, writerBefore, writerAfter, saved})
			if err != nil {
				t.Fatal(err)
			}
			retainNativeFileReceipt(t, "schema-rebuild-"+c.name+"-verification", string(proof))
			if c.name == "typed_generated" && !c.rollback {
				nativeExecute(t, l, "UPDATE vv SET v=X'00AC' WHERE id=42;DELETE FROM vv WHERE id=91;INSERT INTO vv(id,v) VALUES(99,X'00AD')")
				if nativeExecute(t, l, "SELECT count(*) FROM audit").Sets[0].Rows[0][0].Text != "1" {
					t.Fatal("restored view or table programs were not preserved")
				}
			}
		})
	}
}

func TestNativeRQLiteSchemaRebuildCancellationCloseAndLostOriginalAcknowledgements(t *testing.T) {
	for _, fixture := range []struct {
		prefix    string
		fk, cycle bool
	}{{"", true, false}, {"fk-off-", false, false}, {"cycle-", true, true}, {"baseline-fk-on-", true, false}, {"baseline-fk-off-", false, false}, {"scope-fk-on-", true, false}, {"scope-fk-off-", false, false}} {
		for _, mode := range []string{"caller", "document", "session", "lost"} {
			label := fixture.prefix + mode
			t.Run(label, func(t *testing.T) {
				profile, _ := nativeFixtureConfigured(t, "", "", fixture.fk)
				s := openNative(t, profile, "original-fixture-password")
				l := lease(t, s, 727)
				if fixture.cycle {
					created, err := s.request(context.Background(), "/db/execute", []any{
						"CREATE TABLE target(id INTEGER PRIMARY KEY AUTOINCREMENT,parent INTEGER NOT NULL REFERENCES child(id) ON DELETE SET NULL,v BLOB)",
						"CREATE TABLE child(id INTEGER PRIMARY KEY,parent INTEGER NOT NULL REFERENCES target(id) ON DELETE SET NULL)",
						"PRAGMA defer_foreign_keys=ON",
						"INSERT INTO target VALUES(42,7,X'00FF')",
						"INSERT INTO child VALUES(7,42)",
						"UPDATE sqlite_sequence SET seq=500 WHERE name='target'",
						"CREATE VIEW vv AS SELECT * FROM target",
					}, true, true)
					retainNativeFileReceipt(t, "schema-rebuild-held-"+label+"-setup", created.Raw)
					if err != nil {
						t.Fatal(err)
					}
				} else {
					nativeExecute(t, l, "CREATE TABLE target(id INTEGER PRIMARY KEY AUTOINCREMENT,v BLOB);INSERT INTO target VALUES(42,X'00FF');UPDATE sqlite_sequence SET seq=500 WHERE name='target';CREATE TABLE child(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES target(id) ON DELETE CASCADE);INSERT INTO child VALUES(7,42);CREATE VIEW vv AS SELECT * FROM target")
				}
				if strings.HasPrefix(fixture.prefix, "baseline-") {
					rebuildBaselineSetup(t, s, fixture.fk, "UPDATE child SET parent=99", "schema-rebuild-held-"+label)
				}
				if strings.HasPrefix(fixture.prefix, "scope-") {
					rebuildBaselineSetup(t, s, fixture.fk, "CREATE TABLE unrelated(rowid TEXT,oid TEXT,_rowid_ TEXT,parent INTEGER REFERENCES absent_parent(id));INSERT INTO unrelated VALUES('r','o','_r',99)", "schema-rebuild-held-"+label)
				}
				d := reserveSchemaUnit(t, s, "original-rebuild-held", "target")
				original := nativeSchemaWriteSnapshot(t, d)
				desired := nativeSchemaRebuildDraft(original, rebuildProtocolCase{name: "held"})
				r := contract.SchemaDesignRequest{Original: original, Desired: desired}
				h := &heldSchemaWrite{base: s.transport, marker: "__nyadb_rebuild_", started: make(chan struct{}), ack: make(chan struct{}), raw: make(chan string, 1), lose: mode == "lost"}
				defer h.release.Do(func() { close(h.ack) })
				s.client.Transport = h
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan contract.SchemaDesignOutcome, 1)
				go func() { done <- d.Operations().Apply(ctx, r) }()
				select {
				case <-h.started:
				case <-time.After(15 * time.Second):
					t.Fatal("original reconstruction was not acknowledged")
				}
				raw := <-h.raw
				retainNativeFileReceipt(t, "schema-rebuild-held-"+label+"-transport-original", raw)
				cancel()
				closed := make(chan struct{})
				if mode == "document" || mode == "session" {
					go func() {
						if mode == "document" {
							_ = d.Close()
						} else {
							_ = s.Close()
						}
						close(closed)
					}()
				}
				select {
				case <-done:
					t.Fatal("cancellation abandoned the original executor acknowledgement")
				case <-closed:
					t.Fatal("close released the original reconstruction executor early")
				case <-time.After(30 * time.Millisecond):
				}
				h.release.Do(func() { close(h.ack) })
				out := <-done
				retainSchemaWriteOutcome(t, "schema-rebuild-held-"+label, out)
				if mode == "lost" {
					if out.State != "uncertain" || out.Verified || out.Receipt == nil || out.Receipt.NativeJSON != "" || !errors.Is(out.Error, contract.ErrSchemaCommitUncertain) {
						t.Fatal("lost original acknowledgement was reclassified", out.State, out.Error)
					}
					saved := d.OriginalOutcome()
					_ = nativeSchemaWriteSnapshot(t, d)
					if next := d.Operations().Apply(context.Background(), r); next.State != "not_executed" || h.requests.Load() != 1 || !reflect.DeepEqual(saved, d.OriginalOutcome()) {
						t.Fatal("read cleared the original write barrier or replayed reconstruction")
					}
				} else {
					requireSchemaCommit(t, "schema-rebuild-held-"+label, out)
					if out.Receipt.NativeJSON != raw || h.requests.Load() != 1 {
						t.Fatal("original acknowledgement bytes changed or reconstruction replayed")
					}
				}
				if mode == "document" || mode == "session" {
					select {
					case <-closed:
					case <-time.After(5 * time.Second):
						t.Fatal("original close did not finish after acknowledgement")
					}
				}
			})
		}
	}
}
