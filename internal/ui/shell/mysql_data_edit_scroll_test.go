package shell

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func editingScrolledWorkspace(t *testing.T) (*State, *tab, *ui.Tester) {
	t.Helper()
	s, doc, tt, _ := editingTestWorkspace(t)
	for row := len(doc.dataPage.Rows); row < 100; row++ {
		id := strconv.Itoa(41 + row)
		ref := &mysqlsvc.MySQLRowRef{Database: doc.dataDatabase, Table: doc.dataTable, Columns: []string{"id"}, Values: [][]byte{[]byte(id)}}
		doc.dataPage.Rows = append(doc.dataPage.Rows, []mysqlsvc.MySQLDataCell{{Text: id}, {Text: fmt.Sprintf("note-%03d", row)}, {Text: fmt.Sprintf("extra-%03d", row)}})
		doc.dataPage.RowRefs = append(doc.dataPage.RowRefs, ref)
		doc.dataPage.RowSnapshots = append(doc.dataPage.RowSnapshots, &mysqlsvc.MySQLRowSnapshot{Ref: *ref, Columns: doc.dataPage.Columns, Metadata: "fixture"})
	}
	doc.dataState.ScrollTo(55, ui.Start)
	tt.Frame()
	tt.Frame()
	return s, doc, tt
}

func TestMySQLDataClickEditingKeepsScrolledRowPosition(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprintf("complete=%t", complete), func(t *testing.T) {
			s, doc, tt := editingScrolledWorkspace(t)
			const row = 60
			var release chan struct{}
			if complete {
				doc.dataPage.Rows[row][1].Truncated = true
				doc.dataPage.ColumnMetadata[1].DataType = "longtext"
				snapshot := doc.dataPage.RowSnapshots[row]
				release = make(chan struct{})
				s.mysqlDataRowReader = func(context.Context, int64, string, string, mysqlsvc.MySQLRowRef) (*mysqlsvc.MySQLDataRow, error) {
					<-release
					return &mysqlsvc.MySQLDataRow{Snapshot: snapshot, Values: map[string]mysqlsvc.MySQLDataValue{"note": {Text: "完整原文\n\t完整原文\n\t完整原文"}}}, nil
				}
				tt.Frame()
			}
			cell := editingCellBounds(t, doc, row, 1)
			first, last := doc.dataState.Visible()
			tt.ClickAt(cell.X+cell.W/2, cell.Y+cell.H/2)
			if release != nil {
				tt.Frame()
				close(release)
			}
			waitEditing(t, s, doc, tt)
			tt.Frame()
			if e := doc.dataEdit.Editor; e == nil || e.Row != row || e.Field != "note" {
				t.Fatal("click did not edit the requested row")
			}
			input, ok := tt.Find("单元格编辑 note")
			if !ok || input != cell {
				t.Fatalf("click moved the row: before=%+v editor=%+v", cell, input)
			}
			if nextFirst, nextLast := doc.dataState.Visible(); nextFirst != first || nextLast != last {
				t.Fatalf("click changed viewport: %d..%d -> %d..%d", first, last, nextFirst, nextLast)
			}
			tt.Key(0, ui.KeyEscape)
			tt.Frame()
			if restored := editingCellBounds(t, doc, row, 1); restored != cell {
				t.Fatalf("cancel moved the row: before=%+v restored=%+v", cell, restored)
			}
		})
	}
}

func TestMySQLDataTabNavigationRevealsRowsOutsideViewport(t *testing.T) {
	for _, back := range []bool{false, true} {
		t.Run(fmt.Sprintf("back=%t", back), func(t *testing.T) {
			s, doc, tt := editingScrolledWorkspace(t)
			first, last := doc.dataState.Visible()
			row, source := last, 2
			if back {
				row, source = first, 0
			}
			s.beginMySQLCellEdit(ui.Services{}, doc, row, source, false)
			tt.Frame()
			mods := ui.Modifiers(0)
			if back {
				mods = ui.Shift
			}
			tt.Key(mods, ui.KeyTab)
			tt.Frame()
			wantRow, field := row+1, "id"
			if back {
				wantRow, field = row-1, "extra"
			}
			if e := doc.dataEdit.Editor; e == nil || e.Row != wantRow || e.Field != field {
				t.Fatalf("Tab did not navigate beyond viewport: %+v", e)
			}
			if visibleFirst, visibleLast := doc.dataState.Visible(); wantRow < visibleFirst || wantRow > visibleLast {
				t.Fatalf("navigation left row %d hidden outside %d..%d", wantRow, visibleFirst, visibleLast)
			}
			if _, ok := tt.Find("单元格编辑 " + field); !ok {
				t.Fatal("navigation did not render the target input")
			}
		})
	}
}

