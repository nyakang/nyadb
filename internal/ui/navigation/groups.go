package navigation

import (
	"context"
	"strings"
)

const groupNodePrefix = "\x1fgroup:"

func GroupKey(name string) string { return groupNodePrefix + name }

func IsGroup(node string) bool { return strings.HasPrefix(node, groupNodePrefix) }

func GroupName(node string) string { return strings.TrimPrefix(node, groupNodePrefix) }

func (s *State) Roots() []string {
	query := strings.ToLower(strings.TrimSpace(s.Filter.Search))
	roots := make([]string, 0, len(s.groups)+len(s.profiles))
	for _, group := range s.groups {
		if s.groupMatches(group.Name, query) {
			roots = append(roots, GroupKey(group.Name))
		}
	}
	for _, p := range s.profiles {
		if p.GroupName == "" && s.ProfileVisible(p) {
			roots = append(roots, ConnectionKey(p.ID))
		}
	}
	return roots
}

func (s *State) groupMatches(name, query string) bool {
	if !s.Filter.StarredOnly && s.Filter.Engine == "" && (query == "" || strings.Contains(strings.ToLower(name), query)) {
		return true
	}
	for _, p := range s.profiles {
		if strings.EqualFold(p.GroupName, name) && s.ProfileVisible(p) {
			return true
		}
	}
	return false
}

func (s *State) Children(node string) []string {
	if !IsGroup(node) {
		return s.databaseChildren(node)
	}
	group := GroupName(node)
	names := []string{}
	for _, p := range s.profiles {
		if strings.EqualFold(p.GroupName, group) && s.ProfileVisible(p) {
			names = append(names, ConnectionKey(p.ID))
		}
	}
	return names
}

func (s *State) GroupCount(name string) int {
	count := 0
	for _, p := range s.profiles {
		if strings.EqualFold(p.GroupName, name) {
			count++
		}
	}
	return count
}

func (s *State) BeginGroupEdit(name string) {
	s.GroupEdit.Original = name
	s.GroupEdit.Draft = name
	s.GroupEdit.Error = ""
	s.GroupEdit.Active = true
}

func (s *State) CancelGroupEdit() {
	s.GroupEdit.Active = false
	s.GroupEdit.Original = ""
	s.GroupEdit.Draft = ""
	s.GroupEdit.Error = ""
}

func (s *State) SaveGroup() {
	if s.service == nil {
		return
	}
	name := strings.TrimSpace(s.GroupEdit.Draft)
	var err error
	if s.GroupEdit.Original == "" {
		err = s.service.CreateGroup(context.Background(), name)
	} else {
		err = s.service.RenameGroup(context.Background(), s.GroupEdit.Original, name)
	}
	if err != nil {
		s.GroupEdit.Error = err.Error()
		return
	}
	s.GroupEdit.Active = false
	s.GroupEdit.Original = ""
	s.Group = name
	if s.deps.Changed != nil {
		s.deps.Changed()
	}
	s.selectGroup(name)
	s.report("已保存连接组：" + name)
}

func (s *State) DeleteGroup() {
	if s.service == nil || s.GroupPendingDelete == "" {
		return
	}
	name := s.GroupPendingDelete
	s.GroupPendingDelete = ""
	if err := s.service.DeleteGroup(context.Background(), name); err != nil {
		s.report("删除连接组失败：" + err.Error())
		return
	}
	if s.Group == name {
		s.Group = ""
	}
	if s.deps.Changed != nil {
		s.deps.Changed()
	}
	s.report("已删除虚拟组：" + name + "（连接档案已移至根目录）")
}

// SelectionRow resolves identity before the first draw, so the initial frame
// highlights the target without waiting for another interaction.
func (s *State) SelectionRow(roots []string) int {
	row, selected := 0, -1
	var visit func([]string)
	visit = func(keys []string) {
		for _, key := range keys {
			if key == s.Key {
				selected = row
			}
			row++
			if s.Tree.Open.Has(key) {
				visit(s.Children(key))
			}
		}
	}
	visit(roots)
	return selected
}
