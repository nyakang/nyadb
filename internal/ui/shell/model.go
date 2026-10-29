package shell

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/connectioncatalog"
	"github.com/nyakang/nyadb/internal/ui/connectioneditor"
	"github.com/nyakang/nyadb/internal/ui/dataworkspace"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
	"github.com/nyakang/nyadb/internal/ui/sessionbrowser"
	"github.com/nyakang/nyadb/internal/ui/sqlworkspace"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

type tabKind uint8

const (
	tabObjects tabKind = iota
	tabSQL
	tabData
	tabStructure
	tabMySQLData
	tabSettings
	tabProgram
	tabConnections
)

type tab struct {
	id    uint64
	title string
	kind  tabKind
	query *sqlworkspace.Document

	object *objectworkspace.Document

	data *dataworkspace.Document
}

// State is UI state owned by the main thread. Never retain ui.Element values.
type State struct {
	navigateDatabase, navigateTable string
	navigateProfileID               int64
	sidebarPercent                  float32
	centerPercent                   float32
	inspectorOn                     bool

	preview  *previewState
	settings settingsState

	connectionManagerRow  int
	connectionManagerList ui.ListState
	tabs                  []*tab
	activeTab             int
	nextTabID             uint64
	status                string
	lastAction            string

	connectionEditor     *connectioneditor.State
	connectionCatalog    *connectioncatalog.State
	connectionDialog     bool
	connectionStep       int
	connectionGroup      string
	connectionError      string
	credentialVault      credentials.Vault
	connectionKind       connections.Kind
	setConnectionTitle   func(string)
	showConnectionWindow func()
	hideConnectionWindow func()

	mysqlForceClose bool

	dataWorkspace   *dataworkspace.State
	objectWorkspace *objectworkspace.State
	sqlWorkspace    *sqlworkspace.State
	sessions        *sessionbrowser.State
	navigation      *navigation.State

	profileMode       bool
	profileService    *connections.Service
	selectedProfileID int64
	pendingDeleteID   int64
	deleteConfirm     bool
	appearanceChanged func(theme.Mode)
}

func New() *State { return newState(true) }
func newState(preview bool) *State {

	s := &State{
		connectionCatalog: connectioncatalog.New(),
		sidebarPercent:    100 * (theme.SidebarWidth + theme.SplitterBorder) / 1279,
		centerPercent:     100 * (1278 - theme.SidebarWidth - theme.SplitterBorder - theme.InspectorWidth) / (1278 - theme.SidebarWidth - theme.SplitterBorder),
		inspectorOn:       true,
		status:            "就绪 · UI 布局预览（没有连接真实数据库）",

		nextTabID: 1,
		tabs:      []*tab{{id: 1, title: "对象", kind: tabObjects}},
	}
	if preview {
		s.preview = newPreview()
	}
	s.dataWorkspace = dataworkspace.New(s.dataWorkspaceDependencies())
	s.objectWorkspace = objectworkspace.New(s.objectWorkspaceDependencies())
	s.sqlWorkspace = sqlworkspace.New(s.sqlWorkspaceDependencies())
	s.sessions = sessionbrowser.New(s.sessionBrowserDependencies())
	s.navigation = navigation.New(s.navigationDependencies())
	s.connectionEditor = connectioneditor.New(connectioneditor.Dependencies{
		ValidateMySQL: func(settings connections.ActiveSettings, secret string) error {
			_, err := mysqlsvc.DirectMySQLConfig(settings, secret)
			return err
		},
		TestMySQL: mysqlsvc.TestDirectMySQLConnection,
		Saved:     s.connectionSaved, Back: s.backToConnectionCatalog, Cancel: s.closeConnectionDialog,
	})
	return s
}

func (s *State) currentTab() *tab {
	if s.activeTab < 0 || s.activeTab >= len(s.tabs) {
		s.activeTab = 0
	}
	return s.tabs[s.activeTab]
}

func (s *State) openQuery() {
	s.nextTabID++
	initial := "-- NyaDB UI 预览：SQL 尚不会发送到数据库\nSELECT * FROM customers LIMIT 100;\n"
	profileID := int64(0)
	database := ""
	if s.profileMode {
		initial = "-- 输入 SQL；点击运行后发送至当前连接\n"
		if p, ok := s.currentProfile(); ok && p.Kind == connections.MySQL {
			profileID = p.ID
			if s.sessions.StatusFor(profileID) == "已连接" {
				database = s.sessions.Current().Database
			}
		}
	}
	t := &tab{id: s.nextTabID, title: fmt.Sprintf("SQL %d", s.nextTabID-1), kind: tabSQL}
	t.query = s.sqlWorkspace.Open(t.id, initial, profileID, database, !s.profileMode)
	s.tabs = append(s.tabs, t)
	s.activeTab = len(s.tabs) - 1
	s.status = "已打开 " + t.title
	s.sqlWorkspace.Prepare(t.query)
	if !s.profileMode {
		s.status += "（模拟工作区）"
	}
}

func (s *State) closeActive() {
	s.closeTab(s.activeTab)
}

// closeTab uses an index from the current frame. Closing an inactive tab
// must keep the active document selected by identity, not by stale position.
func (s *State) closeTab(index int) {
	if index <= 0 || index >= len(s.tabs) {
		return // the Objects tab is permanent
	}
	if s.tabs[index].kind == tabMySQLData {
		t := s.tabs[index]
		if s.guardMySQLDataLeave(t, func(_ ui.Services) {
			for i, candidate := range s.tabs {
				if candidate == t {
					s.closeTab(i)
					break
				}
			}
		}) {
			return
		}
	}
	closing := s.tabs[index]
	name := closing.title
	fallback := index
	wasActive := s.currentTab() == closing
	s.removeTabs(func(t *tab) bool { return t == closing })
	if wasActive {
		s.activeTab = min(fallback, len(s.tabs)-1)
	}
	s.status = "已关闭页签 " + name
}

// closeOtherTabs keeps the Objects tab and one selected document.
func (s *State) closeOtherTabs(index int) {
	if index < 0 || index >= len(s.tabs) {
		return
	}
	keepTab := s.tabs[index]
	for i, t := range s.tabs {
		if i != index && t.kind == tabMySQLData {
			if s.guardMySQLDataLeave(t, func(_ ui.Services) {
				for n, candidate := range s.tabs {
					if candidate == keepTab {
						s.closeOtherTabs(n)
						break
					}
				}
			}) {
				return
			}
		}
	}
	s.removeTabs(func(t *tab) bool { return t != keepTab })
	for i, t := range s.tabs {
		if t == keepTab {
			s.activeTab = i
			break
		}
	}
	s.status = "已关闭其他页签"
}

func (s *State) showObjects(kind string) {
	s.activeTab = 0
	if !s.profileMode {
		s.preview.objectRow = 0
		s.preview.kind = kind
	}
	if s.profileMode {
		s.objectWorkspace.ShowCategory(s.sessions.Current().ProfileID, kind)
		s.status = "已切换到" + kind + "对象列表"
		return
	}
	s.status = "已切换到" + kind + "对象列表（模拟数据）"
}

func (s *State) clickToolbar(action string) {
	s.lastAction = action
	switch action {
	case "连接":
		s.openConnectionDialog()
	case "新建查询":
		s.openQuery()
	case "表", "视图", "函数":
		s.showObjects(action)
	case "信息":
		s.inspectorOn = !s.inspectorOn
	default:
		s.status = action + "：UI 预览阶段，实际功能尚未接入"
	}
}
