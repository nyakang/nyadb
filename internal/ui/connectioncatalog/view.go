package connectioncatalog

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

const (
	catalogSidebarWidth      float32 = 225
	catalogFilterHeight      float32 = 23
	catalogSearchHeight      float32 = 54
	catalogSearchInputHeight float32 = 26
)

// View renders the chooser body within the host's available dimensions.
// Selection is stored before onSelect runs; the host owns footer actions,
// validation messages and the transition to the connection editor.
func (s *State) View(c *ui.Context, width, height float32, onSelect func(Choice)) {
	palette := theme.Colors(c)
	// Account for the sidebar, content padding and gaps rather than the
	// outer window width: four fixed cards do not fit the smallest window.
	columns := max(1, min(5, int((width-catalogSidebarWidth-1-28+8)/(116+8))))
	ui.Row(c).Width(width).Height(height).Gap(0).Children(func() {
		s.catalogFilterSidebar(c, height)
		ui.Box(c).Width(1).Background(palette.Border)
		ui.Column(c).Grow(1).Height(height).Gap(0).Children(func() {
			s.catalogSearchToolbar(c)
			ui.Box(c).Height(1).Background(palette.Border)
			ui.Scroll(c).Height(height - catalogSearchHeight - 1).Children(func() {
				ui.Column(c).Padding(12, 14).Gap(15).Children(func() {
					if !s.gridMode {
						s.catalogTree(c, onSelect)
						return
					}
					if len(s.recent) != 0 {
						s.catalogSection(c, "最近使用过的", s.catalogRecents(), columns, onSelect)
					}
					s.catalogSection(c, "全部", s.catalogItems("全部"), columns, onSelect)
					ui.Box(c).Height(1).Background(palette.Border)
					s.catalogSection(c, "云数据库", s.catalogItems("云数据库"), columns, onSelect)
				})
			})
		})
	})
}

func (s *State) catalogFilterSidebar(c *ui.Context, height float32) {
	palette := theme.Colors(c)

	ui.Column(c).Width(catalogSidebarWidth).Height(height).Background(palette.Sidebar).Gap(1).Padding(12, 10).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, 29)).Gap(7).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.Text("连接筛选")).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
		})
		ui.Scroll(c).Height(height - 58).Children(func() {
			ui.Column(c).Gap(0).Children(func() {
				for _, name := range catalogEngines {
					if !s.catalogSidebarFilterVisible(name, false) {
						continue
					}
					label := name
					checked := s.engineFilters[label]
					ui.Row(c).Height(catalogFilterHeight).Gap(4).AlignItems(ui.Center).Children(func() {
						if ui.Checkbox(c, &checked, "").Label(locales.Text("筛选 ") + label).Changed() {
							setCatalogFilter(s.engineFilters, label, checked)
						}
						if item, ok := catalogFilterIcon(label, false); ok {
							s.catalogIcon(c, item, catalogTreeIconSize)
						} else {
							theme.Icon(c, "database", catalogTreeIconSize, palette.Muted)
						}
						ui.Text(c, label).FontSize(theme.Font(c, 12)).SingleLine()
					})
				}
				ui.Box(c).Height(1).Background(palette.Border).Margin(8, 0)
				ui.Row(c).Height(theme.RowHeight(c, 30)).Gap(7).AlignItems(ui.Center).Children(func() {
					ui.Text(c, locales.Text("供应商筛选")).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
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
					checked := s.vendorFilters[label]
					ui.Row(c).Height(catalogFilterHeight).Gap(4).AlignItems(ui.Center).Children(func() {
						if ui.Checkbox(c, &checked, "").Label(locales.Text("筛选供应商 ") + label).Changed() {
							setCatalogFilter(s.vendorFilters, label, checked)
						}
						s.catalogProviderIcon(c, label, catalogTreeIconSize)
						ui.Text(c, displayName).FontSize(theme.Font(c, 12)).SingleLine()
					})
				}
			})
		})
	})
}

