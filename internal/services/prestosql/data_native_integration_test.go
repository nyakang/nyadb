package prestosql

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
)

func nativeDataSession(t *testing.T, p Provider, c connections.PrestoSQLManagedConfig) (*Session, contract.QueryLease, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	t.Cleanup(cancel)
	s, err := p.OpenManaged(ctx, 65, c, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	q, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	return s, q, ctx
}
func nativeDataDocument(t *testing.T, s *Session, name string) (*dataLease, contract.DataRuntime, string) {
	t.Helper()
	n := s.Namespace("")
	lease, err := s.ReserveDataDocument(n.Object("table", name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	return lease.(*dataLease), lease.Runtime(), namespaceLabel(n)
}
func nativeDataInsert(t *testing.T, ctx context.Context, r contract.DataRuntime, label, table string, edits []contract.DataEdit) *contract.DataEditResult {
	t.Helper()
	plan, err := r.PreviewEdits(ctx, 65, label, table, edits)
	if err != nil {
		t.Fatal("native insert preview", err)
	}
	defer plan.Close()
	result, err := r.SubmitEdits(ctx, 65, label, table, plan)
	if err != nil || result == nil || result.Incomplete || result.CommitUncertain || result.Applied != len(edits) || result.Sequential == nil {
		t.Fatal("native insert", err, result)
	}
	for _, step := range result.Sequential.Statements {
		if step.State != contract.DataWriteAcknowledged || step.AffectedRows != 1 || !strings.Contains(step.NativeJSON, `"OriginalResponse":"{`) {
			t.Fatal("complete original insert ACK missing", step)
		}
	}
	return result
}
func TestPrestoSQLPrivateDataNativeTypedInsertPageAndCount(t *testing.T) {
	f, p, c, _ := nativeProvider(t)
	s, q, ctx := nativeDataSession(t, p, c)
	const name = "data_%猫65"
	queryNative(t, q, "", `CREATE TABLE memory.default."data_%猫65" (id bigint, amount decimal(38,20), payload varbinary, note varchar, padded char(4), d date, tm time(3), ts timestamp(6), tz timestamp(3) with time zone, flag boolean, f real, g double, j json, a array(decimal(8,3)), m map(varchar,array(bigint)), r row("名" bigint, text varchar), aj array(json), rj row("名" json, nested array(json))); CREATE TABLE memory.default."data_ax猫65" (foreign_column varchar)`)
	l, r, label := nativeDataDocument(t, s, name)
	edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{
		"id": {Kind: contract.Integer, Text: "9223372036854775807"}, "amount": {Kind: contract.Decimal, Text: "9007199254740993.12345678901234567890"}, "payload": {Kind: contract.Binary, Bytes: []byte{0, 255, 128}}, "note": {Kind: contract.Text, Text: "猫'a;b"}, "padded": {Kind: contract.Text, Text: "x"}, "d": {Kind: contract.DateTime, Text: "2024-02-29"}, "tm": {Kind: contract.DateTime, Text: "12:34:56.789"}, "ts": {Kind: contract.DateTime, Text: "2024-02-29 12:34:56.123456"}, "tz": {Kind: contract.DateTime, Text: "2024-02-29 12:34:56.789 UTC"}, "flag": {Kind: contract.Boolean, Text: "true"}, "f": {Kind: contract.Floating, Text: "NaN"}, "g": {Kind: contract.Floating, Text: "Infinity"}, "j": {Kind: contract.JSON, Text: `{"n":9007199254740993}`}, "a": {Kind: contract.JSON, Text: `["1.230",null]`}, "m": {Kind: contract.JSON, Text: `{"猫":[9223372036854775807,null]}`}, "r": {Kind: contract.JSON, Text: `[7,"a;b"]`}, "aj": {Kind: contract.JSON, Text: `["null",null,"{\"n\":9007199254740993}"]`}, "rj": {Kind: contract.JSON, Text: `["null",["{\"x\":true}",null]]`},
	}}}
	plan, err := r.PreviewEdits(ctx, 65, label, name, edits)
	if err != nil {
		t.Fatal(err)
	}
	original := plan.Snapshot()
	edits[0].Values["payload"].Bytes[0] = 99
	changed := plan.Snapshot()
	changed.Edits[0].Values["payload"].Bytes[1] = 98
	changed.Edits[0].Values["id"] = contract.DataValue{Kind: contract.Integer, Text: "42"}
	result, err := r.SubmitEdits(ctx, 65, label, name, plan)
	if err != nil || result == nil || result.Applied != 1 || result.Incomplete {
		t.Fatal("immutable original insert", err, result)
	}
	if _, err = r.SubmitEdits(ctx, 65, label, name, plan); err == nil {
		t.Fatal("original plan replayed")
	}
	page, err := r.AdvancedLoader(ctx, 65, label, name, contract.DataOptions{Limit: 1, SortColumn: "id"})
	if err != nil {
		t.Fatal("typed page", err)
	}
	if len(page.Rows) != 1 || len(page.Columns) != 18 || page.HasMore || !page.LiveRead {
		t.Fatal("qualified page shape", page)
	}
	row := page.Rows[0]
	expected := map[int]string{0: "9223372036854775807", 1: "9007199254740993.12345678901234567890", 3: "猫'a;b", 4: "x   ", 5: "2024-02-29", 6: "12:34:56.789", 7: "2024-02-29 12:34:56.123456", 8: "2024-02-29 12:34:56.789 UTC", 9: "true", 10: "NaN", 11: "Infinity", 12: `{"n":9007199254740993}`, 13: `["1.230",null]`, 14: `{"猫":[9223372036854775807,null]}`, 15: `[7,"a;b"]`, 16: `["null",null,"{\"n\":9007199254740993}"]`, 17: `["null",["{\"x\":true}",null]]`}
	for i, want := range expected {
		if row[i].Value == nil || row[i].Value.Text != want {
			t.Fatalf("typed field %s: %+v, want %s", page.Columns[i], row[i].Value, want)
		}
	}
	if !bytes.Equal(row[2].Value.Bytes, []byte{0, 255, 128}) {
		t.Fatal("caller mutated original binary")
	}
	longText := strings.Repeat("猫", 1025)
	longBytes := bytes.Repeat([]byte{255}, 1025)
	more := nativeDataInsert(t, ctx, r, label, name, []contract.DataEdit{
		{Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "1"}, "payload": {Kind: contract.Binary, Bytes: []byte{}}, "j": {Kind: contract.JSON, Text: "null"}}},
		{Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "2"}, "payload": {Kind: contract.Binary, Bytes: longBytes}, "note": {Kind: contract.Text, Text: longText}}},
	})
	first, err := r.AdvancedLoader(ctx, 65, label, name, contract.DataOptions{Limit: 1, SortColumn: "id"})
	if err != nil || len(first.Rows) != 1 || !first.HasMore || first.Rows[0][0].Text != "1" || first.Rows[0][2].Null || first.Rows[0][2].Value.Bytes == nil || !first.Rows[0][3].Null || first.Rows[0][12].Null || first.Rows[0][12].Value.Text != "null" {
		t.Fatal("empty binary, omitted SQL NULL or JSON null lost", err, first)
	}
	second, err := r.AdvancedLoader(ctx, 65, label, name, contract.DataOptions{Limit: 1, Offset: 1, SortColumn: "id"})
	if err != nil || !second.HasMore || !second.Rows[0][2].Truncated || len(second.Rows[0][2].Value.Bytes) != 1024 || !second.Rows[0][3].Truncated || second.Rows[0][3].Value.Text != strings.Repeat("猫", 1024) {
		t.Fatal("bounded preview", err)
	}
	filtered, err := r.AdvancedLoader(ctx, 65, label, name, contract.DataOptions{Limit: 2, FilterColumn: "note", FilterValue: "猫'a;b"})
	if err != nil || len(filtered.Rows) != 1 || filtered.Rows[0][0].Text != "9223372036854775807" {
		t.Fatal("typed quoted filter", err)
	}
	count, err := r.Count(ctx, 65, label, name)
	if err != nil || count != 3 {
		t.Fatal("independent count", err, count)
	}
	if l.unresolved != nil {
		t.Fatal("successful writes marked unknown")
	}
	providerProof(t, f, "data-types.json", map[string]any{"snapshot": original, "first_write": result, "more_writes": more, "typed_page": page, "empty_page": first, "bounded_page": second, "filtered_page": filtered, "count": count})
	queryNative(t, q, "", `DROP TABLE memory.default."data_%猫65"; DROP TABLE memory.default."data_ax猫65"`)
	_ = s.Close()
	if err = p.Store.Remove(ctx, "prestosql", c.DriverVersion); err != nil {
		t.Fatal(err)
	}
}

