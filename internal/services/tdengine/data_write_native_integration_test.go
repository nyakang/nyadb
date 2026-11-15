package tdengine

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeDataWrite(t *testing.T, l *dataLease, edits []contract.DataEdit) *contract.DataEditResult {
	t.Helper()
	p, err := l.previewEdits(context.Background(), l.object.ProfileID, l.object.Catalog, l.object.Name, edits)
	if err != nil {
		t.Fatal("original native preview failed", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	r, err := l.submitEdits(context.Background(), l.object.ProfileID, l.object.Catalog, l.object.Name, p)
	progress, valid := contract.SequentialDataWriteProgress(r, len(edits))
	if err != nil || !valid || !progress.Complete || !contract.DataWriteComplete(r, len(edits)) || !r.RefreshRequired || r.CommitConfirmed || r.Verified {
		t.Fatal("original native acknowledgement missing", err, r)
	}
	for _, outcome := range r.Outcomes {
		if outcome.Saved != nil {
			t.Fatal("native acknowledgement fabricated saved row")
		}
	}
	if _, err = l.submitEdits(context.Background(), l.object.ProfileID, l.object.Catalog, l.object.Name, p); err == nil {
		t.Fatal("consumed original plan replayed")
	}
	return r
}

// Independent SQL streaming observes full values, beyond result previews.
func nativeDataSQLRows(t *testing.T, sql contract.QueryLease, db, query string) [][]contract.Cell {
	t.Helper()
	var rows [][]contract.Cell
	_, err := sql.ExecuteStream(context.Background(), db, query, func(_ int, _ []contract.Column, row []contract.Cell) error {
		copy := make([]contract.Cell, len(row))
		for i, cell := range row {
			copy[i] = cell.Clone()
		}
		rows = append(rows, copy)
		return nil
	})
	if err != nil {
		t.Fatal("independent full native observation failed", err)
	}
	return rows
}

func TestNativeTDengineDataWriteExactMergeNullEmptyAndDelete(t *testing.T) {
	p, password := nativeProfile(t)
	s := nativeSession(t, p, password)
	sql := nativeLease(t, s, 1)
	for _, precision := range []string{"ms", "us", "ns"} {
		t.Run(precision, func(t *testing.T) {
			db := nativeDatabase(t, sql, precision)
			nativeSQL(t, sql, db, "CREATE TABLE items(ts TIMESTAMP,i BIGINT,u BIGINT UNSIGNED,dec DECIMAL(38,10),txt VARCHAR(12000),uni NCHAR(64),bin VARBINARY(12000),n INT,b BOOL,f FLOAT,d DOUBLE)")
			o := s.Namespace(db).Object("table", "items")
			l := nativeDataDocument(t, s, o)
			timestamp := map[string]string{"ms": "2024-01-01T00:00:00.123Z", "us": "2024-01-01T00:00:00.123456Z", "ns": "2024-01-01T00:00:00.123456789Z"}[precision]
			values := map[string]contract.DataValue{
				"ts": {Kind: contract.DateTime, Text: timestamp}, "i": {Kind: contract.Integer, Text: "9007199254740993"}, "u": {Kind: contract.Integer, Text: "18446744073709551614"},
				"dec": {Kind: contract.Decimal, Text: "-12345678901234567890.1234567890"}, "txt": {Kind: contract.Text, Text: strings.Repeat("x", 6000)}, "uni": {Kind: contract.Text, Text: "猫'\\Ω\n"},
				"bin": {Kind: contract.Binary, Bytes: []byte{0, 255}, Binary: true}, "n": {Kind: contract.Null}, "b": {Kind: contract.Boolean, Text: "true"}, "f": {Kind: contract.Floating, Text: "8.6"}, "d": {Kind: contract.Floating, Text: "8.600000381469727"},
			}
			plan, err := l.previewEdits(context.Background(), p.ID, db, o.Name, []contract.DataEdit{{Kind: "insert", Values: values}})
			if err != nil {
				t.Fatal("native exact preview failed", err)
			}
			original := plan.Snapshot()
			values["i"] = contract.DataValue{Kind: contract.Integer, Text: "1"}
			values["bin"].Bytes[0] = 1
			copy := plan.Snapshot()
			copy.Edits[0].Values["bin"].Bytes[0] = 2
			copy.Items[0].Destination.Name = "foreign"
			if !reflect.DeepEqual(original, plan.Snapshot()) {
				t.Fatal("caller replaced original native request")
			}
			result, err := l.submitEdits(context.Background(), p.ID, db, o.Name, plan)
			if err != nil || !contract.DataWriteComplete(result, 1) {
				t.Fatal("native exact insert failed", err, result)
			}
			page, err := l.page(context.Background(), p.ID, db, o.Name, contract.DataOptions{Limit: 10})
			if err != nil || len(page.Rows) != 1 {
				t.Fatal("native original insert not visible", err, page)
			}
			row, err := l.row(context.Background(), p.ID, db, o.Name, *page.RowRefs[0])
			if err != nil || row.Values["ts"].Text != timestamp || row.Values["i"].Text != "9007199254740993" || row.Values["u"].Text != "18446744073709551614" || row.Values["dec"].Text != "-12345678901234567890.1234567890" || row.Values["uni"].Text != "猫'\\Ω\n" || !bytes.Equal(row.Values["bin"].Bytes, []byte{0, 255}) || !row.Values["n"].Null || row.Values["f"].Text != "8.6" || row.Values["d"].Text != "8.600000381469727" {
				t.Fatal("native exact values changed", err, row)
			}
			merge := nativeDataWrite(t, l, []contract.DataEdit{{Kind: "update", Original: row.Snapshot, Values: map[string]contract.DataValue{"i": {Kind: contract.Integer, Text: "43"}}}})
			read := nativeDataSQLRows(t, sql, db, "SELECT i,txt,uni,bin,dec FROM items")[0]
			if read[0].Text != "43" || len(read[1].Text) != 6000 || read[2].Text != "猫'\\Ω\n" || !bytes.Equal(read[3].Bytes, []byte{0, 255}) || read[4].Text != "-12345678901234567890.1234567890" {
				t.Fatal("native merge erased omitted fields", read)
			}
			page, err = l.page(context.Background(), p.ID, db, o.Name, contract.DataOptions{Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			cleared := nativeDataWrite(t, l, []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"i": {Kind: contract.Null}, "txt": {Kind: contract.Null}, "bin": {Kind: contract.Null}, "dec": {Kind: contract.Null}}}})
			read = nativeSQL(t, sql, db, "SELECT i,txt,bin,dec,uni FROM items").Sets[0].Rows[0]
			for _, c := range read[:4] {
				if c.Kind != contract.Null {
					t.Fatal("explicit NULL filtered out", read)
				}
			}
			if read[4].Text != "猫'\\Ω\n" {
				t.Fatal("explicit clearing changed omitted field")
			}
			page, err = l.page(context.Background(), p.ID, db, o.Name, contract.DataOptions{Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			empty := nativeDataWrite(t, l, []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"txt": {Kind: contract.Text}, "bin": {Kind: contract.Binary, Binary: true}}}})
			read = nativeSQL(t, sql, db, "SELECT txt,bin FROM items").Sets[0].Rows[0]
			if read[0].Kind != contract.Text || read[0].Text != "" || read[1].Kind != contract.Binary || len(read[1].Bytes) != 0 {
				t.Fatal("empty value became NULL", read)
			}
			page, err = l.page(context.Background(), p.ID, db, o.Name, contract.DataOptions{Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			deleted := nativeDataWrite(t, l, []contract.DataEdit{{Kind: "delete", Original: page.RowSnapshots[0]}})
			if n, err := l.count(context.Background(), p.ID, db, o.Name); err != nil || n != 0 {
				t.Fatal("native qualified delete failed", n, err)
			}
			nativeEvidence(t, "data-write-exact-"+precision, map[string]any{"insert_request": original, "insert": result, "merge": merge, "explicit_null": cleared, "empty_values": empty, "delete": deleted, "original_complete_row": row})
		})
	}
}

func TestNativeTDengineDataWriteStableOriginalChildAndRevokedMetadata(t *testing.T) {
	p, password := nativeProfile(t)
	s := nativeSession(t, p, password)
	sql := nativeLease(t, s, 1)
	db := nativeDatabase(t, sql, "ns")
	nativeSQL(t, sql, db, "CREATE STABLE meters(ts TIMESTAMP,k INT COMPOSITE KEY,v INT,note VARCHAR(64)) TAGS(site NCHAR(32));CREATE TABLE child_a USING meters TAGS('A');CREATE TABLE child_b USING meters TAGS('B');INSERT INTO child_a VALUES(1704067200123456789,1,11,'original A');INSERT INTO child_b VALUES(1704067200123456789,1,22,'original B')")
	o := s.Namespace(db).Object("stable", "meters")
	l := nativeDataDocument(t, s, o)
	page, err := l.page(context.Background(), p.ID, db, o.Name, contract.DataOptions{Limit: 10, SortColumn: "tbname"})
	if err != nil {
		t.Fatal(err)
	}
	result := nativeDataWrite(t, l, []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"v": {Kind: contract.Integer, Text: "44"}}}})
	if result.Sequential.Statements[0].Destination != s.Namespace(db).Object("table", "child_a") {
		t.Fatal("stable write destination crossed original child")
	}
	rows := nativeSQL(t, sql, db, "SELECT tbname,v,note,site FROM meters ORDER BY tbname").Sets[0].Rows
	if len(rows) != 2 || rows[0][1].Text != "44" || rows[0][2].Text != "original A" || rows[0][3].Text != "A" || rows[1][1].Text != "22" || rows[1][3].Text != "B" {
		t.Fatal("stable write changed TAGs or sibling", rows)
	}
	page, err = l.page(context.Background(), p.ID, db, o.Name, contract.DataOptions{Limit: 10, SortColumn: "tbname"})
	if err != nil {
		t.Fatal(err)
	}
	for _, edit := range []contract.DataEdit{
		{Kind: "delete", Original: page.RowSnapshots[0]},
		{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"site": {Kind: contract.Text, Text: "foreign"}}},
		{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"k": {Kind: contract.Integer, Text: "2"}}},
		{Kind: "insert", Values: map[string]contract.DataValue{"tbname": {Kind: contract.Text, Text: "missing_child"}, "ts": {Kind: contract.DateTime, Text: "2024-01-01T00:00:01Z"}, "k": {Kind: contract.Integer, Text: "1"}}},
	} {
		if plan, err := l.previewEdits(context.Background(), p.ID, db, o.Name, []contract.DataEdit{edit}); err == nil {
			_ = plan.Close()
			t.Fatal("unsupported stable write admitted", edit)
		}
	}
	insert := nativeDataWrite(t, l, []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"tbname": {Kind: contract.Text, Text: "child_b"}, "ts": {Kind: contract.DateTime, Text: "2024-01-01T00:00:01Z"}, "k": {Kind: contract.Integer, Text: "2"}, "v": {Kind: contract.Integer, Text: "55"}}}})
	if insert.Sequential.Statements[0].Destination.Name != "child_b" {
		t.Fatal("approved child insert changed destination")
	}
	page, err = l.page(context.Background(), p.ID, db, o.Name, contract.DataOptions{Limit: 10, SortColumn: "tbname"})
	if err != nil {
		t.Fatal(err)
	}
	edit := contract.DataEdit{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"v": {Kind: contract.Integer, Text: "66"}}}
	plan, err := l.previewEdits(context.Background(), p.ID, db, o.Name, []contract.DataEdit{edit})
	if err != nil {
		t.Fatal(err)
	}
	nativeSQL(t, sql, db, "ALTER STABLE meters ADD COLUMN added INT")
	revoked, err := l.submitEdits(context.Background(), p.ID, db, o.Name, plan)
	if !errors.Is(err, contract.ErrDataConflict) || revoked.Applied != 0 || revoked.Sequential.Statements[0].State != contract.DataWriteNotSent || revoked.CommitUncertain {
		t.Fatal("revoked metadata dispatched original write", err, revoked)
	}
	if _, err = l.previewEdits(context.Background(), p.ID, db, o.Name, []contract.DataEdit{edit}); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("incomplete original batch reopened for replay", err)
	}
	nativeEvidence(t, "data-write-stable", map[string]any{"update": result, "sibling_and_tags": rows, "insert": insert, "revoked_plan": revoked})
}

