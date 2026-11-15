package rqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeDataDocument(t *testing.T, s *Session, name string) (*dataDocument, contract.DataRuntime) {
	t.Helper()
	o := contract.ObjectRef{ProfileID: s.ProfileID(), Catalog: "main", Kind: "table", Name: name}
	l, err := s.ReserveDataDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.(*dataDocument), l.Runtime()
}

func nativeDataPage(t *testing.T, s *Session, r contract.DataRuntime, name string) *contract.DataPage {
	t.Helper()
	p, err := r.Loader(context.Background(), s.ProfileID(), "main", name, 0, 10)
	if err != nil || p == nil || p.Editable == nil || !*p.Editable {
		t.Fatalf("native editable page: %v", err)
	}
	return p
}

func nativeDataSave(t *testing.T, s *Session, r contract.DataRuntime, name string, edits []contract.DataEdit) *contract.DataEditResult {
	t.Helper()
	out, err := r.Saver(context.Background(), s.ProfileID(), "main", name, edits)
	if err != nil || !out.CommitConfirmed || out.CommitUncertain || out.Incomplete || out.Applied != len(edits) || len(out.Outcomes) != len(edits) || !out.RefreshRequired || out.NativeJSON == "" {
		t.Fatalf("native atomic data save: %v (%+v)", err, out)
	}
	return out
}

func nativeGuardCleanup(t *testing.T, s *Session) {
	t.Helper()
	// Query the same native writer by beginning with a temporary DDL. A normal
	// /db/query reader cannot prove that the writer's TEMP guards were removed.
	r, err := s.request(context.Background(), "/db/request", []any{
		"CREATE TEMP TABLE _nyadb_cleanup_probe(ok INTEGER)",
		"SELECT count(*) FROM temp.sqlite_schema WHERE substr(name,1,13)='_nyadb_guard_'",
		"DROP TABLE temp._nyadb_cleanup_probe",
	}, true, true)
	rows, e := decodedRows(r, 1, 1)
	if err != nil || e != nil || len(rows) != 1 || rows[0][0].Text != "0" {
		t.Fatalf("native original writer leaked a request guard: %v %v", err, e)
	}
}

func nativeValuesSnapshot(row *contract.DataRow) *contract.RowSnapshot {
	original := contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Edits: []contract.DataEdit{{Original: row.Snapshot}}}).Edits[0].Original
	original.Mode = "values"
	for _, c := range row.Columns {
		v := row.Values[c.Name]
		cell := contract.Cell{Kind: v.Kind, Text: v.Text, Bytes: bytes.Clone(v.Bytes)}
		if v.Null {
			cell.Kind = contract.Null
		}
		original.Original = append(original.Original, cell)
	}
	return original
}

