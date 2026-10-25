package shell

import (
	"context"
	"strings"

	"github.com/nyakang/nyadb/internal/services/connections"
)

const groupNodePrefix = "\x1fgroup:"

func groupNode(name string) string     { return groupNodePrefix + name }
func isGroupNode(node string) bool     { return strings.HasPrefix(node, groupNodePrefix) }
func groupFromNode(node string) string { return strings.TrimPrefix(node, groupNodePrefix) }

func (s *State) profileRoots() []string {
	query := strings.ToLower(strings.TrimSpace(s.search))
	roots := make([]string, 0, len(s.profileGroups)+len(s.profileEntries))
	for _, group := range s.profileGroups {
		if s.groupMatches(group.Name, query) {
			roots = append(roots, groupNode(group.Name))
		}
	}
	for _, p := range s.profileEntries {
		if p.GroupName == "" && s.profileVisible(p) {
			roots = append(roots, profileNode(p.ID))
		}
	}
	return roots
}

func (s *State) groupMatches(name, query string) bool {
	if !s.profileStarredOnly && s.profileEngine == "" && (query == "" || strings.Contains(strings.ToLower(name), query)) {
		return true
	}
	for _, p := range s.profileEntries {
		if strings.EqualFold(p.GroupName, name) && s.profileVisible(p) {
			return true
		}
	}
	return false
}

func (s *State) profileChildren(node string) []string {
	if !isGroupNode(node) {
		return s.navigationChildren(node)
	}
	group := groupFromNode(node)
	names := []string{}
	for _, p := range s.profileEntries {
		if strings.EqualFold(p.GroupName, group) && s.profileVisible(p) {
			names = append(names, profileNode(p.ID))
		}
	}
	return names
}

func (s *State) groupCount(name string) int {
	count := 0
	for _, p := range s.profileEntries {
		if strings.EqualFold(p.GroupName, name) {
			count++
		}
	}
	return count
}

func (s *State) selectGroup(name string) {
	s.selectedGroup = name
	s.selectedProfileID = 0
	s.explorerName = groupNode(name)
	s.explorerRow = -1
	s.objectRow = -1
	s.status = "已选择虚拟组：" + name
}

func (s *State) beginGroupEdit(name string) {
	s.groupEditOriginal = name
	s.groupDraft = name
	s.groupError = ""
	s.groupEditing = true
}

func (s *State) saveGroup() {
	if s.profileService == nil {
		return
	}
	name := strings.TrimSpace(s.groupDraft)
	var err error
	if s.groupEditOriginal == "" {
		err = s.profileService.CreateGroup(context.Background(), name)
	} else {
		err = s.profileService.RenameGroup(context.Background(), s.groupEditOriginal, name)
	}
	if err != nil {
		s.groupError = err.Error()
		return
	}
	s.groupEditing = false
	s.groupEditOriginal = ""
	s.selectedGroup = name
	s.loadProfiles()
	s.selectGroup(name)
	s.status = "已保存连接组：" + name
}

func (s *State) deleteGroup() {
	if s.profileService == nil || s.groupPendingDelete == "" {
		return
	}
	name := s.groupPendingDelete
	s.groupPendingDelete = ""
	if err := s.profileService.DeleteGroup(context.Background(), name); err != nil {
		s.status = "删除连接组失败：" + err.Error()
		return
	}
	if s.selectedGroup == name {
		s.selectedGroup = ""
	}
	s.loadProfiles()
	s.status = "已删除虚拟组：" + name + "（连接档案已移至根目录）"
}

func (s *State) moveProfileToGroup(id int64, name string) {
	if s.profileService == nil {
		return
	}
	if err := s.profileService.MoveToGroup(context.Background(), id, name); err != nil {
		s.status = "移动连接失败：" + err.Error()
		return
	}
	s.selectedGroup = ""
	s.selectedProfileID = id
	s.loadProfiles()
	s.status = "已移动连接到指定位置"
}

// Compile-time reminder that the view consumes persistent groups rather than
// treating server database names or connection profiles as virtual folders.
var _ = connections.Group{}

// outlineRowForSelection resolves an identity before the first draw. Setting
// List.Selected only after Outline has drawn produces a missing highlight until
// the next interaction, which is visible in headless first-frame captures.
func (s *State) outlineRowForSelection(roots []string) int {
	row, selected := 0, -1
	var visit func([]string)
	visit = func(keys []string) {
		for _, key := range keys {
			if key == s.explorerName {
				selected = row
			}
			row++
			if s.explorerTree.Open.Has(key) {
				visit(s.profileChildren(key))
			}
		}
	}
	visit(roots)
	return selected
}