func TestNativeTDengineDataWritePartialFailureRetainsOriginalPrefix(t *testing.T) {
	p, password := nativeProfile(t)
	s := nativeSession(t, p, password)
	sql := nativeLease(t, s, 1)
	db := nativeDatabase(t, sql, "ns")
	nativeSQL(t, sql, db, "CREATE TABLE items(ts TIMESTAMP,v INT)")
	l := nativeDataDocument(t, s, s.Namespace(db).Object("table", "items"))
	edits := []contract.DataEdit{}
	for _, timestamp := range []string{"2024-01-01T00:00:00.123456789Z", "1970-01-01T00:00:00.000000001Z", "2024-01-01T00:00:00.123456791Z"} {
		edits = append(edits, contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"ts": {Kind: contract.DateTime, Text: timestamp}, "v": {Kind: contract.Integer, Text: "42"}}})
	}
	plan, err := l.previewEdits(context.Background(), p.ID, db, "items", edits)
	if err != nil {
		t.Fatal(err)
	}
	r, err := l.submitEdits(context.Background(), p.ID, db, "items", plan)
	progress, valid := contract.SequentialDataWriteProgress(r, 3)
	if !isServerError(err) || !valid || progress.Acknowledged != 1 || progress.Complete || progress.Unknown || !r.Incomplete || r.CommitUncertain || r.Sequential.Statements[1].State != contract.DataWriteFailed || r.Sequential.Statements[2].State != contract.DataWriteNotSent {
		t.Fatal("original sequential prefix/failure lost", err, r)
	}
	if n, err := l.count(context.Background(), p.ID, db, "items"); err != nil || n != 1 {
		t.Fatal("successful native prefix rolled back", n, err)
	}
	if _, err := l.previewEdits(context.Background(), p.ID, db, "items", edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("failed batch permitted replay", err)
	}
	nativeEvidence(t, "data-write-partial", r)
}