func TestNativeRQLiteAtomicTypedInsertUpdateDeleteAndOriginalValidation(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	const name = "写入🐱"
	nativeExecute(t, l, `CREATE TABLE "写入🐱"(id INTEGER PRIMARY KEY,note TEXT DEFAULT 'native default',raw BLOB,flex,generated TEXT AS(note||id));INSERT INTO "写入🐱"(id,note,raw,flex) VALUES(1,CAST(X'610062' AS TEXT),X'00FF',CAST(1 AS REAL)),(2,'second',NULL,2)`)
	_, r := nativeDataDocument(t, s, name)
	page := nativeDataPage(t, s, r, name)
	edits := []contract.DataEdit{
		{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Kind: contract.Text, Text: "改🐱\x00后"}, "raw": {Kind: contract.Binary, Binary: true, Bytes: []byte{0, 255, 39}}, "flex": {Kind: contract.Floating, Text: "2.5"}}},
		{Kind: "delete", Original: page.RowSnapshots[1]},
		{Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "9007199254740993"}, "note": {Default: true}, "flex": {Kind: contract.Integer, Text: "9223372036854775807"}}},
	}
	out := nativeDataSave(t, s, r, name, edits)
	if out.Outcomes[2].GeneratedID != 9007199254740993 || !reflect.DeepEqual(out.Outcomes[0].Original, page.RowSnapshots[0]) {
		t.Fatal("original typed key or mutation INSERT identifier was lost")
	}
	page = nativeDataPage(t, s, r, name)
	if len(page.Rows) != 2 || page.Rows[0][1].Value.Text != "改🐱\x00后" || !bytes.Equal(page.Rows[0][2].Value.Bytes, []byte{0, 255, 39}) || page.Rows[0][3].Value.Kind != contract.Floating || page.Rows[1][1].Text != "native default" || page.Rows[1][3].Value.Text != "9223372036854775807" {
		t.Fatal("native typed writes, defaults, NUL or original deletion changed")
	}
	nativeGuardCleanup(t, s)
	full, err := r.RowReader(context.Background(), p.ID, "main", name, *page.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	original := nativeValuesSnapshot(full)
	original.Fingerprints = nil // Exercise complete values independently of hashes.
	nativeDataSave(t, s, r, name, []contract.DataEdit{{Kind: "update", Original: original, Values: map[string]contract.DataValue{"note": {Default: true}}}})
	page = nativeDataPage(t, s, r, name)
	full, err = r.RowReader(context.Background(), p.ID, "main", name, *page.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	original = nativeValuesSnapshot(full) // Both supplied payloads must be checked.
	original.Original[1].Text = "contradictory complete original"
	rejected, err := r.Saver(context.Background(), p.ID, "main", name, []contract.DataEdit{{Kind: "update", Original: original, Values: map[string]contract.DataValue{"note": {Text: "must not apply"}}}})
	if !errors.Is(err, contract.ErrDataConflict) || rejected.CommitUncertain || rejected.Applied != 0 || rejected.NativeJSON == "" {
		t.Fatalf("contradictory original values/fingerprints were accepted: %v", err)
	}
	nativeGuardCleanup(t, s)
	before := nativeDataPage(t, s, r, name)
	nativeExecute(t, l, `UPDATE "写入🐱" SET note='outside change' WHERE id=9007199254740993`)
	rejected, err = r.Saver(context.Background(), p.ID, "main", name, []contract.DataEdit{
		{Kind: "update", Original: before.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "first must roll back"}}},
		{Kind: "update", Original: before.RowSnapshots[1], Values: map[string]contract.DataValue{"note": {Text: "stale second"}}},
	})
	if !errors.Is(err, contract.ErrDataConflict) || rejected.CommitUncertain || rejected.CommitConfirmed || rejected.Applied != 0 || len(rejected.Outcomes) != 0 || !strings.Contains(rejected.NativeJSON, `"rows_affected":1`) {
		t.Fatalf("earlier mutation in a rejected batch was counted as committed: %v", err)
	}
	after := nativeDataPage(t, s, r, name)
	if after.Rows[0][1].Text != "native default" || after.Rows[1][1].Text != "outside change" {
		t.Fatal("original failed transaction did not roll back earlier write")
	}
	nativeGuardCleanup(t, s)
	for _, edit := range []contract.DataEdit{
		{Kind: "update", Original: after.RowSnapshots[0], Values: map[string]contract.DataValue{"generated": {Text: "forged"}}},
		{Kind: "update", Original: after.RowSnapshots[0], Values: map[string]contract.DataValue{"unknown": {Text: "forged"}}},
		{Kind: "insert", Original: after.RowSnapshots[0]},
	} {
		if _, err := r.Saver(context.Background(), p.ID, "main", name, []contract.DataEdit{edit}); err == nil {
			t.Fatal("unsupported generated/unknown/original insert payload was accepted")
		}
	}
	wrong := contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Edits: []contract.DataEdit{{Kind: "delete", Original: after.RowSnapshots[0]}}}).Edits
	wrong[0].Original.Ref.Object.ProfileID++
	if _, err := r.Saver(context.Background(), p.ID, "main", name, wrong); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("save escaped the original profile identity")
	}
	nativeExecute(t, l, `ALTER TABLE "写入🐱" ADD COLUMN later TEXT`)
	if _, err := r.Saver(context.Background(), p.ID, "main", name, []contract.DataEdit{{Kind: "delete", Original: after.RowSnapshots[0]}}); err == nil {
		t.Fatal("old schema snapshot was accepted after native DDL")
	}
}

