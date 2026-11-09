package sqlworkspace

import (
	"bytes"
	"encoding/base64"
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphresult"
	"github.com/nyakang/nyadb/internal/ui/gridview"
)

func projectedNodeResult(kind string) *contract.SQLResult {
	r := graphresult.Project("RETURN n", contract.GraphResult{Attempted: true, Completed: true, Columns: []string{"n"}, Rows: [][]contract.GraphValue{{{Kind: "node", Node: &contract.GraphNode{ElementID: "original", Properties: []contract.GraphProperty{{Name: "a", Value: contract.GraphValue{Kind: kind, Text: "9223372036854775807"}}, {Name: "b", Value: contract.GraphValue{Kind: "bytes", Text: base64.StdEncoding.EncodeToString([]byte{0, 255})}}}}}}}})
	return &r
}

func TestGraphPropertyReadingLayoutCopyAndSavedVisibilityKeepOriginalColumns(t *testing.T) {
	d := &Document{commandDialect: "neo4j"}
	d.setResult(projectedNodeResult("integer"))
	if !d.hiddenResultColumns["sql_0"] || selectedResult(d).Columns[1].Name != "n.a" {
		t.Fatal("native property view did not hide only its original pure node")
	}
	d.selectedRow = 0
	d.grid.Columns = ui.TableLayout{Order: []string{"sql_2", "sql_1", "sql_0"}, Widths: map[string]float32{"sql_1": 270}}
	d.grid.Cells.All = true
	if got := gridview.Copy(resultData(d, true)); got != "0x00ff\t9223372036854775807" || len(resultData(d, false).Columns) != 3 {
		t.Fatal("property copy lost visual order/precision/bytes or full export lost original node", got)
	}
	d.hiddenResultColumns["sql_0"] = false
	d.setResult(projectedNodeResult("string"))
	if d.hiddenResultColumns["sql_0"] || d.grid.Columns.Widths["sql_1"] != 270 || !reflect.DeepEqual(d.grid.Columns.Order, []string{"sql_2", "sql_1", "sql_0"}) {
		t.Fatal("new observed type discarded saved property reading layout or original-column choice")
	}
}

func TestGraphPropertySnapshotsAndExportDraftsOwnProjectionAndNativeGraph(t *testing.T) {
	s := New(Dependencies{})
	d := s.Open(1, "RETURN n", 77, "neo4j", false)
	d.commandDialect = "neo4j"
	d.setResult(projectedNodeResult("integer"))
	d.executedSQL = "RETURN n"
	d.selectedRow = 0
	d.grid.Cells.All = true
	s.openCachedExport(d)
	if !s.pinResult(d) || len(d.snapshots) != 1 {
		t.Fatal("property result could not retain a bounded snapshot")
	}
	snap := d.snapshots[0].Document
	d.result.Graph.Rows[0][0].Node.ElementID = "changed"
	d.result.Sets[0].Rows[0][2].Bytes[0] = 3
	d.result.Sets[0].DefaultHiddenColumns[0] = 99
	if snap.result.Graph.Rows[0][0].Node.ElementID != "original" || !bytes.Equal(snap.result.Sets[0].Rows[0][2].Bytes, []byte{0, 255}) || snap.result.Sets[0].DefaultHiddenColumns[0] != 0 || !snap.hiddenResultColumns["sql_0"] || !bytes.Equal(d.cachedExport.Cached.Rows[0][2].Bytes, []byte{0, 255}) {
		t.Fatal("snapshot or export draft borrowed original graph/property bytes or presentation hints")
	}
	d.removeSnapshot(0)
	if !snap.closed || snap.result != nil || snap.hiddenResultColumns != nil {
		t.Fatal("closing property snapshot retained native/projected caches")
	}
}