func TestNativeTDengineDataWriteBatchPreviewBudgetAndDuplicateKeys(t *testing.T) {
	p, password := nativeProfile(t)
	s := nativeSession(t, p, password)
	sql := nativeLease(t, s, 1)
	db := nativeDatabase(t, sql, "ns")
	nativeSQL(t, sql, db, "CREATE TABLE items(ts TIMESTAMP,txt VARCHAR(12000))")
	l := nativeDataDocument(t, s, s.Namespace(db).Object("table", "items"))
	var edits []contract.DataEdit
	for i := 0; i < 128; i++ {
		edits = append(edits, contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{
			"ts":  {Kind: contract.DateTime, Text: time.Unix(1704067200, 123456789+int64(i)).UTC().Format(time.RFC3339Nano)},
			"txt": {Kind: contract.Text, Text: strings.Repeat("x", 9000)},
		}})
	}
	plan, err := l.previewEdits(context.Background(), p.ID, db, "items", edits)
	if err != nil || len(plan.Snapshot().NativeJSON) <= contract.DataEditMaxBytes {
		t.Fatal("complete plan inherited the per-editor byte limit", err)
	}
	if _, err = l.previewEdits(context.Background(), p.ID, db, "items", edits); !errors.Is(err, contract.ErrSQLSessionLimit) {
		t.Fatal("document reserved concurrent original previews", err)
	}
	if err = plan.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = l.previewEdits(context.Background(), p.ID, db, "items", []contract.DataEdit{edits[0], edits[0]}); err == nil {
		t.Fatal("duplicate destination/key admitted into one batch")
	}
	if n, err := l.count(context.Background(), p.ID, db, "items"); err != nil || n != 0 {
		t.Fatal("preview submitted a native write", n, err)
	}
	nativeEvidence(t, "data-write-preview-budget", map[string]any{"rows": len(edits), "each_complete_value_bytes": 9000, "preview_limit": dataWritePlanMaxBytes, "preview_closed_without_writes": true, "duplicate_original_destination_rejected": true})
}

