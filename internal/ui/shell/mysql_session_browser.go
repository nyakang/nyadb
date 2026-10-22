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

// The Objects tab uses the same compact native command bar whether a session
// is open or a virtual group is selected. Empty tables keep their column header.
func (s *State) mysqlDatabaseWorkspace(c *ui.Context) {
	p, ok := s.currentProfile()
	connected := ok && s.mysqlSessionStateFor(p.ID) == "已连接"
	database := s.mysqlSelectedDatabase
	items := s.mysqlSelectedObjects()
	canOpen := connected && database != "" && s.objectRow >= 0 && s.objectRow < len(items)
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Height(30).Gap(5).Padding(2, 5).AlignItems(ui.Center).
			Background(theme.Header).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
			for _, name := range []string{"打开表", "设计表"} {
				action := name
				label := "打开选中表数据"
				if action == "设计表" {
					label = "打开选中表结构"
				}
				iconName := "open-table"
				if action == "设计表" {
					iconName = "design-table"
				}
				if ui.Button(c, "").Label(label).Disabled(!canOpen).Padding(2, 5).
					Children(func() {
						ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
							theme.Icon(c, iconName, 13, theme.Blue)
							ui.Text(c, action).FontSize(11)
						})
					}).Clicked() {
					obj := items[s.objectRow]
					if action == "打开表" {
						s.openMySQLData(c.Services(), database, obj.Name)
					} else {
						s.openMySQLStructure(c.Services(), database, obj.Name)
					}
				}
			}
			for _, action := range []struct{ label, icon string }{
				{"新建表", "new-table"}, {"删除表", "delete-table"},
				{"导入向导", "import"}, {"导出向导", "export"},
			} {
				item := action
				ui.Button(c, "").Label(item.label).Disabled(true).Padding(2, 5).Children(func() {
					ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
						theme.Icon(c, item.icon, 13, theme.Muted)
						ui.Text(c, item.label).FontSize(11)
					})
				})
			}
			for _, kind := range []string{"全部", "表", "视图"} {
				filter := kind
				if ui.Button(c, filter).Label("筛选对象："+filter).Disabled(!connected).Padding(2, 4).Clicked() {
					s.mysqlObjectKind = filter
					s.objectRow = -1
				}
			}
			ui.Box(c).Grow(1)
			if ui.Button(c, "").Label("刷新数据库列表").Tooltip("刷新数据库列表").Disabled(!connected || s.mysqlSessionResults != nil).Padding(2, 5).
				Children(func() { theme.Icon(c, "refresh", 14, theme.Blue) }).Clicked() {
				s.refreshMySQLDatabases(c.Services())
			}
			if ui.Button(c, "").Label("刷新元数据："+database).Tooltip("刷新当前数据库").Disabled(!connected || database == "" || s.mysqlObjectPending != "").Padding(2, 5).
				Children(func() { theme.Icon(c, "refresh", 14, theme.Muted) }).Clicked() {
				s.loadMySQLObjects(c.Services(), database, true)
			}
		})
		if connected && len(s.mysqlSessionDatabases) > 0 {
			ui.Row(c).Height(25).Background(theme.Panel).Padding(1, 6).Gap(4).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "数据库").FontSize(11).TextColor(theme.Muted)
				ui.ScrollHorizontal(c).Grow(1).Children(func() {
					ui.Row(c).Gap(3).Children(func() {
						for _, name := range s.mysqlSessionDatabases {
							db := name
							if ui.Button(c, db).Label("切换数据库："+db).Padding(1, 5).Clicked() {
								s.selectMySQLDatabase(c.Services(), db)
							}
						}
					})
				})
			})
		}
		if !connected {
			// The native table's virtual list requires an owner with rows. Draw only
			// its header in the empty selection state; live lists use ui.Table.
			ui.Row(c).Height(24).Gap(0).Background(theme.Panel).Children(func() {
				for _, column := range mysqlObjectColumns {
					ui.Box(c).Width(column.Width).Padding(3, 6).BorderWidth(0, 1, 0, 0).BorderColor(theme.Border).Children(func() {
						ui.Text(c, column.Title).FontSize(11).TextColor(theme.Text).SingleLine()
					})
				}
			})
			ui.Box(c).Grow(1)
			return
		}
		if s.mysqlSessionResults != nil {
			ui.Text(c, "正在读取数据库列表…").Padding(8).TextColor(theme.Muted)
		}
		if s.mysqlSessionError != "" {
			ui.Text(c, s.mysqlSessionError).Padding(8).TextColor(theme.Gold)
		}
		s.mysqlObjectWorkspace(c)
	})
}
