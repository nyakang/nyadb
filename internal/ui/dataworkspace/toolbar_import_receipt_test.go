package dataworkspace

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestImportReceiptArchiveRetainsOriginalRangesAndExportsCapturedPrefix(t *testing.T) {
	s, d, _ := completionWizard(t, "import")
	j, e := newMySQLDataImportJournal(d.transfer.Assets, 3)
	if e != nil {
		t.Fatal(e)
	}
	d.transfer.Journal = j
	r := contract.DataImportBatchReceipt{Object: d.object, Generation: 1, Sequence: 1, FirstRow: 1, LastRow: 2, State: "committed", CommittedRows: 2, NativeJSON: `{"exact":9007199254740993,"original":"猫"}`}
	if e = j.RecordReceipt(2, 150, r); e != nil {
		t.Fatal(e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	path := filepath.Join(t.TempDir(), "receipts.jsonl")
	s.exportImportReceipts(ui.Services{}, d, func(mygo.SaveDialogOptions) (string, error) { close(entered); <-release; return path, nil })
	<-entered
	// A later append must not enter the snapshot captured before the picker.
	later := r
	later.Sequence = 2
	later.FirstRow, later.LastRow = 3, 3
	later.State = "uncertain"
	if e = j.RecordReceipt(2, 150, later); e != nil {
		t.Fatal(e)
	}
	close(release)
	select {
	case e = <-d.transfer.ReceiptExport.Results:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("receipt export stalled")
	}
	d.transfer.ReceiptExport.Cancel()
	d.transfer.ReceiptExport.Cancel, d.transfer.ReceiptExport.Results = nil, nil
	f, e := os.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatal("missing original receipt")
	}
	var record mysqlDataImportReceiptRecord
	if e = json.Unmarshal(scanner.Bytes(), &record); e != nil {
		t.Fatal(e)
	}
	if record.SourceIndex != 2 || record.SourceOffset != 150 || !reflect.DeepEqual(record.Receipt, r) || scanner.Scan() {
		t.Fatal("archive changed original metadata or adopted later receipt")
	}
	if e = j.Record(contract.DataImportRowResult{Row: 1, Kind: "insert", Status: "committed"}); e != nil {
		t.Fatal(e)
	}
	if e = j.Record(contract.DataImportRowResult{Row: 2, Kind: "insert", Status: "uncertain"}); e != nil {
		t.Fatal(e)
	}
	row, e := j.Row(0, false, true)
	if e != nil || row.Status != "committed" {
		t.Fatal("later uncertainty erased original committed row")
	}
}

func TestImportReceiptArchiveCloseWaitsForCapturedReaderAndCancelsLateExport(t *testing.T) {
	s, d, _ := completionWizard(t, "import")
	j, e := newMySQLDataImportJournal(d.transfer.Assets, 1)
	if e != nil {
		t.Fatal(e)
	}
	d.transfer.Journal = j
	if e = j.RecordReceipt(0, 0, contract.DataImportBatchReceipt{State: "uncertain", NativeJSON: `{"original":true}`}); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "preserve.jsonl")
	if e = os.WriteFile(path, []byte("original destination"), 0600); e != nil {
		t.Fatal(e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	s.exportImportReceipts(ui.Services{}, d, func(mygo.SaveDialogOptions) (string, error) { close(entered); <-release; return path, nil })
	<-entered
	results := d.transfer.ReceiptExport.Results
	archive := j.receipts.Name()
	s.Close(d)
	close(release)
	select {
	case e = <-results:
		if e == nil {
			t.Fatal("closed original export wrote late picker destination")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closed export stalled")
	}
	data, e := os.ReadFile(path)
	if e != nil || string(data) != "original destination" {
		t.Fatal("late export changed original destination", e)
	}
	if _, e = os.Stat(archive); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("captured archive retained after close", e)
	}
}

func TestImportReceiptArchiveReaderSurvivesOwnerCloseWithoutKeepingOtherAssets(t *testing.T) {
	owner := &mysqlDataSpoolOwner{}
	j, e := newMySQLDataImportJournal(owner, 1)
	if e != nil {
		t.Fatal(e)
	}
	if e = j.RecordReceipt(0, 0, contract.DataImportBatchReceipt{NativeJSON: `{"complete":true}`}); e != nil {
		t.Fatal(e)
	}
	r, e := j.receiptSnapshot()
	if e != nil {
		t.Fatal(e)
	}
	owner.Close()
	if data, e := io.ReadAll(r); e != nil || !json.Valid(data) {
		t.Fatal("close released original captured descriptor", e)
	}
	if e = r.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(j.receipts.Name()); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("archive snapshot leaked file", e)
	}
}

func TestImportResultUsesOnlyValidatedOriginalReceiptAndKeepsDisplayOwnership(t *testing.T) {
	_, d, _ := completionWizard(t, "import")
	d.transfer.Generation = 7
	d.object = contract.ObjectRef{ProfileID: d.profileID, Catalog: d.database, Kind: "table", Name: d.table}
	r := &contract.DataImportBatchReceipt{Object: d.object, Generation: 7, Sequence: 1, FirstRow: 1, LastRow: 1, State: "committed", CommittedRows: 1, Rows: []contract.DataImportRowResult{{Row: 1, Table: d.table, Kind: "insert", Status: "committed", Outcome: &contract.DataEditOutcome{GeneratedID: 91}}}}
	d.transfer.Batch = []mysqlDataBatchReceipt{{From: 150, To: 151, Database: d.database, Table: d.table, Result: &contract.DataImportStreamResult{LastReceipt: r}}}
	row := mysqlDataImportResultRow(d, 150)
	if row.Row != 151 || row.Outcome == nil || row.Outcome.GeneratedID != 91 {
		t.Fatal("original source offset changed", row)
	}
	row.Outcome.GeneratedID = -1
	if r.Rows[0].Row != 1 || r.Rows[0].Outcome.GeneratedID != 91 {
		t.Fatal("display mutated original native receipt")
	}
	for _, change := range []func(){func() { r.Generation++ }, func() { r.Object.ProfileID++ }, func() { r.Rows[0].Table = "foreign" }, func() { r.LastRow++ }} {
		before := contract.CloneDataImportBatchReceipt(*r)
		change()
		if row = mysqlDataImportResultRow(d, 150); row.Status == "committed" {
			t.Fatal("foreign/incomplete original receipt reached result table")
		}
		*r = before
	}
}

func TestImportResultDiskReadFailureDoesNotInventUnknownWrite(t *testing.T) {
	_, d, _ := completionWizard(t, "import")
	j, err := newMySQLDataImportJournal(d.transfer.Assets, 1)
	if err != nil {
		t.Fatal(err)
	}
	d.transfer.Journal = j
	if err = j.Record(contract.DataImportRowResult{Row: 1, Status: "committed", Kind: "insert"}); err != nil {
		t.Fatal(err)
	}
	if err = j.index.Close(); err != nil {
		t.Fatal(err)
	}
	row := mysqlDataImportResultRow(d, 0)
	if row.Status != "unavailable" || row.Error == "" || d.transfer.Uncertain {
		t.Fatal("result disk read failure invented unknown native write", row)
	}
}
