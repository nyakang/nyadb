package shell

import (
	"context"
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

const (
	PreviewRows    = 100_000
	PreviewColumns = 50
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
)

type tab struct {
	id     uint64
	title  string
	kind   tabKind
	editor *editor.State

	programProfileID int64
	programDatabase  string
	program          connections.MySQLDBObject

	structureDatabase, structureTable string
	structureProfileID                int64
	structure                         *connections.MySQLTableStructure
	structurePage                     int
	structureFieldRow                 int
	structureColumns                  ui.ListState
	structureIndexes                  ui.ListState
	structureForeignKeys              ui.ListState
	structureLoading                  bool
	structureErr                      string
	structureCancel                   context.CancelFunc
	structureResults                  <-chan mysqlStructureResult
	structureRequest                  uint64

	dataDatabase, dataTable string
	dataProfileID           int64
	dataOffset              int64
	dataLimit               int
	dataPage                *connections.MySQLDataPage
	dataInfo                mysqlTableInfoState
	dataReadInfo            mysqlDataReadInfo
	dataLayout              mysqlDataLayoutState
	dataState               ui.ListState
	dataColumns             []ui.TableColumn
	dataLoading             bool
	dataErr                 string
	dataCancel              context.CancelFunc
	dataResults             <-chan mysqlDataResult
	dataRequest             uint64
	dataSelection           ui.Selection[int]
	dataSelectedRow         int
	dataSort                ui.SortOrder
	dataSortColumn          string
	dataSortDescending      bool
	dataFilterColumnDraft   string
	dataFilterModeDraft     string
	dataFilterValueDraft    string
	dataFilterColumn        string
	dataFilterMode          string
	dataFilterValue         string
	dataRulesExpanded       bool
	dataRuleError           string
	dataFilterExtraDraft    []mysqlDataRuleDraft
	dataFilterRules         []connections.MySQLDataFilterRule
	dataSortDraft           []mysqlDataSortDraft
	dataSortRules           []connections.MySQLDataSortRule
	dataHiddenColumns       map[string]bool
	dataShowFieldTypes      bool
	dataUseKeyset           bool
	dataAfter               *connections.MySQLDataCursor
	dataCursorHistory       map[int64]*connections.MySQLDataCursor
	dataChunkField          string
	dataChunkRow            int
	dataChunkNext           int64
	dataChunkText           string
	dataChunkTotal          int64
	dataChunkDone           bool
	dataChunkLoading        bool
	dataChunkErr            string
	dataChunkCancel         context.CancelFunc
	dataChunkResults        <-chan mysqlChunkResult
	dataChunkRequest        uint64
}

type object struct {
	name, kind, modified, engine, comment string
	rows                                  string
}

var mockObjects = []object{
	{name: "customers", kind: "表", modified: "2026-10-20", engine: "InnoDB", rows: "12,800", comment: "客户信息"},
	{name: "orders", kind: "表", modified: "2026-10-19", engine: "InnoDB", rows: "56,420", comment: "订单记录"},
	{name: "products", kind: "表", modified: "2026-10-18", engine: "InnoDB", rows: "2,450", comment: "商品目录"},
	{name: "audit_logs", kind: "表", modified: "2026-10-17", engine: "InnoDB", rows: "100,000", comment: "操作日志"},
	{name: "active_customers", kind: "视图", modified: "2026-10-16", engine: "VIEW", rows: "—", comment: "活跃客户"},
	{name: "monthly_revenue", kind: "视图", modified: "2026-10-12", engine: "VIEW", rows: "—", comment: "月度收入"},
	{name: "get_discount", kind: "函数", modified: "2026-10-10", engine: "SQL", rows: "—", comment: "折扣计算"},
}

var objectColumns = []ui.TableColumn{
	{Title: "名称", Width: 205},
	{Title: "修改日期", Width: 130},
	{Title: "数据长度", Width: 110},
	{Title: "引擎", Width: 110},
	{Title: "行", Width: 100},
	{Title: "注释", Width: 210},
}

// State is UI state owned by the main thread. Never retain ui.Element values.
type State struct {
	mysqlTableInfoLoader  mysqlTableInfoLoader
	mysqlTableCountLoader mysqlTableCountLoader
	dataLayoutPreferences mysqlDataLayoutPreferences
	sidebarPercent        float32
	centerPercent         float32
	editorPercent         float32
	inspectorOn           bool
	inspectorDetailsOpen  bool
	inspectorObjectsOpen  bool

	explorerTree      ui.OutlineState[string]
	explorerSelection ui.Selection[string]
	explorerMenuBuilt bool
	explorerRow       int
	explorerName      string
	search            string
	objectKind        string
	objectRow         int
	objectState       ui.ListState
	dataState         ui.ListState
	dataColumns       []ui.TableColumn

	tabs       []*tab
	activeTab  int
	nextTabID  uint64
	demoRuns   int
	status     string
	lastAction string

	connectionDialog             bool
	connectionStep               int
	connectionEditorTab          int
	connectionKind               connections.Kind
	connectionDraft              connections.Profile
	connectionPort               string
	mysqlAdvancedNumbers         [5]string
	mysqlCompatibilityVisible    bool
	mysqlDatabaseTable           ui.ListState
	mysqlInitialQuery            string
	mysqlInitialQueryPreview     string
	mysqlInitialQueryDraft       string
	mysqlInitialQueryOpen        bool
	mysqlInitialQueryError       string
	mysqlSSLPassphrase           string
	mysqlSSLSessionVersion       uint64
	mysqlSSHPort                 string
	mysqlSSHPassword             string
	mysqlSSHPassphrase           string
	mysqlSSHSessionVersion       uint64
	mysqlHTTPSubtab              int
	mysqlHTTPProxyPort           string
	mysqlHTTPPassword            string
	mysqlHTTPProxyPassword       string
	mysqlHTTPPassphrase          string
	mysqlHTTPSessionVersion      uint64
	connectionSecret             string
	connectionSavePassword       bool
	credentialVault              credentials.Vault
	connectionPasswordLoadFailed bool
	connectionError              string
	connectionProfilesVisible    bool
	connectionActiveConfig       int
	connectionActiveTarget       int // Active for future sessions, independent of highlighted row.
	connectionMainConfig         connectionEditorSnapshot
	connectionOtherConfigs       []connectionEditorSnapshot
	connectionSelectedName       string
	connectionSearch             string
	connectionGridMode           bool
	connectionTreeOpen           map[string]bool
	connectionRecent             []string
	connectionEngineFilters      map[string]bool
	connectionVendorFilters      map[string]bool
	setConnectionTitle           func(string)
	showConnectionWindow         func()
	hideConnectionWindow         func()
	bundledIcons                 map[string]*ui.Bitmap
	providerIcons                map[string]*ui.Bitmap
	researchIcons                map[string]*ui.Bitmap

	connectionNameFocusPending bool
	mysqlSessionManager        *connections.MySQLSessionManager
	mysqlWorkspaces            map[int64]*mysqlSessionWorkspace
	mysqlSessionCancel         context.CancelFunc
	mysqlSessionResults        <-chan mysqlSessionResult
	mysqlSessionProfileID      int64
	mysqlSessionGeneration     uint64
	mysqlSessionStatus         string
	mysqlSessionError          string
	mysqlSessionPassword       string
	mysqlSessionDatabases      []string
	mysqlSessionOpener         mysqlSessionOpener
	mysqlDatabaseLister        mysqlDatabaseLister
	mysqlSelectedDatabase      string
	mysqlObjectKind            string
	mysqlObjectCache           map[string][]connections.MySQLDBObject
	mysqlObjectErrors          map[string]string
	mysqlObjectPending         string
	mysqlObjectRequest         uint64
	mysqlObjectCancel          context.CancelFunc
	mysqlObjectResults         <-chan mysqlObjectResult
	mysqlObjectLister          mysqlObjectLister
	mysqlDatabaseNodes         map[string]*mysqlDatabaseNode
	mysqlStructureLoader       mysqlStructureLoader
	mysqlDataLoader            mysqlDataLoader
	mysqlDataAdvancedLoader    mysqlDataAdvancedLoader

	// MySQL connection checks run off-thread; only UI code reads/writes these.
	connectionTesting        bool
	connectionTestMessage    string
	connectionTestGeneration uint64
	connectionTestCancel     context.CancelFunc
	connectionTestResults    <-chan mysqlTestResult
	mysqlConnectionTester    mysqlConnectionTester

	profileMode             bool
	profileService          *connections.Service
	profileEntries          []connections.Profile
	profileGroups           []connections.Group
	selectedGroup           string
	profileStarredOnly      bool
	profileEngine           connections.Kind
	navigationFilterRestore *profileFilterSnapshot
	navigateDatabase        string
	navigateTable           string
	navigateProfileID       int64
	groupEditing            bool
	groupEditOriginal       string
	groupDraft              string
	groupError              string
	groupDeleteConfirm      bool
	groupPendingDelete      string
	selectedProfileID       int64
	pendingDeleteID         int64
	deleteConfirm           bool
	settingsPreview         *editor.State
	settingsSampleInput     string
	settingsSampleCheck     bool
	settingsSampleRow       int
	settingsSampleTable     ui.ListState
	appearanceChanged       func(theme.Mode)
	sqliteFilePicker        func()
	mysqlSettingsPicker     func()
	mysqlSSLFilePicker      func(MySQLSSLFile)
	mysqlSSHKeyPicker       func()
	mysqlHTTPFilePicker     func(MySQLHTTPFile)
}

func New() *State {
	columns := make([]ui.TableColumn, PreviewColumns)
	columns[0] = ui.TableColumn{Title: "id", Width: 90, Fixed: true}
	columns[1] = ui.TableColumn{Title: "name", Width: 180}
	columns[2] = ui.TableColumn{Title: "email", Width: 240}
	for i := 3; i < len(columns); i++ {
		columns[i] = ui.TableColumn{Title: fmt.Sprintf("field_%02d", i-2), Width: 150}
	}
	s := &State{
		sidebarPercent:          100 * (theme.SidebarWidth + theme.SplitterBorder) / 1279,
		centerPercent:           100 * (1278 - theme.SidebarWidth - theme.SplitterBorder - theme.InspectorWidth) / (1278 - theme.SidebarWidth - theme.SplitterBorder),
		editorPercent:           48,
		inspectorOn:             true,
		inspectorDetailsOpen:    true,
		inspectorObjectsOpen:    true,
		connectionGridMode:      true,
		connectionTreeOpen:      map[string]bool{},
		connectionEngineFilters: map[string]bool{},
		connectionVendorFilters: map[string]bool{},
		objectKind:              "表",
		explorerName:            "我的连接",
		explorerRow:             -1,
		status:                  "就绪 · UI 布局预览（没有连接真实数据库）",
		dataColumns:             columns,
		nextTabID:               1,
		tabs:                    []*tab{{id: 1, title: "对象", kind: tabObjects}},
	}
	s.objectState.Selected = &s.objectRow
	s.explorerTree.List.Selected = &s.explorerRow
	s.bundledIcons, _ = bundledCatalogIcons() // always use committed assets when valid
	s.providerIcons, _ = bundledProviderIcons()
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
	buf := editor.NewBuffer("-- NyaDB UI 预览：SQL 尚不会发送到数据库\nSELECT * FROM customers LIMIT 100;\n")
	buf.SetLanguage(editor.LanguageNamed("SQL"))
	t := &tab{
		id: s.nextTabID, title: fmt.Sprintf("SQL %d", s.nextTabID-1),
		kind: tabSQL, editor: &editor.State{Buffer: buf, WrapGuides: []int{100}},
	}
	s.tabs = append(s.tabs, t)
	s.activeTab = len(s.tabs) - 1
	s.status = "已打开 " + t.title + "（模拟工作区）"
}

func (s *State) openData(name string) {
	for i, t := range s.tabs {
		if t.kind == tabData && t.title == name {
			s.activeTab = i
			return
		}
	}
	s.nextTabID++
	s.tabs = append(s.tabs, &tab{id: s.nextTabID, title: name, kind: tabData})
	s.activeTab = len(s.tabs) - 1
	s.status = "正在预览 " + name + " 的模拟数据"
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
	if s.tabs[index].kind == tabStructure {
		s.cancelMySQLStructure(s.tabs[index])
	} else if s.tabs[index].kind == tabMySQLData {
		s.disposeMySQLDataTab(s.tabs[index])
	}
	name := s.tabs[index].title
	s.tabs = append(s.tabs[:index], s.tabs[index+1:]...)
	if s.activeTab > index {
		s.activeTab--
	} else if s.activeTab == index && s.activeTab >= len(s.tabs) {
		s.activeTab = len(s.tabs) - 1
	}
	s.status = "已关闭页签 " + name
}

// closeOtherTabs keeps the Objects tab and one selected document.
func (s *State) closeOtherTabs(index int) {
	if index < 0 || index >= len(s.tabs) {
		return
	}
	for i, t := range s.tabs {
		if i != index && t.kind == tabStructure {
			s.cancelMySQLStructure(t)
		} else if i != index && t.kind == tabMySQLData {
			s.disposeMySQLDataTab(t)
		}
	}
	if index == 0 {
		s.tabs = s.tabs[:1]
		s.activeTab = 0
	} else {
		s.tabs = []*tab{s.tabs[0], s.tabs[index]}
		s.activeTab = 1
	}
	s.status = "已关闭其他页签"
}

func (s *State) showObjects(kind string) {
	s.objectKind = kind
	s.activeTab = 0
	s.objectRow = 0
	if s.profileMode {
		s.mysqlObjectKind = kind
		s.objectRow = -1
		s.status = "已切换到" + kind + "对象列表"
		return
	}
	s.status = "已切换到" + kind + "对象列表（模拟数据）"
}

func (s *State) visibleObjects() []object {
	if s.profileMode {
		return nil // real metadata is deferred until M2
	}
	if s.explorerName == "" || s.explorerName == "我的连接" {
		return nil
	}
	items := make([]object, 0, len(mockObjects))
	for _, item := range mockObjects {
		if item.kind == s.objectKind {
			items = append(items, item)
		}
	}
	return items
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

func PreviewCell(row, col int) string {
	switch col {
	case 0:
		return fmt.Sprint(row + 1)
	case 1:
		return fmt.Sprintf("customer_%06d", row+1)
	case 2:
		return fmt.Sprintf("user%06d@example.test", row+1)
	default:
		return fmt.Sprintf("value_%d_%d", row+1, col)
	}
}
