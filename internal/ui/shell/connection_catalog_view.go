package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// The chooser follows the observed large two-pane information architecture.
// It uses independent NyaDB symbols by default. In explicit local research
// mode only, runtime-loaded PNGs may replace catalog symbols for comparison.
func (s *State) connectionTypes(c *ui.Context, nativeWindow bool) {
	w, h := c.Size()
	dialogWidth := min(float32(860), w-72)
	dialogHeight := min(float32(648), h-76)
	bodyHeight := dialogHeight - 78
	if nativeWindow {
		dialogWidth = w
		dialogHeight = h
		bodyHeight = h - 40
	}
	columns := 5
	if dialogWidth < 820 {
		columns = 4
	}
	if dialogWidth < 690 {
		columns = 3
	}
	ui.Column(c).Width(dialogWidth).Height(dialogHeight).Gap(0).Children(func() {
		if !nativeWindow {
			ui.Row(c).Height(38).Padding(0, 3).AlignItems(ui.Center).Children(func() {
				theme.Icon(c, "connection", 20, theme.Blue)
				ui.Text(c, "新建连接").FontSize(17).Bold()
			})
			ui.Box(c).Height(1).Background(theme.Border)
		}
		ui.Row(c).Height(bodyHeight).Gap(0).Children(func() {
			s.catalogFilterSidebar(c, bodyHeight)
			ui.Box(c).Width(1).Background(theme.Border)
			ui.Column(c).Grow(1).Height(bodyHeight).Gap(0).Children(func() {
				s.catalogSearchToolbar(c)
				ui.Box(c).Height(1).Background(theme.Border)
				ui.Scroll(c).Height(bodyHeight - 46).Children(func() {
					ui.Column(c).Padding(12, 14).Gap(15).Children(func() {
						if len(s.connectionRecent) != 0 {
							s.catalogSection(c, "最近使用过的", s.catalogRecents(), columns)
						} else {
							common := make([]catalogEntry, 0, 3)
							for _, item := range databaseCatalog {
								if item.section == "全部" && item.kind != "" &&
									catalogMatch(item, s.connectionSearch, s.connectionEngineFilters, s.connectionVendorFilters) {
									common = append(common, item)
								}
							}
							s.catalogSection(c, "常用类型", common, columns)
						}
						s.catalogSection(c, "全部", s.catalogItems("全部"), columns)
						ui.Box(c).Height(1).Background(theme.Border)
						s.catalogSection(c, "云数据库", s.catalogItems("云数据库"), columns)
					})
				})
			})
		})
		ui.Box(c).Height(1).Background(theme.Border)
		ui.Row(c).Height(38).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "通过 URI 新建连接…").Clicked() {
				s.connectionError = "URI 导入尚未支持"
			}
			ui.Box(c).Grow(1)
			if s.connectionError != "" {
				ui.Text(c, s.connectionError).FontSize(11).TextColor(theme.Gold).SingleLine()
			}
			if ui.PrimaryButton(c, "下一步").Clicked() {
				s.nextConnectionStep()
			}
			if ui.Button(c, "取消连接向导").Clicked() {
				s.closeConnectionDialog()
			}
		})
	})
}

func (s *State) catalogFilterSidebar(c *ui.Context, height float32) {
	ui.Column(c).Width(214).Height(height).Background(theme.Sidebar).Gap(8).Padding(11, 9).Children(func() {
		ui.Row(c).Height(25).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "连接筛选").FontSize(12).Bold()
			ui.Box(c).Grow(1)
			if ui.Button(c, "重置").Label("重置连接筛选").Clicked() {
				s.clearCatalogFilters()
			}
		})
		ui.Scroll(c).Height(height - 55).Children(func() {
			ui.Column(c).Gap(5).Children(func() {
				for _, name := range catalogEngines {
					label := name
					checked := s.connectionEngineFilters[label]
					if ui.Checkbox(c, &checked, label).Changed() {
						setCatalogFilter(s.connectionEngineFilters, label, checked)
					}
				}
				ui.Box(c).Height(1).Background(theme.Border).Margin(8, 0)
				ui.Text(c, "供应商筛选").FontSize(12).Bold()
				for _, name := range catalogVendors {
					label := name
					checked := s.connectionVendorFilters[label]
					if ui.Checkbox(c, &checked, label).Changed() {
						setCatalogFilter(s.connectionVendorFilters, label, checked)
					}
				}
			})
		})
	})
}

func (s *State) catalogSearchToolbar(c *ui.Context) {
	ui.Row(c).Height(45).Padding(6, 13).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "选择一个连接类型：").FontSize(12)
		ui.Box(c).Grow(1)
		if ui.Button(c, "网格视图").Clicked() {
			s.connectionGridMode = true
		}
		if ui.Button(c, "列表视图").Clicked() {
			s.connectionGridMode = false
		}
		ui.SearchField(c, &s.connectionSearch).Label("搜索连接类型").Placeholder("搜索").Width(176)
	})
}

func (s *State) catalogSection(c *ui.Context, title string, items []catalogEntry, columns int) {
	if len(items) == 0 {
		return
	}
	ui.Column(c).Gap(10).Children(func() {
		ui.Text(c, title).FontSize(13).Bold()
		if !s.connectionGridMode {
			for _, e := range items {
				item := e
				s.catalogListItem(c, item)
			}
			return
		}
		// Five columns match the original's normal-size grid at this viewport.
		for i := 0; i < len(items); i += columns {
			start := i
			ui.Row(c).Gap(8).Children(func() {
				for j := start; j < len(items) && j < start+columns; j++ {
					item := items[j]
					s.catalogIconItem(c, item)
				}
			})
		}
	})
}

func (s *State) catalogIconItem(c *ui.Context, item catalogEntry) {
	bg := theme.Panel
	border := theme.Panel
	if s.connectionKind != "" && item.kind != "" && item.kind == s.connectionKind && item.section == "全部" {
		bg = ui.Hex("#214564")
		border = theme.Accent
	}
	control := ui.ButtonBase(c).Label("选择 "+item.name).Width(110).Height(105).
		Radius(3).Background(bg).Border(1, border)
	control.Children(func() {
		ui.Column(c).AlignItems(ui.Center).Justify(ui.Center).Gap(7).Children(func() {
			s.catalogIcon(c, item, 45)
			ui.Text(c, item.name).Width(104).TextAlign(ui.Center).FontSize(11)
			if item.kind == "" {
				ui.Text(c, "尚未支持").FontSize(10).TextColor(theme.Muted)
			}
		})
	})
	if control.Clicked() {
		s.chooseCatalogEntry(item)
	}
}

func (s *State) catalogListItem(c *ui.Context, item catalogEntry) {
	control := ui.ButtonBase(c).Label("选择 " + item.name).FillWidth().Height(43).
		Radius(3).Background(theme.Panel)
	control.Children(func() {
		ui.Row(c).Padding(5, 10).AlignItems(ui.Center).Gap(10).Children(func() {
			s.catalogIcon(c, item, 30)
			ui.Text(c, item.name).FontSize(12).Grow(1)
			if item.kind == "" {
				ui.Text(c, "尚未支持").FontSize(10).TextColor(theme.Muted)
			}
			if item.kind != "" && item.kind == s.connectionKind {
				ui.Text(c, "✓").TextColor(theme.Blue)
			}
		})
	})
	if control.Clicked() {
		s.chooseCatalogEntry(item)
	}
}