type nativeDataProxy struct {
	server       *httptest.Server
	writes       atomic.Int32
	mode         string
	started, ack chan struct{}
	raw          chan string
}

func proxyNativeData(t *testing.T, p connections.Profile, mode string) *nativeDataProxy {
	t.Helper()
	c, _ := connections.RQLiteProfileConfig(p)
	proxy := &nativeDataProxy{mode: mode, started: make(chan struct{}), ack: make(chan struct{}), raw: make(chan string, 1)}
	proxy.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forward, err := http.NewRequest(http.MethodPost, c.Endpoint+r.URL.RequestURI(), r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(502)
			return
		}
		forward.Header = r.Header.Clone()
		res, err := http.DefaultClient.Do(forward)
		if err != nil {
			t.Error(err)
			w.WriteHeader(502)
			return
		}
		defer res.Body.Close()
		var body bytes.Buffer
		if _, err := body.ReadFrom(res.Body); err != nil {
			t.Error(err)
			return
		}
		if r.URL.Path == "/db/request" {
			count := proxy.writes.Add(1)
			select {
			case proxy.raw <- body.String():
			default:
			}
			if count == 1 {
				close(proxy.started)
			}
			switch mode {
			case "held":
				<-proxy.ack
			case "lost":
				connection, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = connection.Close()
				return
			case "malformed":
				body.Reset()
				body.WriteString(`{"results":[{},{}]}`)
			}
		}
		for k, v := range res.Header {
			w.Header()[k] = v
		}
		if mode == "malformed" && r.URL.Path == "/db/request" {
			w.Header().Del("Content-Length")
		}
		w.WriteHeader(res.StatusCode)
		_, _ = w.Write(body.Bytes())
	}))
	t.Cleanup(func() {
		select {
		case <-proxy.ack:
		default:
			close(proxy.ack)
		}
		proxy.server.Close()
	})
	return proxy
}

func TestNativeRQLiteDataCancelCloseAndQueuedWriteWaitOriginalReceipt(t *testing.T) {
	p := nativeFixture(t, "")
	original := openNative(t, p, "original-fixture-password")
	nativeExecute(t, lease(t, original, 1), "CREATE TABLE data_ack(id INTEGER PRIMARY KEY,note TEXT);INSERT INTO data_ack VALUES(1,'original')")
	proxy := proxyNativeData(t, p, "held")
	defer func() {
		select {
		case <-proxy.ack:
		default:
			close(proxy.ack)
		}
	}()
	q := testProfile(t, proxy.server.URL)
	q.Username, q.RQLiteOptions.Auth = "fixture", "basic"
	s := openNative(t, q, "original-fixture-password")
	d, r := nativeDataDocument(t, s, "data_ack")
	page := nativeDataPage(t, s, r, "data_ack")
	edits := []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "applied once"}}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type saved struct {
		out *contract.DataEditResult
		err error
	}
	done := make(chan saved, 1)
	go func() { out, err := r.Saver(ctx, q.ID, "main", "data_ack", edits); done <- saved{out, err} }()
	select {
	case <-proxy.started:
	case <-time.After(10 * time.Second):
		t.Fatal("original data write did not dispatch")
	}
	raw := <-proxy.raw
	cancel()
	queuedCtx, queuedCancel := context.WithCancel(context.Background())
	queued := make(chan error, 1)
	go func() { _, err := r.Saver(queuedCtx, q.ID, "main", "data_ack", edits); queued <- err }()
	queuedCancel()
	if err := <-queued; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued canceled write: %v", err)
	}
	closed, sessionClosed := make(chan struct{}), make(chan struct{})
	go func() { _ = d.Close(); close(closed) }()
	go func() { _ = s.Close(); close(sessionClosed) }()
	select {
	case <-done:
		t.Fatal("save discarded its held original acknowledgement")
	case <-closed:
		t.Fatal("document close released unacknowledged data write")
	case <-sessionClosed:
		t.Fatal("session close released unacknowledged data write")
	case <-time.After(40 * time.Millisecond):
	}
	close(proxy.ack)
	result := <-done
	if result.err != nil || !result.out.CommitConfirmed || result.out.Applied != 1 || result.out.NativeJSON != raw {
		t.Fatalf("cancel/close erased original committed receipt: %v", result.err)
	}
	for _, ch := range []chan struct{}{closed, sessionClosed} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("original close did not finish after acknowledgement")
		}
	}
	if proxy.writes.Load() != 1 {
		t.Fatal("original canceled write was replayed or queued write dispatched")
	}
	if _, err := r.Loader(context.Background(), q.ID, "main", "data_ack", 0, 1); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed original document admitted a read")
	}
}

