package dataworkspace

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func newFillPerformanceFixture() (*State, *Document) {
	s := New(Dependencies{Session: func() Session { return Session{ProfileID: 19, Generation: 7, Connected: true} }})
	d := &Document{id: 2, profileID: 19, database: "db", table: "records", limit: 100, selectedRow: 0, hiddenColumns: map[string]bool{}}
	p := &contract.DataPage{Database: "db", Table: "records"}
	for col := 0; col < 10; col++ {
		name := fmt.Sprintf("c%d", col)
		p.Columns = append(p.Columns, name)
		p.ColumnMetadata = append(p.ColumnMetadata, contract.StructureColumn{Name: name, DataType: "varchar", Type: "varchar(1024)", Nullable: true})
	}
	for row := 0; row < 100; row++ {
		cells := make([]contract.DataCell, 10)
		for i := range cells {
			cells[i] = contract.DataCell{Text: strings.Repeat("x", 512)}
		}
		ref := &contract.RowRef{Database: "db", Table: "records", Columns: []string{"id"}, Values: [][]byte{[]byte(fmt.Sprint(row))}}
		p.Rows = append(p.Rows, cells)
		p.RowRefs = append(p.RowRefs, ref)
		p.RowSnapshots = append(p.RowSnapshots, &contract.RowSnapshot{Ref: *ref, Columns: p.Columns, Metadata: "fixture"})
		if row < 20 {
			d.selection.Add(row)
		}
	}
	d.page = p
	d.grid.Cells = ui.ColumnRange{All: true}
	d.fill = mysqlDataFillState{Mode: "值", Text: "filled"}
	s.documents[d.id] = d
	return s, d
}
func TestFill200CellsRetainsOneCompleteUndo(t *testing.T) {
	s, d := newFillPerformanceFixture()
	if err := s.fillSelectedData(d); err != nil {
		t.Fatal(err)
	}
	if len(d.edit.Undo) != 1 {
		t.Fatalf("history groups: %d", len(d.edit.Undo))
	}
	if !s.undoDraft(d, false) {
		t.Fatal("undo unavailable")
	}
	for _, row := range d.page.Rows {
		for _, cell := range row {
			if cell.Text != strings.Repeat("x", 512) {
				t.Fatal("undo left partial fill")
			}
		}
	}
	if !s.undoDraft(d, true) || d.page.Rows[19][9].Text != "filled" {
		t.Fatal("redo incomplete")
	}
}
func BenchmarkFill200Cells(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		s, d := newFillPerformanceFixture()
		b.StartTimer()
		if err := s.fillSelectedData(d); err != nil {
			b.Fatal(err)
		}
	}
}
