package rqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestNativeRQLiteBoundedDataReadsAndCompleteOriginalSnapshots(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	nativeExecute(t, l, `CREATE TABLE original_grid(id INTEGER PRIMARY KEY,txt TEXT,raw BLOB,dynamic,empty TEXT,missing TEXT);
INSERT INTO original_grid VALUES(1,CAST(X'610062' AS TEXT),X'610062',CAST(1 AS REAL),'',NULL);
INSERT INTO original_grid VALUES(9007199254740993,replace(hex(zeroblob(256)),'0','汉'),zeroblob(1536),9223372036854775807,'',NULL);`)
	o := contract.ObjectRef{ProfileID: p.ID, Catalog: "main", Kind: "table", Name: "original_grid"}
	d, err := s.ReserveDataDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	r := d.Runtime()
	if r.Saver == nil || !r.Capabilities.EditableTables || !r.NativeReceipts || r.Verify == nil || r.Transactions.Begin != nil || !r.Capabilities.Keyset {
		t.Fatal("original document data write capabilities or transaction scope changed")
	}
	page, err := r.AdvancedLoader(context.Background(), p.ID, "main", o.Name, contract.DataOptions{Limit: 1})
	if err != nil || len(page.Rows) != 1 || !page.HasMore || !page.StableOrder || page.RowRefs[0] == nil || page.RowSnapshots[0] == nil {
		t.Fatalf("original qualified page failed: %v", err)
	}
	if page.Rows[0][1].Value.Text != "a\x00b" || !bytes.Equal(page.Rows[0][2].Value.Bytes, []byte("a\x00b")) || page.Rows[0][3].Value.Kind != contract.Floating || page.Rows[0][4].Null || !page.Rows[0][5].Null {
		t.Fatal("native storage type, NUL, empty or NULL identity changed")
	}
	for _, test := range []struct {
		column int
		tag    string
		value  []byte
	}{{1, "text", []byte("a\x00b")}, {2, "blob", []byte("a\x00b")}, {4, "text", nil}} {
		digest := sha256.Sum256(append([]byte(test.tag+"\x00"), test.value...))
		if page.RowSnapshots[0].Fingerprints[test.column] == nil || *page.RowSnapshots[0].Fingerprints[test.column] != hex.EncodeToString(digest[:]) {
			t.Fatal("complete original type-tagged digest mismatch")
		}
	}
	if *page.RowSnapshots[0].Fingerprints[1] == *page.RowSnapshots[0].Fingerprints[2] || page.RowSnapshots[0].Fingerprints[5] != nil {
		t.Fatal("TEXT/BLOB or NULL fingerprint identities were conflated")
	}
	full, err := r.RowReader(context.Background(), p.ID, "main", o.Name, *page.RowRefs[0])
	if err != nil || !reflect.DeepEqual(full.Snapshot, page.RowSnapshots[0]) || full.Values["txt"].Text != "a\x00b" {
		t.Fatalf("complete editing read changed its original page snapshot: %v", err)
	}
	second, err := r.Loader(context.Background(), p.ID, "main", o.Name, 1, 1)
	if err != nil || len(second.Rows) != 1 || second.HasMore || second.Rows[0][0].Value.Text != "9007199254740993" || second.Rows[0][3].Value.Text != "9223372036854775807" {
		t.Fatalf("exact integer offset page failed: %v", err)
	}
	if len(second.Rows[0][1].Value.Text) != 1023 || !second.Rows[0][1].Truncated || len(second.Rows[0][2].Value.Bytes) != 1024 || !second.Rows[0][2].Truncated {
		t.Fatal("original byte-bounded preview changed or lost truncation")
	}
	full, err = r.RowReader(context.Background(), p.ID, "main", o.Name, *second.RowRefs[0])
	if err != nil || len(full.Values["txt"].Text) != 1536 || len(full.Values["raw"].Bytes) != 1536 || !reflect.DeepEqual(full.Snapshot, second.RowSnapshots[0]) {
		t.Fatalf("full value did not retain the bounded page's complete fingerprint: %v", err)
	}
	for _, test := range []struct {
		field  string
		offset int64
		limit  int
		text   string
		binary bool
	}{{"txt", 0, 2, "a\x00", false}, {"txt", 2, 1, "b", false}, {"raw", 0, 2, "0x6100", true}} {
		chunk, e := r.Chunk(context.Background(), p.ID, "main", o.Name, test.field, *page.RowRefs[0], test.offset, test.limit)
		if e != nil || chunk.Text != test.text || chunk.Binary != test.binary || chunk.TotalUnits != 3 || chunk.NextOffset != test.offset+int64(test.limit) {
			t.Fatalf("explicit original chunk failed: %v", e)
		}
	}
	chunk, err := r.Chunk(context.Background(), p.ID, "main", o.Name, "txt", *second.RowRefs[0], 0, 1024)
	if err != nil || len(chunk.Text) != 1023 || chunk.NextOffset != 1023 || chunk.TotalUnits != 1536 || !chunk.HasMore {
		t.Fatalf("UTF-8 byte chunk boundary failed: %v", err)
	}
	if _, err := r.Chunk(context.Background(), p.ID, "main", o.Name, "txt", *second.RowRefs[0], 1, 1024); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("an offset inside a UTF-8 code point published replacement bytes")
	}
	count, err := r.Count(context.Background(), p.ID, "main", o.Name)
	if err != nil || count != 2 {
		t.Fatal("original table count failed")
	}
	filtered, err := r.AdvancedLoader(context.Background(), p.ID, "main", o.Name, contract.DataOptions{Limit: 3, Filters: []contract.DataFilterRule{{Column: "id", Mode: "equal", Value: "9007199254740993"}}, Sorts: []contract.DataSortRule{{Column: "id", Descending: true}}})
	if err != nil || len(filtered.Rows) != 1 || strings.Contains(filtered.QuerySQL, "9007199254740993") || !strings.Contains(filtered.QuerySQL, "?") {
		t.Fatalf("filtered page leaked parameters or selected wrong identity: %v", err)
	}
	readerProfile := p
	readerProfile.Username, readerProfile.RQLiteOptions.ReadOnly = "reader", true
	reader := openNative(t, readerProfile, "reader-password")
	readerPage, err := reader.loadData(context.Background(), p.ID, "main", o.Name, 0, 1)
	if err != nil || len(readerPage.Rows) != 1 || readerPage.RowSnapshots[0].Metadata == page.RowSnapshots[0].Metadata {
		t.Fatalf("query-only data reads failed or escaped their original session scope: %v", err)
	}
	if _, err := r.Loader(context.Background(), p.ID+1, "main", o.Name, 0, 1); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("document escaped its original profile")
	}
	if keyed, err := r.AdvancedLoader(context.Background(), p.ID, "main", o.Name, contract.DataOptions{Limit: 1, UseKeyset: true}); err != nil || !keyed.KeysetAvailable || keyed.NextCursor == nil {
		t.Fatal("native cursor capability has no original boundary", err)
	}
	nativeExecute(t, l, "UPDATE original_grid SET txt='changed' WHERE id=1")
	changed, err := r.RowReader(context.Background(), p.ID, "main", o.Name, *page.RowRefs[0])
	if err != nil || reflect.DeepEqual(changed.Snapshot, page.RowSnapshots[0]) {
		t.Fatal("a racing complete value change retained the old row observation")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Count(context.Background(), p.ID, "main", o.Name); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed original document admitted a read")
	}
}

