package dataworkspace

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func editingCellBounds(t *testing.T, doc *Document, row, source int) ui.Rect {
	t.Helper()
	for _, hit := range doc.edit.Hits {
		if hit.Row == row && hit.Source == source {
			return hit.Bounds
		}
	}
	t.Fatalf("missing cell bounds row=%d source=%d", row, source)
	return ui.Rect{}
}

func TestMySQLDataEditorFillsCellWithoutResizingRows(t *testing.T) {
	before := theme.CurrentMode()
	t.Cleanup(func() { theme.SetMode(before) })
	for _, mode := range []theme.Mode{theme.ModeDark, theme.ModeLight} {
		for _, zoom := range []int{75, 100, 150} {
			t.Run(fmt.Sprintf("%s/%d", mode, zoom), func(t *testing.T) {
				theme.SetMode(mode)
				_, doc, tt, _ := editingTestWorkspace(t)
				doc.edit.Zoom = zoom
				tt.Frame()
				tt.Frame()
				cell := editingCellBounds(t, doc, 0, 1)
				next, _ := tt.Find("beta")
				tt.ClickAt(cell.X+cell.W/2, cell.Y+cell.H/2)
				tt.Frame()
				input, ok := tt.Find("单元格编辑 note")
				if !ok || input != cell {
					t.Fatalf("editor does not cover cell: input=%+v cell=%+v", input, cell)
				}
				if after, _ := tt.Find("beta"); after != next {
					t.Fatalf("editing resized the next row: %+v -> %+v", next, after)
				}
				// Native grid lines own the final right/bottom pixel. The blue
				// frame must remain visible immediately inside each of four edges.
				blue := theme.PaletteFor(mode, true).Focus
				points := [][2]float32{{cell.X + 0.5, cell.Y + cell.H/2}, {cell.X + cell.W - 1.5, cell.Y + cell.H/2}, {cell.X + cell.W/2, cell.Y + 0.5}, {cell.X + cell.W/2, cell.Y + cell.H - 1.5}}
				for _, point := range points {
					got := tt.Image().RGBAAt(int(point[0]), int(point[1]))
					if got.R != blue.R || got.G != blue.G || got.B != blue.B {
						t.Fatalf("missing edge at %v: %v, want %v", point, got, blue)
					}
				}
				tt.Key(0, ui.KeyEscape)
				tt.Frame()
				if restored := editingCellBounds(t, doc, 0, 1); restored != cell {
					t.Fatalf("exit resized cell: %+v -> %+v", cell, restored)
				}
			})
		}
	}
}

func TestMySQLDataWholeCellClicksStayWithinTheirField(t *testing.T) {
	_, doc, tt, f := editingTestWorkspace(t)
	for _, target := range [][2]int{{0, 1}, {1, 2}, {2, 2}} {
		row, source := target[0], target[1]
		tt.Frame()
		cell := editingCellBounds(t, doc, row, source)
		for _, point := range [][2]float32{{0.5, 0.5}, {cell.W - 0.5, 0.5}, {0.5, cell.H - 0.5}, {cell.W - 0.5, cell.H - 0.5}, {cell.W / 2, cell.H / 2}} {
			tt.ClickAt(cell.X+point[0], cell.Y+point[1])
			if e := doc.edit.Editor; e == nil || e.Row != row || e.Field != doc.page.Columns[source] {
				t.Fatalf("click %v missed target %+v: %+v", point, target, e)
			}
			tt.Key(0, ui.KeyEscape)
			tt.Frame()
		}
	}
	cell := editingCellBounds(t, doc, 0, 1)
	tt.ClickAt(cell.X+cell.W, cell.Y+cell.H/2)
	if e := doc.edit.Editor; e == nil || e.Field != "extra" || e.Row != 0 {
		t.Fatalf("column boundary did not belong to adjacent field: %+v", e)
	}
	tt.Key(0, ui.KeyEscape)
	tt.ClickAt(cell.X+cell.W/2, cell.Y+cell.H)
	if e := doc.edit.Editor; e == nil || e.Field != "note" || e.Row != 1 {
		t.Fatalf("row boundary did not belong to adjacent row: %+v", e)
	}
	tt.Key(0, ui.KeyEscape)
	if f.writeCount() != 0 || mysqlDataHasDraft(doc) {
		t.Fatal("unchanged NULL/empty/text clicks created a draft or write")
	}
}

