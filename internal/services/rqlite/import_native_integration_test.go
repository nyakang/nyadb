package rqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeImportDocument(t *testing.T, s *Session, name string) *dataDocument {
	t.Helper()
	l, err := s.ReserveDataDocument(contract.ObjectRef{ProfileID: s.ProfileID(), Catalog: "main", Kind: "table", Name: name})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.(*dataDocument)
}

func TestNativeRQLiteImportRowRecordFailureRetainsCompleteAcknowledgedBatch(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	nativeExecute(t, lease(t, s, 1), "CREATE TABLE import_record_failure(id INTEGER PRIMARY KEY,v TEXT)")
	d := nativeImportDocument(t, s, "import_record_failure")
	path := filepath.Join(t.TempDir(), "read-only-results.jsonl")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	edits := make([]contract.DataEdit, 1300)
	for i := range edits {
		edits[i] = nativeImportEdit(i+1, "original")
	}
	calls := 0
	out, err := d.importStream(context.Background(), p.ID, "main", d.object.Name, contract.DataImportStreamRequest{
		Rows: int64(len(edits)),
		Open: func(context.Context) (contract.DataImportRowSource, error) {
			return &importMemoryRows{rows: edits}, nil
		},
		RecordReceipt: func(contract.DataImportBatchReceipt) error { return nil },
		Record: func(row contract.DataImportRowResult) error {
			calls++
			if row.Row == 1001 {
				_, e := f.Write([]byte("original row"))
				return e
			}
			return nil
		},
	})
	if err == nil || out.Committed || out.CommitUncertain || out.CommittedRows != 1024 || out.Progress.Processed != 1024 || calls != 1001 || len(out.Rows) != 1000 {
		t.Fatalf("row-record failure erased acknowledged batch projection: committed=%d processed=%d callbacks=%d sample=%d uncertain=%v err=%v", out.CommittedRows, out.Progress.Processed, calls, len(out.Rows), out.CommitUncertain, err)
	}
	encoded, e := json.Marshal(out.LastReceipt)
	if e != nil {
		t.Fatal(e)
	}
	var projected struct {
		Rows []contract.DataImportRowResult
	}
	if e = json.Unmarshal(encoded, &projected); e != nil {
		t.Fatal(e)
	}
	if len(projected.Rows) != 64 {
		t.Fatal("last original receipt lost its complete batch after diagnostic sample filled")
	}
	for i, row := range projected.Rows {
		if row.Row != 961+i || row.Status != "committed" || row.Outcome == nil || row.Outcome.GeneratedID != int64(961+i) {
			t.Fatal("original complete row outcome changed", row.Row)
		}
	}
	if nativeImportRows(t, s, "SELECT count(*) FROM import_record_failure")[0][0].Text != "1024" {
		t.Fatal("row-record failure sent future rows or replayed original batch")
	}
}

func TestNativeRQLiteImportCallbacksCannotMutateOriginalAcknowledgedRows(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	nativeExecute(t, lease(t, s, 1), "CREATE TABLE import_owned_receipt(id INTEGER PRIMARY KEY,v TEXT)")
	d := nativeImportDocument(t, s, "import_owned_receipt")
	out, e := d.importStream(context.Background(), p.ID, "main", d.object.Name, contract.DataImportStreamRequest{Rows: 1, Open: func(context.Context) (contract.DataImportRowSource, error) {
		return &importMemoryRows{rows: []contract.DataEdit{nativeImportEdit(1, "owned")}}, nil
	}, RecordReceipt: func(r contract.DataImportBatchReceipt) error {
		if len(r.Rows) != 1 || r.Rows[0].Outcome.GeneratedID != 1 {
			return errors.New("original receipt projection missing")
		}
		r.Rows[0].Row = -1
		r.Rows[0].Outcome.GeneratedID = -99
		return nil
	}, Record: func(row contract.DataImportRowResult) error {
		if row.Row != 1 || row.Outcome.GeneratedID != 1 {
			return errors.New("receipt callback changed row projection")
		}
		row.Outcome.GeneratedID = -42
		return nil
	}})
	if e != nil || !out.Committed || out.Rows[0].Outcome.GeneratedID != 1 || out.LastReceipt.Rows[0].Row != 1 || out.LastReceipt.Rows[0].Outcome.GeneratedID != 1 {
		t.Fatal("callbacks changed original native outcome", e)
	}
}

