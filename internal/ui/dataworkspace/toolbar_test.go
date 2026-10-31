package dataworkspace

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestTableToolbarKeepsOrderAndNativeOverflow(t *testing.T) {
	s, doc, _, _ := editingTestWorkspace(t)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, doc); s.Modals(c) }, 1800, 650)
	last := float32(-1)
	for _, label := range []string{"表配置文件", "开始事务", "单元格编辑器", "筛选 & 排序", "列", "历史", "AI 自动填充", "数据分析", "导入", "导出"} {
		r, ok := tt.Find(label)
		if !ok || r.X <= last {
			t.Fatalf("missing or misplaced command %s: %+v", label, r)
		}
		last = r.X
	}
	tt.SetSize(500, 650)
	toolbar, ok := tt.Find("数据操作")
	if !ok || toolbar.H > theme.CommandBarHeight+4 {
		t.Fatalf("toolbar wrapped: %+v", toolbar)
	}
	if err := clickDataToolbar(tt, "列"); err != nil {
		t.Fatal(err)
	}
	if !doc.toolbar.columnsOpen || !tt.HasText("显示或隐藏列") {
		t.Fatal("overflow lost columns command")
	}
	if err := clickDataToolbar(tt, "历史"); err != nil {
		t.Fatal(err)
	}
	if doc.toolbar.dock != "历史" || !tt.HasText("时间戳") {
		t.Fatalf("overflow lost history dock: %v", tt.Texts())
	}
}

func TestTableToolbarFillUsesVisualColumnsAndWholeRowSelection(t *testing.T) {
	s, doc, _, f := editingTestWorkspace(t)
	doc.selectedRow = 0
	doc.selection.Add(0)
	doc.grid.Columns.Order = []string{"note", "id", "extra"}
	doc.grid.Cells = ui.ColumnRange{Anchor: "note", Cursor: "extra"}
	doc.fill = mysqlDataFillState{Mode: "序列", Start: "100", Step: "1"}
	if err := s.fillSelectedData(doc); err != nil {
		t.Fatal(err)
	}
	if doc.page.Rows[0][1].Text != "100" || doc.page.Rows[0][0].Text != "101" || doc.page.Rows[0][2].Text != "102" {
		t.Fatalf("fill ignored visual order: %+v", doc.page.Rows[0])
	}
	s.discardMySQLData(doc)
	doc.hiddenColumns["id"] = true
	doc.grid.Cells = ui.ColumnRange{All: true}
	doc.fill = mysqlDataFillState{Mode: "值", Text: "whole row"}
	if err := s.fillSelectedData(doc); err != nil {
		t.Fatal(err)
	}
	if doc.page.Rows[0][0].Text != "41" || doc.page.Rows[0][1].Text != "whole row" || doc.page.Rows[0][2].Text != "whole row" || f.writeCount() != 0 {
		t.Fatalf("whole-row fill changed hidden fields or saved: %+v", doc.page.Rows[0])
	}
}

func TestTableToolbarDockPreservesRectangleSelection(t *testing.T) {
	s, doc, _, f := editingTestWorkspace(t)
	doc.selectedRow = 1
	doc.selection.Add(0)
	doc.selection.Add(1)
	doc.grid.Cells = ui.ColumnRange{Anchor: "note", Cursor: "extra"}
	doc.toolbar.dock = "文本"
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, doc) }, 1400, 650)
	for range 3 {
		tt.Frame()
	}
	fields, rows, _ := mysqlDataSelectedPreview(doc)
	if doc.selection.Len() != 2 || doc.grid.Cells.Anchor != "note" || doc.grid.Cells.Cursor != "extra" ||
		!slices.Equal(fields, []string{"note", "extra"}) || len(rows) != 2 || doc.edit.Editor != nil || f.writeCount() != 0 {
		t.Fatalf("dock changed rectangle: fields=%v rows=%v range=%+v", fields, rows, doc.grid.Cells)
	}
}

func TestTableToolbarFillRejectsBinaryAndSpatialBeforeChangingTextCells(t *testing.T) {
	for _, kind := range []string{"blob", "bit", "point"} {
		t.Run(kind, func(t *testing.T) {
			s, doc, _, f := editingTestWorkspace(t)
			doc.selectedRow = 0
			doc.selection.Add(0)
			doc.grid.Cells = ui.ColumnRange{Anchor: "note", Cursor: "extra"}
			doc.page.ColumnMetadata[2].DataType = kind
			doc.fill = mysqlDataFillState{Mode: "值", Text: "do not partially apply"}
			if err := s.fillSelectedData(doc); err == nil || mysqlDataHasDraft(doc) || doc.page.Rows[0][1].Text != "alpha" || f.writeCount() != 0 {
				t.Fatal("unsupported fill partly changed text or sent a binary value as text")
			}
		})
	}
}

func TestTableToolbarDockStagesTextAndPreservesCloseDraft(t *testing.T) {
	s, doc, _, f := editingTestWorkspace(t)
	doc.selectedRow = 0
	doc.selection.Add(0)
	doc.grid.Cells = ui.ColumnRange{Anchor: "note", Cursor: "note"}
	tt := ui.NewTester(func(c *ui.Context) { s.Consume(c.Services()); theme.Apply(c); s.View(c, doc); s.Modals(c) }, 1400, 650)
	if err := tt.Click("单元格编辑器"); err != nil {
		t.Fatal(err)
	}
	waitEditing(t, s, doc, tt)
	if doc.toolbar.dock != "文本" || !tt.HasText("单元格编辑器内容") {
		t.Fatalf("editor did not dock: %v", tt.Texts())
	}
	if err := tt.Click("单元格编辑器内容"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("docked draft")
	if err := tt.Click("关闭编辑器"); err != nil {
		t.Fatal(err)
	}
	if doc.toolbar.dock != "" || !mysqlDataHasDraft(doc) || doc.page.Rows[0][1].Text != "docked draft" || f.writeCount() != 0 {
		t.Fatal("dock close discarded or implicitly wrote the edit")
	}
}

func TestTableToolbarFillValidatesWholeSelectionBeforeStaging(t *testing.T) {
	s, doc, _, f := editingTestWorkspace(t)
	doc.selectedRow = 0
	doc.selection.Add(0)
	doc.selection.Add(1)
	doc.grid.Cells = ui.ColumnRange{Anchor: "note", Cursor: "note"}
	doc.fill = mysqlDataFillState{Mode: "值", Text: "filled"}
	if err := s.fillSelectedData(doc); err != nil {
		t.Fatal(err)
	}
	if !doc.edit.Batch || len(doc.edit.Drafts) != 2 || doc.page.Rows[0][1].Text != "filled" || doc.page.Rows[1][1].Text != "filled" || f.writeCount() != 0 {
		t.Fatal("fill did not stage complete selection")
	}
	s.discardMySQLData(doc)
	doc.page.ColumnMetadata[1].Nullable = false
	doc.fill = mysqlDataFillState{Mode: "NULL"}
	before := doc.page.Rows[0][1]
	if err := s.fillSelectedData(doc); err == nil {
		t.Fatal("accepted invalid NULL fill")
	}
	if doc.page.Rows[0][1] != before || mysqlDataHasDraft(doc) {
		t.Fatal("invalid fill partly changed grid")
	}
	doc.page.ColumnMetadata[1].Nullable = true
	doc.fill = mysqlDataFillState{Mode: "序列", Start: "9223372036854775807", Step: "1"}
	if err := s.fillSelectedData(doc); err == nil {
		t.Fatal("integer overflow accepted")
	}
	if mysqlDataHasDraft(doc) {
		t.Fatal("overflow partly filled grid")
	}
}
