package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func (s *State) inspector(c *ui.Context) {
	if s.profileMode {
		s.profileInspector(c)
		return
	}
	ui.Column(c).Fill().Background(theme.Panel).Children(func() {
		ui.Row(c).Height(31).Padding(4, 10).AlignItems(ui.Center).
			Background(theme.Header).Children(func() {
			ui.Text(c, "信息").Bold().FontSize(12)
			ui.Box(c).Grow(1)
			if ui.Button(c, "×").Label("隐藏信息面板").Padding(2, 6).Clicked() {
				s.inspectorOn = false
			}
		})
		ui.Column(c).Padding(16).Gap(14).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(10).Children(func() {
				theme.Icon(c, "connection", 40, theme.Muted)
				ui.Column(c).Gap(5).Children(func() {
					ui.Text(c, s.explorerName).FontSize(16).Bold()
					ui.Text(c, "模拟资源 / 未连接").FontSize(11).TextColor(theme.Muted)
				})
			})
			ui.Box(c).Height(1).Background(theme.Border)
			ui.Collapsible(c, "基本信息", &s.inspectorDetailsOpen, func() {
				s.property(c, "名称", s.explorerName)
				s.property(c, "类型", "模拟连接或对象")
				s.property(c, "状态", "仅界面预览")
				s.property(c, "工作区", s.currentTab().title)
			})
			ui.Divider(c)
			ui.Collapsible(c, "当前对象列表", &s.inspectorObjectsOpen, func() {
				s.property(c, "分类", s.objectKind)
			})
			ui.Text(c, "操作不会连接到数据库，也不会修改用户数据。").
				TextColor(theme.Muted).FontSize(11)
		})
	})
}

func (s *State) property(c *ui.Context, label, value string) {
	ui.Row(c).Gap(10).Children(func() {
		ui.Text(c, label).Width(62).FontSize(12).TextColor(theme.Muted)
		ui.Text(c, value).FontSize(12).SingleLine()
	})
}