func TestNativeRQLiteImportUnknownBlocksOriginalWritesAndRecoveryNeverReplays(t *testing.T) {
	for _, mode := range []string{"lost", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			p := nativeFixture(t, "")
			original := openNative(t, p, "original-fixture-password")
			nativeExecute(t, lease(t, original, 1), "CREATE TABLE import_unknown(id INTEGER PRIMARY KEY,v TEXT)")
			proxy := proxyNativeData(t, p, mode)
			proxied := testProfile(t, proxy.server.URL)
			proxied.ID = p.ID
			proxied.Username = "fixture"
			proxied.RQLiteOptions.Auth = "basic"
			s := openNative(t, proxied, "original-fixture-password")
			d := nativeImportDocument(t, s, "import_unknown")
			out, rows, e := nativeImportStream(d, []contract.DataEdit{nativeImportEdit(1, "applied once")}, contract.DataImportRequest{}, nil, nil)
			if !errors.Is(e, contract.ErrDataCommitUncertain) || !out.CommitUncertain || out.CommittedRows != 0 || len(rows) != 1 || rows[0].Status != "uncertain" || out.LastReceipt.State != "uncertain" {
				t.Fatal("lost applied batch was acknowledged or rolled back", e)
			}
			if _, _, e = nativeImportStream(d, []contract.DataEdit{nativeImportEdit(2, "must not run")}, contract.DataImportRequest{}, nil, nil); !errors.Is(e, contract.ErrDataCommitUncertain) || proxy.writes.Load() != 1 {
				t.Fatal("unknown import was replayed", e)
			}
			if _, e = d.Runtime().Saver(context.Background(), p.ID, "main", d.object.Name, []contract.DataEdit{nativeImportEdit(2, "must not run")}); !errors.Is(e, contract.ErrDataCommitUncertain) || proxy.writes.Load() != 1 {
				t.Fatal("save bypassed unknown original import", e)
			}
			page, e := d.Runtime().Loader(context.Background(), p.ID, "main", d.object.Name, 0, 2)
			if e != nil || len(page.Rows) != 1 || page.Rows[0][1].Text != "applied once" {
				t.Fatal("original explicit read failed", e)
			}
			if _, _, e = nativeImportStream(d, []contract.DataEdit{nativeImportEdit(2, "must not run")}, contract.DataImportRequest{}, nil, nil); !errors.Is(e, contract.ErrDataCommitUncertain) {
				t.Fatal("fresh read settled original unknown import")
			}
			if actual := nativeImportRows(t, original, "SELECT count(*) FROM import_unknown")[0][0].Text; actual != "1" {
				t.Fatal("original batch replayed")
			}
			if e = d.Close(); e != nil {
				t.Fatal(e)
			}
			if _, _, e = nativeImportStream(d, []contract.DataEdit{nativeImportEdit(2, "must not run")}, contract.DataImportRequest{}, nil, nil); !errors.Is(e, contract.ErrRevoked) {
				t.Fatal("closed import lease executed", e)
			}
			fresh := nativeImportDocument(t, original, "import_unknown")
			if page, e = fresh.Runtime().Loader(context.Background(), p.ID, "main", fresh.object.Name, 0, 2); e != nil || len(page.Rows) != 1 || proxy.writes.Load() != 1 {
				t.Fatal("explicit original-profile recovery replayed import", e)
			}
		})
	}
}