func TestNativeRQLiteUnknownDataWriteCannotBeResolvedByLaterReadsOrRecovery(t *testing.T) {
	for _, mode := range []string{"lost", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			p := nativeFixture(t, "")
			original := openNative(t, p, "original-fixture-password")
			l := lease(t, original, 1)
			nativeExecute(t, l, "CREATE TABLE unknown_data(id INTEGER PRIMARY KEY,note TEXT);INSERT INTO unknown_data VALUES(1,'original')")
			proxy := proxyNativeData(t, p, mode)
			q := testProfile(t, proxy.server.URL)
			q.Username, q.RQLiteOptions.Auth = "fixture", "basic"
			s := openNative(t, q, "original-fixture-password")
			d, r := nativeDataDocument(t, s, "unknown_data")
			page := nativeDataPage(t, s, r, "unknown_data")
			edits := []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "applied once"}}}}
			out, err := r.Saver(context.Background(), q.ID, "main", "unknown_data", edits)
			if !errors.Is(err, contract.ErrDataCommitUncertain) || !out.CommitUncertain || !out.Incomplete || out.CommitConfirmed || out.Applied != 0 || len(out.Outcomes) != 1 {
				t.Fatalf("unknown original data result: %v", err)
			}
			if mode == "malformed" && out.NativeJSON != `{"results":[{},{}]}` {
				t.Fatal("original malformed native receipt was reconstructed or discarded")
			}
			actual := nativeExecute(t, l, "SELECT note FROM unknown_data WHERE id=1")
			if actual.Sets[0].Rows[0][0].Text != "applied once" {
				t.Fatal("unknown test write was not actually applied by original server")
			}
			later := nativeDataPage(t, s, r, "unknown_data")
			if later.Rows[0][1].Text != "applied once" {
				t.Fatal("independent later native read failed")
			}
			verified, err := r.Verify(context.Background(), q.ID, "main", "unknown_data", copyDataResult(out))
			if err != nil || verified.Status != "indeterminate" || !reflect.DeepEqual(verified.Recovered, out) {
				t.Fatal("later read settled or erased original unknown receipt")
			}
			blocked, err := r.Saver(context.Background(), q.ID, "main", "unknown_data", edits)
			if !errors.Is(err, contract.ErrDataCommitUncertain) || !reflect.DeepEqual(blocked, out) || proxy.writes.Load() != 1 {
				t.Fatal("unknown original write was replayed")
			}
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
			d.mu.Lock()
			retained := d.unknown
			d.mu.Unlock()
			if retained != nil {
				t.Fatal("closed document retained unknown receipt cache")
			}
			_, recovered := nativeDataDocument(t, s, "unknown_data")
			fresh := nativeDataPage(t, s, recovered, "unknown_data")
			if fresh.Rows[0][1].Text != "applied once" || proxy.writes.Load() != 1 || !out.CommitUncertain {
				t.Fatal("explicit original-profile recovery replayed edits or changed old receipt")
			}
		})
	}
}