func TestNativeTDengineDataWriteLostAcknowledgementCloseAndSiblingIsolation(t *testing.T) {
	p, password := nativeProfile(t)
	s := nativeSession(t, p, password)
	sql := nativeLease(t, s, 1)
	db := nativeDatabase(t, sql, "ns")
	nativeSQL(t, sql, db, "CREATE TABLE items(ts TIMESTAMP,v INT)")
	o := s.Namespace(db).Object("table", "items")
	statement := "INSERT INTO " + qualified(db, "items") + "(`ts`,`v`) VALUES(1704067200123456789,42)"
	held, release := make(chan json.RawMessage, 1), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var opened, writes atomic.Int64
	upgrader := websocket.Upgrader{}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downstream, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer downstream.Close()
		upstream, _, err := websocket.DefaultDialer.Dial("ws://"+net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))+"/ws", nil)
		if err != nil {
			return
		}
		defer upstream.Close()
		opened.Add(1)
		var request atomic.Uint64
		clientDone := make(chan struct{})
		go func() {
			defer close(clientDone)
			defer upstream.Close()
			defer downstream.Close()
			for {
				kind, raw, e := downstream.ReadMessage()
				if e != nil {
					return
				}
				if kind == websocket.BinaryMessage && len(raw) >= 30 && binary.LittleEndian.Uint64(raw[16:]) == 6 && string(raw[30:]) == statement {
					request.Store(binary.LittleEndian.Uint64(raw))
					writes.Add(1)
				}
				if upstream.WriteMessage(kind, raw) != nil {
					return
				}
			}
		}()
		defer func() { downstream.Close(); upstream.Close(); <-clientDone }()
		for {
			kind, raw, e := upstream.ReadMessage()
			if e != nil {
				return
			}
			var response nativeResponse
			if kind == websocket.TextMessage && json.Unmarshal(raw, &response) == nil && request.Load() != 0 && response.Request == request.Load() && response.Action == "binary_query" {
				held <- append(json.RawMessage(nil), raw...)
				<-release
				return
			}
			if downstream.WriteMessage(kind, raw) != nil {
				return
			}
		}
	}))
	t.Cleanup(proxy.Close)
	u, _ := url.Parse(proxy.URL)
	port, _ := strconv.ParseUint(u.Port(), 10, 16)
	throughProxy := p
	throughProxy.Host, throughProxy.Port = u.Hostname(), uint16(port)
	original := nativeSession(t, throughProxy, password)
	l := nativeDataDocument(t, original, o)
	sibling := nativeDataDocument(t, original, o)
	edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"ts": {Kind: contract.DateTime, Text: "2024-01-01T00:00:00.123456789Z"}, "v": {Kind: contract.Integer, Text: "42"}}}}
	plan, err := l.previewEdits(context.Background(), p.ID, db, o.Name, edits)
	if err != nil {
		t.Fatal(err)
	}
	type completion struct {
		r   *contract.DataEditResult
		err error
	}
	completed := make(chan completion, 1)
	go func() {
		r, err := l.submitEdits(context.Background(), p.ID, db, o.Name, plan)
		completed <- completion{r, err}
	}()
	var receipt json.RawMessage
	select {
	case receipt = <-held:
	case <-time.After(15 * time.Second):
		t.Fatal("original native write acknowledgement not observed")
	}
	var response nativeResponse
	if json.Unmarshal(receipt, &response) != nil || response.Code != 0 || response.Affected != 1 || !response.Update {
		t.Fatal("held frame is not original accepted write")
	}
	if n, err := s.count(context.Background(), p.ID, db, o.Name); err != nil || n != 1 {
		t.Fatal("independent read did not observe native write", n, err)
	}
	before := opened.Load()
	closed := make(chan struct{})
	go func() { _ = l.Close(); close(closed) }()
	var out completion
	select {
	case out = <-completed:
	case <-time.After(10 * time.Second):
		t.Fatal("original close did not settle write executor")
	}
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("original close did not await executor")
	}
	progress, valid := contract.SequentialDataWriteProgress(out.r, 1)
	if !valid || !progress.Unknown || progress.Complete || !errors.Is(out.err, contract.ErrDataCommitUncertain) || !errors.Is(out.err, context.Canceled) || out.r.Applied != 0 || out.r.Sequential.Statements[0].NativeJSON != "" {
		t.Fatal("later observed state promoted original lost acknowledgement", out.err, out.r)
	}
	if _, err = l.submitEdits(context.Background(), p.ID, db, o.Name, plan); err == nil || opened.Load() != before || writes.Load() != 1 {
		t.Fatal("closed original plan reconnected/replayed", err)
	}
	page, err := sibling.page(context.Background(), p.ID, db, o.Name, contract.DataOptions{Limit: 10})
	if err != nil || len(page.Rows) != 1 || page.Rows[0][1].Value.Text != "42" {
		t.Fatal("closed write retired sibling read", err, page)
	}
	nativeSQL(t, sql, db, "SELECT * FROM items")
	if writes.Load() != 1 {
		t.Fatal("later sibling reads replayed original write")
	}
	releaseOnce.Do(func() { close(release) })
	nativeEvidence(t, "data-write-lost-original", map[string]any{"held_original_acknowledgement": receipt, "original_result": out.r, "error": out.err.Error(), "sibling_page": page, "native_writes": writes.Load()})
}
