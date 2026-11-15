package dataworkspace

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/rqlite"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestNativeRQLiteImportWizardPartialRowsParkedCompletionAndCompleteReceiptExport(t *testing.T) {
	p := rqliteGridFixture(t)
	raw, e := rqlite.OpenSession(context.Background(), p, "")
	if e != nil {
		t.Fatal(e)
	}
	defer raw.Close()
	rqliteGridSQL(t, raw, "CREATE TABLE native_grid(id INTEGER PRIMARY KEY,note TEXT);INSERT INTO native_grid VALUES(1,'original')")
	s, active, original := rqliteGridState(t, raw)
	d := rqliteGridDocument(t, s, raw, 1)
	if s.runtimeFor(d).StreamImporter == nil || contract.ComposeData(raw).StreamImporter != nil {
		t.Fatal("import not exclusively bound to original document")
	}
	events := []TableImportEvent{}
	s.deps.TableImported = func(_ ui.Services, event TableImportEvent) { events = append(events, event) }
	s.openDataTransfer(d, "import")
	d.transfer.ContinueOnError = false
	var source strings.Builder
	source.WriteString("id,note\n")
	for i := 2; i <= 66; i++ {
		fmt.Fprintf(&source, "%d,new-%d\n", i, i)
	}
	source.WriteString("1,duplicate\n")
	path := filepath.Join(t.TempDir(), "rows.csv")
	if e = os.WriteFile(path, []byte(source.String()), 0600); e != nil {
		t.Fatal(e)
	}
	s.readDataImportPath(ui.Services{}, d, path)
	waitDataTransfer(t, s, d)
	s.prepareDataImport(ui.Services{}, d)
	waitDataTransfer(t, s, d)
	if d.transfer.MappedSpool == nil {
		t.Fatal("native wizard source was not fully prepared", d.transfer.Err)
	}
	d.transfer.Confirmation = "native_grid"
	d.transfer.Step = 4
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.transferModals(c, d) }, 1280, 800)
	tt.Frame()
	if !tt.HasText("导入按批次提交；取消或后续失败不会回滚已提交的批次。") {
		t.Fatal("wizard omitted incremental commit semantics")
	}
	if e = tt.Click("确认导入"); e != nil {
		t.Fatal(e)
	}
	active.ProfileID++
	rqliteGridWait(t, func() bool { return len(d.transfer.Results) > 0 })
	s.Consume(ui.Services{})
	if !d.transfer.Busy || d.transfer.Journal != nil || len(events) != 0 {
		t.Fatal("parked import published under another original profile")
	}
	*active = *original
	rqliteGridWait(t, func() bool { tt.Frame(); return !d.transfer.Busy && !d.loading })
	if d.transfer.Uncertain || d.transfer.Completed || d.transfer.Journal == nil || d.transfer.Journal.count != 66 || len(events) != 1 || events[0].ProfileID != p.ID || events[0].Generation != original.Generation || events[0].Completed || events[0].Uncertain {
		t.Fatal("known partial import was lost or marked uncertain", d.transfer.Err, events)
	}
	if len(d.transfer.Batch) != 1 || d.transfer.Batch[0].Result.CommittedRows != 64 {
		t.Fatal("confirmed partial native count lost")
	}
	for i, want := range map[int64]string{0: "committed", 63: "committed", 64: "rolled_back", 65: "failed"} {
		row, e := d.transfer.Journal.Row(i, false, false)
		if e != nil || row.Status != want {
			t.Fatal("native original row settlement lost", i, row, e)
		}
	}
	if actual := rqliteGridSQL(t, raw, "SELECT count(*) FROM native_grid"); actual.Rows[0][0] != "65" {
		t.Fatal("native partial import replayed or changed original rows")
	}
	export := filepath.Join(t.TempDir(), "all-receipts.jsonl")
	d.transfer.SaveDialog = func(mygo.SaveDialogOptions) (string, error) { return export, nil }
	tt.Frame()
	rqliteGridCapture(t, tt, "rqlite-import-partial-original-receipts")
	if e = tt.Click("导出全部导入回执"); e != nil {
		t.Fatal(e)
	}
	rqliteGridWait(t, func() bool { tt.Frame(); return d.transfer.ReceiptExport.Results == nil })
	if d.transfer.ReceiptExport.Err != "" {
		t.Fatal(d.transfer.ReceiptExport.Err)
	}
	f, e := os.Open(export)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 32<<20)
	count := 0
	for scanner.Scan() {
		var record mysqlDataImportReceiptRecord
		if e = json.Unmarshal(scanner.Bytes(), &record); e != nil {
			t.Fatal(e)
		}
		count++
		if record.SourceIndex != 0 || record.SourceOffset != 0 || record.Receipt.Sequence != int64(count) || record.Receipt.Generation != original.Generation || record.Receipt.Object != d.object || !json.Valid([]byte(record.Receipt.NativeJSON)) {
			t.Fatal("archive lost original native full receipt identity")
		}
		if count == 1 && record.Receipt.State != "committed" || count == 2 && record.Receipt.State != "rolled_back" {
			t.Fatal("native batch settlement changed")
		}
	}
	if scanner.Err() != nil || count != 2 {
		t.Fatal("complete original native archive truncated", scanner.Err(), count)
	}
}

