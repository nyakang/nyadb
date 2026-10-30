package dataworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

const mysqlDataMaxProfiles = 20
const mysqlDataProfilesMaxBytes = 256 << 10

// These settings deliberately exclude connection details, SQL, result rows and
// editing drafts. The old presentation-only layout preference stays compatible.
type mysqlDataTableProfile struct {
	Name    string                         `json:"name"`
	Layout  mysqlDataSavedLayout           `json:"layout"`
	Filters []mysqlsvc.MySQLDataFilterRule `json:"filters,omitempty"`
	Sorts   []mysqlsvc.MySQLDataSortRule   `json:"sorts,omitempty"`
	Limit   int                            `json:"limit"`
}

type mysqlDataProfiles struct {
	Version int                     `json:"version"`
	Items   []mysqlDataTableProfile `json:"items"`
}

type mysqlDataToolsState struct {
	profilesLoaded    bool
	profiles          []mysqlDataTableProfile
	profilesErr       string
	profileOpen       bool
	profileSave       bool
	profileName       string
	profileChoice     string
	profileSearch     string
	activeProfile     string
	historyActiveRead *mysqlDataHistoryEntry
	historyStats      mysqlDataHistoryCounts
	historyFilter     string
	history           []mysqlDataHistoryEntry
	historyRequest    uint64
	historyRecorded   bool
	historyOpen       bool
	historyChoice     string
	analysisOpen      bool
	analysis          *mysqlDataAnalysis
	analysisSettings  bool
	analysisHeaders   bool
	analysisScope     string
	analysisField     string
	analysisErr       string
	analysisBusy      bool
	analysisCanceling bool
	analysisRequest   uint64
	analysisCancel    context.CancelFunc
	analysisResults   <-chan mysqlDataAnalysisResult
}

func mysqlDataProfilesKey(t *Document) string {
	return strings.Replace(mysqlDataLayoutKey(t), "mysql_data_layout:", "mysql_data_profiles:", 1)
}

func mysqlDataCurrentRules(t *Document) ([]mysqlsvc.MySQLDataFilterRule, []mysqlsvc.MySQLDataSortRule) {
	filters := append([]mysqlsvc.MySQLDataFilterRule(nil), t.filterRules...)
	if len(filters) == 0 && t.filterMode != "" {
		filters = append(filters, mysqlsvc.MySQLDataFilterRule{Column: t.filterColumn, Mode: t.filterMode, Value: t.filterValue})
	}
	for i := range filters {
		if filters[i].Mode == "is_null" || filters[i].Mode == "is_not_null" {
			filters[i].Value = ""
		}
	}
	sorts := append([]mysqlsvc.MySQLDataSortRule(nil), t.sortRules...)
	if len(sorts) == 0 && t.sortColumn != "" {
		sorts = append(sorts, mysqlsvc.MySQLDataSortRule{Column: t.sortColumn, Descending: t.sortDescending})
	}
	return filters, sorts
}

func validMySQLDataProfile(p mysqlDataTableProfile) bool {
	if strings.TrimSpace(p.Name) == "" || utf8.RuneCountInString(p.Name) > 80 || !utf8.ValidString(p.Name) ||
		p.Layout.Version != mysqlDataLayoutVersion || p.Limit <= 0 || p.Limit > mysqlsvc.MySQLDataMaxPageSize ||
		len(p.Filters) > mysqlsvc.MySQLDataMaxFilterRules || len(p.Sorts) > mysqlsvc.MySQLDataMaxSortRules {
		return false
	}
	for _, f := range p.Filters {
		if f.Column == "" || len(f.Value) > 4096 {
			return false
		}
		switch f.Mode {
		case "equal", "not_equal", "contains", "not_contains", "like", "not_like", "less", "greater", "is_null", "is_not_null":
		default:
			return false
		}
	}
	seen := map[string]bool{}
	for _, sort := range p.Sorts {
		if sort.Column == "" || seen[sort.Column] {
			return false
		}
		seen[sort.Column] = true
	}
	return true
}

