package sqlworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
	"sort"
	"strings"
)

func (s *State) SelectAssetGroup(profile int64, group string) {
	b := s.assetBrowser(profile)
	b.group = group
	b.database = ""
	b.search = ""
	b.selected = ""
	b.row = -1
}
func (s *State) AssetShortcut(profile int64, id string, key ui.Key) {
	switch key {
	case ui.KeyF2:
		s.beginAsset(profile, id, "rename")
	case ui.KeyDelete:
		s.beginAsset(profile, id, "delete")
	}
}
func (s *State) AssetGroupShortcut(profile int64, id string, key ui.Key) {
	switch key {
	case ui.KeyF2:
		s.beginAsset(profile, id, "rename_group")
	case ui.KeyDelete:
		s.beginAsset(profile, id, "delete_group")
	}
}
func (s *State) AssetContextMenu(m *ui.Menu, profile int64, id string, open func(queries.Named)) bool {
	n, ok := s.asset(profile, id)
	if !ok {
		return false
	}
	writable := s.assetWritable(profile) && !s.assetPending(id)
	action, group := "", ""
	if m.Item(locales.Text("打开查询")).Shortcut(0, ui.KeyEnter).Chosen() {
		action = "open"
	}
	if m.Item(locales.Text("重命名查询")).Shortcut(0, ui.KeyF2).Disabled(!writable).Chosen() {
		action = "rename"
	}
	m.Submenu(locales.Text("移动到查询分组"), func(sub *ui.Menu) {
		if sub.Item(locales.Text("未分组")).Checked(s.AssetGroup(profile, id) == "").Chosen() {
			action = "move"
		}
		for _, g := range s.AssetGroups(profile) {
			if sub.Item(g.Name).Checked(s.AssetGroup(profile, id) == g.ID).Chosen() {
				action, group = "move", g.ID
			}
		}
	}).Disabled(!writable)
	m.Separator()
	if m.Item(locales.Text("删除查询…")).Shortcut(0, ui.KeyDelete).Disabled(!writable).Chosen() {
		action = "delete"
	}
	if action == "" {
		s.persistence.assetMenu = &assetMenuScope{profile: profile, id: id, revision: n.Revision, catalogRevision: s.persistence.catalogs[profile].Revision}
		return true
	}
	scope := s.persistence.assetMenu
	s.persistence.assetMenu = nil
	if scope == nil || scope.profile != profile || scope.id != id || s.persistence.assetScope != profile || scope.revision != n.Revision {
		return false
	}
	switch action {
	case "open":
		if open != nil {
			open(n)
		}
	case "move":
		if scope.catalogRevision != s.persistence.catalogs[profile].Revision {
			return false
		}
		s.ChangeGroup(profile, "move", id, group)
	default:
		s.beginAsset(profile, id, action)
	}
	return false
}
func (s *State) AssetGroupContextMenu(m *ui.Menu, profile int64, id string) bool {
	action := ""
	if m.Item(locales.Text("重命名查询分组")).Disabled(!s.assetWritable(profile)).Chosen() {
		action = "rename_group"
	}
	if m.Item(locales.Text("删除查询分组…")).Disabled(!s.assetWritable(profile)).Chosen() {
		action = "delete_group"
	}
	if action == "" {
		s.persistence.assetGroupMenu = &assetMenuScope{profile: profile, id: id, revision: s.persistence.catalogs[profile].Revision}
		return true
	}
	scope := s.persistence.assetGroupMenu
	s.persistence.assetGroupMenu = nil
	if scope == nil || scope.profile != profile || scope.id != id || scope.revision != s.persistence.catalogs[profile].Revision || s.persistence.assetScope != profile {
		return false
	}
	s.beginAsset(profile, id, action)
	return false
}
func (s *State) AssetsView(c *ui.Context, profile int64, open func(queries.Named)) {
	s.ActivateAssets(profile)
	b := s.assetBrowser(profile)
	palette := theme.Colors(c)
	ui.Column(c).Fill().Gap(0).Children(func() {
		ui.Row(c).Wrap().Padding(5, 8).Gap(7).AlignItems(ui.Center).Background(palette.Header).Children(func() {
			ui.Text(c, locales.Text("命名查询")).Bold()
			if ui.Button(c, locales.Text("刷新查询")).Disabled(!s.assetWritable(profile)).Clicked() {
				s.RefreshAssets(profile)
			}
			ui.TextInput(c, &b.search).Label(locales.Text("搜索查询名称")).Placeholder(locales.Text("搜索查询名称")).Width(180)
			dbs := []string{""}
			seen := map[string]bool{}
			for _, n := range s.Named(profile) {
				if n.Database != "" && !seen[n.Database] {
					seen[n.Database] = true
					dbs = append(dbs, n.Database)
				}
			}
			sort.Strings(dbs[1:])
			dbNames := append([]string(nil), dbs...)
			dbNames[0] = locales.Text("全部数据库")
			dbLabels := assetFilterLabels(dbNames, dbs)
			selectedDB := dbLabels[0]
			for i, database := range dbs {
				if database == b.database {
					selectedDB = dbLabels[i]
				}
			}
			if ui.Select(c, &selectedDB, dbLabels).Label(locales.Text("查询数据库筛选")).Width(150).Changed() {
				for i, label := range dbLabels {
					if selectedDB == label {
						b.database = dbs[i]
						break
					}
				}
			}
			groups := []string{locales.Text("全部分组"), locales.Text("未分组")}
			ids := []string{"", "ungrouped"}
			selectedGroup := groups[0]
			for _, g := range s.AssetGroups(profile) {
				groups = append(groups, g.Name)
				ids = append(ids, g.ID)
			}
			groups = assetFilterLabels(groups, ids)
			for i, id := range ids {
				if id == b.group {
					selectedGroup = groups[i]
				}
			}
			if ui.Select(c, &selectedGroup, groups).Label(locales.Text("查询分组筛选")).Width(150).Changed() {
				for i, name := range groups {
					if name == selectedGroup {
						b.group = ids[i]
						break
					}
				}
			}
			ui.Button(c, locales.Text("管理查询分组")).Disabled(!s.assetWritable(profile)).Menu(func(m *ui.Menu) {
				if m.Item(locales.Text("新建查询分组…")).Chosen() {
					s.beginAsset(profile, "", "new_group")
				}
				for _, g := range s.AssetGroups(profile) {
					g := g
					m.Submenu(g.Name, func(sub *ui.Menu) { s.AssetGroupContextMenu(sub, profile, g.ID) })
				}
			})
		})
		if s.persistence.loading {
			ui.Text(c, locales.Text("等待加载")).Padding(8)
			return
		}
		if b.err != "" {
			ui.Text(c, b.err).TextColor(palette.Error).Padding(6)
		}
		if s.persistence.blocked {
			ui.Text(c, locales.Text("查询存储不可用，原有加密记录已保留")).TextColor(palette.Error).Padding(6)
		}
		names := s.filteredAssets(profile)
		b.row = -1
		for i, n := range names {
			if n.ID == b.selected {
				b.row = i
			}
		}
		b.list.Selected = &b.row
		b.list.Key = func(row int) any {
			if row < 0 || row >= len(names) {
				return nil
			}
			return names[row].ID
		}
		b.list.CellMenu = func(m *ui.Menu, row, col int) {
			if row >= 0 && row < len(names) {
				b.selected = names[row].ID
				s.AssetContextMenu(m, profile, b.selected, open)
			}
		}
		columns := []ui.TableColumn{{ID: "name", Title: locales.Text("查询名称"), Width: 240}, {ID: "database", Title: locales.Text("数据库/schema"), Width: 180}, {ID: "group", Title: locales.Text("查询分组"), Width: 160}}
		table := theme.Table(c, &b.list, columns, len(names), func(row, col int) {
			n := names[row]
			value := n.Name
			if col == 1 {
				value = n.Database
			}
			if col == 2 {
				value = s.AssetGroupName(profile, s.AssetGroup(profile, n.ID))
				if value == "" {
					value = locales.Text("未分组")
				}
			}
			ui.Text(c, value).SingleLine()
		}).Grow(1)
		if b.row >= 0 && b.row < len(names) {
			b.selected = names[b.row].ID
			if table.Submitted() && open != nil {
				open(names[b.row])
			}
			for _, key := range []ui.Key{ui.KeyF2, ui.KeyDelete} {
				if table.Shortcut(0, key) {
					s.AssetShortcut(profile, b.selected, key)
				}
			}
		}
		if len(names) == 0 {
			ui.Text(c, locales.Text("尚无匹配的已保存查询")).Padding(8).TextColor(palette.Muted)
		}
	})
}