func TestNativeRQLiteImportCancelAndCloseWaitForOriginalAcknowledgement(t *testing.T) {
	p := nativeFixture(t, "")
	original := openNative(t, p, "original-fixture-password")
	nativeExecute(t, lease(t, original, 1), "CREATE TABLE import_ack(id INTEGER PRIMARY KEY,v TEXT)")
	proxy := proxyNativeData(t, p, "held")
	q := testProfile(t, proxy.server.URL)
	q.ID = p.ID
	q.Username = "fixture"
	q.RQLiteOptions.Auth = "basic"
	s := openNative(t, q, "original-fixture-password")
	d := nativeImportDocument(t, s, "import_ack")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	result := make(chan *contract.DataImportStreamResult, 1)
	go func() {
		out, e := d.importStream(ctx, p.ID, "main", d.object.Name, contract.DataImportStreamRequest{Rows: 1, Open: func(context.Context) (contract.DataImportRowSource, error) {
			return &importMemoryRows{rows: []contract.DataEdit{nativeImportEdit(1, "v")}}, nil
		}, RecordReceipt: func(contract.DataImportBatchReceipt) error { return nil }})
		result <- out
		done <- e
	}()
	select {
	case <-proxy.started:
	case <-time.After(5 * time.Second):
		t.Fatal("original native batch did not reach executor")
	}
	cancel()
	closed := make(chan error, 1)
	go func() { closed <- d.Close() }()
	select {
	case <-done:
		t.Fatal("cancel settled before original acknowledgement")
	case <-closed:
		t.Fatal("close released original unacknowledged batch")
	case <-time.After(30 * time.Millisecond):
	}
	close(proxy.ack)
	select {
	case e := <-done:
		if e != nil {
			t.Fatal("complete original last-batch acknowledgement lost", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native import acknowledgement stalled")
	}
	select {
	case e := <-closed:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original import close stalled")
	}
	out := <-result
	if !out.Committed || out.CommitUncertain || out.CommittedRows != 1 || proxy.writes.Load() != 1 {
		t.Fatal("cancel/close changed original native commit")
	}
}

func TestNativeRQLiteImportAdmissionAndForeignKeysRemainOriginal(t *testing.T) {
	p, _ := nativeFixtureConfigured(t, "", "", true)
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	nativeExecute(t, l, `CREATE TABLE import_parent(id INTEGER PRIMARY KEY,v TEXT);CREATE TABLE import_child(id INTEGER PRIMARY KEY,v TEXT,parent INTEGER REFERENCES import_parent(id) ON DELETE CASCADE);INSERT INTO import_parent VALUES(1,'original');INSERT INTO import_child VALUES(1,'child',1);`)
	d := nativeImportDocument(t, s, "import_parent")
	for _, test := range []struct {
		id         int64
		db         string
		generation uint64
		receipt    bool
		want       error
	}{{p.ID + 1, "main", s.Generation(), true, connections.ErrInvalid}, {p.ID, "foreign", s.Generation(), true, connections.ErrInvalid}, {p.ID, "main", s.Generation() + 1, true, contract.ErrRevoked}, {p.ID, "main", s.Generation(), false, contract.ErrDataPage}} {
		opened := false
		r := contract.DataImportStreamRequest{Rows: 1, Generation: test.generation, Open: func(context.Context) (contract.DataImportRowSource, error) {
			opened = true
			return &importMemoryRows{rows: []contract.DataEdit{nativeImportEdit(2, "v")}}, nil
		}}
		if test.receipt {
			r.RecordReceipt = func(contract.DataImportBatchReceipt) error { return nil }
		}
		if _, e := d.importStream(context.Background(), test.id, test.db, d.object.Name, r); !errors.Is(e, test.want) || opened {
			t.Fatal("invalid original admission opened source", e)
		}
	}
	child := nativeImportDocument(t, s, "import_child")
	edit := nativeImportEdit(2, "bad")
	edit.Values["parent"] = contract.DataValue{Kind: contract.Integer, Text: "999"}
	out, e := child.importData(context.Background(), p.ID, "main", child.object.Name, contract.DataImportRequest{Edits: []contract.DataEdit{edit}})
	if e == nil || out.CommitUncertain || out.CommittedRows != 0 || out.Receipts[0].State != "rolled_back" {
		t.Fatal("native FK violation bypassed or uncertain")
	}
	out, e = d.importData(context.Background(), p.ID, "main", d.object.Name, contract.DataImportRequest{Mode: "replace", Edits: []contract.DataEdit{nativeImportEdit(2, "replace")}})
	if e != nil || !out.Committed || nativeImportRows(t, s, "SELECT count(*) FROM import_child")[0][0].Text != "0" {
		t.Fatal("replace did not retain native FK cascade semantics", e)
	}
}
func nativeImportEdit(id int, value string) contract.DataEdit {
	return contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: fmt.Sprint(id)}, "v": {Kind: contract.Text, Text: value}}}
}
func nativeImportRows(t *testing.T, s *Session, sql string) [][]contract.Cell {
	t.Helper()
	r, e := s.request(context.Background(), "/db/query", []any{sql}, true, false)
	if e != nil {
		t.Fatal(e)
	}
	rows, e := decodedRows(r, 0, len(r.Results[0].Columns))
	if e != nil {
		t.Fatal(e)
	}
	return rows
}
func TestNativeRQLiteImportAllModesOriginalAtomicReceipts(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	if r := s.DataImports(); r.Importer != nil || r.StreamImporter != nil || r.NewTableImporter != nil {
		t.Fatal("session exposes writes outside original document")
	}
	for _, mode := range []string{"append", "update", "upsert", "insert_missing", "delete", "replace"} {
		t.Run(mode, func(t *testing.T) {
			name := "import_" + mode
			nativeExecute(t, l, "CREATE TABLE "+quote(name)+"(id INTEGER PRIMARY KEY,v TEXT NOT NULL,omitted TEXT DEFAULT 'native-default'); INSERT INTO "+quote(name)+"(id,v) VALUES(1,'original');")
			d := nativeImportDocument(t, s, name)
			r := contract.DataImportRequest{Mode: mode, Edits: []contract.DataEdit{nativeImportEdit(2, "new")}}
			if mode != "append" && mode != "replace" {
				r.MatchColumns = []string{"id"}
				r.Edits = append([]contract.DataEdit{nativeImportEdit(1, "updated")}, r.Edits...)
			}
			out, e := d.Runtime().Importer(context.Background(), p.ID, "main", name, r)
			if e != nil || out == nil || !out.Committed || out.CommitUncertain || len(out.Receipts) != 1 || !out.Incremental {
				t.Fatalf("native mode not acknowledged: %+v %v", out, e)
			}
			if receipt := out.Receipts[0]; receipt.State != "committed" || receipt.NativeJSON == "" || receipt.Object != d.object || receipt.Generation != s.Generation() || receipt.Sequence != 1 || receipt.FirstRow != 1 || receipt.LastRow != int64(len(r.Edits)) {
				t.Fatal("original native receipt identity/range missing")
			}
			rows := nativeImportRows(t, s, "SELECT id,v,omitted FROM "+quote(name)+" ORDER BY id")
			switch mode {
			case "append":
				if len(rows) != 2 || rows[0][1].Text != "original" || rows[1][1].Text != "new" || out.CommittedRows != 1 {
					t.Fatal("append/default semantics changed")
				}
			case "update":
				if len(rows) != 1 || rows[0][1].Text != "updated" || out.Rows[1].Status != "skipped" || out.CommittedRows != 1 {
					t.Fatal("update did not skip absent key")
				}
			case "upsert":
				if len(rows) != 2 || rows[0][1].Text != "updated" || rows[1][1].Text != "new" || out.CommittedRows != 2 {
					t.Fatal("upsert semantics changed")
				}
			case "insert_missing":
				if len(rows) != 2 || rows[0][1].Text != "original" || out.Rows[0].Status != "skipped" || out.CommittedRows != 1 {
					t.Fatal("insert_missing changed matching row")
				}
			case "delete":
				if len(rows) != 0 || out.CommittedRows != 1 || out.Rows[1].Status != "skipped" {
					t.Fatal("delete did not skip absent key")
				}
			case "replace":
				if len(rows) != 1 || rows[0][0].Text != "2" || out.CommittedRows != 1 || !out.Changed {
					t.Fatal("replace did not atomically clear first batch")
				}
			}
			for _, row := range rows {
				if row[2].Text != "native-default" {
					t.Fatal("omitted default not applied")
				}
			}
		})
	}
}

