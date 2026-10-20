package shell

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
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
)

type tab struct {
	id     uint64
	title  string
	kind   tabKind
	editor *editor.State
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
	sidebarPercent float32
	centerPercent  float32
	editorPercent  float32
	inspectorOn    bool

	explorerTree ui.OutlineState[string]
	explorerRow  int
	explorerName string
	search       string
	objectKind   string
	objectRow    int
	objectState  ui.ListState
	dataState    ui.ListState
	dataColumns  []ui.TableColumn

	tabs       []*tab
	activeTab  int
	nextTabID  uint64
	demoRuns   int
	status     string
	lastAction string
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
		sidebarPercent: 22,
		centerPercent:  75,
		editorPercent:  48,
		inspectorOn:    true,
		objectKind:     "表",
		explorerName:   "我的连接",
		explorerRow:    -1,
		status:         "就绪 · UI 布局预览（没有连接真实数据库）",
		dataColumns:    columns,
		nextTabID:      1,
		tabs:           []*tab{{id: 1, title: "对象", kind: tabObjects}},
	}
	s.objectState.Selected = &s.objectRow
	s.explorerTree.List.Selected = &s.explorerRow
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
	if s.activeTab == 0 || len(s.tabs) == 1 {
		return // the Objects tab is permanent
	}
	name := s.tabs[s.activeTab].title
	s.tabs = append(s.tabs[:s.activeTab], s.tabs[s.activeTab+1:]...)
	if s.activeTab >= len(s.tabs) {
		s.activeTab = len(s.tabs) - 1
	}
	s.status = "已关闭页签 " + name
}

func (s *State) showObjects(kind string) {
	s.objectKind = kind
	s.activeTab = 0
	s.objectRow = 0
	s.status = "已切换到" + kind + "对象列表（模拟数据）"
}

func (s *State) visibleObjects() []object {
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
