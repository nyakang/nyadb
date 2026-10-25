package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

const (
	catalogSidebarWidth float32 = 225
	catalogFilterHeight float32 = 23
	catalogSearchHeight float32 = 54
	catalogFooterHeight float32 = 42
)

// The chooser follows the observed large two-pane information architecture.
// It uses independent NyaDB symbols by default. In explicit local research
// mode only, runtime-loaded PNGs may replace catalog symbols for comparison.
func (s *State) connectionTypes(c *ui.Context, nativeWindow bool) {
	w, h := c.Size()
	dialogWidth := min(float32(860), w-72)
	dialogHeight := min(float32(648), h-76)
	bodyHeight := dialogHeight - catalogFooterHeight - 40
	if nativeWindow {
		dialogWidth = w
		dialogHeight = h
		bodyHeight = h - catalogFooterHeight - 1
	}
	// Account for the sidebar, content padding and gaps rather than the
	// outer window width: four fixed cards do not fit the smallest window.
	columns := max(1, min(5, int((dialogWidth-catalogSidebarWidth-1-28+8)/(116+8))))
	ui.Column(c).Width(dialogWidth).Height(dialogHeight).Gap(0).Children(func() {
		if !nativeWindow {
			ui.Row(c).Height(38).Padding(0, 3).AlignItems(ui.Center).Children(func() {
				theme.Icon(c, "connection", 20, theme.Blue)
				ui.Text(c, locales.Text("新建连接")).FontSize(17).Bold()
			})
			ui.Box(c).Height(1).Background(theme.Border)
		}
		ui.Row(c).Height(bodyHeight).Gap(0).Children(func() {
			s.catalogFilterSidebar(c, bodyHeight)
			ui.Box(c).Width(1).Background(theme.Border)
			ui.Column(c).Grow(1).Height(bodyHeight).Gap(0).Children(func() {
				s.catalogSearchToolbar(c)
				ui.Box(c).Height(1).Background(theme.Border)
				ui.Scroll(c).Height(bodyHeight - catalogSearchHeight - 1).Children(func() {
					ui.Column(c).Padding(12, 14).Gap(15).Children(func() {
						if !s.connectionGridMode {
							s.catalogTree(c)
							return
						}
						if len(s.connectionRecent) != 0 {
							s.catalogSection(c, "最近使用过的", s.catalogRecents(), columns)
						}
						s.catalogSection(c, "全部", s.catalogItems("全部"), columns)
						ui.Box(c).Height(1).Background(theme.Border)
						s.catalogSection(c, "云数据库", s.catalogItems("云数据库"), columns)
					})
				})
			})
		})
		ui.Box(c).Height(1).Background(theme.Border)
		ui.Row(c).Height(catalogFooterHeight).Padding(5, 9).Gap(7).AlignItems(ui.Center).Background(theme.Toolbar).Children(func() {
			ui.Button(c, locales.Text("通过 URI 新建连接…")).Height(connectionFooterControlHeight).
				Label(locales.Text("URI 导入尚未支持")).Disabled(true)
			ui.Box(c).Grow(1)
			if s.connectionError != "" {
				ui.Text(c, s.connectionError).FontSize(11).TextColor(theme.Gold).SingleLine().MinWidth(0).Grow(1)
			}
			if ui.Button(c, locales.Text("取消")).Label(locales.Text("取消连接向导")).Width(80).Height(connectionFooterControlHeight).Clicked() {
				s.closeConnectionDialog()
			}
			if ui.Button(c, locales.Text("下一步")).Width(80).Height(connectionFooterControlHeight).Disabled(s.connectionKind == "").Clicked() {
				s.nextConnectionStep()
			}
		})
	})
}

