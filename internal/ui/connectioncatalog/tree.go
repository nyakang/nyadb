package connectioncatalog

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// The original chooser list is a tree of *database types and vendors*, not
// a one-column grid. Observed live in Navicat 18 (O), 2026-10-21.
const (
	catalogTreeRowHeight float32 = 19
	catalogTreeIconSize  float32 = 16
)

var catalogTreeDirectEngines = []string{
	"MySQL", "PostgreSQL", "SQL Server", "Oracle", "SQLite",
	"MariaDB", "MongoDB", "Redis", "Snowflake",
}

// This ordering was observed in the native chooser. The displayed vendor
// labels intentionally differ from certain internal catalog identifiers.
var catalogTreeVendorOrder = []string{
	"阿里云", "华为", "腾讯云", "达梦", "电科金仓", "亚信星辰",
	"瀚高", "OceanBase", "AWS", "Fujitsu", "Google Cloud",
	"Microsoft", "MongoDB", "Oracle", "Redis",
}

var catalogTreeVendorLabels = map[string]string{
	"亚信星辰":    "平凯星辰",
	"MongoDB": "MongoDB Atlas",
	"Redis":   "Redis Enterprise Cloud",
}

type catalogTreeGroup struct {
	vendor string
	label  string
	items  []catalogEntry
}

// Navicat narrows the left filter sidebar as well as the right tree when
// searching. Only filters for matching items remain visible.
func (s *State) catalogSidebarFilterVisible(label string, vendor bool) bool {
	if strings.TrimSpace(s.search) == "" {
		return true
	}
	for _, item := range databaseCatalog {
		if !catalogMatch(item, s.search, nil, nil) {
			continue
		}
		if vendor && (item.vendor == label || (label == "达梦" && item.section == "全部" && item.engine == "达梦")) {
			return true
		}
		if !vendor && item.section == "全部" && item.engine == label {
			return true
		}
	}
	return false
}

func (s *State) catalogTreeDirect() []catalogEntry {
	items := make([]catalogEntry, 0, len(catalogTreeDirectEngines))
	for _, name := range catalogTreeDirectEngines {
		for _, item := range databaseCatalog {
			if item.name == name && item.section == "全部" &&
				catalogMatch(item, s.search, s.engineFilters, s.vendorFilters) {
				items = append(items, item)
				break
			}
		}
	}
	return items
}

// catalogTreeGroups keeps vendor ordering stable, filters children and
// automatically exposes matching search results (without changing the
// expansion state the user chose before typing a search).
func (s *State) catalogTreeGroups() []catalogTreeGroup {
	byVendor := make(map[string][]catalogEntry)
	for _, item := range databaseCatalog {
		if item.section == "全部" && item.engine == "达梦" {
			if catalogMatch(item, s.search, s.engineFilters, s.vendorFilters) {
				byVendor["达梦"] = append(byVendor["达梦"], item)
			}
			continue
		}
		if item.section != "云数据库" || !catalogMatch(item,
			s.search, s.engineFilters, s.vendorFilters) {
			continue
		}
		byVendor[item.vendor] = append(byVendor[item.vendor], item)
	}
	groups := make([]catalogTreeGroup, 0, len(byVendor))
	seen := map[string]bool{}
	add := func(vendor string) {
		if seen[vendor] || len(byVendor[vendor]) == 0 {
			return
		}
		seen[vendor] = true
		label := catalogTreeVendorLabels[vendor]
		if label == "" {
			label = vendor
		}
		groups = append(groups, catalogTreeGroup{
			vendor: vendor, label: label, items: byVendor[vendor],
		})
	}
	for _, vendor := range catalogTreeVendorOrder {
		add(vendor)
	}
	// Unknown future vendors are still displayed; preserve catalog order.
	for _, item := range databaseCatalog {
		if item.section == "云数据库" {
			add(item.vendor)
		}
	}
	return groups
}

func (s *State) catalogTree(c *ui.Context, onSelect func(Choice)) {
	palette := theme.Colors(c)

	ui.Column(c).Gap(0).Children(func() {
		recent := s.catalogRecents()
		if len(recent) > 0 {
			s.catalogTreeHeading(c, "最近使用过的")
			for _, item := range recent {
				s.catalogTreeLeaf(c, item, 0, onSelect)
			}
		}
		direct := s.catalogTreeDirect()
		groups := s.catalogTreeGroups()
		if len(direct)+len(groups) == 0 {
			ui.Text(c, "没有匹配的连接类型").FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
			return
		}
		s.catalogTreeHeading(c, "全部")
		for _, item := range direct {
			s.catalogTreeLeaf(c, item, 0, onSelect)
		}
		for _, group := range groups {
			group := group
			open := s.treeOpen[group.vendor] ||
				strings.TrimSpace(s.search) != ""
			button := ui.ButtonBase(c).Label("展开 " + group.label).
				FillWidth().Height(catalogTreeRowHeight).Radius(0).Background(ui.Transparent)
			button.Children(func() {
				ui.Row(c).FillWidth().Gap(4).Padding(0, 5).AlignItems(ui.Center).Children(func() {
					arrow := "▸"
					if open {
						arrow = "▾"
					}
					ui.Text(c, arrow).Width(14).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
					s.catalogProviderIcon(c, group.vendor, catalogTreeIconSize)
					ui.Text(c, group.label).FontSize(theme.Font(c, 12)).SingleLine()
				})
			})
			if button.Clicked() {
				s.treeOpen[group.vendor] = !s.treeOpen[group.vendor]
			}
			if open {
				for _, item := range group.items {
					s.catalogTreeLeaf(c, item, 1, onSelect)
				}
			}
		}
	})
}

func (s *State) catalogTreeHeading(c *ui.Context, title string) {
	palette := theme.Colors(c)

	ui.Row(c).Height(theme.RowHeight(c, 28)).Padding(0, 8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, title).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
	})
}

func (s *State) catalogTreeLeaf(c *ui.Context, item catalogEntry, depth int, onSelect func(Choice)) {
	palette := theme.Colors(c)

	selected := s.selected.Name == item.name
	bg := ui.Transparent
	if selected {
		bg = palette.Selection
	}
	button := ui.ButtonBase(c).Label("选择 " + item.name).
		FillWidth().Height(catalogTreeRowHeight).Radius(0).Background(bg)
	button.Children(func() {
		ui.Row(c).FillWidth().Gap(4).Padding(0, 5).AlignItems(ui.Center).Children(func() {
			indent := float32(18)
			if depth > 0 {
				indent = 36
			}
			ui.Box(c).Width(indent)
			s.catalogTreeIcon(c, item)
			ui.Text(c, item.name).FontSize(theme.Font(c, 12)).SingleLine()
		})
	})
	if button.Clicked() {
		s.chooseCatalogEntry(item, onSelect)
	}
}

func (s *State) catalogTreeIcon(c *ui.Context, item catalogEntry) {
	if bitmap := s.researchIcons[item.name]; bitmap != nil {
		ui.Image(c, bitmap).Size(catalogTreeIconSize, catalogTreeIconSize).Fit(ui.Contain)
		return
	}
	if bitmap := s.bundledIcons[item.name]; bitmap != nil {
		ui.Image(c, bitmap).Size(catalogTreeIconSize, catalogTreeIconSize).Fit(ui.Contain)
		return
	}
	// Missing brand assets are replaced by NyaDB's independent symbols.
	s.catalogIcon(c, item, catalogTreeIconSize)
}
