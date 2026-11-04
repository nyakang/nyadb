package shell

import "github.com/nyakang/nyadb/internal/ui/theme"

type tabDrag struct {
	Owner *State
	ID    uint64
}

func (s *State) tabIndex(id uint64) int {
	for i, t := range s.tabs {
		if t.id == id {
			return i
		}
	}
	return -1
}
func (s *State) moveTab(id, before uint64) bool {
	from, to := s.tabIndex(id), s.tabIndex(before)
	if from <= 0 || to <= 0 || from == to || s.tabs[from].pinned != s.tabs[to].pinned {
		return false
	}
	active := s.currentTab().id
	moving := s.tabs[from]
	s.tabs = append(s.tabs[:from], s.tabs[from+1:]...)
	if from < to {
		to--
	}
	s.tabs = append(s.tabs, nil)
	copy(s.tabs[to+1:], s.tabs[to:])
	s.tabs[to] = moving
	s.selectTab(active)
	return true
}
func (s *State) pinTab(id uint64) {
	index := s.tabIndex(id)
	if index <= 0 {
		return
	}
	active := s.currentTab().id
	t := s.tabs[index]
	t.pinned = !t.pinned
	s.tabs = append(s.tabs[:index], s.tabs[index+1:]...)
	target := 1
	for target < len(s.tabs) && s.tabs[target].pinned {
		target++
	}
	s.tabs = append(s.tabs, nil)
	copy(s.tabs[target+1:], s.tabs[target:])
	s.tabs[target] = t
	s.selectTab(active)
}
func (s *State) closeTabID(id uint64) { s.closeTab(s.tabIndex(id)) }
func (s *State) closeRight(id uint64) {
	index := s.tabIndex(id)
	if index < 0 {
		return
	}
	ids := map[uint64]bool{}
	for _, t := range s.tabs[index+1:] {
		if !t.pinned {
			ids[t.id] = true
		}
	}
	s.removeTabs(func(t *tab) bool { return ids[t.id] })
}
func (s *State) cycleTabs(reverse bool) {
	if len(s.tabs) < 2 {
		return
	}
	index := s.activeTab + 1
	if reverse {
		index = s.activeTab - 1
	}
	index = (index + len(s.tabs)) % len(s.tabs)
	s.selectTab(s.tabs[index].id)
}
func (s *State) tabFocus(t *tab) {
	s.editTarget = themeEmptyTarget()
	if t.query != nil {
		s.sqlWorkspace.Focus(t.query)
	}
	if t.data != nil {
		s.dataWorkspace.Focus(t.data)
	}
}
func themeEmptyTarget() theme.EditTarget { return theme.EditTarget{} }
