package navigation

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) View(c *ui.Context) {
	palette := theme.Colors(c)

	ui.Column(c).Fill().Background(palette.Sidebar).Children(func() {
		header := ui.Row(c).Height(theme.RowHeight(c, 22)).Padding(0, 17).AlignItems(ui.Center).Background(palette.Sidebar).Gap(6)
		header.Children(func() {
			theme.Icon(c, "connection", 15, palette.Muted)
			ui.Text(c, locales.Text("我的连接")).FontSize(theme.Font(c, assetFontSize))
			ui.Box(c).Grow(1)
			if header.Hovered() {
				header.Background(palette.Selection)
				header.TextColor(palette.SelectionText)
				settings := ui.ButtonBase(c).Label(locales.Text("连接面板设置")).Tooltip(locales.Text("连接面板设置")).Padding(3, 4).Radius(2)
				settings.Children(func() { theme.Icon(c, "settings", 15, palette.Muted) })
				settings.Menu(func(m *ui.Menu) { s.settingsMenu(m) })
			}
		})
		header.ContextMenu(func(m *ui.Menu) { s.settingsMenu(m) })
		// New groups have no tree row yet; existing groups edit in place.
		if s.GroupEdit.Active && s.GroupEdit.Original == "" {
			ui.Row(c).Padding(3, 6).Gap(4).AlignItems(ui.Center).Children(func() {
				entry := ui.TextInput(c, &s.GroupEdit.Draft).Label(locales.Text("连接组名称")).Placeholder(locales.Text("组名称")).Grow(1).AutoFocus()
				if entry.Submitted() {
					s.SaveGroup()
				}
				if ui.Button(c, locales.Text("保存")).Label(locales.Text("保存组")).Clicked() {
					s.SaveGroup()
				}
				if ui.Button(c, "×").Label(locales.Text("取消编辑组")).Clicked() {
					s.CancelGroupEdit()
				}
			})
		}
		if s.GroupEdit.Active && s.GroupEdit.Error != "" {
			ui.Text(c, s.GroupEdit.Error).Padding(2, 8).TextColor(palette.Error).FontSize(theme.Font(c, 11))
		}
		navigator := ui.Column(c).Grow(1).MinHeight(0).Gap(0)
		navigator.Children(func() {
			roots := s.Roots()
			if len(roots) == 0 {
				msg := "暂无连接，点击工具栏“连接”新建连接"
				if strings.TrimSpace(s.Filter.Search) != "" || s.Filter.StarredOnly || s.Filter.Engine != "" {
					msg = "没有匹配的连接或组"
				}
				ui.Text(c, locales.Text(msg)).Padding(8).TextColor(palette.Muted).FontSize(theme.Font(c, 11))
				return
			}
			selectedKey := s.Key
			if s.Row < 0 {
				s.Row = s.SelectionRow(roots)
			} else if s.Row < s.Tree.Rows() {
				selectedKey = s.Tree.Item(s.Row)
			}
			// MyGo selects a right-clicked row before building its popup only
			// with Selection. Keep this navigator's choice singular, using the
			// native cursor for both pointer and keyboard navigation.
			s.selection.Clear()
			if selectedKey != "" {
				s.selection.Add(selectedKey)
			}
			s.Tree.List.Selection = &s.selection
			s.Tree.List.Selected = &s.Row
			s.Tree.Indent = 14       // native disclosure steps: group -> connection -> database
			s.Tree.LeadingInset = 14 // reserve the same left tree gutter at every depth
			s.Tree.RowContextMenu = func(key string, menu *ui.Menu) {
				s.contextMenu(c.Services(), key, menu)
			}
			savedTheme := c.Theme()
			denseTheme := *savedTheme
			// The native triangle has its own 10-DIP hit target; no need to
			// inflate spacing for its legibility.
			denseTheme.Spacing = 1
			// Collapsed nodes carry muted, secondary disclosure marks. Keep
			// the branch click target, but avoid competing with the database
			// cylinder or the selection highlight.
			denseTheme.TextMuted = palette.Muted.Mix(palette.Sidebar, .42)
			ConfigureDisclosure(&s.Tree)
			c.SetTheme(&denseTheme)
			tree := theme.NavigationOutline(c, &s.Tree, roots, s.Children, func(key string) { s.renderRow(c, key) }).Grow(1).Padding(0).Gap(0)
			c.SetTheme(savedTheme)
			if s.menuBuilt {
				// Outline can finish applying an earlier pointer selection after
				// the row's menu callback. Keep the menu's identity authoritative
				// and resolve its row on the next pass instead of restoring that
				// earlier click as the menu target.
				s.Row = -1
				s.menuBuilt = false
			} else if s.Row >= 0 && s.Row < s.Tree.Rows() {
				key := s.Tree.Item(s.Row)
				if key != s.Key {
					s.selectNode(c.Services(), key)
				}
			}
			if tree.Submitted() {
				if s.deps.Submit != nil {
					s.deps.Submit(c.Services(), Parse(s.Key))
				}
			}
			for _, shortcut := range []Shortcut{{Key: ui.KeyF2}, {Key: ui.KeyDelete}, {Modifiers: ui.Ctrl, Key: ui.KeyN}, {Modifiers: ui.Ctrl, Key: ui.KeyD}, {Key: ui.KeyF5}} {
				if tree.Shortcut(shortcut.Modifiers, shortcut.Key) && s.deps.Shortcut != nil {
					s.deps.Shortcut(c.Services(), Parse(s.Key), shortcut)
				}
			}
			if s.deps.SyncExpansion != nil {
				s.deps.SyncExpansion(c.Services())
			}
		})
		s.footer(c)
	})
}

