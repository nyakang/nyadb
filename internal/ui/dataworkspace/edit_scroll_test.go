package dataworkspace

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/locales"
)

func editingScrolledWorkspace(t *testing.T) (*State, *Document, *ui.Tester) {
	t.Helper()
	s, doc, tt, _ := editingTestWorkspace(t)
	for row := len(doc.page.Rows); row < 100; row++ {
		id := strconv.Itoa(41 + row)
		ref := &mysqlsvc.MySQLRowRef{Database: doc.database, Table: doc.table, Columns: []string{"id"}, Values: [][]byte{[]byte(id)}}
		doc.page.Rows = append(doc.page.Rows, []mysqlsvc.MySQLDataCell{{Text: id}, {Text: fmt.Sprintf("note-%03d", row)}, {Text: fmt.Sprintf("extra-%03d", row)}})
		doc.page.RowRefs = append(doc.page.RowRefs, ref)
		doc.page.RowSnapshots = append(doc.page.RowSnapshots, &mysqlsvc.MySQLRowSnapshot{Ref: *ref, Columns: doc.page.Columns, Metadata: "fixture"})
	}
	doc.grid.ScrollTo(55, ui.Start)
	tt.Frame()
	tt.Frame()
	return s, doc, tt
}

func TestMySQLDataPendingCompleteReadKeepsCellPosition(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure=%t", fail), func(t *testing.T) {
			s, doc, tt := editingScrolledWorkspace(t)
			const row = 60
			doc.page.Rows[row][1].Truncated = true
			snapshot := doc.page.RowSnapshots[row]
			release := make(chan struct{})
			s.RowReader = func(ctx context.Context, _ int64, _, _ string, _ mysqlsvc.MySQLRowRef) (*mysqlsvc.MySQLDataRow, error) {
				select {
				case <-release:
					if fail {
						return nil, mysqlsvc.ErrMySQLDataEditLimit
					}
					return &mysqlsvc.MySQLDataRow{Snapshot: snapshot, Values: map[string]mysqlsvc.MySQLDataValue{"note": {Text: "complete text"}}}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			t.Cleanup(func() { s.stopMySQLDataEdit(doc) })
			tt.Frame()
			tt.Frame()
			cell := editingCellBounds(t, doc, row, 1)
			first, last := doc.grid.Visible()
			tt.ClickAt(cell.X+20, cell.Y+10)
			for frame := 0; frame < 3; frame++ {
				tt.Frame()
				if !doc.edit.Busy || !tt.HasText(locales.Text("正在处理表数据…")) {
					t.Fatal("fixture did not hold the complete read pending")
				}
				if pending := editingCellBounds(t, doc, row, 1); pending != cell {
					t.Fatalf("pending read moved the cell in frame %d: before=%+v pending=%+v", frame, cell, pending)
				}
				if nextFirst, nextLast := doc.grid.Visible(); nextFirst != first || nextLast != last {
					t.Fatalf("pending read changed viewport: %d..%d -> %d..%d", first, last, nextFirst, nextLast)
				}
			}
			close(release)
			waitEditing(t, s, doc, tt)
			for frame := 0; frame < 3; frame++ {
				tt.Frame()
				if fail {
					if doc.edit.Err == "" || !tt.HasText(doc.edit.Err) {
						t.Fatal("failed read did not show its error")
					}
					if after := editingCellBounds(t, doc, row, 1); after != cell {
						t.Fatalf("failed read moved the cell: before=%+v after=%+v", cell, after)
					}
				} else if input, ok := tt.Find("单元格编辑 note"); !ok || input != cell {
					t.Fatalf("completed read moved the cell: before=%+v input=%+v", cell, input)
				}
				if nextFirst, nextLast := doc.grid.Visible(); nextFirst != first || nextLast != last {
					t.Fatalf("completed read changed viewport: %d..%d -> %d..%d", first, last, nextFirst, nextLast)
				}
			}
		})
	}
}