func TestMySQLDataFirstPaintAndResizeClicksUseVisibleBounds(t *testing.T) {
	s, doc, _, _ := editingTestWorkspace(t)
	// A new window has no previous-frame cell bounds. Click its first
	// committed picture without priming it with additional Frame calls.
	tt := ui.NewTester(func(c *ui.Context) {
		s.Consume(c.Services())
		theme.Apply(c)
		if !doc.closed {
			s.View(c, doc)
		}
		s.Modals(c)
	}, 1000, 650)
	if err := tt.Click("alpha"); err != nil {
		t.Fatal(err)
	}
	if e := doc.edit.Editor; e == nil || e.Row != 0 || e.Field != "note" {
		t.Fatal("first painted cell only selected instead of editing")
	}
	tt.Key(0, ui.KeyEscape)
	tt.SetSize(850, 650)
	if err := tt.Click("alpha"); err != nil {
		t.Fatal(err)
	}
	if e := doc.edit.Editor; e == nil || e.Row != 0 || e.Field != "note" {
		t.Fatal("resized cell used old pointer coordinates")
	}
}

func TestMySQLDataPointerObserverLeavesGesturesAndSelectionToNativeGrid(t *testing.T) {
	s, doc, tt, _ := editingTestWorkspace(t)
	cell := editingCellBounds(t, doc, 0, 1)
	doc.selection.Add(0)
	doc.selection.Add(1)
	doc.grid.ColumnSelection = []string{"id", "extra"}
	doc.grid.Cells = ui.ColumnRange{Anchor: "id", Cursor: "extra"}
	for _, event := range []ui.InputEvent{
		{Kind: ui.InputPointerUp},
		{Kind: ui.InputPointerDown},
		{Kind: ui.InputPointerMove, X: cell.W + 20},
		{Kind: ui.InputPointerUp, X: cell.W + 20},
		{Kind: ui.InputPointerDown, Mods: ui.Shift},
		{Kind: ui.InputPointerUp, Mods: ui.Shift},
		{Kind: ui.InputPointerDown, Button: 2},
	} {
		if s.mysqlDataCellPointer(ui.Services{}, doc, 0, 1, cell, event) {
			t.Fatal("passive editor observation consumed a native gesture")
		}
		if doc.edit.Editor != nil || doc.selection.Len() != 2 || doc.grid.Cells.Anchor != "id" || doc.grid.Cells.Cursor != "extra" || len(doc.grid.ColumnSelection) != 2 {
			t.Fatal("pointer observation changed selection or opened an editor")
		}
	}
	tt.Frame()
}

func TestMySQLDataSingleClickReadsCompleteTextAndReusesDraft(t *testing.T) {
	for _, kind := range []string{"longtext", "json"} {
		t.Run(kind, func(t *testing.T) {
			s, doc, tt, f := editingTestWorkspace(t)
			full := strings.Repeat("中文😀\n\t", 700)
			if kind == "json" {
				full = `{"text":"` + strings.Repeat("中文😀", 700) + `"}`
			}
			doc.page.ColumnMetadata[2].DataType = kind
			doc.page.Rows[0][2] = mysqlsvc.MySQLDataCell{Text: "preview", Truncated: true}
			var reads atomic.Int32
			snapshot := doc.page.RowSnapshots[0]
			s.RowReader = func(context.Context, int64, string, string, mysqlsvc.MySQLRowRef) (*mysqlsvc.MySQLDataRow, error) {
				reads.Add(1)
				return &mysqlsvc.MySQLDataRow{Snapshot: snapshot, Values: map[string]mysqlsvc.MySQLDataValue{"extra": {Text: full}}}, nil
			}
			tt.Frame()
			cell := editingCellBounds(t, doc, 0, 2)
			tt.ClickAt(cell.X+cell.W/2, cell.Y+cell.H/2)
			waitEditing(t, s, doc, tt)
			if e := doc.edit.Editor; e == nil || e.Dedicated || e.Text != full || copyFocusedText(tt) != full {
				t.Fatal("single click did not focus/select complete text in the cell")
			}
			tt.Key(0, ui.KeyEscape)
			if mysqlDataHasDraft(doc) || f.writeCount() != 0 {
				t.Fatal("canceling a complete read changed data")
			}
			tt.ClickAt(cell.X+cell.W/2, cell.Y+cell.H/2)
			waitEditing(t, s, doc, tt)
			// Appending keeps every original newline and Document, including those
			// outside the bounded preview and the visible input viewport.
			tt.Key(0, ui.KeyEnd)
			tt.Type("tail")
			tt.Key(0, ui.KeyEnter)
			if d := mysqlDataDraftForRow(doc, 0); d == nil || d.Values["extra"].Text != full+"tail" {
				t.Fatal("editing lost original complete text")
			}
			s.beginMySQLCellEdit(ui.Services{}, doc, 0, 2, false)
			tt.Frame()
			if reads.Load() != 2 || doc.edit.Editor.Text != full+"tail" || doc.edit.Editor.Dedicated {
				t.Fatal("revisiting complete draft re-read or discarded it")
			}
		})
	}
}

