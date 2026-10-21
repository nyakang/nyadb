package shell

import (
	"context"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func (s *State) refreshMySQLDatabases(services ui.Services) {
	id := s.mysqlSessionProfileID
	if id == 0 || s.mysqlSessionStatus != "已连接" || s.mysqlSessionManager == nil || s.mysqlSessionResults != nil {
		return
	}
	ctx := context.Background() // Manager also binds the operation to the session lifetime.
	s.cancelMySQLObjectQuery()
	ch := make(chan mysqlSessionResult, 1)
	generation := s.mysqlSessionGeneration
	s.mysqlSessionResults = ch
	manager := s.mysqlSessionManager
	go func() {
		names, err := manager.Databases(ctx, id)
		ch <- mysqlSessionResult{generation: generation, id: id, names: names, metadataErr: err}
		services.Invalidate()
	}()
}

func (s *State) mysqlDatabaseWorkspace(c *ui.Context) {
	p, ok := s.currentProfile()
	connected := ok && s.mysqlSessionStateFor(p.ID) == "已连接"
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Height(34).Gap(8).Padding(4, 8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "数据库").Bold().FontSize(13)
			if ui.Button(c, "刷新数据库列表").Disabled(!connected || s.mysqlSessionResults != nil).Clicked() {
				s.refreshMySQLDatabases(c.Services())
			}
		})
		if !connected {
			ui.Column(c).Grow(1).Center().Children(func() {
				theme.Icon(c, "database", 36, theme.Muted)
				ui.Text(c, "请选择 MySQL 连接并点击「连接」").FontSize(13).TextColor(theme.Muted)
				ui.Text(c, "这里不展示模拟数据库对象").FontSize(11).TextColor(theme.Muted)
			})
			return
		}
		if s.mysqlSessionResults != nil {
			ui.Text(c, "正在读取数据库列表…").Padding(12)
		}
		if s.mysqlSessionError != "" {
			ui.Text(c, s.mysqlSessionError).Padding(10).TextColor(theme.Gold)
		}
		s.mysqlObjectWorkspace(c)
	})
}