func (s *State) loadDataProfiles(t *Document) {
	if t == nil || t.closed || t.tools.profilesLoaded {
		return
	}
	t.tools.profilesLoaded = true
	if s.dataLayoutPreferences.read == nil {
		return
	}
	value, err := s.dataLayoutPreferences.read(mysqlDataProfilesKey(t))
	if err != nil {
		t.tools.profilesErr = locales.Text("读取表配置文件失败")
		return
	}
	if value == "" {
		return
	}
	var saved mysqlDataProfiles
	if len(value) > mysqlDataProfilesMaxBytes || json.Unmarshal([]byte(value), &saved) != nil || saved.Version != 1 || len(saved.Items) > mysqlDataMaxProfiles {
		t.tools.profilesErr = locales.Text("表配置文件格式无效")
		return
	}
	seen := map[string]bool{}
	for _, p := range saved.Items {
		if !validMySQLDataProfile(p) || seen[p.Name] {
			t.tools.profilesErr = locales.Text("表配置文件格式无效")
			return
		}
		seen[p.Name] = true
	}
	t.tools.profiles = saved.Items
}

// Publish the new list only after its preference write succeeds.
func (s *State) writeDataProfiles(t *Document, items []mysqlDataTableProfile) error {
	if s.dataLayoutPreferences.write == nil {
		return errors.New(locales.Text("表配置文件存储不可用"))
	}
	encoded, err := json.Marshal(mysqlDataProfiles{Version: 1, Items: items})
	if err != nil || len(encoded) > mysqlDataProfilesMaxBytes {
		return errors.New(locales.Text("表配置文件格式无效"))
	}
	if err := s.dataLayoutPreferences.write(mysqlDataProfilesKey(t), string(encoded)); err != nil {
		return errors.New(locales.Text("保存表配置文件失败"))
	}
	t.tools.profiles = items
	return nil
}

func (s *State) saveDataProfile(t *Document, name string) error {
	if t == nil || t.closed || t.page == nil || t.loading {
		return errors.New(locales.Text("请等待表数据加载完成"))
	}
	s.loadDataProfiles(t)
	if t.tools.profilesErr != "" {
		return errors.New(t.tools.profilesErr)
	}
	s.restoreMySQLDataLayout(t)
	var layout mysqlDataSavedLayout
	if json.Unmarshal([]byte(mysqlDataLayoutSnapshot(t)), &layout) != nil {
		return errors.New(locales.Text("表配置文件格式无效"))
	}
	filters, sorts := mysqlDataCurrentRules(t)
	p := mysqlDataTableProfile{Name: strings.TrimSpace(name), Layout: layout, Filters: filters, Sorts: sorts, Limit: t.limit}
	if !validMySQLDataProfile(p) {
		return errors.New(locales.Text("配置名称不能为空且最多 80 个字符"))
	}
	items := append([]mysqlDataTableProfile(nil), t.tools.profiles...)
	for i := range items {
		if items[i].Name == p.Name {
			items[i] = p
			if err := s.writeDataProfiles(t, items); err != nil {
				return err
			}
			t.tools.activeProfile = p.Name
			return nil
		}
	}
	if len(items) >= mysqlDataMaxProfiles {
		return errors.New(locales.Text("最多保存 20 个表配置文件"))
	}
	if err := s.writeDataProfiles(t, append(items, p)); err != nil {
		return err
	}
	t.tools.activeProfile = p.Name
	return nil
}

func (s *State) renameDataProfile(t *Document, oldName, newName string) error {
	newName = strings.TrimSpace(newName)
	if newName == "" || utf8.RuneCountInString(newName) > 80 || !utf8.ValidString(newName) {
		return errors.New(locales.Text("配置名称不能为空且最多 80 个字符"))
	}
	items := append([]mysqlDataTableProfile(nil), t.tools.profiles...)
	found := -1
	for i, p := range items {
		if p.Name == newName && p.Name != oldName {
			return errors.New(locales.Text("配置名称已存在"))
		}
		if p.Name == oldName {
			found = i
		}
	}
	if found < 0 {
		return errors.New(locales.Text("请选择表配置文件"))
	}
	items[found].Name = newName
	if err := s.writeDataProfiles(t, items); err != nil {
		return err
	}
	if t.tools.activeProfile == oldName {
		t.tools.activeProfile = newName
	}
	return nil
}

func (s *State) deleteDataProfile(t *Document, name string) error {
	items := make([]mysqlDataTableProfile, 0, len(t.tools.profiles))
	for _, p := range t.tools.profiles {
		if p.Name != name {
			items = append(items, p)
		}
	}
	if err := s.writeDataProfiles(t, items); err != nil {
		return err
	}
	if t.tools.activeProfile == name {
		t.tools.activeProfile = ""
	}
	return nil
}