func TestNativeRQLiteExactCollationIdentitySchemaAndFullReadBounds(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	nativeExecute(t, l, `CREATE TABLE native_keys(k TEXT COLLATE NOCASE NOT NULL,note TEXT);
CREATE UNIQUE INDEX exact_key ON native_keys(k COLLATE BINARY);
INSERT INTO native_keys VALUES('a','lower'),('A','upper');`)
	o := contract.ObjectRef{ProfileID: p.ID, Catalog: "main", Kind: "table", Name: "native_keys"}
	page, err := s.pageData(context.Background(), p.ID, "main", o.Name, contract.DataOptions{Object: o, Limit: 3})
	if err != nil || len(page.Rows) != 2 || page.Rows[0][0].Text != "A" || page.Rows[1][0].Text != "a" {
		t.Fatalf("binary identity tie breaker failed: %v", err)
	}
	for i, value := range []string{"upper", "lower"} {
		row, e := s.readDataRow(context.Background(), p.ID, "main", o.Name, *page.RowRefs[i])
		if e != nil || row.Values["note"].Text != value {
			t.Fatalf("column collation matched another original key: %v", e)
		}
	}
	table, err := s.table(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	source, err := dataSource(table, contract.DataOptions{Limit: 3}, false)
	if err != nil {
		t.Fatal(err)
	}
	nativeExecute(t, l, "ALTER TABLE native_keys ADD COLUMN extra TEXT")
	if _, err := s.readDataProjection(context.Background(), table, source, 1024, 4, false); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("old metadata was attached to a changed native schema")
	}
	nativeExecute(t, l, "CREATE TABLE oversized(id INTEGER PRIMARY KEY,one BLOB,two BLOB);INSERT INTO oversized VALUES(1,zeroblob(600000),zeroblob(600000))")
	o = contract.ObjectRef{ProfileID: p.ID, Catalog: "main", Kind: "table", Name: "oversized"}
	ref := contract.RowRef{Object: o, Database: "main", Table: o.Name, Columns: []string{"id"}, Values: [][]byte{[]byte("1")}, TypedValues: []contract.Cell{{Kind: contract.Integer, Text: "1"}}}
	started := time.Now()
	if _, err := s.readDataRow(context.Background(), p.ID, "main", o.Name, ref); !errors.Is(err, contract.ErrDataEditLimit) {
		t.Fatalf("total full-row bound was not enforced in the original query: %v", err)
	}
	t.Logf("native oversized full row refused before full digest work in %s", time.Since(started))
	nativeExecute(t, l, "CREATE TABLE invalid_text(id INTEGER PRIMARY KEY,txt TEXT);INSERT INTO invalid_text VALUES(1,CAST(X'80FF' AS TEXT))")
	if _, err := s.loadData(context.Background(), p.ID, "main", "invalid_text", 0, 1); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatalf("invalid original UTF-8 was silently normalized: %v", err)
	}
	nativeExecute(t, l, `CREATE TABLE "原始🐱表"("键" INTEGER PRIMARY KEY,"内容🐱" TEXT);INSERT INTO "原始🐱表" VALUES(1,'完整值');CREATE VIEW "原始🐱视图" AS SELECT * FROM "原始🐱表"`)
	unicodePage, err := s.loadData(context.Background(), p.ID, "main", "原始🐱表", 0, 1)
	if err != nil || len(unicodePage.Rows) != 1 || unicodePage.Columns[1] != "内容🐱" || unicodePage.RowRefs[0] == nil {
		t.Fatalf("native UTF-8 table/column name bytes changed: %v", err)
	}
	unicodeRow, err := s.readDataRow(context.Background(), p.ID, "main", "原始🐱表", *unicodePage.RowRefs[0])
	if err != nil || unicodeRow.Values["内容🐱"].Text != "完整值" {
		t.Fatalf("native qualified UTF-8 editing read failed: %v", err)
	}
	view, err := s.loadData(context.Background(), p.ID, "main", "原始🐱视图", 0, 1)
	if err != nil || len(view.Rows) != 1 || view.Object.Kind != "view" {
		t.Fatalf("native UTF-8 view name bytes changed: %v", err)
	}
}

