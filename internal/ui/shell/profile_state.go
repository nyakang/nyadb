package shell

import (
	"context"
	"errors"
	"fmt"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
)

// NewWithProfiles replaces the demo connection roots with persisted,
// non-secret profiles. Sessions and database-name metadata are available after an explicit connect.
func NewWithProfiles(service *connections.Service) *State {
	s := newState(false)
	s.profileMode = true
	s.profileService = service
	s.connectionEditor.SetProfileService(service)
	s.navigation.SetProfileService(service)
	s.navigation.Key = "我的连接"
	s.navigation.Row = -1
	s.status = "就绪 · 连接档案已加载（尚未连接数据库）"
	s.loadProfiles()
	return s
}

func (s *State) loadProfiles() {
	if s.profileService == nil {
		s.status = "连接档案存储不可用"
		return
	}
	list, err := s.profileService.List(context.Background())
	if err != nil {
		s.status = "读取连接档案失败：" + err.Error()
		return
	}
	groups, groupErr := s.profileService.ListGroups(context.Background())
	if groupErr != nil {
		groups = s.navigation.Groups()
		s.status = "读取连接分组失败：" + groupErr.Error()
	}
	if !s.navigation.SetProfiles(list, groups, s.selectedProfileID) {
		s.selectedProfileID = 0
	}
}

func (s *State) currentProfile() (connections.Profile, bool) {
	for _, p := range s.navigation.Profiles() {
		if p.ID == s.selectedProfileID {
			return p, true
		}
	}
	return connections.Profile{}, false
}

func (s *State) startEditProfile() {
	p, ok := s.currentProfile()
	if !ok {
		s.status = "请先选择连接"
		return
	}
	s.connectionDialog = true
	s.connectionStep = 1
	s.connectionKind = p.Kind
	s.connectionEditor.OpenProfile(p)
	if s.showConnectionWindow != nil {
		s.showConnectionWindow()
	}
}

func (s *State) duplicateProfile() {
	p, ok := s.currentProfile()
	if !ok {
		s.status = "请先选择连接"
		return
	}
	// Choose a fresh name instead of overwriting an existing connection.
	name := p.Name + " 副本"
	for n := 2; s.hasProfileName(name); n++ {
		name = fmt.Sprintf(locales.Text("%s 副本 %d"), p.Name, n)
	}
	created, err := s.profileService.Duplicate(context.Background(), p.ID, name)
	if err != nil {
		s.status = "复制连接失败：" + err.Error()
		return
	}
	s.selectedProfileID = created.ID
	s.loadProfiles()
	s.status = "已复制连接：" + created.Name
}

func (s *State) hasProfileName(name string) bool {
	for _, p := range s.navigation.Profiles() {
		if p.Name == name {
			return true
		}
	}
	return false
}

func (s *State) confirmDeleteProfile() {
	if s.selectedProfileID == 0 || s.profileService == nil {
		return
	}
	s.pendingDeleteID = s.selectedProfileID
	s.deleteConfirm = true
}

func (s *State) deleteProfile() {
	if s.profileService == nil || s.pendingDeleteID == 0 {
		return
	}
	id := s.pendingDeleteID
	s.pendingDeleteID = 0
	s.deleteProfileID(id)
}

func (s *State) deleteProfileID(id int64) {
	p, err := s.profileService.Get(context.Background(), id)
	if err != nil {
		s.status = "无法读取待删除连接：" + err.Error()
		return
	}
	if s.guardMySQLProfileLeave(id, func(ui.Services) { s.deleteProfileID(id) }) {
		return
	}
	err = s.profileService.Delete(context.Background(), p.ID)
	if err != nil {
		s.status = "删除连接失败：" + err.Error()
		return
	}
	// Deleting a parked connection must not disconnect the selected/active one.
	s.sessions.Disconnect(p.ID)
	tunnelErr := s.forgetTunnelProfile(p)
	if s.selectedProfileID == p.ID {
		s.selectedProfileID = 0
	}
	s.loadProfiles()
	var credentialErr error
	if (p.Kind.IsMySQLFamily() || p.Kind == connections.PostgreSQL || p.Kind == connections.ClickHouse || p.Kind == connections.SQLServer) && s.credentialVault != nil {
		credentialErr = s.credentialVault.Delete(p.ID, "")
		for _, variant := range p.Variants {
			credentialErr = errors.Join(credentialErr, s.credentialVault.Delete(p.ID, variant.Name))
		}
	}
	s.status = "已删除连接档案：" + p.Name + "（未删除数据库文件）"
	if credentialErr != nil {
		s.status = "连接已删除，但清理系统凭据失败：" + errors.Join(credentialErr, tunnelErr).Error()
	} else if tunnelErr != nil {
		s.status = "连接已删除，但清理隧道凭据失败：" + tunnelErr.Error()
	}
	_ = s.queueInitialSQLCleanup(profileInitialSQLIDs(p))
}

func (s *State) selectGroup(name string) {
	s.navigation.Group = name
	s.selectedProfileID = 0
	s.navigation.Key = navigation.GroupKey(name)
	s.navigation.Row = -1
	s.objectWorkspace.SelectRow(s.sessions.Current().ProfileID, -1)
	s.status = "已选择虚拟组：" + name
}

func (s *State) moveProfileToGroup(id int64, name string) {
	if s.profileService == nil {
		return
	}
	if err := s.profileService.MoveToGroup(context.Background(), id, name); err != nil {
		s.status = "移动连接失败：" + err.Error()
		return
	}
	s.navigation.Group = ""
	s.selectedProfileID = id
	s.loadProfiles()
	s.status = "已移动连接到指定位置"
}
