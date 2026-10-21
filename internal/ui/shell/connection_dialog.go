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
	s.connectionDialog = true
	s.connectionStep = 0
	s.connectionEditorTab = 0
	s.connectionKind = ""
	s.connectionSelectedName = ""
	s.connectionSearch = ""
	s.connectionEngineFilters = map[string]bool{}
	s.connectionVendorFilters = map[string]bool{}
	s.connectionDraft = connections.Profile{}
	s.connectionPort = ""
	s.connectionSecret = ""
	s.clearMySQLSSLPassphrase()
	s.clearMySQLSSHSecrets()
	s.clearMySQLInitialQuery()
	s.connectionError = ""
	if s.showConnectionWindow != nil {
		s.showConnectionWindow()
	}
}

func (s *State) closeConnectionDialog() {
	s.connectionDialog = false
	s.connectionSecret = ""
	s.clearMySQLSSLPassphrase()
	s.clearMySQLSSHSecrets()
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
	s.connectionDraft = draft
	s.clearMySQLSSLPassphrase()
	s.clearMySQLInitialQuery()
	if draft.Kind == connections.MySQL {
		s.initMySQLAdvancedInputs()
		s.initMySQLSSHInputs()
	}
	s.connectionPort = strconv.Itoa(int(draft.Port))
	if draft.Kind == connections.SQLite {
		s.connectionPort = ""
	}
	s.connectionError = ""
	s.connectionStep = 1
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
	p, err := s.draftProfile()
	if err != nil {
		s.connectionError = err.Error()
		return
	}
	if s.profileService == nil {
		s.connectionError = "连接档案尚未接入本地存储"
		return
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