// selectProfileByName ensures context commands target the right-clicked row,
// even when it is not the previously selected connection.

func (s *State) renderRow(c *ui.Context, key string) {
	palette := theme.Colors(c)

	// Outline supplies the indentation and disclosure arrow. Fill the remaining
	// row so its context menu also receives clicks after a short name.
	if IsGroup(key) {
		name := GroupName(key)
		if s.GroupEdit.Active && s.GroupEdit.Original == name {
			ui.Row(c).Grow(1).MinWidth(0).Height(theme.RowHeight(c, theme.TreeRowHeight+3)).
				Padding(0, 13).AlignItems(ui.Center).Gap(2).Children(func() {
				theme.Icon(c, "folder-filled", 16, palette.Gold)
				entry := ui.TextInput(c, &s.GroupEdit.Draft).Label(locales.Text("连接组名称")).
					Height(theme.RowHeight(c, 20)).Grow(1).MinWidth(35).AutoFocus()
				if entry.Submitted() {
					s.SaveGroup()
				}
				if ui.ButtonBase(c).Label(locales.Text("保存组")).Tooltip(locales.Text("保存组")).
					Size(20, 20).Padding(2).Children(func() {
					theme.Icon(c, "check", 14, palette.Success)
				}).Clicked() {
					s.SaveGroup()
				}
				if ui.ButtonBase(c).Label(locales.Text("取消编辑组")).Tooltip(locales.Text("取消编辑组")).
					Size(20, 20).Padding(2).Children(func() {
					theme.Icon(c, "close", 14, palette.Muted)
				}).Clicked() {
					s.CancelGroupEdit()
				}
			})
			return
		}
		ui.Row(c).Grow(1).MinWidth(0).Height(theme.RowHeight(c, assetRowHeight)).Padding(0, 8).AlignItems(ui.Center).Gap(4).Children(func() {
			theme.Icon(c, "folder-filled", 16, palette.Gold)
			ui.Text(c, name).FontSize(theme.Font(c, assetFontSize)).TextColor(palette.Text).SingleLine()
		}).ContextMenu(func(m *ui.Menu) {
			s.contextMenu(c.Services(), key, m)
		})
		return
	}
	n := Parse(key)
	label, icon := n.Object, "table"
	if n.Category == "查询" && n.Kind == "object" && s.deps.Queries != nil {
		icon = "query"
		for _, q := range s.deps.Queries(n.ProfileID, n.Database) {
			if q.Kind == n.Object {
				label = q.Name
				break
			}
		}
	}
	switch n.Kind {
	case "connection":
		for _, p := range s.profiles {
			if p.ID != n.ProfileID {
				continue
			}
			row := ui.Row(c).Grow(1).MinWidth(0).Height(theme.RowHeight(c, assetRowHeight)).Padding(0, 6).AlignItems(ui.Center).Gap(4).Children(func() {
				status := s.sessionStatus(p.ID)
				NavigationIcon(c, p.Kind, status, 16)
				ui.Text(c, p.Name).FontSize(theme.Font(c, assetFontSize)).TextColor(palette.Text).SingleLine()
			})
			row.Label(p.Name + " · " + locales.Text(s.sessionStatus(p.ID)))
			row.ContextMenu(func(m *ui.Menu) {
				s.contextMenu(c.Services(), key, m)
			})

		}
		return
	case "catalog":
		label, icon = n.Catalog, "database-solid"
	case "database":
		label, icon = n.Database, "database-solid"
	case "category":
		label = locales.Text(n.Category)
		switch n.Category {
		case "函数":
			icon = "function"
		case "存储过程":
			icon = "function"
		case "事件":
			icon = "refresh"
		case "查询":
			icon = "query"
		case "备份":
			icon = "backup"
		}
	case "status":
		icon, label = "refresh", locales.Text("等待加载")
		if n.Object == "loading" {
			label = locales.Text("正在读取对象…")
		} else if n.Object == "error" {
			label = locales.Text("读取失败，点击重试")
		}
	}
	if n.Category == "视图" {
		icon = "view"
	}
	inset := float32(5)
	row := ui.Row(c).Grow(1).MinWidth(0).Height(theme.RowHeight(c, assetRowHeight)).Padding(0, inset).AlignItems(ui.Center).Gap(4).Children(func() {
		ink := palette.Text
		iconInk := palette.Text.Mix(palette.Blue, .22)
		if n.Kind == "category" && SupportedCategory(n.Category) || n.Kind == "object" {
			// Database object categories use a soft functional blue, rather
			// than the gray used for the unimplemented query/backup nodes.
			iconInk = palette.Blue.Mix(palette.Muted, .17)
		}
		if n.Kind == "database" {
			// Neutral cylinders and a colored current-database indicator.
			iconInk = palette.Muted.Mix(palette.Text, .11)
			if s.Key == key {
				iconInk = palette.Success.Mix(palette.Sidebar, .23)
			}
		}
		if n.Kind == "status" || n.Kind == "category" && !SupportedCategory(n.Category) {
			ink = palette.Muted
			iconInk = palette.Muted
		}
		if n.Kind == "status" && n.Object == "error" {
			ink = palette.Error
			iconInk = palette.Error
		}
		theme.Icon(c, icon, 15, iconInk)
		ui.Text(c, label).FontSize(theme.Font(c, assetFontSize)).TextColor(ink).SingleLine()
	})
	if n.Kind == "category" && !SupportedCategory(n.Category) {
		row.Label(label + " · " + locales.Text("尚未支持元数据浏览"))
	}
	if n.Kind == "status" {
		if n.Object == "error" {
			_, err := s.loadState(n.ProfileID, n.Database)
			row.Label(label + "：" + err)
		}
		if row.Clicked() && n.Object == "error" {
			s.selectNode(c.Services(), DatabaseNode(n.ProfileID, n.Database).Key())
			s.openDatabase(c.Services(), n, true)
		}
		return
	}
	if n.Kind == "database" {
		row.Label("数据库：" + n.Database)
	}
	row.ContextMenu(func(m *ui.Menu) {
		s.contextMenu(c.Services(), key, m)
	})
	// The content row owns pointer clicks; handle its double click directly.
	// Outline's Submitted still handles Enter and clicks in its native gutter.
	if n.Kind == "database" && row.DoubleClicked() {
		s.selectNode(c.Services(), key)
		s.openDatabase(c.Services(), n, false)
	} else if row.Clicked() {
		s.selectNode(c.Services(), key)
	}

}

