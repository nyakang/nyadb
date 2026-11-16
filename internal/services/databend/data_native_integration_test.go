package databend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func reserveNativeData(t *testing.T, s *Session, name string) *dataLease {
	t.Helper()
	l, err := s.ReserveDataDocument(s.Namespace(s.profile.Database).Object("table", name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.(*dataLease)
}
func nativeDataPage(t *testing.T, l *dataLease, o contract.DataOptions) *contract.DataPage {
	t.Helper()
	if o.Limit == 0 {
		o.Limit = 200
	}
	p, err := l.page(context.Background(), l.object.ProfileID, l.object.Schema, l.object.Name, o)
	if err != nil {
		t.Fatal("original native data page", err)
	}
	return p
}
func nativeDataPlan(t *testing.T, l *dataLease, edits []contract.DataEdit) contract.DataMutationPlan {
	t.Helper()
	p, err := l.previewEdits(context.Background(), l.object.ProfileID, l.object.Schema, l.object.Name, edits)
	if err != nil {
		t.Fatal("original native write preview", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}
func nativeDataSubmit(t *testing.T, l *dataLease, edits []contract.DataEdit) *contract.DataEditResult {
	t.Helper()
	p := nativeDataPlan(t, l, edits)
	r, err := l.submitEdits(context.Background(), l.object.ProfileID, l.object.Schema, l.object.Name, p)
	if err != nil || !contract.DataWriteComplete(r, len(edits)) {
		t.Fatal("complete original independent writes", err, r)
	}
	return r
}

func TestDatabendDataOriginalTypedPagesChunksEditingAndStreamingExports(t *testing.T) {
	f := managedNativeFixture(t)
	_ = managedNativeStore(t, f)
	s := openNativeProvider(t, f, nativeProviderProfile(t, f, 601, filepath.Join(f.Directory, t.Name(), "store")))
	q := reserveNativeQuery(t, s, 1)
	providerQuery(t, q, f.Database, `CREATE TABLE data_values51 (id UInt64 NOT NULL, exact Decimal(38,20) NOT NULL, payload Binary NOT NULL, txt String NOT NULL, j Nullable(Variant), a Array(UInt64) NOT NULL, m Map(String,UInt64) NOT NULL, absent Nullable(String))`)
	providerQuery(t, q, f.Database, `INSERT INTO data_values51 VALUES (18446744073709551615,9007199254740993.12345678901234567890,unhex(repeat('00ff80',700)),repeat('喵😀x',1200),parse_json('null'),[9007199254740993],MAP(['x'],[18446744073709551615]),NULL),(2,1,unhex('00'),'small',NULL,[],MAP([],[]),NULL)`)
	l := reserveNativeData(t, s, "data_values51")
	// Holding the sole metadata slot cannot block metadata read on the
	// original workload data handle.
	reserved, receipts, err := s.process.OpenSession(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range receipts {
		s.observed("data-held-metadata", r)
	}
	p := nativeDataPage(t, l, contract.DataOptions{SortColumn: "id", SortDescending: true})
	rc, err := reserved.Close()
	s.observed("data-held-metadata-close", rc)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Rows) != 2 || p.Rows[0][0].Value.Text != "18446744073709551615" || p.Rows[0][1].Value.Text != "9007199254740993.12345678901234567890" || !p.Rows[0][2].Truncated || !p.Rows[0][3].Truncated || p.Rows[0][4].Value.Kind != contract.JSON || p.Rows[0][4].Value.Text != "null" || p.Rows[1][4].Value.Kind != contract.Null || p.Rows[0][5].Value.Text != "[9007199254740993]" || p.Rows[0][6].Value.Text != `{"x":18446744073709551615}` {
		t.Fatal("original type/precision/null or bounded preview lost", p.Rows)
	}
	if !bytes.Equal(p.Rows[0][2].Value.Bytes, bytes.Repeat([]byte{0, 255, 128}, 342)[:1024]) || len([]rune(p.Rows[0][3].Value.Text)) != 1024 {
		t.Fatal("native binary/unicode preview changed")
	}
	n, err := l.count(context.Background(), s.ProfileID(), f.Database, l.object.Name)
	if err != nil || n != 2 {
		t.Fatal("original count", n, err)
	}
	row, err := l.row(context.Background(), s.ProfileID(), f.Database, l.object.Name, *p.RowRefs[0])
	if err != nil || len(row.Values["payload"].Bytes) != 2100 || len([]rune(row.Values["txt"].Text)) != 3600 {
		t.Fatal("complete original editing values", err)
	}
	nativeMap := localNativeQuery(t, f.localTransportFixture, "SELECT json_pretty(to_variant(m)) FROM "+qualified(l.object)+" WHERE id=18446744073709551615")
	if len(nativeMap) != 1 || len(nativeMap[0]) != 1 {
		t.Fatal("independent native map text oracle")
	}
	mapSuffix := string([]rune(nativeMap[0][0])[5:])
	for _, test := range []struct {
		column   string
		offset   int64
		limit    int
		expected string
	}{{"txt", 1024, 3, "😀x喵"}, {"payload", 1023, 3, "0x00ff80"}, {"j", 1, 3, "ull"}, {"m", 5, 1024, mapSuffix}} {
		chunk, err := l.chunk(context.Background(), s.ProfileID(), f.Database, l.object.Name, test.column, *p.RowRefs[0], test.offset, test.limit)
		if err != nil || chunk.Text != test.expected || chunk.NextOffset <= test.offset {
			t.Fatal("exact native field slice", test, chunk, err)
		}
	}
	var jsonl bytes.Buffer
	if err := l.jsonl(context.Background(), l.object, &jsonl); err != nil {
		t.Fatal("original typed JSONL", err)
	}
	if len(strings.Split(strings.TrimSpace(jsonl.String()), "\n")) != 2 || !strings.Contains(jsonl.String(), "18446744073709551615") {
		t.Fatal("JSONL incomplete or precision lost")
	}
	pages := reserveNativeData(t, s, "managed_pages")
	first := nativeDataPage(t, pages, contract.DataOptions{Limit: 200, SortColumn: "id"})
	last := nativeDataPage(t, pages, contract.DataOptions{Limit: 200, Offset: 200, SortColumn: "id"})
	if len(first.Rows) != 200 || !first.HasMore || len(last.Rows) != 5 || last.HasMore || last.Rows[0][0].Value.Text != "200" {
		t.Fatal("native LIMIT/OFFSET page boundary")
	}
	providerQuery(t, q, f.Database, `CREATE TABLE data_export51 (id UInt64 NOT NULL, txt String NOT NULL); INSERT INTO data_export51 VALUES (1,repeat('喵',400000)),(2,repeat('xxxxxxxxxx',943719))`)
	large := reserveNativeData(t, s, "data_export51")
	lp := nativeDataPage(t, large, contract.DataOptions{FilterColumn: "id", FilterValue: "1"})
	if _, err := large.row(context.Background(), s.ProfileID(), f.Database, large.object.Name, *lp.RowRefs[0]); !errors.Is(err, contract.ErrDataEditLimit) {
		t.Fatal("editing limit was bypassed", err)
	}
	rows, exportBytes := 0, 0
	err = large.export(context.Background(), s.ProfileID(), f.Database, large.object.Name, contract.DataOptions{FilterColumn: "id", FilterValue: "1"}, nil, func(values []contract.DataValue) error { rows++; exportBytes += len(values[1].Text); return nil })
	if err != nil || rows != 1 || exportBytes != 1200000 {
		t.Fatal("stream export reused editing limit", err, rows, exportBytes)
	}
	selected := 0
	err = large.export(context.Background(), s.ProfileID(), f.Database, large.object.Name, contract.DataOptions{}, []string{"id"}, func(values []contract.DataValue) error { selected++; return nil })
	if err != nil || selected != 2 {
		t.Fatal("unselected large field blocked export", err, selected)
	}
	err = large.export(context.Background(), s.ProfileID(), f.Database, large.object.Name, contract.DataOptions{FilterColumn: "id", FilterValue: "2"}, nil, func([]contract.DataValue) error { t.Error("oversize row was published"); return nil })
	if !errors.Is(err, contract.ErrDataPage) {
		t.Fatal("native frame budget silently truncated", err)
	}
	writeDataEvidence(t, f, "data-read.json", map[string]any{"page_rows": 205, "unicode_units": 3600, "binary_bytes": 2100, "json_null_distinct": true, "export_bytes": exportBytes, "selected_rows": selected, "export_max_row_bytes": dataExportMaxBytes, "editing_max_row_bytes": contract.DataEditMaxBytes})
}

func TestDatabendDataOriginalImmutableWritesConflictsAndUnsentSuffix(t *testing.T) {
	f := managedNativeFixture(t)
	_ = managedNativeStore(t, f)
	s := openNativeProvider(t, f, nativeProviderProfile(t, f, 602, filepath.Join(f.Directory, t.Name(), "store")))
	q := reserveNativeQuery(t, s, 1)
	providerQuery(t, q, f.Database, `CREATE TABLE data_writes51 (id UInt64 NOT NULL, payload Binary NOT NULL, j Nullable(Variant), a Array(UInt64) NOT NULL, m Map(String,UInt64) NOT NULL, txt String DEFAULT 'native default')`)
	l := reserveNativeData(t, s, "data_writes51")
	edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "18446744073709551615"}, "payload": {Kind: contract.Binary, Bytes: []byte{0, 255, 128}, Binary: true}, "j": {Kind: contract.JSON, Text: "null"}, "a": {Kind: contract.JSON, Text: "[9007199254740993]"}, "m": {Kind: contract.JSON, Text: `{"x":18446744073709551615}`}}}}
	plan := nativeDataPlan(t, l, edits)
	edits[0].Values["payload"].Bytes[0] = 99
	view := plan.Snapshot()
	view.Edits[0].Values["payload"].Bytes[1] = 99
	r, err := l.submitEdits(context.Background(), s.ProfileID(), f.Database, l.object.Name, plan)
	if err != nil || !contract.DataWriteComplete(r, 1) || r.CommitConfirmed || !strings.Contains(r.NativeJSON, "Original") {
		t.Fatal("immutable native insert receipts", err)
	}
	if _, err := l.submitEdits(context.Background(), s.ProfileID(), f.Database, l.object.Name, plan); err == nil {
		t.Fatal("consumed original insert replayed")
	}
	p := nativeDataPage(t, l, contract.DataOptions{})
	if !bytes.Equal(p.Rows[0][1].Value.Bytes, []byte{0, 255, 128}) || p.Rows[0][5].Value.Text != "native default" {
		t.Fatal("mutable input or omitted defaults changed")
	}
	row, err := l.row(context.Background(), s.ProfileID(), f.Database, l.object.Name, *p.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	nativeDataSubmit(t, l, []contract.DataEdit{{Kind: "update", Original: row.Snapshot, Values: map[string]contract.DataValue{"txt": {Kind: contract.Text, Text: "喵'\\\x00"}, "j": {Kind: contract.Null, Null: true}}}})
	p = nativeDataPage(t, l, contract.DataOptions{})
	if p.Rows[0][5].Value.Text != "喵'\\\x00" || p.Rows[0][2].Value.Kind != contract.Null {
		t.Fatal("native escaped text or SQL NULL changed")
	}
	nativeDataSubmit(t, l, []contract.DataEdit{{Kind: "delete", Original: p.RowSnapshots[0]}})
	providerQuery(t, q, f.Database, `INSERT INTO data_writes51 VALUES (1,unhex('00'),NULL,[],MAP([],[]),'duplicate'),(1,unhex('00'),NULL,[],MAP([],[]),'duplicate')`)
	p = nativeDataPage(t, l, contract.DataOptions{})
	if _, err := l.previewEdits(context.Background(), s.ProfileID(), f.Database, l.object.Name, []contract.DataEdit{{Kind: "delete", Original: p.RowSnapshots[0]}}); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("duplicate original values admitted", err)
	}
	providerQuery(t, q, f.Database, `INSERT INTO data_writes51 VALUES (2,unhex('00'),NULL,[],MAP([],[]),'unique')`)
	p = nativeDataPage(t, l, contract.DataOptions{FilterColumn: "id", FilterValue: "2"})
	plan = nativeDataPlan(t, l, []contract.DataEdit{{Kind: "delete", Original: p.RowSnapshots[0]}, {Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "3"}, "payload": {Kind: contract.Binary, Bytes: []byte{3}}, "a": {Kind: contract.JSON, Text: "[]"}, "m": {Kind: contract.JSON, Text: "{}"}}}})
	providerQuery(t, q, f.Database, `UPDATE data_writes51 SET txt='changed' WHERE id=2`)
	r, err = l.submitEdits(context.Background(), s.ProfileID(), f.Database, l.object.Name, plan)
	if !errors.Is(err, contract.ErrDataConflict) || !r.Incomplete || r.CommitUncertain || r.Applied != 0 || r.Sequential.Statements[0].State != contract.DataWriteNotSent || r.Sequential.Statements[1].State != contract.DataWriteNotSent {
		t.Fatal("stale original dispatched or suffix lost", err, r)
	}
	if _, err := l.previewEdits(context.Background(), s.ProfileID(), f.Database, l.object.Name, edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("incomplete document was reauthorized", err)
	}
	writeDataEvidence(t, f, "data-writes.json", map[string]any{"native_insert_update_delete": true, "exact_typed_values": true, "duplicates_rejected": true, "stale_receipt": r, "immutable_inputs": true, "no_transaction_claim": true})
}

