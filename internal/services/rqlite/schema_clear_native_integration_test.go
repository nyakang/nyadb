package rqlite

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestNativeRQLiteSchemaClearNoSequenceWideSchemaAndTriggerIdentityChanges(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 714)
	columns := make([]string, 2000)
	columns[0] = "id INTEGER PRIMARY KEY"
	for i := 1; i < len(columns); i++ {
		columns[i] = fmt.Sprintf("c%d BLOB", i)
	}
	nativeExecute(t, l, "CREATE TABLE clear_wide("+strings.Join(columns, ",")+");INSERT INTO clear_wide(id,c1,c1999) VALUES(9007199254740993,X'00FF',X'FE00')")
	for i := range 200 {
		nativeExecute(t, l, fmt.Sprintf("CREATE INDEX clear_wide_i%d ON clear_wide(c%d)", i, i+1))
	}
	if nativeExecute(t, l, "SELECT count(*) FROM sqlite_schema WHERE name='sqlite_sequence'").Sets[0].Rows[0][0].Text != "0" {
		t.Fatal("no-sequence native case created a sequence table")
	}
	d := reserveSchemaUnit(t, s, "clear-no-global-sequence-wide", "clear_wide")
	original := nativeSchemaWriteSnapshot(t, d)
	out := d.Operations().Clear(context.Background(), contract.SchemaClearRequest{Original: original})
	actual := requireSchemaCommit(t, "schema-clear-wide-no-sequence", out)
	if out.Rows != 1 || len(actual.Schema.Table.Columns()) != 2000 || len(actual.Schema.Table.Definitions) != 200 || !reflect.DeepEqual(original.Schema.SQLite, actual.Schema.SQLite) {
		t.Fatal("wide clear discarded complete schema")
	}
	if nativeExecute(t, l, "SELECT count(*) FROM sqlite_schema WHERE name='sqlite_sequence'").Sets[0].Rows[0][0].Text != "0" {
		t.Fatal("clear manufactured a global identity table")
	}
	nativeExecute(t, l, "CREATE TABLE clear_own_sequence(id INTEGER PRIMARY KEY AUTOINCREMENT,v TEXT);INSERT INTO clear_own_sequence VALUES(42,'original');CREATE TRIGGER clear_own_delete AFTER DELETE ON clear_own_sequence BEGIN UPDATE sqlite_sequence SET seq=777 WHERE name='clear_own_sequence'; END")
	for _, reset := range []bool{false, true} {
		name := fmt.Sprintf("schema-clear-own-sequence-%t", reset)
		if reset {
			nativeExecute(t, l, "INSERT INTO clear_own_sequence VALUES(500,'reset next')")
		}
		x := reserveSchemaUnit(t, s, name, "clear_own_sequence")
		r := contract.SchemaClearRequest{Original: nativeSchemaWriteSnapshot(t, x), ResetIdentity: reset}
		requireSchemaCommit(t, name, x.Operations().Clear(context.Background(), r))
		rows := nativeExecute(t, l, "SELECT seq FROM sqlite_sequence WHERE name='clear_own_sequence'").Sets[0].Rows
		if reset && len(rows) != 0 || !reset && (len(rows) != 1 || rows[0][0].Text != "777") {
			t.Fatal("clear suppressed original trigger identity behavior", rows)
		}
	}
}

func TestNativeRQLiteSchemaClearCompleteIdentityFramesExceedGenericReadCap(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 715)
	nativeExecute(t, l, "CREATE TABLE clear_many_identities(id INTEGER PRIMARY KEY AUTOINCREMENT,v BLOB);INSERT INTO clear_many_identities VALUES(42,X'00FF');WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<100001) INSERT INTO sqlite_sequence(name,seq) SELECT 'unselected_'||x,CASE WHEN x%3=0 THEN NULL WHEN x%3=1 THEN X'00FF' ELSE 1.25 END FROM n")
	d := reserveSchemaUnit(t, s, "clear-complete-identities", "clear_many_identities")
	out := d.Operations().Clear(context.Background(), contract.SchemaClearRequest{Original: nativeSchemaWriteSnapshot(t, d), ResetIdentity: true})
	requireSchemaCommit(t, "schema-clear-complete-identities", out)
	var native receipt
	if json.Unmarshal([]byte(out.Receipt.NativeJSON), &native) != nil {
		t.Fatal("large original identity receipt is incomplete")
	}
	found := false
	for i, result := range native.Results {
		if len(result.Columns) == 7 && result.Columns[0] == "identity_phase" {
			found = true
			if len(result.Values) != 200005 || verifySchemaClearIdentities(native, i) != nil {
				t.Fatal("complete identity capture used capped generic rows", len(result.Values))
			}
		}
	}
	if !found || nativeExecute(t, l, "SELECT count(*) FROM sqlite_sequence").Sets[0].Rows[0][0].Text != "100001" {
		t.Fatal("clear omitted or changed unselected identity frames")
	}
}