// Select uses visible text as its value; keep sentinel labels distinct from
// legal database/group names without reserving those names in storage.
func assetFilterLabels(names, ids []string) []string {
	labels := append([]string(nil), names...)
	seen := make(map[string]bool, len(labels))
	for i, name := range labels {
		label := name
		for seen[label] {
			label += " · " + ids[i]
		}
		labels[i], seen[label] = label, true
	}
	return labels
}
func (s *State) assetModals(c *ui.Context) {
	d := s.persistence.assetDialog
	if d == nil {
		return
	}
	if d.profile != s.persistence.assetScope {
		s.persistence.assetDialog = nil
		return
	}
	visible := true
	ui.Modal(c, &visible, func() {
		ui.Column(c).Width(440).MaxWidthPercent(95).Padding(18).Gap(10).Children(func() {
			title := locales.Text("重命名查询")
			switch d.kind {
			case "delete":
				title = locales.Text("删除命名查询？")
			case "new_group":
				title = locales.Text("新建查询分组")
			case "rename_group":
				title = locales.Text("重命名查询分组")
			case "delete_group":
				title = locales.Text("删除查询分组？")
			}
			ui.Text(c, title).Bold()
			deleting := d.kind == "delete" || d.kind == "delete_group"
			if deleting {
				ui.Text(c, d.name)
				message := locales.Text("已打开的 SQL 标签将保留为未命名草稿。")
				if d.kind == "delete_group" {
					message = locales.Text("组内查询将移至未分组，不会删除查询。")
				}
				ui.Text(c, message)
			} else {
				input := ui.TextInput(c, &d.name).Label(locales.Text("名称")).ReadOnly(s.assetPending(d.id) || s.persistence.catalogBusy[d.profile]).AutoFocus()
				if input.Submitted() {
					s.submitAssetDialog(d)
				}
			}
			if d.err != "" {
				ui.Text(c, d.err).TextColor(theme.Colors(c).Error)
			}
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, locales.Text("取消")).Clicked() {
					visible = false
				}
				label := locales.Text("确认")
				if deleting {
					label = locales.Text("删除")
				}
				if ui.PrimaryButton(c, label).Disabled(!s.assetWritable(d.profile) || s.assetPending(d.id) || (!deleting && strings.TrimSpace(d.name) == "")).Clicked() {
					s.submitAssetDialog(d)
				}
			})
		})
	})
	if !visible {
		s.persistence.assetDialog = nil
	}
}
func (s *State) submitAssetDialog(d *assetDialog) {
	if d != s.persistence.assetDialog || d.profile != s.persistence.assetScope || !s.assetWritable(d.profile) {
		return
	}
	if d.kind == "rename" || d.kind == "delete" {
		n, ok := s.asset(d.profile, d.id)
		if !ok || n.Revision != d.revision {
			d.err = locales.Text("查询已变更，请重新选择")
			return
		}
		if s.assetPending(d.id) {
			return
		}
	}
	if strings.Contains(d.kind, "group") && s.Catalog(d.profile).Revision != d.revision {
		d.err = locales.Text("查询分组已变更，请重新选择")
		return
	}
	switch d.kind {
	case "rename":
		s.RenameAsset(d.profile, d.id, d.name)
	case "delete":
		s.DeleteAsset(d.profile, d.id)
	default:
		s.ChangeGroup(d.profile, d.kind, d.id, d.name)
	}
}