func TestPrestoSQLPrivateDataNativeAcknowledgementAndConsumedPlan(t *testing.T) {
	f, p, c, _ := nativeProvider(t)
	observe := p.Observe
	var armed atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	p.Observe = func(method string, receipt jdbcplugin.Receipt) {
		observe(method, receipt)
		if armed.Load() && strings.HasPrefix(method, "data-") && strings.HasSuffix(method, "/executeQuery") {
			value, err := decodeResult(receipt)
			if err == nil && value.completed && value.affectedRows == 1 {
				once.Do(func() { close(entered); <-release })
			}
		}
	}
	s, q, ctx := nativeDataSession(t, p, c)
	queryNative(t, q, "", `CREATE TABLE memory.default.ack_data65 (id bigint)`)
	_, r, label := nativeDataDocument(t, s, "ack_data65")
	edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "65"}}}}
	plan, err := r.PreviewEdits(ctx, 65, label, "ack_data65", edits)
	if err != nil {
		t.Fatal(err)
	}
	active, cancel := context.WithCancel(ctx)
	defer cancel()
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	type outcome struct {
		result *contract.DataEditResult
		err    error
	}
	done := make(chan outcome, 1)
	armed.Store(true)
	go func() { v, e := r.SubmitEdits(active, 65, label, "ack_data65", plan); done <- outcome{v, e} }()
	select {
	case <-entered:
	case <-time.After(45 * time.Second):
		t.Fatal("real INSERT acknowledgement missing")
	}
	_ = plan.Close()
	if _, err = r.PreviewEdits(ctx, 65, label, "ack_data65", edits); !errors.Is(err, contract.ErrSQLSessionLimit) {
		t.Fatal("consumed plan admitted another write", err)
	}
	cancel()
	unblock()
	var got outcome
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("acknowledged executor failed to settle")
	}
	if got.result == nil || got.result.Applied != 1 || got.result.Incomplete || got.result.CommitUncertain || got.result.Sequential.Statements[0].State != contract.DataWriteAcknowledged {
		t.Fatal("actual native ACK lost to caller cancellation", got.err, got.result)
	}
	n, err := r.Count(ctx, 65, label, "ack_data65")
	if err != nil || n != 1 {
		t.Fatal("acknowledged write replayed", err, n)
	}
	providerProof(t, f, "data-acknowledgement.json", map[string]any{"receipt": got.result, "count": n, "consumed_plan_blocks_new_preview": true, "caller_cancelled_after_native_ack": true})
	queryNative(t, q, "", `DROP TABLE memory.default.ack_data65`)
	_ = s.Close()
	if err = p.Store.Remove(ctx, "prestosql", c.DriverVersion); err != nil {
		t.Fatal(err)
	}
}