func TestDatabendDataOriginalDroppedReplyKeepsUnknownWriteAndIndependentDocument(t *testing.T) {
	f := managedNativeFixture(t)
	var dropped atomic.Bool
	var dispatches atomic.Int32
	product := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(raw))
		target := req.Method == "POST" && req.URL.Path == "/v1/query" && bytes.Contains(raw, []byte("INSERT INTO")) && bytes.Contains(raw, []byte("data_unknown51"))
		if target {
			dispatches.Add(1)
		}
		code, headers, body := forwardFixture(t, f.Endpoint, req)
		if target && dispatches.Load() == 2 && dropped.CompareAndSwap(false, true) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(502)
			_, _ = w.Write([]byte(`{"original_reply_dropped":true}`))
			return
		}
		fixtureResponse(w, code, headers, body)
	}))
	defer product.Close()
	_ = managedNativeStore(t, f)
	proxied := f
	proxied.Endpoint = product.URL
	s := openNativeProvider(t, proxied, nativeProviderProfile(t, proxied, 603, filepath.Join(f.Directory, t.Name(), "store")))
	q := reserveNativeQuery(t, s, 1)
	providerQuery(t, q, f.Database, `CREATE TABLE data_unknown51 (id UInt64 NOT NULL)`)
	l := reserveNativeData(t, s, "data_unknown51")
	var edits []contract.DataEdit
	for i := 1; i <= 3; i++ {
		edits = append(edits, contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: strconv.Itoa(i)}}})
	}
	plan := nativeDataPlan(t, l, edits)
	r, err := l.submitEdits(context.Background(), s.ProfileID(), f.Database, l.object.Name, plan)
	if !errors.Is(err, contract.ErrDataCommitUncertain) || !r.CommitUncertain || !r.Incomplete || r.Applied != 1 || dispatches.Load() != 2 || r.Sequential.Statements[0].State != contract.DataWriteAcknowledged || r.Sequential.Statements[1].State != contract.DataWriteUnknown || r.Sequential.Statements[2].State != contract.DataWriteNotSent {
		t.Fatal("original dropped reply replayed or settled by later observation", err, r, dispatches.Load())
	}
	if progress, valid := contract.SequentialDataWriteProgress(r, 3); !valid || !progress.Unknown || progress.Acknowledged != 1 {
		t.Fatal("unknown original receipt contract", progress, valid)
	}
	if rows := localNativeQuery(t, f.localTransportFixture, "SELECT id FROM "+qualified(l.object)+" ORDER BY id"); len(rows) != 2 || rows[0][0] != "1" || rows[1][0] != "2" {
		t.Fatal("original independent writes/suffix differ", rows)
	}
	_ = nativeDataPage(t, l, contract.DataOptions{})
	if _, err := l.previewEdits(context.Background(), s.ProfileID(), f.Database, l.object.Name, edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("later read settled unknown write", err)
	}
	other := reserveNativeData(t, s, "data_unknown51")
	nativeDataSubmit(t, other, []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "4"}}}})
	if r.Sequential.Statements[1].State != contract.DataWriteUnknown {
		t.Fatal("independent document rewrote old receipt")
	}
	writeDataEvidence(t, f, "data-unknown.json", map[string]any{"original_receipt": r, "dispatches": dispatches.Load(), "unknown_preserved": true, "independent_document_survived": true})
}