func TestNativeRQLiteImportExactStorageNewTableAndNativeCollations(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	nativeExecute(t, l, `CREATE TABLE import_types(id INTEGER PRIMARY KEY,txt TEXT,raw BLOB,num INTEGER,numeric NUMERIC,r REAL,dynamic,strict_any); CREATE TABLE import_strict(id INTEGER PRIMARY KEY,any_value ANY) STRICT;
CREATE TABLE import_keys(id INTEGER PRIMARY KEY,k TEXT,v TEXT); CREATE UNIQUE INDEX import_nocase ON import_keys(k COLLATE NOCASE); INSERT INTO import_keys VALUES(1,'original','old'),(2,NULL,'null');`)
	d := nativeImportDocument(t, s, "import_types")
	values := map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "9007199254740993"}, "txt": {Text: "a\x00b"}, "raw": {Kind: contract.Binary, Bytes: []byte{0, 255, 1}}, "num": {Text: "9223372036854775807"}, "numeric": {Text: "1/2"}, "r": {Text: "0.5"}, "dynamic": {Kind: contract.Floating, Text: "1"}}
	out, e := d.importData(context.Background(), p.ID, "main", d.object.Name, contract.DataImportRequest{Edits: []contract.DataEdit{{Kind: "insert", Values: values}}})
	if e != nil || !out.Committed {
		t.Fatal(e)
	}
	rows := nativeImportRows(t, s, `SELECT id,hex(txt),hex(raw),num,numeric,typeof(r),typeof(dynamic) FROM import_types`)
	if len(rows) != 1 || rows[0][0].Text != "9007199254740993" || rows[0][1].Text != "610062" || rows[0][2].Text != "00FF01" || rows[0][3].Text != "9223372036854775807" || rows[0][4].Text != "1/2" || rows[0][5].Text != "real" || rows[0][6].Text != "real" {
		t.Fatal("native exact storage changed")
	}
	// Ordinary SQLite numeric columns can store complete nonnumeric TEXT.
	// The bounded exact-number parser must not narrow that native field limit.
	long := strings.Repeat("猫", 4096)
	out, e = d.importData(context.Background(), p.ID, "main", d.object.Name, contract.DataImportRequest{Edits: []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "0"}, "num": {Text: long}}}}})
	if e != nil || !out.Committed || out.Rows[0].Outcome.GeneratedID != 0 || nativeImportRows(t, s, "SELECT num FROM import_types WHERE id=0")[0][0].Text != long {
		t.Fatal("native dynamic numeric-column TEXT or zero insert ID changed", e)
	}
	for i, v := range []string{"0.1", "9007199254740993"} {
		bad := nativeImportEdit(i+1, "unused")
		delete(bad.Values, "v")
		bad.Values["r"] = contract.DataValue{Text: v}
		out, e = d.importData(context.Background(), p.ID, "main", d.object.Name, contract.DataImportRequest{Edits: []contract.DataEdit{bad}})
		if e == nil || out.CommitUncertain || out.CommittedRows != 0 || len(out.Receipts) != 0 {
			t.Fatal("inexact numeric silently rounded or dispatched")
		}
	}
	strict := nativeImportDocument(t, s, "import_strict")
	out, e = strict.importData(context.Background(), p.ID, "main", strict.object.Name, contract.DataImportRequest{Edits: []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"id": {Text: "1"}, "any_value": {Kind: contract.Text, Text: "000123"}}}}})
	if e != nil || !out.Committed || nativeImportRows(t, s, "SELECT typeof(any_value),any_value FROM import_strict")[0][1].Text != "000123" {
		t.Fatal("STRICT ANY changed source class", e)
	}
	keys := nativeImportDocument(t, s, "import_keys")
	out, e = keys.importData(context.Background(), p.ID, "main", keys.object.Name, contract.DataImportRequest{Mode: "update", MatchColumns: []string{"k"}, Edits: []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"k": {Text: "ORIGINAL"}, "v": {Text: "changed"}}}}})
	if e != nil || out.CommittedRows != 1 || nativeImportRows(t, s, "SELECT v FROM import_keys WHERE id=1")[0][0].Text != "changed" {
		t.Fatal("native nullable unique index collation not used", e)
	}
	out, e = d.importData(context.Background(), p.ID, "main", "import_created", contract.DataImportRequest{NewTable: true, Columns: []contract.DataImportColumn{{Name: "id", Type: "INTEGER", PrimaryKey: true}, {Name: "v", Type: "TEXT"}}, Edits: []contract.DataEdit{nativeImportEdit(1, "created")}})
	if e != nil || !out.Created || out.Receipts[0].Object.Name != "import_created" || nativeImportRows(t, s, "SELECT v FROM import_created")[0][0].Text != "created" {
		t.Fatal("new target not created on original document", e)
	}
}