func TestPrestoSQLPrivateDataNativePartialBatchMetadataChange(t *testing.T) {
	f, p, c, _ := nativeProvider(t)
	observe := p.Observe
	var armed atomic.Bool
	var afterFirst func()
	p.Observe = func(method string, receipt jdbcplugin.Receipt) {
		observe(method, receipt)
		if armed.Load() && strings.HasPrefix(method, "data-") && strings.HasSuffix(method, "/executeQuery") {
			result, err := decodeResult(receipt)
			if err == nil && result.completed && result.affectedRows == 1 && armed.CompareAndSwap(true, false) {
				afterFirst()
			}
		}
	}
	s, q, ctx := nativeDataSession(t, p, c)
	const name = "partial_data65"
	queryNative(t, q, "", `CREATE TABLE memory.default.partial_data65 (id bigint)`)
	_, r, label := nativeDataDocument(t, s, name)
	edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "1"}}}, {Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "2"}}}, {Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "3"}}}}
	plan, err := r.PreviewEdits(ctx, 65, label, name, edits)
	if err != nil {
		t.Fatal(err)
	}
	afterFirst = func() {
		queryNative(t, q, "", `CREATE TABLE memory.default.partial_copy65 AS SELECT id, CAST(NULL AS BIGINT) AS new_field FROM memory.default.partial_data65; DROP TABLE memory.default.partial_data65; CREATE TABLE memory.default.partial_data65 AS SELECT * FROM memory.default.partial_copy65; DROP TABLE memory.default.partial_copy65`)
	}
	armed.Store(true)
	result, err := r.SubmitEdits(ctx, 65, label, name, plan)
	if !errors.Is(err, contract.ErrDataConflict) || result == nil || result.Applied != 1 || !result.Incomplete || result.CommitUncertain || len(result.Sequential.Statements) != 3 || result.Sequential.Statements[0].State != contract.DataWriteAcknowledged || result.Sequential.Statements[1].State != contract.DataWriteNotSent || result.Sequential.Statements[2].State != contract.DataWriteNotSent {
		t.Fatal("metadata change lost partial prefix or dispatched suffix", err, result)
	}
	if _, err = r.PreviewEdits(ctx, 65, label, name, edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("incomplete batch admitted replay", err)
	}
	count, err := r.Count(ctx, 65, label, name)
	if err != nil || count != 1 {
		t.Fatal("partial write was replayed", err, count)
	}
	page, err := r.Loader(ctx, 65, label, name, 0, 10)
	if err != nil || len(page.Columns) != 2 || len(page.Rows) != 1 || page.Rows[0][0].Text != "1" || !page.Rows[0][1].Null {
		t.Fatal("original schema change not observed", err)
	}
	providerProof(t, f, "data-partial-metadata.json", map[string]any{"receipt": result, "count": count, "page": page, "later_statements_not_sent": true})
	queryNative(t, q, "", `DROP TABLE memory.default.partial_data65`)
	_ = s.Close()
	if err = p.Store.Remove(ctx, "prestosql", c.DriverVersion); err != nil {
		t.Fatal(err)
	}
}
