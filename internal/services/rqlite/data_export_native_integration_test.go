package rqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestNativeRQLiteCompleteExportTypedJSONLFilteringAndCancellation(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	nativeExecute(t, l, `CREATE TABLE native_export(id INTEGER PRIMARY KEY,note TEXT,raw BLOB,v);INSERT INTO native_export VALUES(1,CAST(X'610062' AS TEXT),zeroblob(8192),9223372036854775807),(2,NULL,X'00FF',CAST(0.1 AS REAL)),(3,'last',X'',NULL);`)
	_, r := nativeDataDocument(t, s, "native_export")
	var captured [][]contract.DataValue
	err := r.Exporter(context.Background(), p.ID, "main", "native_export", contract.DataOptions{SortColumn: "id", SortDescending: true, Filters: []contract.DataFilterRule{{Column: "id", Mode: "less", Value: "3"}}}, []string{"raw", "note", "v"}, func(row []contract.DataValue) error { captured = append(captured, row); return nil })
	if err != nil || len(captured) != 2 || !bytes.Equal(captured[0][0].Bytes, []byte{0, 255}) || !captured[0][1].Null || captured[0][2].Kind != contract.Floating || len(captured[1][0].Bytes) != 8192 || captured[1][1].Text != "a\x00b" || captured[1][2].Text != "9223372036854775807" {
		t.Fatal("complete typed native export changed selection/storage/bytes", err)
	}
	var stream bytes.Buffer
	o := contract.ObjectRef{ProfileID: p.ID, Catalog: "main", Kind: "table", Name: "native_export"}
	if err := r.JSONL(context.Background(), o, &stream); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&stream)
	var header struct {
		Version int
		Columns []contract.Column
	}
	if err := decoder.Decode(&header); err != nil || header.Version != 1 || len(header.Columns) != 4 {
		t.Fatal("native JSONL lost neutral typed header", err)
	}
	var cells []contract.Cell
	if err := decoder.Decode(&cells); err != nil || len(cells) != 4 || len(cells[2].Bytes) != 8192 || cells[1].Text != "a\x00b" || cells[3].Kind != contract.Integer || cells[3].Text != "9223372036854775807" {
		t.Fatal("native JSONL used bounded or untyped display values", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rows := 0
	err = r.Exporter(ctx, p.ID, "main", "native_export", contract.DataOptions{}, []string{"id"}, func([]contract.DataValue) error { rows++; cancel(); return nil })
	if !errors.Is(err, context.Canceled) || rows != 1 {
		t.Fatal("export cancellation delivered additional rows", err, rows)
	}
	marker := errors.New("original sink failure")
	err = r.Exporter(context.Background(), p.ID, "main", "native_export", contract.DataOptions{}, []string{"id"}, func([]contract.DataValue) error { return marker })
	if !errors.Is(err, marker) {
		t.Fatal("export ignored original sink failure", err)
	}
	nativeExecute(t, l, "CREATE TABLE invalid_export(id INTEGER PRIMARY KEY,note TEXT);INSERT INTO invalid_export VALUES(1,CAST(X'80FF' AS TEXT))")
	_, bad := nativeDataDocument(t, s, "invalid_export")
	if err := bad.Exporter(context.Background(), p.ID, "main", "invalid_export", contract.DataOptions{}, []string{"note"}, func([]contract.DataValue) error { t.Error("invalid text was normalized"); return nil }); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("invalid UTF-8 export was accepted", err)
	}
}

