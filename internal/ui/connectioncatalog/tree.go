package connectioncatalog

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// The original chooser list is a tree of *database types and vendors*, not
// a one-column grid. Observed live in Navicat 18 (O), 2026-10-21.
const (
	catalogTreeRowHeight float32 = 19
	catalogTreeIconSize  float32 = 16
)

var catalogTreeDirectEngines = []string{
	"MySQL", "PostgreSQL", "SQL Server", "Oracle", "SQLite", "DuckDB", "ClickHouse", "Apache Doris", "StarRocks", "Trino",
	"MariaDB", "TiDB", "OceanBase", "CockroachDB", "openGauss", "GaussDB", "KingbaseES", "MongoDB", "Redis", "Elasticsearch", "Easysearch", "Apache Solr", "Apache Cassandra", "Apache CouchDB", "Meilisearch", "Snowflake",
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

// Section is part of the key because a recent entry also appears in All.
type catalogNodeKey struct{ Section, Vendor, Name string }

func (s *State) catalogTree(c *ui.Context, onSelect func(Choice)) {
	p := theme.Colors(c)
	roots := []catalogNodeKey{}
	entries := map[catalogNodeKey]catalogEntry{}
	children := map[catalogNodeKey][]catalogNodeKey{}
	add := func(section string, items []catalogEntry) {
		if len(items) == 0 {
			return
		}
		roots = append(roots, catalogNodeKey{Section: section})
		for _, item := range items {
			key := catalogNodeKey{Section: section, Name: item.name}
			roots = append(roots, key)
			entries[key] = item
		}
	}
	add("最近使用过的", s.catalogRecents())
	direct, groups := s.catalogTreeDirect(), s.catalogTreeGroups()
	if len(direct)+len(groups) == 0 {
		ui.Text(c, locales.Text("没有匹配的连接类型")).TextColor(p.Muted)
		return
	}
	roots = append(roots, catalogNodeKey{Section: "全部"})
	for _, item := range direct {
		key := catalogNodeKey{Section: "全部", Name: item.name}
		roots = append(roots, key)
		entries[key] = item
	}
	labels := map[string]string{}
	searching := strings.TrimSpace(s.search) != ""
	if searching != s.treeSearching {
		s.tree.Open.Clear()
		s.treeSearching = searching
		if !searching {
			for vendor, open := range s.treeOpen {
				if open {
					s.tree.Open.Add(catalogNodeKey{Section: "全部", Vendor: vendor})
				}
			}
		}
	}
	for _, group := range groups {
		key := catalogNodeKey{Section: "全部", Vendor: group.vendor}
		roots = append(roots, key)
		labels[group.vendor] = group.label
		children[key] = []catalogNodeKey{}
		for _, item := range group.items {
			leaf := catalogNodeKey{Section: "全部", Vendor: group.vendor, Name: item.name}
			children[key] = append(children[key], leaf)
			entries[leaf] = item
		}
		if searching {
			s.tree.Open.Add(key)
		} else if s.treeOpen[group.vendor] {
			s.tree.Open.Add(key)
		}
	}
	s.tree.List.Selected = &s.treeRow
	s.tree.List.Header = func(row int) bool { k := s.tree.Item(row); return k.Name == "" && k.Vendor == "" }
	s.tree.List.Label = func(row int) string {
		k := s.tree.Item(row)
		if k.Name != "" {
			return locales.Text("选择 ") + k.Name
		}
		if k.Vendor != "" {
			return locales.Text("展开 ") + labels[k.Vendor]
		}
		return locales.Text(k.Section)
	}
	s.tree.Indent = 18
	s.tree.DisclosureSize = 14
	s.tree.Disclosure = ui.OutlineTriangle
	base := c.Theme()
	dense := *base
	dense.Spacing = 1
	c.SetTheme(&dense)
	tree := theme.Outline(c, &s.tree, roots, func(k catalogNodeKey) []catalogNodeKey { return children[k] }, func(k catalogNodeKey) {
		if k.Name == "" && k.Vendor == "" {
			ui.Text(c, locales.Text(k.Section)).MinHeight(theme.RowHeight(c, 28)).TextColor(p.Muted).Background(p.Panel)
			return
		}
		label := locales.Text("选择 ") + k.Name
		if k.Name == "" {
			label = locales.Text("展开 ") + labels[k.Vendor]
		}
		row := ui.Row(c).Label(label).FillWidth().Height(theme.RowHeight(c, catalogTreeRowHeight) - 2).Gap(4).AlignItems(ui.Center)
		row.Children(func() {
			if k.Name == "" {
				s.catalogProviderIcon(c, k.Vendor, catalogTreeIconSize)
				ui.Text(c, labels[k.Vendor]).SingleLine().Grow(1).MinWidth(0).Tooltip(labels[k.Vendor])
				return
			}
			item := entries[k]
			s.catalogTreeIcon(c, item)
			ui.Text(c, item.name).SingleLine().Grow(1).MinWidth(0).Tooltip(item.name)
			s.catalogAvailability(c, item, s.selected.Name == item.name)
		})
		if k.Name == "" && row.Clicked() {
			if s.tree.Open.Has(k) {
				s.tree.Open.Remove(k)
			} else {
				s.tree.Open.Add(k)
			}
		}
	}).Label(locales.Text("连接类型列表")).Grow(1).MinHeight(0).Padding(0).Gap(0)
	c.SetTheme(base)
	if !searching {
		for _, group := range groups {
			s.treeOpen[group.vendor] = s.tree.Open.Has(catalogNodeKey{Section: "全部", Vendor: group.vendor})
		}
	}
	if s.treeRow >= 0 && s.treeRow < s.tree.Rows() {
		k := s.tree.Item(s.treeRow)
		if item, ok := entries[k]; ok && (tree.Changed() || s.selected.Name != item.name) {
			s.chooseCatalogEntry(item, onSelect)
		}
	}
}

func (s *State) catalogAvailability(c *ui.Context, item catalogEntry, selected bool) {
	color := theme.Colors(c).Muted
	if selected {
		color = theme.Colors(c).SelectionText
	}
	text := locales.Text("尚未支持")
	if item.kind != "" {
		text = locales.Text("可连接")
	}
	ui.Text(c, text).FontSize(theme.Font(c, 10)).TextColor(color).SingleLine().Tooltip(text)
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
