package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// connectionMySQLDatabases reproduces the offline Database tab: a custom-list
// toggle, a native two-column table and add/remove actions. The original reads
// server metadata when custom lists are enabled. NyaDB deliberately does not
// invent metadata or attempt a connection before a database driver exists.
func (s *State) connectionMySQLDatabases(c *ui.Context, width, height float32) {
	palette := theme.Colors(c)

	pageWidth := max(float32(520), width-40)
	pageHeight := max(float32(280), height-174)
	enabled := &s.connectionDraft.MySQLAdvanced.UseCustomDatabaseList
	ui.Column(c).Width(pageWidth).Height(pageHeight).Gap(6).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, connectionFormInputHeight)).AlignItems(ui.Center).Children(func() {
			connectionFormCheckbox(c, enabled, "使用自定义数据库列表", false)
		})
		columns := []ui.TableColumn{
			{Title: "数据库", Width: pageWidth - 92},
			{Title: "自动打开", Width: 88},
		}
		// No connection session exists yet. A zero-row native table preserves
		// the exact empty state without fake databases or checked entries.
		theme.Table(c, &s.mysqlDatabaseTable, columns, 0, func(_, _ int) {}).
			Width(pageWidth).Grow(1).Border(1, palette.Border).
			Disabled(!*enabled)
		ui.Row(c).Height(theme.RowHeight(c, 30)).Gap(8).Children(func() {
			ui.Button(c, "添加数据库到列表").
				Width(198).Height(theme.RowHeight(c, 29)).Disabled(true).
				Label("添加数据库到列表（需要连接 MySQL）")
			ui.Button(c, "从列表中移除数据库").
				Width(198).Height(theme.RowHeight(c, 29)).Disabled(true).
				Label("从列表中移除数据库（列表为空）")
		})
		if *enabled {
			ui.Text(c, "需要建立数据库连接后才能读取列表").
				FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
		}
	})
}