func TestNativeRQLiteImportWizardRowRecordFailureKeepsCompleteOriginalBatch(t *testing.T) {
	p := rqliteGridFixture(t)
	raw := rqliteGridOpen(t, p)
	rqliteGridSQL(t, raw, "CREATE TABLE native_grid(id INTEGER PRIMARY KEY,note TEXT);INSERT INTO native_grid VALUES(0,'original')")
	m := dbruntime.New()
	defer m.Close()
	dbruntime.RegisterBuiltins(m, nil)
	for i := 0; i < 2; i++ {
		if err := m.Connect(context.Background(), p, ""); err != nil {
			t.Fatal(err)
		}
	}
	public, err := m.Session(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if public.Generation() == raw.Generation() {
		t.Fatal("public/native generation distinction was not exercised")
	}
	original := &Session{ProfileID: p.ID, Generation: public.Generation(), Connected: true}
	active := *original
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(id int64) Session {
		if id == p.ID {
			return *original
		}
		return Session{}
	}, RuntimeFor: func(id int64) Runtime { return m.Data(id) }})
	defer s.CloseAll()
	d := rqliteGridDocument(t, s, public, 1)
	path := filepath.Join(t.TempDir(), "read-only-result.jsonl")
	if e := os.WriteFile(path, nil, 0600); e != nil {
		t.Fatal(e)
	}
	f, e := os.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	// Wrap the original document executor, with a real owned filesystem error.
	// No production hooks or process-wide filesystem limits are involved.
	importer := d.boundRuntime.StreamImporter
	d.boundRuntime.StreamImporter = func(ctx context.Context, id int64, db, name string, request contract.DataImportStreamRequest) (*contract.DataImportStreamResult, error) {
		record := request.Record
		request.Record = func(row contract.DataImportRowResult) error {
			if row.Row == 1001 {
				_, err := f.Write([]byte("original row"))
				return err
			}
			return record(row)
		}
		return importer(ctx, id, db, name, request)
	}
	events := []TableImportEvent{}
	s.deps.TableImported = func(_ ui.Services, event TableImportEvent) { events = append(events, event) }
	s.openDataTransfer(d, "import")
	d.transfer.ContinueOnError = false
	var source strings.Builder
	source.WriteString("id,note\n")
	for i := 1; i <= 1300; i++ {
		fmt.Fprintf(&source, "%d,new-%d\n", i, i)
	}
	input := filepath.Join(t.TempDir(), "rows.csv")
	if e = os.WriteFile(input, []byte(source.String()), 0600); e != nil {
		t.Fatal(e)
	}
	s.readDataImportPath(ui.Services{}, d, input)
	waitDataTransfer(t, s, d)
	s.prepareDataImport(ui.Services{}, d)
	waitDataTransfer(t, s, d)
	if d.transfer.MappedSpool == nil {
		t.Fatal("original source preparation failed", d.transfer.Err)
	}
	d.transfer.Confirmation, d.transfer.Step = "native_grid", 4
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.transferModals(c, d) }, 1280, 800)
	tt.Frame()
	if e = tt.Click("确认导入"); e != nil {
		t.Fatal(e)
	}
	active.ProfileID++
	rqliteGridWait(t, func() bool { return len(d.transfer.Results) > 0 })
	s.Consume(ui.Services{})
	if !d.transfer.Busy || d.transfer.Journal != nil || len(events) != 0 {
		t.Fatal("parked original row-record failure published under another profile")
	}
	active = *original
	rqliteGridWait(t, func() bool { tt.Frame(); return !d.transfer.Busy && !d.loading })
	if d.transfer.Completed || d.transfer.Uncertain || d.transfer.Err == "" || d.transfer.Journal == nil || d.transfer.Journal.count != 1000 || len(d.transfer.Batch) != 1 {
		t.Fatal("known acknowledged write was lost or made uncertain", d.transfer.Err)
	}
	result := d.transfer.Batch[0].Result
	if result.CommittedRows != 1024 || result.Progress.Processed != 1024 || result.ReceiptsRecorded != 16 || result.LastReceipt.FirstRow != 961 || result.LastReceipt.LastRow != 1024 || len(result.LastReceipt.Rows) != 64 {
		t.Fatal("original complete native batch projection lost")
	}
	if len(events) != 1 || events[0].ProfileID != p.ID || events[0].Generation != original.Generation || events[0].Completed || events[0].Uncertain {
		t.Fatal("partial original completion identity changed", events)
	}
	for i := int64(1000); i < 1024; i++ {
		row := mysqlDataImportResultRow(d, i)
		if row.Row != int(i+1) || row.Status != "committed" || row.Outcome == nil || row.Outcome.GeneratedID != i+1 || row.Table != "native_grid" {
			t.Fatal("visible result erased an acknowledged native row", i, row)
		}
		row.Outcome.GeneratedID = -1
	}
	if row := mysqlDataImportResultRow(d, 1024); row.Status != "not_processed" {
		t.Fatal("future source row was submitted or invented", row)
	}
	if mysqlDataImportResultRow(d, 1000).Outcome.GeneratedID != 1001 {
		t.Fatal("display changed original native receipt")
	}
	if actual := rqliteGridSQL(t, raw, "SELECT count(*) FROM native_grid"); actual.Rows[0][0] != "1025" {
		t.Fatal("recording failure replayed a batch or submitted future rows")
	}
	export := filepath.Join(t.TempDir(), "complete-original-receipts.jsonl")
	d.transfer.SaveDialog = func(mygo.SaveDialogOptions) (string, error) { return export, nil }
	tt.Frame()
	rqliteGridCapture(t, tt, "rqlite-import-row-record-failure-original-receipts")
	if e = tt.Click("导出全部导入回执"); e != nil {
		t.Fatal(e)
	}
	rqliteGridWait(t, func() bool { tt.Frame(); return d.transfer.ReceiptExport.Results == nil })
	if d.transfer.ReceiptExport.Err != "" {
		t.Fatal(d.transfer.ReceiptExport.Err)
	}
	archive, e := os.Open(export)
	if e != nil {
		t.Fatal(e)
	}
	defer archive.Close()
	scanner := bufio.NewScanner(archive)
	scanner.Buffer(make([]byte, 4096), 32<<20)
	count, rows := 0, 0
	for scanner.Scan() {
		var record mysqlDataImportReceiptRecord
		if e = json.Unmarshal(scanner.Bytes(), &record); e != nil {
			t.Fatal(e)
		}
		count++
		r := record.Receipt
		if record.SourceIndex != 0 || record.SourceOffset != 0 || r.Sequence != int64(count) || r.Generation != original.Generation || r.Object != d.object || r.State != "committed" || contract.ValidateDataImportBatchReceipt(r) != nil || !json.Valid([]byte(r.NativeJSON)) {
			t.Fatal("archive changed original complete native receipt")
		}
		for _, row := range r.Rows {
			rows++
			if row.Row != rows || row.Outcome == nil || row.Outcome.GeneratedID != int64(rows) {
				t.Fatal("archive lost original row projection", rows)
			}
		}
	}
	if scanner.Err() != nil || count != 16 || rows != 1024 {
		t.Fatal("original native receipts were truncated", scanner.Err(), count, rows)
	}
}