func (s *State) applyDataProfile(services ui.Services, t *Document, p mysqlDataTableProfile) {
	if t == nil || t.closed {
		return
	}
	// The guarded callback owns its maps/slices even if the named profile is
	// renamed or overwritten while an edit-save acknowledgement is pending.
	encoded, _ := json.Marshal(p)
	var owned mysqlDataTableProfile
	if json.Unmarshal(encoded, &owned) != nil || !validMySQLDataProfile(owned) {
		t.ruleError = locales.Text("表配置文件格式无效")
		return
	}
	if s.GuardLeave(t, func(next ui.Services) { s.applyDataProfile(next, t, owned) }) {
		return
	}
	if t.page == nil || t.loading {
		t.ruleError = locales.Text("请等待表数据加载完成")
		return
	}
	if !mysqlDataRulesAvailable(t, owned.Filters, owned.Sorts) {
		t.ruleError = locales.Text("配置中的筛选或排序字段已不可用")
		return
	}
	s.restoreMySQLDataLayout(t)
	t.grid.Columns = ui.TableLayout{Order: owned.Layout.Order, Widths: owned.Layout.Widths}
	t.hiddenColumns = owned.Layout.HiddenColumns
	t.showFieldTypes = owned.Layout.ShowFieldTypes
	t.layout.showRowNumbers = owned.Layout.ShowRowNumbers
	reconcileMySQLDataLayout(t)
	t.filterColumn, t.filterMode, t.filterValue = "", "", ""
	t.filterRules = owned.Filters
	t.sortColumn, t.sortDescending = "", false
	t.sortRules = owned.Sorts
	t.sort = ui.SortOrder{}
	if len(owned.Sorts) > 0 {
		t.sort = ui.SortOrder{Column: owned.Sorts[0].Column, Descending: owned.Sorts[0].Descending}
	}
	mysqlDataRestoreRuleDrafts(t, owned.Filters, owned.Sorts)
	t.limit, t.ruleError = owned.Limit, ""
	t.tools.activeProfile = owned.Name
	s.flushMySQLDataLayout(t)
	s.Refresh(services, t)
}

func mysqlDataRulesAvailable(t *Document, filters []mysqlsvc.MySQLDataFilterRule, sorts []mysqlsvc.MySQLDataSortRule) bool {
	for _, f := range filters {
		found := false
		for _, col := range t.columns {
			found = found || col.ID == f.Column
		}
		if !found {
			return false
		}
	}
	for _, rule := range sorts {
		found := false
		for _, col := range t.columns {
			found = found || col.ID == rule.Column && col.Sortable
		}
		if !found {
			return false
		}
	}
	return true
}

func (s *State) profilesMenu(c *ui.Context, t *Document, m *ui.Menu) {
	s.loadDataProfiles(t)
	m.Submenu(locales.Text("加载配置文件"), func(sub *ui.Menu) {
		if len(t.tools.profiles) == 0 {
			sub.Item(locales.Text("没有已保存的表配置")).Disabled(true)
		}
		for _, p := range t.tools.profiles {
			if sub.Item(p.Name).Disabled(t.page == nil || t.loading || t.edit.Busy || t.edit.Uncertain != nil).Chosen() {
				s.applyDataProfile(c.Services(), t, p)
			}
		}
	})
	if m.Item(locales.Text("保存配置文件")).Disabled(t.tools.activeProfile == "" || t.page == nil || t.loading || s.dataLayoutPreferences.write == nil).Chosen() {
		if err := s.saveDataProfile(t, t.tools.activeProfile); err != nil {
			s.setStatus(err.Error())
		}
	}
	if m.Item(locales.Text("另存配置文件为…")).Disabled(t.page == nil || t.loading || s.dataLayoutPreferences.write == nil).Chosen() {
		t.tools.profileOpen, t.tools.profileSave, t.tools.profileName = true, true, ""
	}
	if m.Item(locales.Text("关闭配置文件")).Disabled(t.tools.activeProfile == "").Chosen() {
		t.tools.activeProfile = ""
	}
	m.Separator()
	if m.Item(locales.Text("管理配置文件…")).Chosen() {
		t.tools.profileOpen, t.tools.profileSave = true, false
		t.tools.profileSearch = ""
		if len(t.tools.profiles) > 0 {
			t.tools.profileChoice = t.tools.profiles[0].Name
			t.tools.profileName = t.tools.profileChoice
		}
	}
}

