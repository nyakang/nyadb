package objectworkspace

import (
	"encoding/json"
	"fmt"
	"github.com/egoist/mygo/ui"
	"sort"
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
func (s *State) objectGroupsView(c *ui.Context) {
	b := s.currentBrowser()
	groups := append([]string{"全部分组"}, s.ObjectGroups(b.ProfileID, b.Database)...)
	selected := b.groupFilter
	if selected == "" {
		selected = "全部分组"
	}
	if ui.Select(c, &selected, groups).Label("对象分组筛选").Width(150).Changed() {
		b.groupFilter = selected
		if selected == "全部分组" {
			b.groupFilter = ""
		}
		b.Row = -1
		b.selection.Clear()
		b.List = ui.ListState{Columns: b.List.Columns}
	}
	if ui.Button(c, "重命名分组").Disabled(b.groupFilter == "" || b.groupDraft == "").Clicked() {
		s.RenameObjectGroup(b.ProfileID, b.Database, b.groupFilter, b.groupDraft)
	}
	if ui.Button(c, "删除分组").Disabled(b.groupFilter == "").Clicked() {
		s.RenameObjectGroup(b.ProfileID, b.Database, b.groupFilter, "")
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