type clearReceiptMutation struct {
	base              http.RoundTripper
	mode              string
	original, emitted string
	writes            int
}

func (m *clearReceiptMutation) RoundTrip(r *http.Request) (*http.Response, error) {
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
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	var results []map[string]json.RawMessage
	if err := json.Unmarshal(payload["results"], &results); err != nil {
		return nil, err
	}
	for _, result := range results {
		var columns []string
		_ = json.Unmarshal(result["columns"], &columns)
		if len(columns) != 7 || columns[0] != "identity_phase" {
			continue
		}
		var values [][]json.RawMessage
		_ = json.Unmarshal(result["values"], &values)
		switch m.mode {
		case "missing-row":
			values = values[:len(values)-1]
		case "missing-marker":
			for i, row := range values {
				if string(row[1]) != "null" {
					values = append(values[:i], values[i+1:]...)
					break
				}
			}
		case "duplicate-rowid":
			for i, row := range values {
				if string(row[1]) == "null" {
					values = append(values, values[i])
					break
				}
			}
		case "invalid-hex":
			for i, row := range values {
				if string(row[1]) == "null" {
					values[i][4] = json.RawMessage(`"not hex"`)
					break
				}
			}
		case "column-label":
			columns[0] = "foreign_phase"
			result["columns"], _ = json.Marshal(columns)
		}
		result["values"], _ = json.Marshal(values)
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

func TestNativeRQLiteSchemaClearRejectsIncompleteOriginalIdentityReceiptWithoutReplay(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 716)
	for _, mode := range []string{"missing-row", "missing-marker", "duplicate-rowid", "invalid-hex", "column-label"} {
		t.Run(mode, func(t *testing.T) {
			table := "clear_fault_" + strings.ReplaceAll(mode, "-", "_")
			nativeExecute(t, l, "CREATE TABLE "+schemaddl.Quote(table)+"(id INTEGER PRIMARY KEY AUTOINCREMENT,v BLOB);INSERT INTO "+schemaddl.Quote(table)+" VALUES(42,X'00FF')")
			d := reserveSchemaUnit(t, s, "clear-identity-fault-"+mode, table)
			request := contract.SchemaClearRequest{Original: nativeSchemaWriteSnapshot(t, d)}
			base := s.client.Transport
			mutated := &clearReceiptMutation{base: base, mode: mode}
			s.client.Transport = mutated
			out := d.Operations().Clear(context.Background(), request)
			s.client.Transport = base
			retainNativeFileReceipt(t, "schema-clear-identity-fault-"+mode+"-transport-original", mutated.original)
			retainSchemaWriteOutcome(t, "schema-clear-identity-fault-"+mode, out)
			if out.State != "committed" || out.Verified || out.Error == nil || out.Receipt == nil || out.Receipt.NativeJSON != mutated.emitted {
				t.Fatal("incomplete controlled identity acknowledgement accepted or lost", mode, out.State, out.Error)
			}
			if nativeExecute(t, l, "SELECT count(*) FROM "+schemaddl.Quote(table)).Sets[0].Rows[0][0].Text != "0" {
				t.Fatal("original native transaction did not commit before receipt fault")
			}
			retained := d.OriginalOutcome()
			_ = nativeSchemaWriteSnapshot(t, d)
			next := d.Operations().Clear(context.Background(), request)
			if next.State != "not_executed" || mutated.writes != 1 || !reflect.DeepEqual(retained, d.OriginalOutcome()) {
				t.Fatal("later read reconstructed or replayed clear")
			}
		})
	}
}

func TestNativeRQLiteSchemaClearReadOnlyQueryOnlyAndTemporaryWriterGuard(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 717)
	nativeExecute(t, l, "CREATE TABLE clear_permissions(id INTEGER PRIMARY KEY,v BLOB);INSERT INTO clear_permissions VALUES(42,X'00FF')")
	for _, mode := range []string{"read-only", "query-only", "temporary-writer"} {
		t.Run(mode, func(t *testing.T) {
			x := s
			if mode != "temporary-writer" {
				profile := p
				profile.ID = 82
				if mode == "read-only" {
					profile.RQLiteOptions.ReadOnly = true
					x = openNative(t, profile, "original-fixture-password")
				} else {
					profile.Username = "reader"
					x = openNative(t, profile, "reader-password")
				}
			}
			d := reserveSchemaUnit(t, x, "clear-permissions-"+mode, "clear_permissions")
			original := nativeSchemaWriteSnapshot(t, d)
			if mode == "temporary-writer" {
				nativeExecute(t, l, "CREATE TEMP TRIGGER clear_temp_program AFTER DELETE ON main.clear_permissions BEGIN SELECT 1; END")
			}
			run := d.Operations().Clear
			if mode == "read-only" {
				if run != nil || d.Operations().CanClear != nil || x.SchemaDesignDocumentFeatures().CanClear != nil {
					t.Fatal("read-only original document exposed clear")
				}
				run = d.clear // Even bypassing the public binding cannot dispatch.
			}
			out := run(context.Background(), contract.SchemaClearRequest{Original: original})
			if mode == "temporary-writer" {
				nativeExecute(t, l, "DROP TRIGGER temp.clear_temp_program")
			}
			retainSchemaWriteOutcome(t, "schema-clear-permissions-"+mode, out)
			if out.Error == nil || out.Verified || mode == "temporary-writer" && out.State != "rolled_back" || mode != "temporary-writer" && out.State != "not_executed" {
				t.Fatal("clear admitted forbidden writer", mode, out.State, out.Error)
			}
			if mode == "query-only" && (out.Receipt == nil || out.Receipt.NativeJSON == "") {
				t.Fatal("query-only clear lost original HTTP rejection")
			}
			if nativeExecute(t, l, "SELECT count(*) FROM clear_permissions").Sets[0].Rows[0][0].Text != "1" || nativeExecute(t, l, "SELECT count(*) FROM temp.sqlite_schema WHERE type='trigger'").Sets[0].Rows[0][0].Text != "0" {
				t.Fatal("forbidden clear changed rows or left a TEMP program")
			}
		})
	}
}