type importTestSource struct {
	importMemoryRows
	closeErr error
}

func (r *importTestSource) Close() error { return r.closeErr }
func nativeImportStream(d *dataDocument, rows []contract.DataEdit, request contract.DataImportRequest, closeErr error, record func(contract.DataImportBatchReceipt) error) (*contract.DataImportStreamResult, []contract.DataImportRowResult, error) {
	recorded := []contract.DataImportRowResult{}
	if record == nil {
		record = func(contract.DataImportBatchReceipt) error { return nil }
	}
	out, e := d.importStream(context.Background(), d.object.ProfileID, "main", d.object.Name, contract.DataImportStreamRequest{Request: request, Rows: int64(len(rows)), Generation: d.s.Generation(), Open: func(context.Context) (contract.DataImportRowSource, error) {
		return &importTestSource{importMemoryRows: importMemoryRows{rows: rows}, closeErr: closeErr}, nil
	}, Record: func(r contract.DataImportRowResult) error { recorded = append(recorded, r); return nil }, RecordReceipt: record})
	return out, recorded, e
}
func TestNativeRQLiteImportPartialBatchesRollbackAndArchiveFailure(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	for _, scenario := range []string{"rollback", "continue", "close-failure", "archive-failure"} {
		t.Run(scenario, func(t *testing.T) {
			name := "import_" + scenario
			nativeExecute(t, l, "CREATE TABLE "+quote(name)+"(id INTEGER PRIMARY KEY,v TEXT NOT NULL)")
			d := nativeImportDocument(t, s, name)
			edits := []contract.DataEdit{}
			for i := 1; i <= 65; i++ {
				edits = append(edits, nativeImportEdit(i, "v"))
			}
			if scenario == "rollback" || scenario == "continue" {
				edits = append(edits, nativeImportEdit(1, "duplicate"))
			}
			var closeErr error
			if scenario == "close-failure" {
				closeErr = io.ErrUnexpectedEOF
			}
			receipts := []contract.DataImportBatchReceipt{}
			out, rows, e := nativeImportStream(d, edits, contract.DataImportRequest{ContinueOnError: scenario == "continue"}, closeErr, func(r contract.DataImportBatchReceipt) error {
				receipts = append(receipts, r)
				if scenario == "archive-failure" {
					return io.ErrShortWrite
				}
				return nil
			})
			switch scenario {
			case "rollback":
				if e == nil || out.Committed || out.CommitUncertain || out.CommittedRows != 64 || len(rows) != 66 || rows[64].Status != "rolled_back" || rows[65].Status != "failed" || receipts[1].State != "rolled_back" {
					t.Fatalf("partial native rollback changed: %+v rows=%+v %v", out, rows[64:], e)
				}
			case "continue":
				if e != nil || !out.Committed || out.CommittedRows != 65 || rows[65].Status != "failed" || len(receipts) != 66 {
					t.Fatalf("single-row native transaction errors not isolated: %+v %v", out, e)
				}
			case "close-failure":
				if !errors.Is(e, closeErr) || out.CommittedRows != 64 || len(rows) != 65 || rows[64].Status != "not_processed" {
					t.Fatal("source close failure sent withheld last batch")
				}
			case "archive-failure":
				if !errors.Is(e, io.ErrShortWrite) || out.CommittedRows != 64 || out.LastReceipt.NativeJSON == "" || len(rows) != 64 || rows[0].Status != "committed" || out.CommitUncertain {
					t.Fatal("archive failure erased acknowledged original batch")
				}
			}
			want := out.CommittedRows
			actual := nativeImportRows(t, s, "SELECT count(*) FROM "+quote(name))[0][0].Text
			if actual != fmt.Sprint(want) {
				t.Fatal("confirmed rows disagree with native database", actual, want)
			}
		})
	}
}