func TestNativeRQLiteStockSHA256LargeAndBatchedValues(t *testing.T) {
	p := nativeFixture(t, "")
	p.RQLiteOptions.DatabaseTimeoutMilliseconds = 120000
	p.RQLiteOptions.RequestTimeoutSeconds = 180
	s := openNative(t, p, "original-fixture-password")
	input := []string{"SELECT -1 AS id,NULL AS v", "SELECT 91,X''", "SELECT 17,X'61006280FF'", "SELECT 32,zeroblob(1048576)"}
	query := "WITH RECURSIVE " + hashCTEs(strings.Join(input, " UNION ALL ")) + " SELECT id,digest FROM _nyadb_hash ORDER BY id"
	started := time.Now()
	r, err := s.request(context.Background(), "/db/query", []any{query}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := decodedRows(r, 0, 2)
	if err != nil || len(rows) != 4 || rows[0][0].Text != "-1" || rows[0][1].Kind != contract.Null {
		t.Fatal("native batched digests lost NULL or original identities")
	}
	for i, test := range []struct {
		id   string
		data []byte
	}{{"17", []byte{0x61, 0, 0x62, 0x80, 0xff}}, {"32", make([]byte, 1048576)}, {"91", nil}} {
		digest := sha256.Sum256(test.data)
		if rows[i+1][0].Text != test.id || rows[i+1][1].Text != hex.EncodeToString(digest[:]) {
			t.Fatal("stock native SHA256 differed from the complete original bytes")
		}
	}
	t.Logf("native SHA256 batch including 1 MiB completed in %s, request SQL bytes=%d", time.Since(started), len(query))
	if len(r.Raw) > 1024 {
		t.Fatal(fmt.Sprintf("native hash request fetched hidden original values (%d bytes)", len(r.Raw)))
	}
}