func (s *State) catalogSearchToolbar(c *ui.Context) {
	palette := theme.Colors(c)

	ui.Row(c).Height(catalogSearchHeight).Padding(6, 13).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, locales.Text("选择一个连接类型：")).FontSize(theme.Font(c, 12))
		ui.Box(c).Grow(1)
		selected := 0 // Grid first, list second.
		if !s.gridMode {
			selected = 1
		}
		segments := ui.SegmentedBase(c, &selected, 2)
		segments.Track.Label(locales.Text("连接类型视图")).Height(theme.RowHeight(c, 26)).Padding(1).Gap(2).
			Radius(2).Background(palette.Panel).Border(1, palette.Border).Children(func() {
			for i, symbol := range []string{"catalog-grid", "catalog-list"} {
				label := "网格视图"
				if i == 1 {
					label = "列表视图"
				}
				segment := segments.Segment(i).Label(locales.Text(label)).Width(26).Height(theme.RowHeight(c, 24)).Radius(1)
				if selected == i {
					segment.Background(palette.Header)
				}
				segment.Children(func() {
					theme.Icon(c, symbol, 16, palette.Text)
				})
			}
		})
		if segments.Track.Changed() {
			s.gridMode = selected == 0
		}
		ui.SearchField(c, &s.search).Label(locales.Text("搜索连接类型")).Placeholder(locales.Text("搜索")).Width(236).Height(theme.RowHeight(c, catalogSearchInputHeight))
	})
}

func (s *State) catalogSection(c *ui.Context, title string, items []catalogEntry, columns int, onSelect func(Choice)) {
	if len(items) == 0 {
		return
	}
	ui.Column(c).Gap(10).Children(func() {
		ui.Text(c, locales.Text(title)).FontSize(theme.Font(c, 13)).Bold()
		if !s.gridMode {
			for _, e := range items {
				item := e
				s.catalogListItem(c, item, onSelect)
			}
			return
		}
		// Five columns match the original's normal-size grid at this viewport.
		for i := 0; i < len(items); i += columns {
			start := i
			ui.Row(c).Gap(8).Children(func() {
				for j := start; j < len(items) && j < start+columns; j++ {
					item := items[j]
					s.catalogIconItem(c, item, onSelect)
				}
			})
		}
	})
}

func (s *State) catalogIconItem(c *ui.Context, item catalogEntry, onSelect func(Choice)) {
	palette := theme.Colors(c)

	bg := palette.Panel
	border := palette.Panel
	if item.name == s.selected.Name {
		bg = palette.Accent.Alpha(0.25)
		border = palette.Accent
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
			ui.Text(c, item.name).Width(109).TextAlign(ui.Center).FontSize(theme.Font(c, 11))
		})
	})
	if control.Clicked() {
		s.chooseCatalogEntry(item, onSelect)
	}
}

func (s *State) catalogListItem(c *ui.Context, item catalogEntry, onSelect func(Choice)) {
	palette := theme.Colors(c)

	control := ui.ButtonBase(c).Label(locales.Text("选择 ") + item.name).FillWidth().Height(43).
		Radius(3).Background(palette.Panel)
	control.Children(func() {
		ui.Row(c).Padding(5, 10).AlignItems(ui.Center).Gap(10).Children(func() {
			s.catalogIcon(c, item, 30)
			ui.Text(c, item.name).FontSize(theme.Font(c, 12)).Grow(1)
			if item.kind == "" {
				ui.Text(c, locales.Text("尚未支持")).FontSize(theme.Font(c, 10)).TextColor(palette.Muted)
			}
			if item.kind != "" && item.kind == s.selected.Kind {
				ui.Text(c, "✓").TextColor(palette.Blue)
			}
		})
	})
	if control.Clicked() {
		s.chooseCatalogEntry(item, onSelect)
	}
}
