package shell

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// The main config and alternatives are edited as raw per-window drafts. No
// SQLite writes occur when changing the selected configuration or closing the
// editor. Unsaved partial numbers survive selection changes.
type connectionEditorSnapshot struct {
	profile       connections.Profile
	name          string // alternative name; empty on main
	port          string
	numbers       [5]string
	sshPort       string
	httpProxyPort string
}

func (s *State) currentConnectionSnapshot() connectionEditorSnapshot {
	return connectionEditorSnapshot{
		profile: s.connectionDraft, port: s.connectionPort,
		numbers: s.mysqlAdvancedNumbers, sshPort: s.mysqlSSHPort,
		httpProxyPort: s.mysqlHTTPProxyPort,
	}
}

func (s *State) resetConnectionConfigurations() {
	s.connectionProfilesVisible = false
	s.connectionActiveConfig = 0
	s.connectionActiveTarget = 0
	s.connectionMainConfig = connectionEditorSnapshot{}
	s.connectionOtherConfigs = nil
}

func (s *State) initializeConnectionConfigurations() {
	s.resetConnectionConfigurations()
	if s.connectionDraft.ID == 0 || s.connectionDraft.Kind != connections.MySQL {
		return
	}
	s.connectionProfilesVisible = true
	s.connectionActiveTarget = 0
	s.connectionMainConfig = s.currentConnectionSnapshot()
	parent := s.connectionDraft
	for _, v := range parent.Variants {
		snap := connectionEditorSnapshot{
			name: v.Name,
			profile: connections.Profile{
				ID: parent.ID, Name: parent.Name, Kind: parent.Kind,
				Host: v.Host, Port: v.Port, Database: v.Database,
				Username: v.Username, TLSMode: v.TLSMode,
				MySQLAdvanced: v.MySQLAdvanced, Notes: parent.Notes,
			},
			port:          strconv.Itoa(int(v.Port)),
			sshPort:       strconv.Itoa(v.MySQLAdvanced.SSH.Port),
			httpProxyPort: strconv.Itoa(v.MySQLAdvanced.HTTP.ProxyPort),
		}
		for i, n := range []int{
			v.MySQLAdvanced.KeepAliveSeconds,
			v.MySQLAdvanced.ConnectTimeoutSeconds,
			v.MySQLAdvanced.ReadTimeoutSeconds,
			v.MySQLAdvanced.WriteTimeoutSeconds,
			v.MySQLAdvanced.MaxSessions,
		} {
			snap.numbers[i] = strconv.Itoa(n)
		}
		s.connectionOtherConfigs = append(s.connectionOtherConfigs, snap)
		if v.Name == parent.ActiveVariant {
			s.connectionActiveTarget = len(s.connectionOtherConfigs)
		}
	}
	// On reopen Navicat highlights the saved Active configuration rather than
	// always resetting the selected row to the primary configuration.
	if s.connectionActiveTarget > 0 {
		s.connectionActiveConfig = s.connectionActiveTarget
		s.restoreConnectionConfiguration(s.connectionOtherConfigs[s.connectionActiveTarget-1])
	}
}

func (s *State) captureConnectionConfiguration() {
	if s.connectionDraft.ID == 0 || s.connectionDraft.Kind != connections.MySQL {
		return
	}
	snap := s.currentConnectionSnapshot()
	if s.connectionActiveConfig == 0 {
		s.connectionMainConfig = snap
	} else if i := s.connectionActiveConfig - 1; i >= 0 && i < len(s.connectionOtherConfigs) {
		snap.name = s.connectionOtherConfigs[i].name
		s.connectionOtherConfigs[i] = snap
		// Notes are shared across the connection's configurations.
		s.connectionMainConfig.profile.Notes = snap.profile.Notes
	}
}

func (s *State) restoreConnectionConfiguration(snap connectionEditorSnapshot) {
	s.connectionDraft = snap.profile
	s.connectionPort = snap.port
	s.mysqlAdvancedNumbers = snap.numbers
	s.mysqlSSHPort = snap.sshPort
	s.mysqlHTTPProxyPort = snap.httpProxyPort
	if s.mysqlCompatibilityVisible && s.connectionEditorTab == 6 {
		// The transient compatibility subpage is not a common main tab.
		s.connectionEditorTab = 1
	}
	s.mysqlCompatibilityVisible = false
	s.mysqlHTTPSubtab = 0
	s.connectionSecret = ""
	s.clearMySQLSSLPassphrase()
	s.clearMySQLSSHSecrets()
	s.clearMySQLHTTPSecrets()
	s.clearMySQLInitialQuery()
	s.connectionError = ""
}