func TestNativeRQLiteExportJSONLAndAnalysisCoverEveryAdmittedColumn(t *testing.T) {
	p := nativeFixture(t, "")
	p.RQLiteOptions.DatabaseTimeoutMilliseconds, p.RQLiteOptions.RequestTimeoutSeconds = 120000, 180
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	var definitions, fields, values []string
	for i := range maxDataColumns {
		name := fmt.Sprintf("v%d", i)
		fields = append(fields, name)
		definitions = append(definitions, name+" INTEGER")
		values = append(values, fmt.Sprint(i))
	}
	nativeExecute(t, l, "CREATE TABLE wide_export("+strings.Join(definitions, ",")+");INSERT INTO wide_export VALUES("+strings.Join(values, ",")+")")
	_, runtime := nativeDataDocument(t, s, "wide_export")
	var stream bytes.Buffer
	o := contract.ObjectRef{ProfileID: p.ID, Catalog: "main", Kind: "table", Name: "wide_export"}
	if err := runtime.JSONL(context.Background(), o, &stream); err != nil {
		t.Fatal("complete wide JSONL export", err)
	}
	decoder := json.NewDecoder(&stream)
	var header struct {
		Version int
		Columns []contract.Column
	}
	var cells []contract.Cell
	if err := decoder.Decode(&header); err != nil || len(header.Columns) != maxDataColumns {
		t.Fatal("wide original header", err)
	}
	if err := decoder.Decode(&cells); err != nil || len(cells) != maxDataColumns {
		t.Fatal("wide original typed row", err)
	}
	for i, cell := range cells {
		if cell.Kind != contract.Integer || cell.Text != fmt.Sprint(i) {
			t.Fatal("wide export changed column order or types")
		}
	}
	analysis, err := runtime.Analyzer(context.Background(), p.ID, "main", "wide_export", contract.DataOptions{}, fields)
	if err != nil || len(analysis.Columns) != maxDataColumns {
		t.Fatal("wide original single-transaction analysis", err)
	}
	for i, column := range analysis.Columns {
		if column.Name != fields[i] || column.Rows != 1 || column.Numeric != 1 || column.Mean != fmt.Sprint(i) || column.StdDeviation != "0" {
			t.Fatal("wide analysis omitted or changed original column", i)
		}
	}
}

func TestNativeRQLiteExportAdaptsCompleteBatchesAndAdmitsFull16MiBRow(t *testing.T) {
	p := nativeFixture(t, "")
	p.RQLiteOptions.DatabaseTimeoutMilliseconds, p.RQLiteOptions.RequestTimeoutSeconds = 120000, 180
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	nativeExecute(t, l, "CREATE TABLE batch_export(id INTEGER PRIMARY KEY,v BLOB);WITH RECURSIVE n(v) AS(VALUES(1) UNION ALL SELECT v+1 FROM n WHERE v<35) INSERT INTO batch_export SELECT v,zeroblob(1048576) FROM n")
	_, r := nativeDataDocument(t, s, "batch_export")
	ids := []string{}
	err := r.Exporter(context.Background(), p.ID, "main", "batch_export", contract.DataOptions{}, []string{"id", "v"}, func(row []contract.DataValue) error {
		if len(row[1].Bytes) != 1048576 {
			return errors.New("truncated native export")
		}
		ids = append(ids, row[0].Text)
		return nil
	})
	if err != nil || len(ids) != 35 {
		t.Fatal("adaptive complete export failed", err, len(ids))
	}
	for i, id := range ids {
		if id != fmt.Sprint(i+1) {
			t.Fatal("adaptive read replayed/skipped original rows")
		}
	}
	var definitions, values, fields []string
	for i := range 16 {
		name := fmt.Sprintf("v%d", i)
		definitions = append(definitions, name+" BLOB")
		values = append(values, "zeroblob(1048576)")
		fields = append(fields, name)
	}
	nativeExecute(t, l, "CREATE TABLE row_export(id INTEGER PRIMARY KEY,"+strings.Join(definitions, ",")+");INSERT INTO row_export VALUES(1,"+strings.Join(values, ",")+")")
	_, r = nativeDataDocument(t, s, "row_export")
	rows := 0
	err = r.Exporter(context.Background(), p.ID, "main", "row_export", contract.DataOptions{}, fields, func(row []contract.DataValue) error {
		rows++
		for _, v := range row {
			if len(v.Bytes) != 1048576 {
				return errors.New("incomplete row")
			}
		}
		return nil
	})
	if err != nil || rows != 1 {
		t.Fatal("complete 16 MiB selected export row refused", err)
	}
	nativeExecute(t, l, "UPDATE row_export SET v0=zeroblob(1048577)")
	if err := r.Exporter(context.Background(), p.ID, "main", "row_export", contract.DataOptions{}, fields, func([]contract.DataValue) error { t.Error("oversized original field delivered"); return nil }); !errors.Is(err, errExportValue) {
		t.Fatal("export field bound not enforced", err)
	}
	// Retained original typed receipts never become reconstructed exports.
	if !reflect.DeepEqual(ids[:3], []string{"1", "2", "3"}) {
		t.Fatal("original export identity changed")
	}
}
