package contract_test

import (
	"github.com/nyakang/nyadb/internal/db/contract"
	"testing"
)

func TestImportReceiptCloneOwnsTypedPreviewsAndOriginalSnapshots(t *testing.T) {
	cell := contract.Cell{Kind: contract.Binary, Bytes: []byte{0, 255}}
	fp := "original"
	snapshot := &contract.RowSnapshot{Original: []contract.Cell{cell}, Fingerprints: []*string{&fp}, Ref: contract.RowRef{TypedValues: []contract.Cell{cell}, Values: [][]byte{{0, 255}}}}
	r := contract.DataImportBatchReceipt{Rows: []contract.DataImportRowResult{{Outcome: &contract.DataEditOutcome{Original: snapshot, Saved: snapshot, PreviewColumns: []string{"raw"}, Previews: []contract.DataCell{{Value: &cell}}}}}}
	copy := contract.CloneDataImportBatchReceipt(r)
	copy.Rows[0].Outcome.Original.Original[0].Bytes[0] = 42
	copy.Rows[0].Outcome.Saved.Ref.TypedValues[0].Bytes[1] = 42
	copy.Rows[0].Outcome.Saved.Ref.Values[0][0] = 42
	*copy.Rows[0].Outcome.Original.Fingerprints[0] = "changed"
	copy.Rows[0].Outcome.Previews[0].Value.Bytes[0] = 42
	copy.Rows[0].Outcome.PreviewColumns[0] = "changed"
	if cell.Bytes[0] != 0 || cell.Bytes[1] != 255 || snapshot.Ref.Values[0][0] != 0 || fp != "original" || r.Rows[0].Outcome.PreviewColumns[0] != "raw" {
		t.Fatal("receipt projection retained mutable caller storage")
	}
}

func TestImportReceiptRejectsIncompleteAndContradictoryNativeBatchProjection(t *testing.T) {
	r := contract.DataImportBatchReceipt{Object: contract.ObjectRef{Name: "original"}, Sequence: 1, FirstRow: 41, LastRow: 42, State: "committed", CommittedRows: 1, Rows: []contract.DataImportRowResult{{Row: 41, Table: "original", Status: "committed", Kind: "insert"}, {Row: 42, Table: "original", Status: "skipped", Kind: "skip"}}}
	if e := contract.ValidateDataImportBatchReceipt(r); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*contract.DataImportBatchReceipt){func(r *contract.DataImportBatchReceipt) { r.Rows = r.Rows[:1] }, func(r *contract.DataImportBatchReceipt) { r.Rows[1].Row = 43 }, func(r *contract.DataImportBatchReceipt) { r.Rows[0].Table = "foreign" }, func(r *contract.DataImportBatchReceipt) { r.CommittedRows = 2 }, func(r *contract.DataImportBatchReceipt) { r.State = "uncertain" }, func(r *contract.DataImportBatchReceipt) { r.Rows[1].Kind = "insert" }} {
		copy := contract.CloneDataImportBatchReceipt(r)
		change(&copy)
		if e := contract.ValidateDataImportBatchReceipt(copy); e == nil {
			t.Fatal("incomplete/contradictory original projection accepted")
		}
	}
}
