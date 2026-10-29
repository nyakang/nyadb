package navigation

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) ProfileVisible(p connections.Profile) bool {
	if s.Filter.StarredOnly && !p.Starred {
		return false
	}
	if s.Filter.Engine != "" && p.Kind != s.Filter.Engine {
		return false
	}
	query := strings.ToLower(strings.TrimSpace(s.Filter.Search))
	if query == "" || strings.Contains(strings.ToLower(p.Name), query) {
		return true
	}
	return p.GroupName != "" && strings.Contains(strings.ToLower(p.GroupName), query)
}

// Manual search/filter changes supersede a temporary active-tab reveal.
func (s *State) ChangeFilter() {
	s.Row = -1
	s.savedFilter = nil
}

func (s *State) RestoreFilter() {
	saved := s.savedFilter
	if saved == nil {
		return
	}
	s.savedFilter = nil
	s.Filter.Search, s.Filter.Engine, s.Filter.StarredOnly = saved.Search, saved.Engine, saved.StarredOnly
	s.Row = -1
	s.report("已恢复连接搜索与筛选条件")
}

func (s *State) filterMenu(m *ui.Menu) {
	if m.Item(locales.Text("所有连接类型")).Checked(s.Filter.Engine == "").Chosen() {
		s.Filter.Engine = ""
		s.ChangeFilter()
	}
	for _, item := range []struct {
		label string
		kind  connections.Kind
	}{
		{"MySQL", connections.MySQL}, {"PostgreSQL", connections.PostgreSQL}, {"SQLite", connections.SQLite},
	} {
		if m.Item(item.label).Checked(s.Filter.Engine == item.kind).Chosen() {
			s.Filter.Engine = item.kind
			s.ChangeFilter()
		}
	}
}

// SuspendFilter temporarily reveals an active tab without losing manual filters.
func (s *State) SuspendFilter() {
	if s.savedFilter == nil && (s.Filter.Search != "" || s.Filter.Engine != "" || s.Filter.StarredOnly) {
		snapshot := s.Filter
		s.savedFilter = &snapshot
	}
	s.Filter = Filter{}
	s.Row = -1
}
func (s *State) CanRestoreFilter() bool { return s.savedFilter != nil }
