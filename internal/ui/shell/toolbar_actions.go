package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/locales"
)

type toolbarScope struct {
	profile    int64
	generation uint64
	database   string
}

func (s *State) toolbarMenu(services ui.Services, action string, m *ui.Menu) {
	w := s.sessions.Current()
	scope := toolbarScope{w.ProfileID, w.Generation, w.Database}
	selected := ""
	if action == "其它" {
		for _, kind := range []string{"存储过程", "事件"} {
			if m.Item(locales.Text(kind)).Disabled(!s.toolbarActionAvailable(action)).Chosen() {
				selected = kind
			}
		}
	} else {
		for _, item := range []struct{ label, operation string }{{"备份数据库…", "backup"}, {"恢复备份…", "restore"}, {"导出结构 SQL…", "sql_structure"}, {"导出结构及数据 SQL…", "sql_data"}} {
			if m.Item(locales.Text(item.label)).Disabled(!s.toolbarActionAvailable(action)).Chosen() {
				selected = item.operation
			}
		}
	}
	if s.toolbarMenus == nil {
		s.toolbarMenus = map[string]toolbarScope{}
	}
	if selected == "" {
		s.toolbarMenus[action] = scope
		return
	}
	original, ok := s.toolbarMenus[action]
	delete(s.toolbarMenus, action)
	if !ok || original != scope || !s.toolbarActionAvailable(action) {
		return
	}
	s.lastAction = action
	if action == "其它" {
		s.showObjects(selected)
	} else {
		s.objectWorkspace.BeginBackup(services, scope.profile, scope.database, "", selected)
	}
}