func (s *State) selectConnectionConfiguration(index int) {
	if index < 0 || index > len(s.connectionOtherConfigs) ||
		index == s.connectionActiveConfig || s.connectionDraft.ID == 0 {
		return
	}
	s.captureConnectionConfiguration()
	s.connectionActiveConfig = index
	if index == 0 {
		s.restoreConnectionConfiguration(s.connectionMainConfig)
	} else {
		snap := s.connectionOtherConfigs[index-1]
		snap.profile.Name = s.connectionMainConfig.profile.Name
		snap.profile.Notes = s.connectionMainConfig.profile.Notes
		s.restoreConnectionConfiguration(snap)
	}
}

// The highlighted editor row and the Active connection target are independent.
// Marking Active remains a draft until the parent dialog is confirmed.
func (s *State) markConnectionConfigurationActive(index int) {
	if s.connectionDraft.ID == 0 || index < 0 || index > len(s.connectionOtherConfigs) {
		return
	}
	s.selectConnectionConfiguration(index)
	s.connectionActiveTarget = index
}

func (s *State) addConnectionConfiguration() {
	if s.connectionDraft.Kind != connections.MySQL || s.connectionDraft.ID == 0 ||
		len(s.connectionOtherConfigs) >= 16 {
		return
	}
	s.captureConnectionConfiguration()
	name := "New Profile"
	for i := 2; ; i++ {
		found := false
		for _, config := range s.connectionOtherConfigs {
			if strings.EqualFold(config.name, name) {
				found = true
				break
			}
		}
		if !found {
			break
		}
		name = fmt.Sprintf("New Profile %d", i)
	}
	// A new configuration starts with the selected connection settings, just
	// like a draft copy. It remains local until Confirm succeeds.
	snap := s.currentConnectionSnapshot()
	snap.name = name
	snap.profile.Name = s.connectionMainConfig.profile.Name
	snap.profile.Notes = s.connectionMainConfig.profile.Notes
	s.connectionOtherConfigs = append(s.connectionOtherConfigs, snap)
	s.connectionActiveConfig = len(s.connectionOtherConfigs)
	s.restoreConnectionConfiguration(snap)
}

// Duplicate and delete affect only the editor draft. No persisted setting is
// changed until the window's Confirm action succeeds.
func (s *State) duplicateConnectionConfiguration(index int) {
	if index <= 0 || index > len(s.connectionOtherConfigs) || len(s.connectionOtherConfigs) >= 16 {
		return
	}
	s.captureConnectionConfiguration()
	source := s.connectionOtherConfigs[index-1]
	base := source.name + " 副本"
	name := base
	for n := 2; ; n++ {
		found := false
		for _, variant := range s.connectionOtherConfigs {
			if strings.EqualFold(variant.name, name) {
				found = true
				break
			}
		}
		if !found {
			break
		}
		name = fmt.Sprintf("%s %d", base, n)
	}
	source.name = name
	s.connectionOtherConfigs = append(s.connectionOtherConfigs, source)
	s.connectionActiveConfig = len(s.connectionOtherConfigs)
	s.restoreConnectionConfiguration(source)
}

func (s *State) removeConnectionConfiguration(index int) {
	if index <= 0 || index > len(s.connectionOtherConfigs) {
		return
	}
	s.captureConnectionConfiguration()
	s.connectionOtherConfigs = append(
		s.connectionOtherConfigs[:index-1], s.connectionOtherConfigs[index:]...)
	if s.connectionActiveConfig == index {
		s.connectionActiveConfig = 0
		s.restoreConnectionConfiguration(s.connectionMainConfig)
	} else if s.connectionActiveConfig > index {
		s.connectionActiveConfig--
	}
	if s.connectionActiveTarget == index {
		s.connectionActiveTarget = 0
	} else if s.connectionActiveTarget > index {
		s.connectionActiveTarget--
	}
}

