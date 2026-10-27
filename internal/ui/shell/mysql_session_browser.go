package shell

import (
	"context"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
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
	palette := theme.Colors(c)

	p, ok := s.currentProfile()
	connected := ok && s.mysqlSessionStateFor(p.ID) == "已连接"
	database := s.mysqlSelectedDatabase
	items := s.mysqlSelectedObjects()
	canOpen := connected && database != "" && s.objectRow >= 0 && s.objectRow < len(items)
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, 30)).Gap(3).Padding(1, 4).AlignItems(ui.Center).Background(palette.Header).Children(func() {
			ui.Toolbar(c, func() {
				// Keep the available read-only actions visible on narrow pages.
				// Unsupported DDL/import/export actions must not crowd them out.
				for _, action := range []struct{ label, icon, accessible string }{
					{"打开表", "open-table", "打开选中表数据"}, {"设计表", "design-table", "打开选中表结构"},
				} {
					item := action
					if ui.ButtonBase(c).Label(locales.Text(item.accessible)).Tooltip(locales.Text(item.label)).Disabled(!canOpen).Padding(2, 4).Height(theme.RowHeight(c, 25)).Children(func() {
						ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
							theme.Icon(c, item.icon, 14, palette.Muted)
							ui.Text(c, locales.Text(item.label)).FontSize(theme.Font(c, 12)).SingleLine()
						})
					}).Clicked() {
						obj := items[s.objectRow]
						if item.label == "打开表" {
							s.openMySQLData(c.Services(), database, obj.Name)
						} else {
							s.openMySQLStructure(c.Services(), database, obj.Name)
						}
					}
				}
				for _, kind := range []string{"全部", "表", "视图"} {
					filter := kind
					button := ui.ButtonBase(c).Label("筛选对象："+filter).Disabled(!connected || database == "").Height(theme.RowHeight(c, 25)).Padding(2, 5).Children(func() { ui.Text(c, locales.Text(filter)).FontSize(theme.Font(c, 11)) })
					if connected && s.mysqlObjectKind == filter {
						button.Background(palette.Border.Alpha(.35))
					}
					if button.Clicked() {
						s.filterMySQLObjects(filter)
					}
				}
			}).Label(locales.Text("对象操作")).Grow(1).MinWidth(0).Padding(0).Gap(2).Background(palette.Header)
			// One refresh affordance follows the current scope.
			label := "刷新数据库列表"
			if database != "" {
				label = "刷新元数据：" + database
			}
			if ui.ButtonBase(c).Label(label).Tooltip(locales.Text("刷新")).Disabled(!connected || s.mysqlSessionResults != nil || s.mysqlObjectPending != "").Size(25, 25).Children(func() { theme.Icon(c, "refresh", 14, palette.Blue) }).Clicked() {
				if database == "" {
					s.refreshMySQLDatabases(c.Services())
				} else {
					s.loadMySQLObjects(c.Services(), database, true)
				}
			}
		})
		if connected && s.mysqlSessionError != "" {
			ui.Text(c, s.mysqlSessionError).Padding(5, 8).FontSize(theme.Font(c, 11)).TextColor(palette.Error)
		}
		s.mysqlObjectWorkspace(c)
	})
}