func TestNativeRQLiteAtomicGuardsOnThreeNodeOriginalFollower(t *testing.T) {
	p, join := nativeFixtureWithJoin(t, "", "")
	follower, _ := nativeFixtureWithJoin(t, "", join)
	third, _ := nativeFixtureWithJoin(t, "", join)
	leader := openNative(t, p, "original-fixture-password")
	nativeExecute(t, lease(t, leader, 1), "CREATE TABLE native_collation(k TEXT COLLATE NOCASE NOT NULL,note TEXT);CREATE UNIQUE INDEX binary_key ON native_collation(k COLLATE BINARY);INSERT INTO native_collation VALUES('A','upper'),('a','lower')")
	follower.RQLiteOptions.Consistency = "strong"
	s := openNative(t, follower, "original-fixture-password")
	transport := &originalEndpointTransport{base: s.transport, host: fmt.Sprintf("%s:%d", follower.Host, follower.Port), t: t}
	s.client.Transport = transport
	_, r := nativeDataDocument(t, s, "native_collation")
	page := nativeDataPage(t, s, r, "native_collation")
	nativeDataSave(t, s, r, "native_collation", []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "exact uppercase"}}}})
	if !transport.forwarded {
		t.Fatal("original follower did not forward its native atomic transaction")
	}
	third.RQLiteOptions.Consistency = "strong"
	thirdSession := openNative(t, third, "original-fixture-password")
	actual := nativeExecute(t, lease(t, thirdSession, 1), "SELECT k,note FROM native_collation ORDER BY k COLLATE BINARY")
	if len(actual.Sets[0].Rows) != 2 || actual.Sets[0].Rows[0][1].Text != "exact uppercase" || actual.Sets[0].Rows[1][1].Text != "lower" {
		t.Fatal("native guarded transaction changed another collation key or missed cluster replication")
	}
	nativeGuardCleanup(t, s)
	// Parse, rather than trim, every original native receipt in the fixture.
	if !json.Valid([]byte(nativeDataSave(t, s, r, "native_collation", []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"k": {Text: "B"}, "note": {Text: "new"}}}}).NativeJSON)) {
		t.Fatal("native complete receipt is not valid JSON")
	}
}

// Context cancellation registration happens after save captures the input,
// before waiting for the document writer. This observes that boundary without
// introducing a production hook or reading caller values from another worker.
type nativeDataCaptureContext struct {
	context.Context
	once     sync.Once
	captured chan struct{}
	done     chan struct{}
}

func (c *nativeDataCaptureContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.captured) })
	return c.done
}