func TestMySQLDataCompleteReadKeepsHorizontalScrollPosition(t *testing.T) {
	s, doc, tt := editingScrolledWorkspace(t)
	const row = 60
	doc.dataState.Columns.Widths = map[string]float32{"id": 400, "note": 400, "extra": 400}
	doc.dataPage.Rows[row][1].Truncated = true
	snapshot := doc.dataPage.RowSnapshots[row]
	release := make(chan struct{})
	s.mysqlDataRowReader = func(context.Context, int64, string, string, mysqlsvc.MySQLRowRef) (*mysqlsvc.MySQLDataRow, error) {
		<-release
		return &mysqlsvc.MySQLDataRow{Snapshot: snapshot, Values: map[string]mysqlsvc.MySQLDataValue{"note": {Text: "complete text"}}}, nil
	}
	tt.Frame()
	tt.Frame()
	before := editingCellBounds(t, doc, row, 1)
	tt.Scroll(before.X+20, before.Y+10, 180, 0)
	tt.Frame()
	cell := editingCellBounds(t, doc, row, 1)
	if cell.X >= before.X {
		t.Fatal("fixture did not scroll horizontally")
	}
	tt.ClickAt(cell.X+20, cell.Y+10)
	tt.Frame()
	close(release)
	waitEditing(t, s, doc, tt)
	tt.Frame()
	if input, ok := tt.Find("单元格编辑 note"); !ok || input != cell {
		t.Fatalf("complete read reset the scrolled cell: before=%+v input=%+v", cell, input)
	}
}

func TestMySQLDataF2RevealsSelectedRowAfterScrollingAway(t *testing.T) {
	_, doc, tt := editingScrolledWorkspace(t)
	const row = 60
	cell := editingCellBounds(t, doc, row, 1)
	tt.ClickAt(cell.X+20, cell.Y+10)
	tt.Key(0, ui.KeyEscape)
	doc.dataState.ScrollTo(90, ui.Start)
	tt.Frame()
	if first, last := doc.dataState.Visible(); row >= first && row <= last {
		t.Fatal("fixture did not scroll the selected row away")
	}
	tt.Key(0, ui.KeyF2)
	tt.Frame()
	if e := doc.dataEdit.Editor; e == nil || e.Row != row || e.Field != "note" {
		t.Fatal("F2 did not edit the selected cell")
	}
	if first, last := doc.dataState.Visible(); row < first || row > last {
		t.Fatal("F2 left the selected row outside the viewport")
	}
}

func TestMySQLDataKeyboardEditWithinViewportKeepsRowPosition(t *testing.T) {
	_, doc, tt := editingScrolledWorkspace(t)
	const row = 60
	cell := editingCellBounds(t, doc, row, 1)
	tt.ClickAt(cell.X+20, cell.Y+10)
	tt.Key(0, ui.KeyTab)
	if e := doc.dataEdit.Editor; e == nil || e.Row != row || e.Field != "extra" {
		t.Fatal("Tab did not move to the next visible field")
	}
	if input, ok := tt.Find("单元格编辑 extra"); !ok || input.Y != cell.Y || input.H != cell.H {
		t.Fatal("Tab repositioned an already visible row")
	}
	tt.Key(ui.Shift, ui.KeyTab)
	tt.Key(0, ui.KeyEscape)
	tt.Key(0, ui.KeyF2)
	if input, ok := tt.Find("单元格编辑 note"); !ok || input != cell {
		t.Fatal("F2 repositioned an already visible row")
	}
}
