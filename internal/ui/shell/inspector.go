package shell

import (
	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) inspector(c *ui.Context) {
	palette := theme.Colors(c)

	if s.profileMode {
		s.profileInspector(c)
		return
	}
	ui.Column(c).Fill().Background(palette.Panel).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, 31)).Padding(4, 10).AlignItems(ui.Center).
			Background(palette.Header).Children(func() {
			ui.Text(c, locales.Text("信息")).Bold().FontSize(theme.Font(c, 12))
			ui.Box(c).Grow(1)
			if ui.Button(c, "×").Label(locales.Text("隐藏信息面板")).Padding(2, 6).Clicked() {
				s.inspectorOn = false
			}
		})
		ui.Column(c).Padding(16).Gap(14).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(10).Children(func() {
				theme.Icon(c, "connection", 40, palette.Muted)
				ui.Column(c).Gap(5).Children(func() {
					ui.Text(c, locales.Text(s.preview.key)).FontSize(theme.Font(c, 16)).Bold()
					ui.Text(c, locales.Text("模拟资源 / 未连接")).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
				})
			})
			ui.Box(c).Height(1).Background(palette.Border)
			ui.Collapsible(c, locales.Text("基本信息"), &s.preview.detailsOpen, func() {
				theme.PropertyRow(c, locales.Text("名称"), s.preview.key)
				theme.PropertyRow(c, locales.Text("类型"), locales.Text("模拟连接或对象"))
				theme.PropertyRow(c, locales.Text("状态"), locales.Text("仅界面预览"))
				theme.PropertyRow(c, locales.Text("工作区"), locales.Text(s.currentTab().title))
			})
			ui.Divider(c)
			ui.Collapsible(c, locales.Text("当前对象列表"), &s.preview.objectsOpen, func() {
				theme.PropertyRow(c, locales.Text("分类"), locales.Text(s.preview.kind))
			})
			ui.Text(c, locales.Text("操作不会连接到数据库，也不会修改用户数据。")).
				TextColor(palette.Muted).FontSize(theme.Font(c, 11))
		})
	})
}