func TestDatabendDataOriginalCancellationWaitsForExecutorAndCloseReleasesRequests(t *testing.T) {
	f := managedNativeFixture(t)
	pageStarted, finalized := make(chan struct{}), make(chan struct{})
	var id atomic.Value
	var blocked atomic.Bool
	var once sync.Once
	var submitted, finalStatus atomic.Int32
	product := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(raw))
		if req.Method == "POST" && bytes.Contains(raw, []byte("data_cancel51")) && bytes.Contains(raw, []byte("__nyadb_value_0")) {
			submitted.Add(1)
			id.Store(req.Header.Get(queryIDHeader))
		}
		originalID, _ := id.Load().(string)
		if originalID != "" && req.Method == "GET" && strings.HasPrefix(req.URL.Path, "/v1/query/"+originalID+"/page/") && blocked.CompareAndSwap(false, true) {
			close(pageStarted)
			select {
			case <-finalized:
			case <-time.After(8 * time.Second):
				t.Error("original data executor never finalized")
			}
		}
		code, headers, body := forwardFixture(t, f.Endpoint, req)
		if originalID != "" && req.Method == "GET" && req.URL.Path == "/v1/query/"+originalID+"/final" {
			finalStatus.Store(int32(code))
			once.Do(func() { close(finalized) })
		}
		fixtureResponse(w, code, headers, body)
	}))
	defer product.Close()
	_ = managedNativeStore(t, f)
	proxied := f
	proxied.Endpoint = product.URL
	s := openNativeProvider(t, proxied, nativeProviderProfile(t, proxied, 604, filepath.Join(f.Directory, t.Name(), "store")))
	q := reserveNativeQuery(t, s, 1)
	providerQuery(t, q, f.Database, `CREATE VIEW data_cancel51 AS SELECT to_uint64(sleep(2)) AS n`)
	lease, err := s.ReserveDataDocument(s.Namespace(f.Database).Object("view", "data_cancel51"))
	if err != nil {
		t.Fatal(err)
	}
	l := lease.(*dataLease)
	done := make(chan error, 1)
	go func() {
		_, err := l.page(context.Background(), s.ProfileID(), f.Database, l.object.Name, contract.DataOptions{Limit: 200})
		done <- err
	}()
	select {
	case <-pageStarted:
	case <-time.After(15 * time.Second):
		_ = l.Close()
		t.Fatal("original data request never entered native page")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- l.Close() }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("original cancellation outcome", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("original data cancellation did not settle")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("data close did not await original executor")
	}
	if submitted.Load() != 1 || finalStatus.Load() != 200 {
		t.Fatal("data close returned before final or replayed request", submitted.Load(), finalStatus.Load())
	}
	providerQuery(t, q, f.Database, "SELECT 646464")
	if _, err := l.page(context.Background(), s.ProfileID(), f.Database, l.object.Name, contract.DataOptions{Limit: 200}); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed data document survived", err)
	}
	writeDataEvidence(t, f, "data-cancellation.json", map[string]any{"original_query_id": id.Load(), "submitted": submitted.Load(), "final_http_status": finalStatus.Load(), "close_waited": true, "independent_sql_survived": true, "native_kill_or_rollback_accepted": false})
}

func writeDataEvidence(t *testing.T, f managedFixture, name string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.Directory, name), raw, 0600); err != nil {
		t.Fatal(err)
	}
}