func TestNativeRQLiteSchemaClearRetainsTableProgramsIdentityFramesAndDeclaredEffects(t *testing.T) {
	p, _ := nativeFixtureConfigured(t, "", "", true)
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 711)
	nativeExecute(t, l, "CREATE TABLE unrelated(id INTEGER PRIMARY KEY AUTOINCREMENT,v TEXT);INSERT INTO unrelated VALUES(99,'keep');CREATE TABLE audit(id INTEGER);CREATE TABLE triggered_identity(id INTEGER PRIMARY KEY AUTOINCREMENT,v BLOB)")
	for _, c := range []struct {
		name, setup string
		reset       bool
		rows        int64
		sequence    string
	}{
		{"clear_keep", `CREATE TABLE clear_keep(id INTEGER PRIMARY KEY AUTOINCREMENT,v ANY,g TEXT GENERATED ALWAYS AS (hex(v)) STORED) STRICT;INSERT INTO clear_keep(id,v) VALUES(9007199254740993,X'00FF'),(9223372036854775807,CAST(X'610062' AS TEXT)),(3,CAST(X'80FF' AS TEXT));CREATE INDEX clear_keep_v ON clear_keep(v);CREATE TRIGGER clear_keep_delete AFTER DELETE ON clear_keep BEGIN INSERT INTO audit VALUES(old.id);INSERT INTO triggered_identity(v) VALUES(CAST(old.g AS BLOB)); END`, false, 3, "9223372036854775807"},
		{"clear_reset", `CREATE TABLE clear_reset(id INTEGER PRIMARY KEY AUTOINCREMENT,v BLOB);INSERT INTO clear_reset VALUES(42,X'00FF'),(200,X'FE00')`, true, 2, ""},
		{"clear_empty_keep", `CREATE TABLE clear_empty_keep(id INTEGER PRIMARY KEY AUTOINCREMENT,v TEXT);INSERT INTO clear_empty_keep VALUES(42,'deleted');DELETE FROM clear_empty_keep`, false, 0, "42"},
		{"clear_empty_reset", `CREATE TABLE clear_empty_reset(id INTEGER PRIMARY KEY AUTOINCREMENT,v TEXT);INSERT INTO clear_empty_reset VALUES(42,'deleted');DELETE FROM clear_empty_reset`, true, 0, ""},
		{"clear_absent", `CREATE TABLE clear_absent(id INTEGER PRIMARY KEY AUTOINCREMENT,v TEXT)`, false, 0, ""},
		{"clear_shadowed", `CREATE TABLE clear_shadowed(rowid TEXT,_rowid_ TEXT,oid TEXT,id INTEGER PRIMARY KEY DESC);INSERT INTO clear_shadowed VALUES('r','_r','o',42)`, false, 1, ""},
		{"clear_without_rowid", `CREATE TABLE clear_without_rowid(k TEXT COLLATE NOCASE,v BLOB,PRIMARY KEY(k)) WITHOUT ROWID;INSERT INTO clear_without_rowid VALUES('A',X'00FF'),('B',X'FE00')`, false, 2, ""},
		{"clear_cascade", `CREATE TABLE clear_cascade(id INTEGER PRIMARY KEY);INSERT INTO clear_cascade VALUES(1);CREATE TABLE clear_cascade_child(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES clear_cascade(id) ON DELETE CASCADE);INSERT INTO clear_cascade_child VALUES(2,1)`, false, 1, ""},
		{"clear_no_sequence", `CREATE TABLE clear_no_sequence(id INTEGER PRIMARY KEY,v TEXT);INSERT INTO clear_no_sequence VALUES(7,'native')`, false, 1, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			nativeExecute(t, l, c.setup)
			d := reserveSchemaUnit(t, s, "original-"+c.name, c.name)
			original := nativeSchemaWriteSnapshot(t, d)
			unrelated := nativeExecute(t, l, "SELECT rowid,typeof(name),hex(name),typeof(seq),hex(seq) FROM sqlite_sequence WHERE name='unrelated'").Sets[0].Rows
			request := contract.SchemaClearRequest{Original: original, ResetIdentity: c.reset}
			out := d.Operations().Clear(context.Background(), request)
			actual := requireSchemaCommit(t, "schema-clear-"+c.name, out)
			if out.Rows != c.rows || out.Receipt.Operation != "clear" || out.Receipt.ResetIdentity != c.reset || out.Receipt.Original.Target != original.Target || actual.Target != original.Target || !actual.Schema.Exists || d.target != original.Target || !reflect.DeepEqual(original.Schema.SQLite, actual.Schema.SQLite) || schemaddl.Signature(original.Schema.Table) != schemaddl.Signature(actual.Schema.Table) {
				t.Fatal("clear changed original table/catalog or immutable receipt", out.State, out.Error)
			}
			if nativeExecute(t, l, "SELECT count(*) FROM main."+schemaddl.Quote(c.name)).Sets[0].Rows[0][0].Text != "0" {
				t.Fatal("native source still has rows")
			}
			sequence := nativeExecute(t, l, "SELECT seq FROM sqlite_sequence WHERE name="+schemaTextLiteral(c.name)).Sets[0].Rows
			if c.sequence == "" && len(sequence) != 0 || c.sequence != "" && (len(sequence) != 1 || sequence[0][0].Text != c.sequence) {
				t.Fatal("clear changed captured sequence policy", sequence)
			}
			if !reflect.DeepEqual(unrelated, nativeExecute(t, l, "SELECT rowid,typeof(name),hex(name),typeof(seq),hex(seq) FROM sqlite_sequence WHERE name='unrelated'").Sets[0].Rows) {
				t.Fatal("clear changed unrelated sequence identity")
			}
			var native struct {
				Results []struct {
					Columns []string
					Values  [][]any
				}
			}
			if json.Unmarshal([]byte(out.Receipt.NativeJSON), &native) != nil {
				t.Fatal("original native clear receipt is incomplete")
			}
			found := false
			for _, result := range native.Results {
				if len(result.Columns) == 7 && result.Columns[0] == "identity_phase" {
					found = true
					if len(result.Values) < 2 {
						t.Fatal("before/after original identity markers omitted")
					}
				}
			}
			if !found {
				t.Fatal("original transaction did not retain complete identity frames")
			}
			retained := d.OriginalOutcome()
			_ = nativeSchemaWriteSnapshot(t, d)
			if !reflect.DeepEqual(retained, d.OriginalOutcome()) {
				t.Fatal("clear read replaced its original write acknowledgement")
			}
			if c.name == "clear_keep" {
				if nativeExecute(t, l, "SELECT count(*) FROM audit").Sets[0].Rows[0][0].Text != "3" || nativeExecute(t, l, "SELECT count(*) FROM triggered_identity").Sets[0].Rows[0][0].Text != "3" {
					t.Fatal("clear skipped declared DELETE effects")
				}
			}
			if c.name == "clear_cascade" && nativeExecute(t, l, "SELECT count(*) FROM clear_cascade_child").Sets[0].Rows[0][0].Text != "0" {
				t.Fatal("clear suppressed declared foreign-key cascade")
			}
			if c.name == "clear_reset" {
				nativeExecute(t, l, "INSERT INTO clear_reset(v) VALUES(X'12')")
				if nativeExecute(t, l, "SELECT id FROM clear_reset").Sets[0].Rows[0][0].Text != "1" {
					t.Fatal("explicit sequence reset did not affect next generated identity")
				}
			}
		})
	}
}

