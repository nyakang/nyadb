package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) emptyWorkbench(c *ui.Context) {
	ui.Column(c).Grow(1).Center().Gap(14).Padding(24).Children(func() {
		ui.Text(c, locales.Text("开始使用 NyaDB")).FontSize(theme.Font(c, 20)).Bold()
		ui.Text(c, locales.Text("连接数据库，或打开 SQL 文件继续工作。")).TextColor(theme.Colors(c).Muted)
		ui.Row(c).Gap(10).Children(func() {
			if ui.PrimaryButton(c, locales.Text("新建连接…")).Clicked() {
				s.openConnectionDialog()
			}
			if ui.Button(c, locales.Text("打开 SQL…")).Disabled(s.sqlWorkspace.PersistenceLoading()).Clicked() {
				s.openQuery()
				if d := s.currentTab().query; d != nil {
					s.sqlWorkspace.OpenFile(d)
				}
			}
			if ui.Button(c, locales.Text("恢复最近工作")).Disabled(!s.sqlWorkspace.PersistenceBlocked()).Clicked() {
				s.sqlWorkspace.RetryPersistence()
			}
		})
	})
}