func TestNativeRQLiteQueuedDataWriteOwnsOriginalMapsKeysBytesAndFingerprints(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	nativeExecute(t, lease(t, s, 1), "CREATE TABLE captured_data(k BLOB PRIMARY KEY NOT NULL,note TEXT,raw BLOB);INSERT INTO captured_data VALUES(X'00FF','original',X'00FF')")
	d, r := nativeDataDocument(t, s, "captured_data")
	page := nativeDataPage(t, s, r, "captured_data")
	row, err := r.RowReader(context.Background(), p.ID, "main", "captured_data", *page.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	edits := []contract.DataEdit{{Kind: "update", Original: nativeValuesSnapshot(row), Values: map[string]contract.DataValue{"note": {Kind: contract.Text, Text: "captured"}, "raw": {Kind: contract.Binary, Binary: true, Bytes: []byte{255, 0, 1}}}}}
	d.writes <- struct{}{}
	var admissionRelease sync.Once
	unblock := func() { admissionRelease.Do(func() { <-d.writes }) }
	defer unblock()
	ctx := &nativeDataCaptureContext{Context: context.Background(), captured: make(chan struct{}), done: make(chan struct{})}
	type result struct {
		out *contract.DataEditResult
		err error
	}
	done := make(chan result, 1)
	go func() { out, err := r.Saver(ctx, p.ID, "main", "captured_data", edits); done <- result{out, err} }()
	select {
	case <-ctx.captured:
	case <-time.After(5 * time.Second):
		t.Fatal("queued writer did not capture original input")
	}
	// Mutate every caller-owned observation while the original save is parked.
	edits[0].Values["raw"].Bytes[0] = 7
	edits[0].Values["note"] = contract.DataValue{Text: "later caller value"}
	edits[0].Original.Ref.Values[0][0] = 7
	edits[0].Original.Ref.TypedValues[0].Bytes[0] = 7
	edits[0].Original.Original[0].Bytes[0] = 7
	*edits[0].Original.Fingerprints[0] = strings.Repeat("0", 64)
	edits[0].Original.Columns[0] = "later"
	edits[0].Original.Ref.Columns[0] = "later"
	unblock()
	select {
	case result := <-done:
		if result.err != nil || !result.out.CommitConfirmed || result.out.Applied != 1 || result.out.Outcomes[0].Original.Columns[0] != "k" || result.out.Outcomes[0].Original.Ref.TypedValues[0].Bytes[0] != 0 {
			t.Fatal("queued write changed its original observation", result.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("original queued write did not finish")
	}
	actual := nativeExecute(t, lease(t, s, 2), "SELECT hex(k),note,hex(raw) FROM captured_data")
	if actual.Sets[0].Rows[0][0].Text != "00FF" || actual.Sets[0].Rows[0][1].Text != "captured" || actual.Sets[0].Rows[0][2].Text != "FF0001" {
		t.Fatal("original queued request retained caller-owned write bytes")
	}
}

func TestNativeRQLiteAtomicLargeOriginalFingerprintAndTriggerRollback(t *testing.T) {
	p := nativeFixture(t, "")
	p.RQLiteOptions.DatabaseTimeoutMilliseconds, p.RQLiteOptions.RequestTimeoutSeconds = 120000, 180
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	nativeExecute(t, l, "CREATE TABLE large_write(id INTEGER PRIMARY KEY,note TEXT,raw BLOB);INSERT INTO large_write VALUES(1,'original',zeroblob(1048576)),(2,'second',NULL)")
	_, r := nativeDataDocument(t, s, "large_write")
	page := nativeDataPage(t, s, r, "large_write")
	if !page.Rows[0][2].Truncated || page.RowSnapshots[0].Fingerprints[2] == nil {
		t.Fatal("large original digest was not captured")
	}
	started := time.Now()
	nativeDataSave(t, s, r, "large_write", []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"raw": {Binary: true, Kind: contract.Binary, Bytes: []byte{0, 255}}}}})
	t.Logf("native atomic guard of complete 1 MiB original finished in %s", time.Since(started))
	nativeExecute(t, l, "CREATE TRIGGER reject_second BEFORE UPDATE ON large_write WHEN NEW.id=2 BEGIN SELECT RAISE(ABORT,'owned trigger rejection'); END")
	page = nativeDataPage(t, s, r, "large_write")
	out, err := r.Saver(context.Background(), p.ID, "main", "large_write", []contract.DataEdit{
		{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "must roll back"}}},
		{Kind: "update", Original: page.RowSnapshots[1], Values: map[string]contract.DataValue{"note": {Text: "native rejection"}}},
	})
	if err == nil || errors.Is(err, contract.ErrDataConflict) || out.CommitUncertain || out.Incomplete || out.CommitConfirmed || out.Applied != 0 || len(out.Outcomes) != 0 || !strings.Contains(out.NativeJSON, `"rows_affected":1`) {
		t.Fatal("native trigger rollback was misclassified or counted earlier changes", err)
	}
	actual := nativeExecute(t, l, "SELECT note,hex(raw) FROM large_write ORDER BY id")
	if actual.Sets[0].Rows[0][0].Text != "original" || actual.Sets[0].Rows[0][1].Text != "00FF" || actual.Sets[0].Rows[1][0].Text != "second" {
		t.Fatal("native failed trigger transaction did not roll back original batch")
	}
	nativeGuardCleanup(t, s)
}