func TestNativeRQLiteSchemaClearConflictsRollbackRowsProgramsSequencesAndTemporaryState(t *testing.T) {
	p, _ := nativeFixtureConfigured(t, "", "", true)
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 712)
	for _, mode := range []string{"restrict", "reinsert", "duplicate-sequence", "invalid-sequence", "late-catalog", "invalid-reset"} {
		t.Run(mode, func(t *testing.T) {
			table := "clear_reject_" + strings.ReplaceAll(mode, "-", "_")
			definition := "id INTEGER PRIMARY KEY AUTOINCREMENT,v BLOB"
			if mode == "invalid-reset" {
				definition = "id INTEGER PRIMARY KEY,v BLOB"
			}
			nativeExecute(t, l, "CREATE TABLE "+schemaddl.Quote(table)+"("+definition+");INSERT INTO "+schemaddl.Quote(table)+" VALUES(42,X'00FF')")
			if mode == "restrict" {
				nativeExecute(t, l, "CREATE TABLE clear_restrict_child(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES "+schemaddl.Quote(table)+"(id));INSERT INTO clear_restrict_child VALUES(1,42)")
			}
			if mode == "reinsert" {
				nativeExecute(t, l, "CREATE TRIGGER restore_clear_deleted AFTER DELETE ON "+schemaddl.Quote(table)+" BEGIN INSERT INTO "+schemaddl.Quote(table)+"(v) VALUES(X'FE00'); END")
			}
			if mode == "duplicate-sequence" {
				nativeExecute(t, l, "INSERT INTO sqlite_sequence(name,seq) VALUES("+schemaTextLiteral(table)+",99)")
			}
			if mode == "invalid-sequence" {
				nativeExecute(t, l, "UPDATE sqlite_sequence SET seq='invalid' WHERE name="+schemaTextLiteral(table))
			}
			d := reserveSchemaUnit(t, s, "clear-reject-"+mode, table)
			original := nativeSchemaWriteSnapshot(t, d)
			if mode == "late-catalog" {
				nativeExecute(t, l, "CREATE TABLE clear_late_catalog(v TEXT)")
			}
			values := nativeExecute(t, l, "SELECT id,typeof(v),hex(v) FROM "+schemaddl.Quote(table)).Sets[0].Rows
			identities := nativeExecute(t, l, "SELECT rowid,typeof(name),hex(name),typeof(seq),hex(seq) FROM sqlite_sequence ORDER BY rowid").Sets[0].Rows
			out := d.Operations().Clear(context.Background(), contract.SchemaClearRequest{Original: original, ResetIdentity: true})
			retainSchemaWriteOutcome(t, "schema-clear-rejected-"+mode, out)
			if out.Verified || out.Error == nil || mode == "invalid-reset" && out.State != "not_executed" || mode != "invalid-reset" && out.State != "rolled_back" {
				t.Fatal("clear admitted original conflict", out.State, out.Error)
			}
			if !reflect.DeepEqual(values, nativeExecute(t, l, "SELECT id,typeof(v),hex(v) FROM "+schemaddl.Quote(table)).Sets[0].Rows) || !reflect.DeepEqual(identities, nativeExecute(t, l, "SELECT rowid,typeof(name),hex(name),typeof(seq),hex(seq) FROM sqlite_sequence ORDER BY rowid").Sets[0].Rows) {
				t.Fatal("failed clear changed original data or sequence frames")
			}
			if nativeExecute(t, l, "SELECT count(*) FROM temp.sqlite_schema WHERE name LIKE '__nyadb_schema_%'").Sets[0].Rows[0][0].Text != "0" {
				t.Fatal("clear rollback retained private writer state")
			}
		})
	}
}

