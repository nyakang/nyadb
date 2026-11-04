package sqlworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/gridview"
	"testing"
)

func TestResultSetsKeepIndependentReadingStateAndReplaceRows(t *testing.T) {
	columns := []contract.Column{{Name: "same", DatabaseType: "TEXT"}}
	r := &contract.SQLResult{Sets: []contract.ResultSet{{Columns: columns, Rows: [][]contract.Cell{{{Kind: contract.Text, Text: "first"}}}}, {Columns: columns, Rows: [][]contract.Cell{{{Kind: contract.Text, Text: "second"}}}}}}
	d := &Document{}
	d.setResult(r)
	d.selection.Add(0)
	d.selectedRow = 0
	d.grid.Columns = ui.TableLayout{Widths: map[string]float32{"sql_0": 220}}
	d.resultFieldOpen = true
	d.resultFieldMode = "JSON"
	d.selectResult(1)
	if d.selection.Len() != 0 || d.resultFieldOpen {
		t.Fatal("result set inherited other selection")
	}
	d.selection.Add(0)
	d.selectedRow = 0
	d.grid.Columns = ui.TableLayout{Widths: map[string]float32{"sql_0": 320}}
	if gridview.Copy(resultData(d, true)) != "second" {
		t.Fatal("copied wrong result")
	}
	d.selectResult(0)
	if d.grid.Columns.Widths["sql_0"] != 220 || !d.resultFieldOpen || d.resultFieldMode != "JSON" || gridview.Copy(resultData(d, true)) != "first" {
		t.Fatal("reading state was not retained")
	}
	d.setResult(r)
	if d.selection.Len() != 0 || d.selectedRow != -1 || d.resultFieldOpen {
		t.Fatal("new execution retained old row selection")
	}
	d.resultViews[0].Grid.Columns.Widths["sql_0"] = 440
	if d.resultViews[1].Grid.Columns.Widths["sql_0"] == 440 {
		t.Fatal("result layouts share mutable maps")
	}
}

func TestResultCopyFollowsVisibleColumnOrder(t *testing.T) {
	d := &Document{}
	d.setResult(&contract.SQLResult{Sets: []contract.ResultSet{{Columns: []contract.Column{{Name: "a"}, {Name: "b"}, {Name: "c"}}, Rows: [][]contract.Cell{{{Kind: contract.Text, Text: "A"}, {Kind: contract.Null}, {Kind: contract.Binary, Bytes: []byte{}}}}}}})
	d.selectedRow = 0
	d.grid.Columns.Order = []string{"sql_2", "sql_1", "sql_0"}
	d.grid.Cells.All = true
	d.hiddenResultColumns["sql_1"] = true
	if got := gridview.Copy(resultData(d, true)); got != "0x\tA" {
		t.Fatalf("visual order/typed values lost: %q", got)
	}
}

func TestWorkspaceKeepsReadingLayoutWithoutCachedRows(t *testing.T) {
	s := New(Dependencies{})
	d := s.Open(2, "SELECT 1", 0, "", false)
	result := &contract.SQLResult{Sets: []contract.ResultSet{{Columns: []contract.Column{{Name: "id"}, {Name: "secret"}}, Rows: [][]contract.Cell{{{Kind: contract.Integer, Text: "1"}, {Kind: contract.Text, Text: "private-row"}}}}}}
	d.setResult(result)
	d.hiddenResultColumns["sql_1"] = true
	d.resultFieldMode = "十六进制"
	w := s.Snapshot(2, []uint64{2})
	restored := New(Dependencies{}).RestoreDocument(w.Documents[0])
	if restored.Result() != nil {
		t.Fatal("workspace restored cached rows")
	}
	restored.setResult(result)
	if !restored.hiddenResultColumns["sql_1"] || restored.resultFieldMode != "十六进制" {
		t.Fatal("workspace lost reading layout")
	}
	restored.hiddenResultColumns["sql_1"] = false
	if !d.hiddenResultColumns["sql_1"] {
		t.Fatal("restored layout aliases live state")
	}
}
