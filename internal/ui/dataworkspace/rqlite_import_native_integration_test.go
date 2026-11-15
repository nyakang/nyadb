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