func TestNativeRQLiteSchemaClearCancellationCloseAndLostReceiptWaitOriginalExecutor(t *testing.T) {
	for _, mode := range []string{"caller", "document", "session", "lost"} {
		t.Run(mode, func(t *testing.T) {
			p := nativeFixture(t, "")
			s := openNative(t, p, "original-fixture-password")
			l := lease(t, s, 713)
			nativeExecute(t, l, "CREATE TABLE clear_held(id INTEGER PRIMARY KEY AUTOINCREMENT,v BLOB);INSERT INTO clear_held VALUES(42,X'00FF')")
			d := reserveSchemaUnit(t, s, "original-clear-held", "clear_held")
			original := nativeSchemaWriteSnapshot(t, d)
			h := &heldSchemaWrite{base: s.transport, marker: "clear_held", started: make(chan struct{}), ack: make(chan struct{}), raw: make(chan string, 1), lose: mode == "lost"}
			defer h.release.Do(func() { close(h.ack) })
			s.client.Transport = h
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := contract.SchemaClearRequest{Original: original, ResetIdentity: true}
			done := make(chan contract.SchemaDesignOutcome, 1)
			go func() { done <- d.Operations().Clear(ctx, request) }()
			select {
			case <-h.started:
			case <-time.After(10 * time.Second):
				t.Fatal("original clear writer did not acknowledge")
			}
			raw := <-h.raw
			retainNativeFileReceipt(t, "schema-clear-held-"+mode+"-transport-original", raw)
			cancel()
			closed := make(chan struct{})
			if mode == "document" || mode == "session" {
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
				t.Fatal("clear cancellation abandoned original acknowledgement")
			case <-closed:
				t.Fatal("clear close abandoned original executor")
			case <-time.After(30 * time.Millisecond):
			}
			h.release.Do(func() { close(h.ack) })
			var out contract.SchemaDesignOutcome
			select {
			case out = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("original clear did not settle")
			}
			retainSchemaWriteOutcome(t, "schema-clear-held-"+mode, out)
			if out.Receipt == nil || out.Receipt.Original.Target != original.Target || !out.Receipt.ResetIdentity || d.target != original.Target {
				t.Fatal("clear lost original request/target")
			}
			if mode == "lost" {
				if out.State != "uncertain" || out.Verified || out.Receipt.NativeJSON != "" {
					t.Fatal("lost clear receipt reconstructed from a later read")
				}
				retained := d.OriginalOutcome()
				_ = nativeSchemaWriteSnapshot(t, d)
				request.ResetIdentity = false
				next := d.Operations().Clear(context.Background(), request)
				if next.State != "not_executed" || h.requests.Load() != 1 || !reflect.DeepEqual(retained, d.OriginalOutcome()) {
					t.Fatal("clear read erased unknown evidence or replayed write")
				}
			} else {
				requireSchemaCommit(t, "schema-clear-held-"+mode, out)
				if out.Receipt.NativeJSON != raw || h.requests.Load() != 1 {
					t.Fatal("clear changed original bytes or replayed")
				}
			}
			if mode == "document" || mode == "session" {
				select {
				case <-closed:
				case <-time.After(5 * time.Second):
					t.Fatal("clear close did not wait for executor")
				}
				if d.OriginalOutcome() != nil {
					t.Fatal("clear terminal close retained receipts")
				}
			}
		})
	}
}