func (s *State) footer(c *ui.Context) {
	palette := theme.Colors(c)

	ui.Column(c).BorderWidth(1, 0, 0, 0).BorderColor(palette.Border).
		Padding(0, 4).Gap(0).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, 24)).Gap(2).AlignItems(ui.Center).Children(func() {
			if ui.SearchField(c, &s.Filter.Search).Label(locales.Text("搜索连接")).Placeholder(locales.Text("搜索")).
				Height(theme.RowHeight(c, 22)).Grow(1).MinWidth(0).BorderWidth(0).Radius(0).
				Background(palette.Sidebar).Changed() {
				s.ChangeFilter()
			}
			if s.CanRestoreFilter() {
				restore := ui.ButtonBase(c).Label(locales.Text("恢复之前的筛选")).
					Tooltip(locales.Text("恢复之前的筛选")).Size(24, theme.RowHeight(c, 24)).Padding(3).Radius(1)
				restore.Children(func() { theme.Icon(c, "refresh", 15, palette.Gold) })
				if restore.Clicked() {
					s.RestoreFilter()
				}
			}
			filter := ui.ButtonBase(c).Label(locales.Text("筛选连接")).Tooltip(locales.Text("筛选连接类型")).
				Width(24).Height(theme.RowHeight(c, 24)).Padding(3).Radius(1)
			filter.Children(func() { theme.Icon(c, "catalog-filter", 15, palette.Muted) })
			if s.Filter.Engine != "" {
				filter.Background(palette.Accent.Alpha(.28))
			}
			filter.Menu(func(m *ui.Menu) { s.filterMenu(m) })
			starred := ui.ButtonBase(c).Label(locales.Text("显示已星标")).Tooltip(locales.Text("只显示已星标的连接")).
				Width(24).Height(theme.RowHeight(c, 24)).Padding(3).Radius(1)
			if s.Filter.StarredOnly {
				starred.Background(palette.Accent.Alpha(.28))
			}
			starred.Children(func() {
				color := palette.Muted
				if s.Filter.StarredOnly {
					color = palette.Gold
				}
				theme.Icon(c, "star", 15, color)
			})
			if starred.Clicked() {
				s.Filter.StarredOnly = !s.Filter.StarredOnly
				s.ChangeFilter()
			}
			active := ui.ButtonBase(c).Label(locales.Text("导航至活动标签的对象")).
				Tooltip("在连接树中定位当前活动数据库对象").
				Width(24).Height(theme.RowHeight(c, 24)).Padding(3).Radius(1)
			canNavigate := s.deps.CanNavigate != nil && s.deps.CanNavigate()
			active.Disabled(!canNavigate)
			active.Children(func() { theme.Icon(c, "navigate-active", 15, palette.Muted) })
			if active.Clicked() {
				if s.deps.NavigateActive != nil {
					s.deps.NavigateActive(c.Services())
				}
			}
			collapse := ui.ButtonBase(c).Label(locales.Text("全部折叠")).
				Tooltip("折叠所有连接组与数据库树").
				Width(24).Height(theme.RowHeight(c, 24)).Padding(3).Radius(1)
			collapse.Children(func() { theme.Icon(c, "collapse-all", 15, palette.Muted) })
			if collapse.Clicked() {
				s.Collapse()
			}
		})
	})
}
