// Package navigation owns the connection navigator's tree, filtering and group
// editor. Session reads and workbench actions are supplied by its host.
package navigation

import (
	"iter"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// Object carries only the identity needed by the tree.
type Object struct{ Name, Kind string }

type Filter struct {
	Search      string
	Engine      connections.Kind
	StarredOnly bool
}

type GroupEdit struct {
	Active                 bool
	Original, Draft, Error string
}

type Shortcut struct {
	Modifiers ui.Modifiers
	Key       ui.Key
}

// Dependencies supplies read-only session metadata and explicit UI actions.
// Callbacks run on the UI thread; the navigator never starts background jobs.
type Dependencies struct {
	Queries        func(int64, string) []Object
	Catalog        func(int64) string
	Engine         func(int64) connections.Kind
	SessionStatus  func(int64) string
	Databases      func(int64) []string
	DatabaseOpened func(int64, string) bool
	Objects        func(int64, string) (iter.Seq[Object], bool)
	LoadState      func(int64, string) (bool, string)
	Select         func(ui.Services, string)
	Submit         func(ui.Services, Node)
	OpenDatabase   func(ui.Services, Node, bool)
	ContextMenu    func(ui.Services, string, *ui.Menu)
	SettingsMenu   func(*ui.Menu)
	Shortcut       func(ui.Services, Node, Shortcut)
	SyncExpansion  func(ui.Services)
	CanNavigate    func() bool
	NavigateActive func(ui.Services)
	Collapsed      func()
	GroupSelected  func(string)
	Changed        func()
	Status         func(string)
}

// State and its MyGo fields are owned by the UI thread. Tree, Row and Key are
// shared with the host to reveal objects after asynchronous metadata reads.
// Profiles and Groups contain non-secret configuration snapshots.
type State struct {
	Tree               ui.OutlineState[string]
	selection          ui.Selection[string]
	Row                int
	Key                string
	menuBuilt          bool
	profiles           []connections.Profile
	groups             []connections.Group
	Group              string
	Filter             Filter
	GroupEdit          GroupEdit
	GroupDeleteConfirm bool
	GroupPendingDelete string
	savedFilter        *Filter
	service            *connections.Service
	deps               Dependencies
}

func New(deps Dependencies) *State {
	s := &State{deps: deps, Row: -1, Key: "我的连接"}
	s.Tree.List.Selected = &s.Row
	return s
}

func (s *State) SetProfileService(service *connections.Service) { s.service = service }

func (s *State) report(message string) {
	if s.deps.Status != nil {
		s.deps.Status(message)
	}
}

func (s *State) sessionStatus(id int64) string {
	if s.deps.SessionStatus != nil {
		return s.deps.SessionStatus(id)
	}
	return "未连接"
}

func (s *State) databases(id int64) []string {
	if s.deps.Databases != nil {
		return s.deps.Databases(id)
	}
	return nil
}

func (s *State) databaseOpened(id int64, database string) bool {
	return s.deps.DatabaseOpened != nil && s.deps.DatabaseOpened(id, database)
}

func (s *State) objects(id int64, database string) (iter.Seq[Object], bool) {
	if s.deps.Objects != nil {
		return s.deps.Objects(id, database)
	}
	return nil, false
}

func (s *State) objectList(id int64, database string) iter.Seq[Object] {
	objects, _ := s.objects(id, database)
	if objects == nil {
		return func(func(Object) bool) {}
	}
	return objects
}

func (s *State) loadState(id int64, database string) (bool, string) {
	if s.deps.LoadState != nil {
		return s.deps.LoadState(id, database)
	}
	return false, ""
}

func (s *State) selectNode(services ui.Services, key string) {
	if s.deps.Select != nil {
		s.deps.Select(services, key)
		return
	}
	s.Key, s.Row = key, -1
	if IsGroup(key) {
		s.Group = GroupName(key)
	} else {
		s.Group = ""
	}
}

func (s *State) selectGroup(name string) {
	s.Group, s.Key, s.Row = name, GroupKey(name), -1
	if s.deps.GroupSelected != nil {
		s.deps.GroupSelected(name)
	}
}

func (s *State) openDatabase(services ui.Services, node Node, force bool) {
	if s.deps.OpenDatabase != nil {
		s.deps.OpenDatabase(services, node, force)
	}
}

func (s *State) contextMenu(services ui.Services, key string, menu *ui.Menu) {
	n := Parse(key)
	if IsGroup(key) || n.Kind == "connection" || n.Kind == "database" || n.Kind == "category" || n.Kind == "object" {
		s.menuBuilt = true
	}
	if s.deps.ContextMenu != nil {
		s.deps.ContextMenu(services, key, menu)
	}
}

func (s *State) settingsMenu(menu *ui.Menu) {
	if s.deps.SettingsMenu != nil {
		s.deps.SettingsMenu(menu)
	}
}

func (s *State) Collapse() {
	s.Tree.Open.Clear()
	s.Row = -1
	if s.deps.Collapsed != nil {
		s.deps.Collapsed()
	}
	s.report("连接树已全部折叠")
}

// Profiles and Groups return read-only snapshots. Refresh through SetProfiles.
func (s *State) Profiles() []connections.Profile { return s.profiles }
func (s *State) Groups() []connections.Group     { return s.groups }

// SetProfiles reconciles tree selection after a refresh or rename. The host's
// active configuration remains distinct from the currently selected tree node.
// The result tells the host whether an active profile or group still exists.
func (s *State) SetProfiles(profiles []connections.Profile, groups []connections.Group, activeProfileID int64) bool {
	selected := Parse(s.Key)
	s.profiles, s.groups = profiles, groups
	s.Row, s.Key = -1, "我的连接"
	groupExists := false
	for _, group := range groups {
		if group.Name == s.Group {
			groupExists = true
			break
		}
	}
	if !groupExists {
		s.Group = ""
	}
	if s.Group != "" {
		s.Key = GroupKey(s.Group)
	}
	for _, profile := range profiles {
		if profile.ID != activeProfileID {
			continue
		}
		s.Key = ConnectionKey(profile.ID)
		if selected.ProfileID == profile.ID && selected.Database != "" && s.sessionStatus(profile.ID) == "已连接" {
			s.Key = selected.Key()
		}
		if profile.GroupName != "" {
			s.Tree.Open.Add(GroupKey(profile.GroupName))
		}
		break
	}
	return s.Key != "我的连接"
}
