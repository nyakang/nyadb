package objectworkspace

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) saveObjectGroups(profile int64, database string) {
	if s.deps.SaveObjectGroups == nil {
		return
	}
	values := map[string]string{}
	for k, group := range s.presentation(profile).groups {
		if k.Database == database && group != "" {
			values[k.Kind+"\x00"+k.Name] = group
		}
	}
	s.deps.SaveObjectGroups(profile, database, values)
}
func (s *State) loadObjectGroups(profile int64, database string) {
	if s.deps.LoadObjectGroups == nil {
		return
	}
	b := s.presentation(profile)
	if b.groups == nil {
		b.groups = map[objectKey]string{}
	}
	for key, group := range s.deps.LoadObjectGroups(profile, database) {
		for i := 0; i < len(key); i++ {
			if key[i] == 0 {
				b.groups[objectKey{profile, database, key[:i], key[i+1:]}] = group
				break
			}
		}
	}
}
func (s *State) ObjectGroups(profile int64, database string) []string {
	set := map[string]bool{}
	for k, g := range s.presentation(profile).groups {
		if k.Database == database && g != "" {
			set[g] = true
		}
	}
	out := []string{}
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
func (s *State) RenameObjectGroup(profile int64, database, from, to string) {
	b := s.presentation(profile)
	for k, g := range b.groups {
		if k.Database == database && g == from {
			if to == "" {
				delete(b.groups, k)
			} else {
				b.groups[k] = to
			}
		}
	}
	if b.groupFilter == from {
		b.groupFilter = to
	}
	s.saveObjectGroups(profile, database)
}

// Object groups edit only local presentation preferences. Their modal owns
// its opening scope and object selection, independently of the live table.
type objectGroupDialog struct {
	scope          browserActionScope
	open           bool
	selected, name string
}

func (s *State) beginObjectGroups(scope browserActionScope) {
	if !s.browserScopeValid(scope, true) {
		return
	}
	s.groupDialog = &objectGroupDialog{scope: scope, open: true, selected: scope.browser.groupFilter}
}

func (s *State) validateGroupDialog() {
	if d := s.groupDialog; d != nil && (!d.open || !s.browserScopeValid(d.scope, true)) {
		s.groupDialog = nil
	}
}

func (s *State) objectGroupModal(c *ui.Context) {
	s.validateGroupDialog()
	d := s.groupDialog
	if d == nil {
		return
	}
	p := theme.Colors(c)
	ui.Modal(c, &d.open, func() {
		width, height := c.Size()
		ui.Column(c).Width(min(float32(510), width-40)).MaxHeight(height - 40).Padding(16).Gap(10).Background(p.Header).Children(func() {
			ui.Text(c, locales.Text("管理组…")).Bold().FontSize(theme.Font(c, 15))
			ui.Text(c, locales.Text("数据库：")+d.scope.database).TextColor(p.Muted)
			ui.Select(c, &d.selected, s.ObjectGroups(d.scope.profile, d.scope.database)).Label(locales.Text("对象分组"))
			ui.TextInput(c, &d.name).Label(locales.Text("对象分组名称")).Placeholder(locales.Text("对象分组名称"))
			ui.Column(c).Gap(6).Children(func() {
				if ui.Button(c, locales.Text("分组所选对象")).Disabled(len(d.scope.objects) == 0 || strings.TrimSpace(d.name) == "").Clicked() {
					if s.browserScopeValid(d.scope, true) {
						s.assignObjectGroup(d.scope.profile, d.scope.database, d.scope.objects, d.name)
						d.selected = strings.TrimSpace(d.name)
					}
				}
				if ui.Button(c, locales.Text("从组中排除")).Disabled(len(d.scope.objects) == 0).Clicked() {
					if s.browserScopeValid(d.scope, true) {
						s.assignObjectGroup(d.scope.profile, d.scope.database, d.scope.objects, "")
					}
				}
				if ui.Button(c, locales.Text("重命名分组")).Disabled(d.selected == "" || strings.TrimSpace(d.name) == "" || d.selected == strings.TrimSpace(d.name)).Clicked() {
					if s.browserScopeValid(d.scope, true) {
						s.RenameObjectGroup(d.scope.profile, d.scope.database, d.selected, strings.TrimSpace(d.name))
						d.selected = strings.TrimSpace(d.name)
					}
				}
				if ui.Button(c, locales.Text("删除分组")).Disabled(d.selected == "").Clicked() {
					if s.browserScopeValid(d.scope, true) {
						s.RenameObjectGroup(d.scope.profile, d.scope.database, d.selected, "")
						d.selected = ""
					}
				}
			})
			ui.Row(c).Justify(ui.End).Children(func() {
				if ui.Button(c, locales.Text("关闭")).Clicked() {
					d.open = false
				}
			})
		})
	})
	if !d.open {
		s.groupDialog = nil
	}
}

// SetObjectGroupPreferences uses NyaDB's own versioned workspace preference
// record; it is independent of database state and proprietary clipboard data.
func (s *State) SetObjectGroupPreferences(read func(string) (string, error), write func(string, string) error) {
	s.deps.LoadObjectGroups = func(profile int64, database string) map[string]string {
		if read == nil {
			return nil
		}
		raw, e := read(fmt.Sprintf("objects/groups/v1/%d/%s", profile, database))
		if e != nil || raw == "" {
			return nil
		}
		var v struct {
			Version int               `json:"version"`
			Entries map[string]string `json:"entries"`
		}
		if json.Unmarshal([]byte(raw), &v) != nil || v.Version != 1 {
			return nil
		}
		return v.Entries
	}
	s.deps.SaveObjectGroups = func(profile int64, database string, values map[string]string) {
		if write == nil {
			return
		}
		raw, e := json.Marshal(struct {
			Version int               `json:"version"`
			Entries map[string]string `json:"entries"`
		}{1, values})
		if e != nil {
			return
		}
		if e = write(fmt.Sprintf("objects/groups/v1/%d/%s", profile, database), string(raw)); e != nil {
			s.report("保存对象分组失败：" + e.Error())
		}
	}
}