func (s *State) profileModal(c *ui.Context, t *Document) {
	ui.Modal(c, &t.tools.profileOpen, func() {
		ui.Column(c).Width(530).MaxWidthPercent(95).Padding(18).Gap(10).Children(func() {
			ui.Text(c, locales.Text("表配置文件")).Bold().FontSize(theme.Font(c, 15))
			ui.Text(c, t.database+"."+t.table).TextColor(theme.Colors(c).Muted)
			ui.Text(c, locales.Text("表的配置文件存储筛选、排序和列设置以供将来使用。")).FontSize(theme.Font(c, 12))
			ui.Text(c, locales.Text("保存已应用规则及每页行数；不会保存行数据或待保存更改。")).FontSize(theme.Font(c, 12))
			if t.tools.profileSave {
				ui.Text(c, locales.Text("同名配置将被替换。"))
			} else {
				ui.TextInput(c, &t.tools.profileSearch).Label(locales.Text("搜索配置文件")).Placeholder(locales.Text("搜索配置文件")).FillWidth()
				choices := make([]string, 0, len(t.tools.profiles))
				for _, p := range t.tools.profiles {
					if strings.Contains(strings.ToLower(p.Name), strings.ToLower(t.tools.profileSearch)) {
						choices = append(choices, p.Name)
					}
				}
				before := t.tools.profileChoice
				matched := false
				for _, name := range choices {
					matched = matched || name == before
				}
				if !matched {
					t.tools.profileChoice = ""
					if len(choices) > 0 {
						t.tools.profileChoice = choices[0]
					}
				}
				ui.Select(c, &t.tools.profileChoice, choices).Label(locales.Text("已保存的表配置")).FillWidth()
				if before != t.tools.profileChoice {
					t.tools.profileName = t.tools.profileChoice
				}
				ui.Text(c, fmt.Sprintf(locales.Text("已保存 %d / 20 个配置"), len(t.tools.profiles)))
			}
			ui.TextInput(c, &t.tools.profileName).Label(locales.Text("表配置名称")).Placeholder(locales.Text("表配置名称")).FillWidth()
			if t.tools.profilesErr != "" {
				ui.Text(c, t.tools.profilesErr).TextColor(theme.Colors(c).Error)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if !t.tools.profileSave {
					if ui.Button(c, locales.Text("加载配置")).Disabled(t.tools.profileChoice == "" || t.loading || t.page == nil).Clicked() {
						for _, p := range t.tools.profiles {
							if p.Name == t.tools.profileChoice {
								s.applyDataProfile(c.Services(), t, p)
								t.tools.profileOpen = false
								break
							}
						}
					}
					if ui.Button(c, locales.Text("删除配置")).Disabled(t.tools.profileChoice == "").Clicked() {
						if err := s.deleteDataProfile(t, t.tools.profileChoice); err != nil {
							s.setStatus(err.Error())
						} else {
							t.tools.profileChoice, t.tools.profileName = "", ""
						}
					}
					if ui.Button(c, locales.Text("重命名配置")).Disabled(t.tools.profileChoice == "").Clicked() {
						if err := s.renameDataProfile(t, t.tools.profileChoice, t.tools.profileName); err != nil {
							s.setStatus(err.Error())
						} else {
							t.tools.profileChoice = strings.TrimSpace(t.tools.profileName)
						}
					}
				} else if ui.PrimaryButton(c, locales.Text("保存配置")).Clicked() {
					if err := s.saveDataProfile(t, t.tools.profileName); err != nil {
						s.setStatus(err.Error())
					} else {
						t.tools.profileOpen = false
					}
				}
				if ui.Button(c, locales.Text("关闭")).Clicked() {
					t.tools.profileOpen = false
				}
			})
		})
	})
}

func (s *State) toolsModals(c *ui.Context, t *Document) {
	if t == nil || t.closed {
		return
	}
	if t.tools.profileOpen {
		s.profileModal(c, t)
	}
	if t.tools.historyOpen {
		s.historyModal(c, t)
	}
	if !t.tools.analysisOpen && t.tools.analysisBusy {
		s.closeDataAnalysis(t)
	}
}