func TestMySQLDataSingleClickBinaryAndNewRecordUseHexEditor(t *testing.T) {
	s, doc, tt, _ := editingTestWorkspace(t)
	doc.page.ColumnMetadata[2].DataType = "blob"
	doc.page.Rows[0][2] = mysqlsvc.MySQLDataCell{Text: "000141"}
	snapshot := doc.page.RowSnapshots[0]
	s.RowReader = func(context.Context, int64, string, string, mysqlsvc.MySQLRowRef) (*mysqlsvc.MySQLDataRow, error) {
		return &mysqlsvc.MySQLDataRow{Snapshot: snapshot, Values: map[string]mysqlsvc.MySQLDataValue{"extra": {Binary: true, Bytes: []byte{0, 1, 65}}}}, nil
	}
	tt.Frame()
	if err := tt.Click("000141"); err != nil {
		t.Fatal(err)
	}
	waitEditing(t, s, doc, tt)
	if e := doc.edit.Editor; e == nil || !e.Dedicated || !e.Open || !e.Original.Binary || e.Text != "000141" {
		t.Fatalf("binary single click did not open hex editor: %+v", e)
	}
	doc.edit.Editor.Text = "invalid"
	s.finishMySQLCellEdit(doc, true)
	if doc.edit.Editor == nil || doc.edit.Err == "" || mysqlDataDraftForRow(doc, 0) != nil {
		t.Fatal("invalid hex was accepted")
	}
	doc.edit.Editor.Text = "000141FF00"
	s.finishMySQLCellEdit(doc, true)
	if v := mysqlDataDraftForRow(doc, 0).Values["extra"]; !v.Binary || !bytes.Equal(v.Bytes, []byte{0, 1, 65, 255, 0}) {
		t.Fatalf("binary editor changed bytes: %+v", v)
	}
	s.discardMySQLData(doc)
	doc.edit.Batch = true
	s.addMySQLDataRecord(ui.Services{}, doc)
	s.finishMySQLCellEdit(doc, false)
	s.beginMySQLCellEdit(ui.Services{}, doc, 3, 2, false)
	if e := doc.edit.Editor; e == nil || !e.Dedicated || !e.Original.Binary || !e.Original.Default {
		t.Fatalf("new binary record did not use hex/default semantics: %+v", e)
	}
	doc.edit.Editor.Text = "00FF"
	s.finishMySQLCellEdit(doc, true)
	if v := mysqlDataDraftForRow(doc, 3).Values["extra"]; !v.Binary || !bytes.Equal(v.Bytes, []byte{0, 255}) {
		t.Fatalf("new binary value was submitted as text: %+v", v)
	}
}

func TestMySQLDataCompleteReadFailureCannotEditPreview(t *testing.T) {
	for _, failure := range []string{"unavailable", "limit", "conflict", "missing-field", "missing-row", "changed-identity"} {
		t.Run(failure, func(t *testing.T) {
			s, doc, tt, f := editingTestWorkspace(t)
			doc.page.Rows[0][2] = mysqlsvc.MySQLDataCell{Text: "preview", Truncated: true}
			snapshot := *doc.page.RowSnapshots[0]
			if failure == "conflict" {
				snapshot.Metadata = "changed"
			}
			if failure != "unavailable" {
				s.RowReader = func(context.Context, int64, string, string, mysqlsvc.MySQLRowRef) (*mysqlsvc.MySQLDataRow, error) {
					if failure == "limit" {
						return nil, mysqlsvc.ErrMySQLDataEditLimit
					}
					if failure == "missing-row" {
						return nil, nil
					}
					values := map[string]mysqlsvc.MySQLDataValue{"extra": {Text: "complete"}}
					if failure == "missing-field" {
						delete(values, "extra")
					}
					return &mysqlsvc.MySQLDataRow{Snapshot: &snapshot, Values: values}, nil
				}
			}
			s.beginMySQLCellEdit(ui.Services{}, doc, 0, 2, false)
			if failure == "changed-identity" {
				ref := *doc.page.RowRefs[0]
				ref.Values = [][]byte{[]byte("other row")}
				doc.page.RowRefs[0] = &ref
			}
			waitEditing(t, s, doc, tt)
			if doc.edit.Editor != nil || doc.edit.Err == "" || mysqlDataHasDraft(doc) || f.writeCount() != 0 {
				t.Fatal("failed complete read opened editor or changed data")
			}
		})
	}
}
