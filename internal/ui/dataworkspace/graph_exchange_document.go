package dataworkspace

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// This is another purpose of the sole data Document. Its original namespace
// lease has no grid, rows or invented label; transfer and leave state are shared.
func (s *State) graphExchangeDocument(c *ui.Context, d *Document) {
	available := s.canUseSession(d) && s.session().ProfileID == d.profileID && d.capabilities.GraphExchange
	disabled := !available || d.Busy() || d.transfer.Uncertain || d.edit.Uncertain != nil
	ui.Column(c).Fill().Padding(16).Gap(10).Children(func() {
		ui.Text(c, locales.Text("完整图交换")).Bold()
		ui.Text(c, fmt.Sprintf(locales.Text("目标数据库：%s"), d.database))
		ui.Text(c, locales.Text("导入和导出整个原数据库的图数据；无需已有标签或关系类型。"))
		if !available {
			ui.Text(c, locales.Text("原数据库会话已失效，请关闭文档后重新打开")).TextColor(theme.Colors(c).Error)
		}
		if d.err != "" {
			ui.Text(c, d.err).TextColor(theme.Colors(c).Error)
		}
		ui.Row(c).Wrap().Gap(8).Children(func() {
			if ui.Button(c, locales.Text("导入向导…")).Disabled(disabled).Clicked() {
				s.openDataImport(d)
			}
			if ui.Button(c, locales.Text("导出向导…")).Disabled(disabled).Clicked() {
				s.openDataExport(d)
			}
			s.nativeWriteReceiptActions(c, d)
		})
		if d.transfer.Err != "" {
			ui.Text(c, d.transfer.Err)
		}
	})
}
