package sqlworkspace

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"strings"
	"testing"
)

func TestResultSnapshotKeepsBytesContextAndTruncationAfterRerun(t *testing.T) {
	s := New(Dependencies{})
	d := s.Open(1, "SELECT secret", 7, "app", false)
	d.executedSQL = "SELECT original"
	d.setResult(&contract.SQLResult{Sets: []contract.ResultSet{{Columns: []contract.Column{{Name: "blob"}}, Rows: [][]contract.Cell{{{Kind: contract.Binary, Bytes: []byte{1, 2}, Truncated: true}}}, Truncation: []string{"rows", "cell"}}}, Steps: []contract.StatementOutcome{{SQL: "SELECT original", State: "executed"}}})
	if !s.pinResult(d) {
		t.Fatal(d.err)
	}
	snap := d.snapshots[0]
	d.result.Sets[0].Rows[0][0].Bytes[0] = 9
	d.result.Sets[0].Truncation[0] = "changed"
	d.result.Sets[0].Columns[0].Name = "changed"
	d.setResult(&contract.SQLResult{Sets: []contract.ResultSet{{Rows: [][]contract.Cell{{{Kind: contract.Text, Text: "new"}}}}}})
	if snap.SQL != "SELECT original" || snap.Namespace.ProfileID != 7 || snap.Namespace.Schema != "app" || snap.Time.IsZero() {
		t.Fatal("snapshot context lost")
	}
	set := selectedResult(snap.Document)
	if set.Rows[0][0].Bytes[0] != 1 || set.Truncation[0] != "rows" || set.Columns[0].Name != "blob" || !set.Rows[0][0].Truncated {
		t.Fatal("snapshot changed after live result mutation")
	}
	snap.Document.selectedRow = 0
	snap.Document.grid.Cells.All = true
	raw, e := EncodeResult(resultData(snap.Document, true), "JSON")
	if e != nil || !strings.Contains(string(raw), `"truncation"`) {
		t.Fatal("selection lost truncation", e)
	}
	d.snapshotIndex = 1
	if !s.resultEditTarget(snap.Document).Valid() {
		t.Fatal("snapshot copy target is inactive")
	}
	d.snapshotIndex = 0
	if s.resultEditTarget(snap.Document).Valid() {
		t.Fatal("stale snapshot target remained active")
	}
	s.Close(d)
	if snap.Document.result != nil || !snap.Document.closed || len(d.snapshots) != 0 {
		t.Fatal("close retained snapshot rows")
	}
}
func TestResultSnapshotsEnforceLimitsAndNeverPersistRows(t *testing.T) {
	s := New(Dependencies{})
	d := s.Open(1, "SELECT 1", 7, "app", false)
	d.setResult(&contract.SQLResult{Sets: []contract.ResultSet{{Columns: []contract.Column{{Name: "x"}}, Rows: [][]contract.Cell{{{Kind: contract.Text, Text: "private-cached-value"}}}}}})
	for i := 0; i < 3; i++ {
		if !s.pinResult(d) {
			t.Fatal(d.err)
		}
	}
	if s.pinResult(d) {
		t.Fatal("unbounded snapshot count")
	}
	w := s.Snapshot(1, []uint64{1})
	restored := New(Dependencies{}).RestoreDocument(w.Documents[0])
	if restored.result != nil || len(restored.snapshots) != 0 {
		t.Fatal("cached rows entered recovery")
	}
	d.removeSnapshot(0)
	d.result.Sets[0].Rows[0][0].Text = strings.Repeat("x", snapshotMaxBytes+1)
	if s.pinResult(d) {
		t.Fatal("unbounded snapshot memory")
	}
}
func TestVisibleSQLRangeDispatchKeepsAuthoredSelection(t *testing.T) {
	s, d, tt, _, _ := sqlUITestState(t)
	d.Editor.Buffer.Set("SELECT 1;\nSELECT 2;")
	d.Editor.Select(11, 17)
	selected := d.Editor.SelectedText()
	received := make(chan string, 1)
	s.Executor = func(_ context.Context, _ int64, _, sql string) (contract.SQLResult, error) {
		received <- sql
		return contract.SQLResult{}, nil
	}
	for _, test := range []struct {
		run  func()
		want string
	}{{func() { s.RunCurrent(nil, d) }, "SELECT 2;"}, {func() { s.RunSelection(nil, d) }, strings.TrimSpace(selected)}, {func() { s.RunAll(nil, d) }, "SELECT 1;\nSELECT 2;"}} {
		test.run()
		waitSQLUI(t, tt, d)
		select {
		case got := <-received:
			if got != test.want {
				t.Fatalf("ran %q, want %q", got, test.want)
			}
		default:
			t.Fatal("scope did not dispatch")
		}
		if d.Editor.SelectedText() != selected {
			t.Fatal("scope changed selection")
		}
	}
}

func TestCachedExportCapturesOriginalResultAndSelectionBeforeRerun(t *testing.T) {
	s := New(Dependencies{})
	d := s.Open(1, "SELECT blob", 7, "app", false)
	d.setResult(&contract.SQLResult{Sets: []contract.ResultSet{{Columns: []contract.Column{{Name: "blob"}}, Rows: [][]contract.Cell{{{Kind: contract.Binary, Bytes: []byte{1, 2}}}}, Truncation: []string{"rows"}}}})
	d.selectedRow = 0
	d.grid.Cells.All = true
	s.openCachedExport(d)
	d.result.Sets[0].Rows[0][0].Bytes[0] = 9
	d.setResult(&contract.SQLResult{Sets: []contract.ResultSet{{Rows: [][]contract.Cell{{{Kind: contract.Text, Text: "new"}}}}}})
	if d.cachedExport.Cached.Rows[0][0].Bytes[0] != 1 || d.cachedExport.Selected.Rows[0][0].Bytes[0] != 1 || d.cachedExport.Selected.Truncation[0] != "rows" {
		t.Fatal("export draft followed rerun or lost selection metadata")
	}
	s.Close(d)
	if d.cachedExport.Cached.Rows != nil || d.cachedExport.Selected.Rows != nil {
		t.Fatal("close retained cached export values")
	}
}
