package shell

import (
	"context"
	"fmt"
	"strconv"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// NewWithProfiles replaces the demo connection roots with persisted,
// non-secret profiles. Actual sessions and metadata remain unimplemented.
func NewWithProfiles(service *connections.Service) *State {
	s := New()
	s.profileMode = true
	s.profileService = service
	s.explorerName = "我的连接"
	s.explorerRow = -1
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
	s.profileEntries = list
	s.explorerRow = -1
	s.explorerName = "我的连接"
	for _, p := range list {
		if p.ID == s.selectedProfileID {
			s.explorerName = p.Name
			break
		}
	}
	if s.explorerName == "我的连接" {
		s.selectedProfileID = 0
	}
}

func (s *State) currentProfile() (connections.Profile, bool) {
	for _, p := range s.profileEntries {
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
	s.connectionEditorTab = 0
	s.connectionKind = p.Kind
	s.connectionDraft = p
	if p.Kind == connections.MySQL {
		s.initMySQLAdvancedInputs()
	}
	s.connectionPort = strconv.Itoa(int(p.Port))
	s.connectionSecret = ""
	s.connectionError = ""
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
		name = fmt.Sprintf("%s 副本 %d", p.Name, n)
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
	for _, p := range s.profileEntries {
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
	p, err := s.profileService.Get(context.Background(), s.pendingDeleteID)
	if err != nil {
		s.status = "无法读取待删除连接：" + err.Error()
		s.pendingDeleteID = 0
		return
	}
	err = s.profileService.Delete(context.Background(), p.ID)
	s.pendingDeleteID = 0
	if err != nil {
		s.status = "删除连接失败：" + err.Error()
		return
	}
	if s.selectedProfileID == p.ID {
		s.selectedProfileID = 0
	}
	s.loadProfiles()
	s.status = "已删除连接档案：" + p.Name + "（未删除数据库文件）"
}