func (s *State) prepareConnectionConfigurations() (connections.Profile, error) {
	selected := s.connectionActiveConfig
	s.captureConnectionConfiguration()
	original := s.currentConnectionSnapshot()
	restore := func() {
		if selected == 0 {
			s.restoreConnectionConfiguration(s.connectionMainConfig)
		} else if selected-1 < len(s.connectionOtherConfigs) {
			s.restoreConnectionConfiguration(s.connectionOtherConfigs[selected-1])
		} else {
			s.restoreConnectionConfiguration(original)
		}
		s.connectionActiveConfig = selected
	}
	defer restore()

	s.restoreConnectionConfiguration(s.connectionMainConfig)
	main, err := s.draftProfile()
	if err != nil {
		s.connectionActiveConfig = 0
		return main, fmt.Errorf("主要配置文件：%w", err)
	}
	main.Variants = nil
	main.ActiveVariant = ""
	if s.connectionActiveTarget > 0 && s.connectionActiveTarget <= len(s.connectionOtherConfigs) {
		main.ActiveVariant = s.connectionOtherConfigs[s.connectionActiveTarget-1].name
	}
	for i, v := range s.connectionOtherConfigs {
		if strings.TrimSpace(v.name) == "" {
			return main, fmt.Errorf("配置文件 %d 的名称不能为空", i+1)
		}
		s.restoreConnectionConfiguration(v)
		// A variant has the same top-level name / ID; its own name is
		// deliberately independent and never becomes a new root profile.
		s.connectionDraft.Name = main.Name
		checked, err := s.draftProfile()
		if err != nil {
			return main, fmt.Errorf("配置文件 %s：%w", v.name, err)
		}
		main.Variants = append(main.Variants, connections.ConnectionVariant{
			Name: v.name, Host: checked.Host, Port: checked.Port,
			Database: checked.Database, Username: checked.Username,
			TLSMode: checked.TLSMode, MySQLAdvanced: checked.MySQLAdvanced,
		})
	}
	return main, nil
}

func (s *State) connectionConfigurationsSidebar(c *ui.Context, height float32) {
	ui.Column(c).Width(258).Height(height).Gap(0).
		Background(theme.Sidebar).BorderWidth(0, 1, 0, 0).
		BorderColor(connectionEditorBorder).Children(func() {
		ui.Row(c).Height(38).Padding(7, 10).AlignItems(ui.Center).Gap(5).
			Background(theme.Header).Children(func() {
			ui.Text(c, "连接配置文件").FontSize(12).TextColor(connectionFieldMuted)
			ui.Box(c).Grow(1)
			if ui.Button(c, "+").Width(22).Height(22).Padding(0, 0).
				Label("添加连接配置文件").
				Disabled(len(s.connectionOtherConfigs) >= 16).Clicked() {
				s.addConnectionConfiguration()
			}
		})
		ui.Column(c).Grow(1).Gap(0).Children(func() {
			s.connectionConfigRow(c, 0, "主要配置文件")
			for i, snap := range s.connectionOtherConfigs {
				s.connectionConfigRow(c, i+1, snap.name)
			}
		})
	})
}

func (s *State) connectionConfigRow(c *ui.Context, index int, name string) {
	ui.Row(c).Height(25).Gap(0).Children(func() {
		if index == s.connectionActiveTarget {
			name += " (Active)"
		}
		selected := index == s.connectionActiveConfig
		bg := theme.Sidebar
		if selected {
			bg = theme.Accent
		}
		row := ui.Button(c, name).Label("切换配置文件："+name).
			Width(257).Height(25).Padding(4, 9).
			Background(bg).Border(0, bg).Justify(ui.Start)
		row.ContextMenu(func(m *ui.Menu) {
			if index != s.connectionActiveTarget {
				if m.Item("设置为活动配置文件").Chosen() {
					s.markConnectionConfigurationActive(index)
				}
			}
			if index == 0 {
				return
			}
			if m.Item("复制配置文件").Chosen() {
				s.duplicateConnectionConfiguration(index)
			}
			if m.Item("删除配置文件").Chosen() {
				s.removeConnectionConfiguration(index)
			}
			if m.Item("重命名配置文件").Chosen() {
				s.selectConnectionConfiguration(index)
			}
		})
		// Native focused row: arrow keys move the editing selection, never
		// the saved Active target. Enter/Space retain MyGo button behavior.
		if row.Shortcut(0, ui.KeyDown) {
			s.selectConnectionConfiguration(min(index+1, len(s.connectionOtherConfigs)))
		}
		if row.Shortcut(0, ui.KeyUp) {
			s.selectConnectionConfiguration(max(index-1, 0))
		}
		if row.Clicked() {
			s.selectConnectionConfiguration(index)
		}
	})
}

func (s *State) connectionConfigurationCaption(c *ui.Context) {
	if s.connectionDraft.ID == 0 || s.connectionDraft.Kind != connections.MySQL ||
		!s.connectionProfilesVisible || s.connectionActiveConfig == 0 {
		return
	}
	index := s.connectionActiveConfig - 1
	if index < 0 || index >= len(s.connectionOtherConfigs) {
		return
	}
	ui.Row(c).Height(24).Padding(2, 15).AlignItems(ui.Center).Gap(9).Children(func() {
		ui.Text(c, "配置文件名称：").FontSize(11).TextColor(connectionFieldMuted)
		ui.TextInput(c, &s.connectionOtherConfigs[index].name).Label("配置文件名称").
			Width(210).Height(21).Padding(2, 6)
	})
}