func (s *State) catalogFilterSidebar(c *ui.Context, height float32) {
	ui.Column(c).Width(catalogSidebarWidth).Height(height).Background(theme.Sidebar).Gap(1).Padding(12, 10).Children(func() {
		ui.Row(c).Height(29).Gap(7).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.Text("连接筛选")).FontSize(12).TextColor(theme.Muted)
		})
		ui.Scroll(c).Height(height - 58).Children(func() {
			ui.Column(c).Gap(0).Children(func() {
				for _, name := range catalogEngines {
					if !s.catalogSidebarFilterVisible(name, false) {
						continue
					}
					label := name
					checked := s.connectionEngineFilters[label]
					ui.Row(c).Height(catalogFilterHeight).Gap(4).AlignItems(ui.Center).Children(func() {
						if ui.Checkbox(c, &checked, "").Label(locales.Text("筛选 ") + label).Changed() {
							setCatalogFilter(s.connectionEngineFilters, label, checked)
						}
						if item, ok := catalogFilterIcon(label, false); ok {
							s.catalogIcon(c, item, catalogTreeIconSize)
						} else {
							theme.Icon(c, "database", catalogTreeIconSize, theme.Muted)
						}
						ui.Text(c, label).FontSize(12).SingleLine()
					})
				}
				ui.Box(c).Height(1).Background(theme.Border).Margin(8, 0)
				ui.Row(c).Height(30).Gap(7).AlignItems(ui.Center).Children(func() {
					ui.Text(c, locales.Text("供应商筛选")).FontSize(12).TextColor(theme.Muted)
				})
				for _, name := range catalogVendors {
					if !s.catalogSidebarFilterVisible(name, true) {
						continue
					}
					label := name
					displayName := catalogTreeVendorLabels[name]
					if displayName == "" {
						displayName = name
					}
					checked := s.connectionVendorFilters[label]
					ui.Row(c).Height(catalogFilterHeight).Gap(4).AlignItems(ui.Center).Children(func() {
						if ui.Checkbox(c, &checked, "").Label(locales.Text("筛选供应商 ") + label).Changed() {
							setCatalogFilter(s.connectionVendorFilters, label, checked)
						}
						s.catalogProviderIcon(c, label, catalogTreeIconSize)
						ui.Text(c, displayName).FontSize(12).SingleLine()
					})
				}
			})
		})
	})
}

func (s *State) catalogSearchToolbar(c *ui.Context) {
	ui.Row(c).Height(catalogSearchHeight).Padding(6, 13).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, locales.Text("选择一个连接类型：")).FontSize(12)
		ui.Box(c).Grow(1)
		selected := 0 // Grid first, list second.
		if !s.connectionGridMode {
			selected = 1
		}
		segments := ui.SegmentedBase(c, &selected, 2)
		segments.Track.Label(locales.Text("连接类型视图")).Height(26).Padding(1).Gap(2).
			Radius(2).Background(theme.Panel).Border(1, theme.Border).Children(func() {
			for i, symbol := range []string{"catalog-grid", "catalog-list"} {
				label := "网格视图"
				if i == 1 {
					label = "列表视图"
				}
				segment := segments.Segment(i).Label(locales.Text(label)).Width(26).Height(24).Radius(1)
				if selected == i {
					segment.Background(theme.Header)
				}
				segment.Children(func() {
					theme.Icon(c, symbol, 16, theme.Text)
				})
			}
		})
		if segments.Track.Changed() {
			s.connectionGridMode = selected == 0
		}
		ui.SearchField(c, &s.connectionSearch).Label(locales.Text("搜索连接类型")).Placeholder(locales.Text("搜索")).Width(236).Height(connectionFormInputHeight)
	})
}

func (s *State) catalogSection(c *ui.Context, title string, items []catalogEntry, columns int) {
	if len(items) == 0 {
		return
	}
	ui.Column(c).Gap(10).Children(func() {
		ui.Text(c, locales.Text(title)).FontSize(13).Bold()
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
	if item.name == s.connectionSelectedName {
		bg = theme.Accent.Alpha(0.25)
		border = theme.Accent
	}
	label := locales.Text("选择 ") + item.name
	if item.kind == "" {
		label += locales.Text("（尚未支持）")
	}
	control := ui.ButtonBase(c).Label(label).Width(116).Height(94).
		Radius(3).Background(bg).Border(1, border)
	control.Children(func() {
		ui.Column(c).AlignItems(ui.Center).Justify(ui.Center).Gap(4).Children(func() {
			s.catalogIcon(c, item, 45)
			ui.Text(c, item.name).Width(109).TextAlign(ui.Center).FontSize(11)
		})
	})
	if control.Clicked() {
		s.chooseCatalogEntry(item)
	}
}

func (s *State) catalogListItem(c *ui.Context, item catalogEntry) {
	control := ui.ButtonBase(c).Label(locales.Text("选择 ") + item.name).FillWidth().Height(43).
		Radius(3).Background(theme.Panel)
	control.Children(func() {
		ui.Row(c).Padding(5, 10).AlignItems(ui.Center).Gap(10).Children(func() {
			s.catalogIcon(c, item, 30)
			ui.Text(c, item.name).FontSize(12).Grow(1)
			if item.kind == "" {
				ui.Text(c, locales.Text("尚未支持")).FontSize(10).TextColor(theme.Muted)
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