func TestMySQLDataUnsupportedFieldErrorKeepsCellPosition(t *testing.T) {
	_, doc, tt := editingScrolledWorkspace(t)
	const row = 60
	doc.page.ColumnMetadata[1].DataType = "geometry"
	tt.Frame()
	tt.Frame()
	cell := editingCellBounds(t, doc, row, 1)
	tt.ClickAt(cell.X+20, cell.Y+10)
	for frame := 0; frame < 3; frame++ {
		tt.Frame()
		if doc.edit.Err == "" || !tt.HasText(doc.edit.Err) {
			t.Fatal("unsupported field did not show its error")
		}
		if after := editingCellBounds(t, doc, row, 1); after != cell {
			t.Fatalf("edit error moved the cell in frame %d: before=%+v after=%+v", frame, cell, after)
		}
	}
	other := editingCellBounds(t, doc, row, 2)
	tt.ClickAt(other.X+20, other.Y+10)
	if input, ok := tt.Find("单元格编辑 extra"); !ok || input != other {
		t.Fatalf("clearing the edit error moved the next cell: before=%+v input=%+v", other, input)
	}
}

func TestMySQLDataClickEditingKeepsScrolledRowPosition(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprintf("complete=%t", complete), func(t *testing.T) {
			s, doc, tt := editingScrolledWorkspace(t)
			const row = 60
			var release chan struct{}
			if complete {
				doc.page.Rows[row][1].Truncated = true
				doc.page.ColumnMetadata[1].DataType = "longtext"
				snapshot := doc.page.RowSnapshots[row]
				release = make(chan struct{})
				s.RowReader = func(context.Context, int64, string, string, mysqlsvc.MySQLRowRef) (*mysqlsvc.MySQLDataRow, error) {
					<-release
					return &mysqlsvc.MySQLDataRow{Snapshot: snapshot, Values: map[string]mysqlsvc.MySQLDataValue{"note": {Text: "完整原文\n\t完整原文\n\t完整原文"}}}, nil
				}
				tt.Frame()
			}
			cell := editingCellBounds(t, doc, row, 1)
			first, last := doc.grid.Visible()
			tt.ClickAt(cell.X+cell.W/2, cell.Y+cell.H/2)
			if release != nil {
				tt.Frame()
				close(release)
			}
			waitEditing(t, s, doc, tt)
			tt.Frame()
			if e := doc.edit.Editor; e == nil || e.Row != row || e.Field != "note" {
				t.Fatal("click did not edit the requested row")
			}
			input, ok := tt.Find("单元格编辑 note")
			if !ok || input != cell {
				t.Fatalf("click moved the row: before=%+v editor=%+v", cell, input)
			}
			if nextFirst, nextLast := doc.grid.Visible(); nextFirst != first || nextLast != last {
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
			first, last := doc.grid.Visible()
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
			if e := doc.edit.Editor; e == nil || e.Row != wantRow || e.Field != field {
				t.Fatalf("Tab did not navigate beyond viewport: %+v", e)
			}
			if visibleFirst, visibleLast := doc.grid.Visible(); wantRow < visibleFirst || wantRow > visibleLast {
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
	doc.grid.Columns.Widths = map[string]float32{"id": 400, "note": 400, "extra": 400}
	doc.page.Rows[row][1].Truncated = true
	snapshot := doc.page.RowSnapshots[row]
	release := make(chan struct{})
	s.RowReader = func(context.Context, int64, string, string, mysqlsvc.MySQLRowRef) (*mysqlsvc.MySQLDataRow, error) {
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
	doc.grid.ScrollTo(90, ui.Start)
	tt.Frame()
	if first, last := doc.grid.Visible(); row >= first && row <= last {
		t.Fatal("fixture did not scroll the selected row away")
	}
	tt.Key(0, ui.KeyF2)
	tt.Frame()
	if e := doc.edit.Editor; e == nil || e.Row != row || e.Field != "note" {
		t.Fatal("F2 did not edit the selected cell")
	}
	if first, last := doc.grid.Visible(); row < first || row > last {
		t.Fatal("F2 left the selected row outside the viewport")
	}
}

func TestMySQLDataKeyboardEditWithinViewportKeepsRowPosition(t *testing.T) {
	_, doc, tt := editingScrolledWorkspace(t)
	const row = 60
	cell := editingCellBounds(t, doc, row, 1)
	tt.ClickAt(cell.X+20, cell.Y+10)
	tt.Key(0, ui.KeyTab)
	if e := doc.edit.Editor; e == nil || e.Row != row || e.Field != "extra" {
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
