package shell

import (
	"fmt"
	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/sessionbrowser"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) startMySQLSession(services ui.Services) {
	p, ok := s.currentProfile()
	if !ok || (!p.Kind.IsMySQLFamily() && !p.Kind.IsPostgreSQLFamily() && p.Kind != connections.SQLite && p.Kind != connections.ClickHouse && p.Kind != connections.Doris && p.Kind != connections.StarRocks && p.Kind != connections.SQLServer && p.Kind != connections.Oracle && p.Kind != connections.Dameng && p.Kind != connections.Redis && p.Kind != connections.MongoDB && p.Kind != connections.DuckDB) || s.profileService == nil {
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
	s.connectionEditor.SetInitialSQLCleanup(nil)
	s.initialSQLCleanup = initialSQLCleanupState{}
	if s.workspaces.cancel != nil {
		s.workspaces.cancel()
		s.workspaces.cancel = nil
	}
	s.workspaces.out = nil
	s.workspaces.store = nil
	s.mysqlForceClose = true
	defer func() { s.mysqlForceClose = false }()
	for _, t := range s.tabs {
		s.disposeTab(t)
	}
	s.dataWorkspace.CloseAll()
	s.objectWorkspace.CloseAll()
	s.sqlWorkspace.CloseAll()
	s.favorites.CloseAll()
	s.sessions.Close()
	s.closeTunnelServices()
}
func (s *State) mysqlSessionToolbar(c *ui.Context) {
	if s.engineSessionToolbar(c) {
		return
	}
	p, ok := s.currentProfile()
	if !ok || !p.Kind.IsMySQLFamily() {
		return
	}
	passwordLabel := locales.Text("本次 MySQL 会话密码")
	connectLabel := locales.Text("连接所选 MySQL")
	if p.Kind != connections.MySQL {
		passwordLabel = fmt.Sprintf(locales.Text("本次 %s 会话密码"), connectionKindName(p.Kind))
		connectLabel = locales.Text("连接所选数据库")
	}
	ui.Column(c).Gap(5).Children(func() {
		if s.sessions.Current().Status != "连接中" && s.sessions.StatusFor(p.ID) != "已连接" {
			ui.TextInput(c, &s.sessions.Current().Password).Password().
				Label(passwordLabel).Placeholder(locales.Text("密码（留空使用已保存密码）")).Height(theme.RowHeight(c, 26))
		}
		ui.Row(c).Gap(5).Children(func() {
			if s.sessions.StatusFor(p.ID) == "连接中" {
				if ui.Button(c, locales.Text("取消连接")).Clicked() {
					s.disconnectMySQLSession()
					s.status = fmt.Sprintf(locales.Text("%s 连接已取消"), connectionKindName(p.Kind))
				}
			} else if s.sessions.StatusFor(p.ID) == "已连接" {
				if ui.Button(c, locales.Text("断开连接")).Clicked() {
					s.disconnectMySQLSession()
					s.status = fmt.Sprintf(locales.Text("%s 已断开"), connectionKindName(p.Kind))
				}
				if ui.Button(c, locales.Text("重连")).Clicked() {
					s.disconnectMySQLSession()
					s.status = "请重新输入密码后点击连接"
				}
			} else if ui.Button(c, locales.Text("连接")).Label(connectLabel).Clicked() {
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
			if node := navigation.Parse(s.navigation.Key); node.ProfileID == id && node.Database != "" {
				s.navigation.Key = navigation.ConnectionKey(id)
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
