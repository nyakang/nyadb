package shell

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *State) openConnectionDialog() {
	s.cancelMySQLConnectionTest()
	s.resetConnectionConfigurations()
	s.connectionDialog = true
	s.connectionNameFocusPending = false
	s.connectionStep = 0
	s.connectionEditorTab = 0
	s.connectionKind = ""
	s.connectionSelectedName = ""
	s.connectionSearch = ""
	s.connectionEngineFilters = map[string]bool{}
	s.connectionVendorFilters = map[string]bool{}
	group := s.selectedGroup
	if group == "" {
		if selected, ok := s.currentProfile(); ok {
			group = selected.GroupName
		}
	}
	s.connectionDraft = connections.Profile{GroupName: group}
	s.connectionPort = ""
	s.connectionSecret = ""
	s.connectionSavePassword = false
	s.clearMySQLSSLPassphrase()
	s.clearMySQLSSHSecrets()
	s.clearMySQLHTTPSecrets()
	s.clearMySQLInitialQuery()
	s.connectionError = ""
	if s.showConnectionWindow != nil {
		s.showConnectionWindow()
	}
}

func (s *State) closeConnectionDialog() {
	s.cancelMySQLConnectionTest()
	s.resetConnectionConfigurations()
	s.connectionDialog = false
	s.connectionSecret = ""
	s.connectionSavePassword = false
	s.clearMySQLSSLPassphrase()
	s.clearMySQLSSHSecrets()
	s.clearMySQLHTTPSecrets()
	s.clearMySQLInitialQuery()
	s.connectionError = ""
	if s.hideConnectionWindow != nil {
		s.hideConnectionWindow()
	}
}

func (s *State) chooseConnectionKind(kind connections.Kind) {
	s.connectionKind = kind
	s.connectionError = ""
}

func (s *State) nextConnectionStep() {
	s.cancelMySQLConnectionTest()
	s.resetConnectionConfigurations()
	if s.connectionKind == "" {
		if !strings.Contains(s.connectionError, "尚未支持") {
			s.connectionError = "请先选择数据库类型"
		}
		return
	}
	draft, err := connections.Defaults(s.connectionKind)
	if err != nil {
		s.connectionError = err.Error()
		return
	}
	draft.GroupName = s.connectionDraft.GroupName
	s.connectionDraft = draft
	s.clearMySQLSSLPassphrase()
	s.clearMySQLInitialQuery()
	if draft.Kind == connections.MySQL {
		// Observed Navicat default: Save Password is preselected for a
		// new MySQL connection. Only offer it with a configured OS vault.
		s.connectionSavePassword = s.credentialVault != nil
		s.initMySQLAdvancedInputs()
		s.initMySQLSSHInputs()
		s.initMySQLHTTPInputs()
	}
	s.connectionPort = strconv.Itoa(int(draft.Port))
	if draft.Kind == connections.SQLite {
		s.connectionPort = ""
	}
	s.connectionError = ""
	s.connectionStep = 1
	// A selection must not insert a new section and move the card beneath
	// the pointer. Record actual use only once Next opens its editor.
	recent := s.connectionSelectedName
	if recent == "" {
		recent = connectionKindName(s.connectionKind)
	}
	s.recordRecentConnection(recent)
	s.connectionNameFocusPending = true
	s.connectionEditorTab = 0
	s.updateConnectionWindowTitle()
}

func (s *State) draftProfile() (connections.Profile, error) {
	p := s.connectionDraft
	if err := s.advanceDraft(&p); err != nil {
		return p, err
	}
	if err := s.advanceMySQLSSH(&p); err != nil {
		return p, err
	}
	if err := s.advanceMySQLHTTP(&p); err != nil {
		return p, err
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return p, fmt.Errorf("请输入连接名称")
	}
	if p.Kind == connections.SQLite {
		if strings.TrimSpace(p.Database) == "" {
			return p, fmt.Errorf("请选择 SQLite 数据库文件")
		}
		return p, nil
	}
	port, err := strconv.ParseUint(strings.TrimSpace(s.connectionPort), 10, 16)
	if err != nil || port == 0 {
		return p, fmt.Errorf("端口必须是 1–65535 之间的数字")
	}
	p.Port = uint16(port)
	if strings.TrimSpace(p.Host) == "" {
		return p, fmt.Errorf("请输入数据库主机")
	}
	return p, nil
}

func (s *State) saveConnectionDraft() {
	var p connections.Profile
	var err error
	if s.connectionDraft.ID != 0 && s.connectionDraft.Kind == connections.MySQL {
		p, err = s.prepareConnectionConfigurations()
	} else {
		p, err = s.draftProfile()
	}
	if err != nil {
		s.connectionError = err.Error()
		return
	}
	if s.profileService == nil {
		s.connectionError = "连接档案尚未接入本地存储"
		return
	}
	var before connections.Profile
	if p.ID > 0 {
		before, err = s.profileService.Get(context.Background(), p.ID)
		if err != nil {
			s.connectionError = "读取原连接失败：" + err.Error()
			return
		}
	}
	var saved connections.Profile
	if p.ID > 0 {
		saved, err = s.profileService.Update(context.Background(), p)
	} else {
		saved, err = s.profileService.Create(context.Background(), p)
	}
	if err != nil {
		s.connectionError = "保存失败（连接名称可能重复或配置无效）：" + err.Error()
		return
	}
	if saved.Kind == connections.MySQL {
		if err := s.persistConnectionPasswords(saved, before); err != nil {
			if p.ID == 0 {
				if rollbackErr := s.profileService.Delete(context.Background(), saved.ID); rollbackErr != nil {
					err = fmt.Errorf("%w；回滚新连接失败：%v", err, rollbackErr)
				}
			} else if _, rollbackErr := s.profileService.Update(context.Background(), before); rollbackErr != nil {
				err = fmt.Errorf("%w；回滚连接配置失败：%v", err, rollbackErr)
			}
			s.connectionError = "系统密钥库保存密码失败（连接配置已尝试回滚）：" + err.Error()
			return
		}
	}
	if s.mysqlSessionProfileID == saved.ID {
		s.disconnectMySQLSession()
	}
	s.selectedProfileID = saved.ID
	s.loadProfiles()
	s.closeConnectionDialog()
	s.status = "已保存连接：" + saved.Name + "（未建立数据库会话）"
}

// connectionModal is a native MyGo modal, not a WebView or imported UI asset.
func (s *State) connectionModal(c *ui.Context) {
	if !s.connectionDialog || s.showConnectionWindow != nil {
		return
	}
	ui.Modal(c, &s.connectionDialog, func() {
		if s.connectionStep == 0 {
			s.connectionTypes(c, false)
			return
		}
		s.connectionEditor(c)
	})
}

func connectionKindName(kind connections.Kind) string {
	switch kind {
	case connections.MySQL:
		return "MySQL"
	case connections.PostgreSQL:
		return "PostgreSQL"
	case connections.SQLite:
		return "SQLite"
	default:
		return "未知类型"
	}
}
