package shell

import (
	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/sessionbrowser"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) startMySQLSession(services ui.Services) {
	p, ok := s.currentProfile()
	if !ok || (p.Kind != connections.MySQL && p.Kind != connections.PostgreSQL && p.Kind != connections.SQLite) || s.profileService == nil {
		s.status = "请选择 MySQL 连接"
		return
	}
	if s.guardMySQLProfileLeave(p.ID, func(next ui.Services) { s.startMySQLSession(next) }) {
		return
	}
	s.sessions.Connect(p, services.Invalidate)
}
func (s *State) disconnectMySQLSession() {
	s.disconnectMySQLProfile(s.sessions.Current().ProfileID)
}
func (s *State) disconnectMySQLProfile(id int64) {
	if s.guardMySQLProfileLeave(id, func(_ ui.Services) { s.disconnectMySQLProfile(id) }) {
		return
	}
	s.sessions.Disconnect(id)
}
func (s *State) closeAllMySQLConnections() {
	if s.guardMySQLProfileLeave(0, func(_ ui.Services) { s.closeAllMySQLConnections() }) {
		return
	}
	s.sessions.DisconnectAll()
}

// CloseMySQLSessions releases all workbench connections when the app exits.
func (s *State) CloseMySQLSessions() { s.closeMySQLSessions() }
func (s *State) closeMySQLSessions() {
	s.mysqlForceClose = true
	defer func() { s.mysqlForceClose = false }()
	for _, t := range s.tabs {
		s.disposeTab(t)
	}
	s.dataWorkspace.CloseAll()
	s.objectWorkspace.CloseAll()
	s.sqlWorkspace.CloseAll()
	s.engineData.CloseAll()
	s.sessions.Close()
	s.closeTunnelServices()
}
func (s *State) mysqlSessionToolbar(c *ui.Context) {
	if s.engineSessionToolbar(c) {
		return
	}
	p, ok := s.currentProfile()
	if !ok || p.Kind != connections.MySQL {
		return
	}
	ui.Column(c).Gap(5).Children(func() {
		if s.sessions.Current().Status != "连接中" && s.sessions.StatusFor(p.ID) != "已连接" {
			ui.TextInput(c, &s.sessions.Current().Password).Password().
				Label("本次 MySQL 会话密码").Placeholder(locales.Text("密码（留空使用已保存密码）")).Height(theme.RowHeight(c, 26))
		}
		ui.Row(c).Gap(5).Children(func() {
			if s.sessions.StatusFor(p.ID) == "连接中" {
				if ui.Button(c, "取消连接").Clicked() {
					s.disconnectMySQLSession()
					s.status = "MySQL 连接已取消"
				}
			} else if s.sessions.StatusFor(p.ID) == "已连接" {
				if ui.Button(c, "断开连接").Clicked() {
					s.disconnectMySQLSession()
					s.status = "MySQL 已断开"
				}
				if ui.Button(c, "重连").Clicked() {
					s.disconnectMySQLSession()
					s.status = "请重新输入密码后点击连接"
				}
			} else if ui.Button(c, "连接").Label("连接所选 MySQL").Clicked() {
				s.startMySQLSession(c.Services())
			}
		})
		if s.sessions.StatusFor(p.ID) == "连接失败" && s.sessions.Current().Error != "" {
			ui.Text(c, s.sessions.Current().Error).FontSize(theme.Font(c, 11)).SingleLine()
		}
	})
}

func (s *State) activateMySQLForCurrentTab() {
	id := s.currentTab().profileID()
	if id == 0 || id == s.sessions.Current().ProfileID {
		return
	}
	s.sessions.Activate(id)
	s.selectedProfileID = id
	s.navigation.Group = ""
	s.navigation.Key = navigation.ConnectionKey(id)
	s.navigation.Row = -1
}

// Clear disclosure for one disconnected profile without affecting its siblings.
func (s *State) clearMySQLDatabaseNavigation(id int64) {
	for key := range s.navigation.Tree.Open.All() {
		n := navigation.Parse(key)
		if n.ProfileID == id && n.Database != "" {
			s.navigation.Tree.Open.Remove(key)
		}
	}
}
func (s *State) sessionBrowserDependencies() sessionbrowser.Dependencies {
	return sessionbrowser.Dependencies{
		ReadSecret: func(id int64, variant string) (string, bool, error) {
			if s.credentialVault == nil {
				return "", false, nil
			}
			return s.credentialVault.Get(id, variant)
		},
		Status:    func(message string) { s.status = message },
		Connected: func(id int64) { s.navigation.Tree.Open.Add(navigation.ConnectionKey(id)) },
		BrowserReset: func(id int64) {
			s.objectWorkspace.ResetBrowser(id)
			s.clearMySQLDatabaseNavigation(id)
			if navigation.Parse(s.navigation.Key).Database != "" {
				s.navigation.Key = navigation.ConnectionKey(s.selectedProfileID)
				s.navigation.Row = -1
			}
		},
		DatabasesChanged: func(id int64, known map[string]bool) {
			for key := range s.navigation.Tree.Open.All() {
				node := navigation.Parse(key)
				if node.ProfileID == id && node.Database != "" && !known[node.Database] {
					s.navigation.Tree.Open.Remove(key)
				}
			}
			s.pruneMySQLTabsFor(id, known)
		},
		ObjectsChanged: s.mysqlObjectsChanged,
		Disconnected: func(id int64) {
			s.objectWorkspace.CancelTableFor(id)
			s.sqlWorkspace.DisconnectProfile(id)
			s.clearMySQLDatabaseNavigation(id)
			s.clearMySQLTabsFor(id)
		},
	}
}