func TestNativeRQLiteFiniteImportRetainsMoreThanPreviewRows(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	nativeExecute(t, l, "CREATE TABLE import_many(id INTEGER PRIMARY KEY,v TEXT)")
	d := nativeImportDocument(t, s, "import_many")
	edits := make([]contract.DataEdit, 1025)
	for i := range edits {
		edits[i] = nativeImportEdit(i+1, "v")
	}
	out, e := d.importData(context.Background(), p.ID, "main", d.object.Name, contract.DataImportRequest{Edits: edits})
	if e != nil || !out.Committed || out.CommittedRows != 1025 || len(out.Rows) != 1025 || len(out.Saved.Outcomes) != 1025 || len(out.Receipts) != 17 {
		t.Fatal("finite result was truncated to UI sample", e)
	}
	if !slices.ContainsFunc(out.Rows, func(r contract.DataImportRowResult) bool { return r.Row == 1025 && r.Outcome.GeneratedID == 1025 }) {
		t.Fatal("original final row outcome lost")
	}
}

func TestNativeRQLiteImportSchemaDriftAndReviewedCountRetainOnlyOriginalBatches(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	for _, scenario := range []string{"schema-before", "schema-after", "short-source", "extra-source", "statement-limit"} {
		t.Run(scenario, func(t *testing.T) {
			name := "import_" + scenario
			nativeExecute(t, l, "CREATE TABLE "+quote(name)+"(id INTEGER PRIMARY KEY,v TEXT)")
			d := nativeImportDocument(t, s, name)
			edits := []contract.DataEdit{}
			for i := 1; i <= 65; i++ {
				edits = append(edits, nativeImportEdit(i, "v"))
			}
			request := contract.DataImportStreamRequest{Rows: 65, Generation: s.Generation(), RecordReceipt: func(receipt contract.DataImportBatchReceipt) error {
				if scenario == "schema-after" && receipt.Sequence == 1 {
					nativeExecute(t, l, "ALTER TABLE "+quote(name)+" ADD COLUMN later TEXT")
				}
				return nil
			}, Open: func(context.Context) (contract.DataImportRowSource, error) {
				if scenario == "schema-before" {
					nativeExecute(t, l, "ALTER TABLE "+quote(name)+" ADD COLUMN later TEXT")
				}
				return &importMemoryRows{rows: edits}, nil
			}}
			if scenario == "short-source" {
				request.Rows = 66
			}
			if scenario == "extra-source" {
				request.Rows = 64
			}
			if scenario == "statement-limit" {
				request.StatementMaxBytes = 1
			}
			out, e := d.importStream(context.Background(), p.ID, "main", name, request)
			want := int64(64)
			if scenario == "schema-before" || scenario == "statement-limit" {
				want = 0
			}
			if e == nil || out.Committed || out.CommitUncertain || out.CommittedRows != want || nativeImportRows(t, s, "SELECT count(*) FROM "+quote(name))[0][0].Text != fmt.Sprint(want) {
				t.Fatalf("changed source/schema dispatched withheld rows: %+v %v", out, e)
			}
			if strings.HasPrefix(scenario, "schema") && !errors.Is(e, contract.ErrDataConflict) {
				t.Fatal("original schema conflict not preserved", e)
			}
		})
	}
}
