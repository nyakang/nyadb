package shell

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// The inspector follows the active real data tab rather than the connection
// profile, but never asks the server for an unbounded cell value.
func (s *State) mysqlDataInspector(c *ui.Context, t *tab) {
	ui.Column(c).Fill().Background(theme.Panel).Children(func() {
		ui.Row(c).Height(31).Padding(4, 10).AlignItems(ui.Center).Background(theme.Header).Children(func() {
			ui.Text(c, "字段信息").Bold().FontSize(12)
			ui.Box(c).Grow(1)
			if ui.Button(c, "×").Label("隐藏信息面板").Padding(2, 6).Clicked() {
				s.inspectorOn = false
			}
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(12).Gap(9).Children(func() {
				theme.Icon(c, "table", 30, theme.Blue)
				ui.Text(c, t.dataTable).Bold().FontSize(15).SingleLine()
				ui.Text(c, t.dataDatabase+" · 只读").FontSize(11).TextColor(theme.Muted)
				ui.Box(c).Height(1).Background(theme.Border)
				if t.dataPage == nil {
					ui.Text(c, "正在读取数据…").TextColor(theme.Muted)
					return
				}
				s.property(c, "当前页", fmt.Sprintf("%d", t.dataOffset/int64(t.dataLimit)+1))
				s.property(c, "行数", fmt.Sprintf("%d", len(t.dataPage.Rows)))
				if t.dataSortColumn != "" {
					s.property(c, "排序", t.dataSortColumn)
				}
				if t.dataFilterMode != "" {
					s.property(c, "筛选", mysqlDataFilterStatus(t))
				}
				ui.Divider(c)
				name, cell, ok := mysqlSelectedDataCell(t)
				if !ok {
					ui.Text(c, "点击数据单元格以查看字段和值的预览").FontSize(12).TextColor(theme.Muted)
					return
				}
				if t.dataChunkField != "" && (t.dataChunkField != name || t.dataChunkRow != t.dataSelectedRow) {
					s.cancelMySQLChunk(t)
				}
				s.property(c, "字段", name)
				s.property(c, "行号", fmt.Sprintf("%d", t.dataOffset+int64(t.dataSelectedRow)+1))
				for i, candidate := range t.dataPage.Columns {
					if candidate == name && i < len(t.dataPage.ColumnKinds) {
						s.property(c, "类型", t.dataPage.ColumnKinds[i])
						break
					}
				}
				if cell.Null {
					s.property(c, "状态", "SQL NULL")
				} else if cell.Truncated {
					s.property(c, "状态", "已截断的预览")
				} else {
					s.property(c, "状态", "预览完整")
				}
				ui.Text(c, cell.Text).FontSize(12).SingleLine()
				if ui.Button(c, "复制单元格预览").Clicked() {
					c.WriteClipboard(mysqlDataCopyPreview(cell))
				}
				row := t.dataSelectedRow
				canRead := !cell.Null && row >= 0 && row < len(t.dataPage.RowRefs) &&
					t.dataPage.RowRefs[row] != nil
				if canRead {
					ui.Text(c, "大字段按需读取 · 单次最多 1024 字符/字节").FontSize(11).TextColor(theme.Muted)
					if ui.Button(c, "读取字段片段").Label("读取所选字段下一段").
						Disabled(t.dataChunkLoading || t.dataChunkDone).Clicked() {
						s.loadMySQLChunk(c.Services(), t)
					}
					if t.dataChunkLoading {
						ui.Text(c, "正在读取字段片段…").FontSize(11)
					}
					if t.dataChunkErr != "" {
						ui.Text(c, t.dataChunkErr).FontSize(11).TextColor(theme.Gold)
					}
					if t.dataChunkField == name && t.dataChunkRow == row && t.dataChunkNext > 0 {
						s.property(c, "已读位置", fmt.Sprintf("%d / %d", t.dataChunkNext, t.dataChunkTotal))
						ui.Text(c, t.dataChunkText).FontSize(11)
						if ui.Button(c, "复制此片段").Clicked() {
							c.WriteClipboard(t.dataChunkText)
						}
					}
				}
			})
		})
	})
}
