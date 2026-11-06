package shell

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/ui/connectioncatalog"
	"github.com/nyakang/nyadb/internal/ui/connectioneditor"
	"github.com/nyakang/nyadb/internal/ui/dataworkspace"
	"github.com/nyakang/nyadb/internal/ui/favoritesworkspace"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
	"github.com/nyakang/nyadb/internal/ui/sessionbrowser"
	"github.com/nyakang/nyadb/internal/ui/sqlworkspace"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
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
	pinned      bool
	placeholder *queries.WorkspaceTab
	id          uint64
	title       string
	kind        tabKind
	query       *sqlworkspace.Document

	object *objectworkspace.Document

	data *dataworkspace.Document
}

// State is UI state owned by the main thread. Never retain ui.Element values.
type State struct {
	workspaces         workspaceManager
	initialSQLCleanup  initialSQLCleanupState
	favorites          *favoritesworkspace.State
	favoriteMenuScope  *favoriteMenuScope
	autoConnectPending bool
	tunnelRuntime      *tunnelRuntime
	runtime            *dbruntime.Manager
	mysqlTemplate      *mysqlsvc.MySQLSessionManager

	navigateDatabase, navigateTable string
	navigateProfileID               int64
	sidebarPercent                  float32
	centerPercent                   float32
	inspectorOn                     bool

	preview  *previewState
	settings settingsState

	connectionManagerRow           int
	connectionManagerList          ui.ListState
	tabs                           []*tab
	activeTab                      int
	nextTabID                      uint64
	status                         string
	lastAction                     string
	toolbarMenus                   map[string]toolbarScope
	documentMenus                  map[string]documentMenuScope
	editFocus, connectionEditFocus string
	editTarget                     theme.EditTarget
	editServices                   ui.Services
	connectionEditTarget           theme.EditTarget
	editMenuHost                   func([]theme.EditMenuEntry)

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
	s.favorites = favoritesworkspace.New(s.favoritesDependencies())
	s.dataWorkspace = dataworkspace.New(s.dataWorkspaceDependencies())
	s.objectWorkspace = objectworkspace.New(s.objectWorkspaceDependencies())
	s.sqlWorkspace = sqlworkspace.New(s.sqlWorkspaceDependencies())
	s.sessions = sessionbrowser.New(s.sessionBrowserDependencies())
	s.navigation = navigation.New(s.navigationDependencies())
	s.initRuntime()
	s.connectionEditor = connectioneditor.New(connectioneditor.Dependencies{
		EditFocus: &s.connectionEditFocus,
		EditTarget: func(target theme.EditTarget) {
			valid := target.Valid
			id := target.ID
			target.Valid = func() bool { return valid != nil && valid() && s.connectionEditFocus == id }
			s.connectionEditTarget = target
		},
		ValidateMySQL: func(settings connections.ActiveSettings, secret string) error {
			_, err := mysqlsvc.DirectMySQLConfig(settings, secret)
			return err
		},
		TestConnection: s.runtime.Test,
		TestMySQL:      mysqlsvc.TestDirectMySQLConnection,
		Saved:          s.connectionSaved, Back: s.backToConnectionCatalog, Cancel: s.closeConnectionDialog,
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
	if s.sqlWorkspace.PersistenceLoading() {
		s.status = locales.Text("等待加载")
		return
	}
	s.nextTabID++
	initial := "-- NyaDB UI 预览：SQL 尚不会发送到数据库\nSELECT * FROM customers LIMIT 100;\n"
	profileID := int64(0)
	database := ""
	if s.profileMode {
		initial = "-- 输入 SQL；点击运行后发送至当前连接\n"
		if p, ok := s.currentProfile(); ok && (p.Kind.IsMySQLFamily() || p.Kind == connections.PostgreSQL || p.Kind == connections.SQLite || p.Kind == connections.ClickHouse || p.Kind == connections.SQLServer || p.Kind == connections.Oracle || p.Kind == connections.DuckDB) {
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
	s.sqlWorkspace.Focus(t.query)
	if !s.profileMode {
		s.status += "（模拟工作区）"
	}
}

func (s *State) showObjects(kind string) {
	s.activeTab = 0
	if !s.profileMode {
		s.preview.objectRow = 0
		s.preview.kind = kind
	}
	if s.profileMode {
		id := s.selectedProfileID
		if id == 0 {
			id = s.sessions.Current().ProfileID
		}
		s.objectWorkspace.ShowCategory(id, kind)
		s.status = "已切换到" + kind + "对象列表"
		return
	}
	s.status = "已切换到" + kind + "对象列表（模拟数据）"
}

func (s *State) clickToolbar(action string) {
	s.dispatchToolbar(action, ui.Services{})
}
func (s *State) dispatchToolbar(action string, services ui.Services) {
	if !s.toolbarActionAvailable(action) {
		return
	}
	s.lastAction = action
	switch action {
	case "连接":
		s.openConnectionDialog()
	case "新建查询":
		s.openQuery()
	case "表", "视图", "函数", "查询":
		s.showObjects(action)
	case "用户":
		w := s.sessions.Current()
		s.objectWorkspace.OpenAccounts(services, w.ProfileID, w.Database, "")
	case "信息":
		s.inspectorOn = !s.inspectorOn
	default:
		s.status = action + "：UI 预览阶段，实际功能尚未接入"
	}
}
