package shell

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

var demoRoots = []string{"测试库", "个人库", "公司库", "开发库", "生产库"}

func demoChildren(node string) []string {
	switch node {
	case "测试库":
		return []string{"app_db"}
	case "app_db":
		return []string{"表", "视图"}
	case "表":
		return []string{"customers", "orders", "products", "audit_logs"}
	case "视图":
		return []string{"active_customers", "monthly_revenue"}
	case "个人库":
		return []string{"personal"}
	case "公司库":
		return []string{"office"}
	case "开发库":
		return []string{"dev"}
	case "生产库":
		return []string{"production"}
	default:
		return nil
	}
}

func (s *State) filteredRoots() []string {
	query := strings.TrimSpace(strings.ToLower(s.preview.search))
	if query == "" {
		return demoRoots
	}
	var roots []string
	for _, name := range demoRoots {
		if strings.Contains(strings.ToLower(name), query) {
			roots = append(roots, name)
		}
	}
	return roots
}

func (s *State) sidebar(c *ui.Context) {
	palette := theme.Colors(c)

	if s.profileMode {
		s.navigation.View(c)
		return
	}
	ui.Column(c).Fill().Background(palette.Sidebar).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, 30)).Padding(4, 10).AlignItems(ui.Center).
			Background(palette.Accent).TextColor(palette.AccentText).Gap(8).Children(func() {
			theme.Icon(c, "connection", 17, palette.AccentText)
			ui.Text(c, locales.Text("我的连接")).FontSize(theme.Font(c, 12)).Bold()
			ui.Box(c).Grow(1)
			if ui.Button(c, "+").Label("添加连接").Padding(1, 6).Clicked() {
				s.clickToolbar("连接")
			}
		})
		ui.Column(c).Grow(1).Padding(4, 5).Children(func() {
			roots := s.filteredRoots()
			if len(roots) == 0 {
				ui.Text(c, "没有匹配的连接").Padding(12).TextColor(palette.Muted)
				return
			}
			s.preview.tree.List.Selected = &s.preview.row
			theme.Outline(c, &s.preview.tree, roots, demoChildren, func(name string) {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
					switch name {
					case "测试库", "个人库", "公司库", "开发库", "生产库", "app_db", "personal", "office", "dev", "production":
						theme.Icon(c, "folder", 15, palette.Gold)
					case "customers", "orders", "products", "audit_logs":
						theme.Icon(c, "table", 15, palette.Blue)
					default:
						theme.Icon(c, "database", 15, palette.Muted)
					}
					ui.Text(c, name).SingleLine().FontSize(theme.Font(c, 12))
				}).ContextMenu(func(m *ui.Menu) {
					if m.Item(locales.Text("选择 ") + name).Chosen() {
						s.preview.key = name
						s.status = "已选择：" + name + "（模拟连接）"
					}
					if m.Item(locales.Text("新建查询")).Chosen() {
						s.preview.key = name
						s.openQuery()
					}
				})
			}).Grow(1)
			if s.preview.row >= 0 && s.preview.row < s.preview.tree.Rows() {
				name := s.preview.tree.Item(s.preview.row)
				if name != s.preview.key {
					s.preview.key = name
					s.preview.objectRow = 0
					s.status = "已选择：" + name + "（模拟连接）"
				}
			}
		})
		ui.Column(c).Padding(6).BorderWidth(1, 0, 0, 0).BorderColor(palette.Border).Children(func() {
			ui.SearchField(c, &s.preview.search).Label(locales.Text("搜索连接")).Placeholder(locales.Text("搜索连接"))
		})
	})
}

const (
	PreviewRows    = 100_000
	PreviewColumns = 50
)

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

type previewState struct {
	kind                     string
	detailsOpen, objectsOpen bool
	tree                     ui.OutlineState[string]
	row                      int
	key, search              string
	grid, objects            ui.ListState
	objectRow                int
	columns                  []ui.TableColumn
}

func newPreview() *previewState {
	columns := make([]ui.TableColumn, PreviewColumns)
	columns[0] = ui.TableColumn{Title: "id", Width: 90, Fixed: true}
	columns[1] = ui.TableColumn{Title: "name", Width: 180}
	columns[2] = ui.TableColumn{Title: "email", Width: 240}
	for i := 3; i < len(columns); i++ {
		columns[i] = ui.TableColumn{Title: fmt.Sprintf("field_%02d", i-2), Width: 150}
	}
	return &previewState{kind: "表", detailsOpen: true, objectsOpen: true, row: -1, key: "我的连接", columns: columns}
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

func (s *State) visibleObjects() []object {
	if s.profileMode {
		return nil // real metadata is deferred until M2
	}
	if s.preview.key == "" || s.preview.key == "我的连接" {
		return nil
	}
	items := make([]object, 0, len(mockObjects))
	for _, item := range mockObjects {
		if item.kind == s.preview.kind {
			items = append(items, item)
		}
	}
	return items
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

func (s *State) objectActions(c *ui.Context) {
	palette := theme.Colors(c)

	ui.Row(c).Height(theme.RowHeight(c, 31)).AlignItems(ui.Center).Gap(6).Padding(2, 6).
		Background(palette.Header).BorderWidth(0, 0, 1, 0).BorderColor(palette.Border).Children(func() {
		ui.Toolbar(c, func() {
			for _, name := range []string{"打开表", "设计表", "新建表", "删除表", "导入向导", "导出向导"} {
				action := name
				if ui.Button(c, locales.Text(action)).Padding(4, 6).Border(1, palette.Border).Radius(3).FontSize(theme.Font(c, 12)).Clicked() {
					if action == "打开表" {
						items := s.visibleObjects()
						if s.preview.objectRow >= 0 && s.preview.objectRow < len(items) && items[s.preview.objectRow].kind == "表" {
							s.openData(items[s.preview.objectRow].name)
						} else {
							s.status = "请先选择一个模拟数据表"
						}
					} else {
						s.status = action + "：将在后续里程碑接入"
					}
				}
			}
		}).Label(locales.Text("对象操作")).Grow(1).MinWidth(0).Padding(0).Gap(6).Background(palette.Header)
		if ui.Button(c, locales.Text("刷新")).Padding(3, 6).Border(1, palette.Border).Radius(3).Clicked() {
			s.status = "已刷新模拟对象列表"
		}
	})
}

func (s *State) objectsWorkspace(c *ui.Context) {
	palette := theme.Colors(c)

	if s.profileMode {
		s.objectWorkspace.BrowserView(c)
		return
	}
	ui.Column(c).Fill().Children(func() {
		s.objectActions(c)
		items := s.visibleObjects()
		if len(items) == 0 {
			ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
				theme.Icon(c, "database", 40, palette.Muted)
				ui.Text(c, locales.Text("选择左侧连接或数据库以浏览对象")).FontSize(theme.Font(c, 14)).TextColor(palette.Muted)
				ui.Text(c, locales.Text("当前为 UI 预览，数据库连接将在 M2 接入")).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
			})
			return
		}
		s.preview.objects.Selected = &s.preview.objectRow
		columns := append([]ui.TableColumn(nil), objectColumns...)
		for i := range columns {
			columns[i].Title = locales.Text(columns[i].Title)
		}
		if theme.Table(c, &s.preview.objects, columns, len(items), func(row, col int) {
			obj := items[row]
			var value string
			switch col {
			case 0:
				value = obj.name
			case 1:
				value = obj.modified
			case 2:
				value = "—"
			case 3:
				value = obj.engine
			case 4:
				value = obj.rows
			case 5:
				value = obj.comment
			}
			if col == 0 {
				ui.Row(c).AlignItems(ui.Center).Gap(7).Children(func() {
					symbol := "table"
					if obj.kind == "视图" {
						symbol = "view"
					}
					if obj.kind == "函数" {
						symbol = "function"
					}
					theme.Icon(c, symbol, 14, palette.Blue)
					ui.Text(c, value).SingleLine()
				})
			} else {
				ui.Text(c, value).SingleLine()
			}
		}).Grow(1).Submitted() {
			if s.preview.objectRow >= 0 && s.preview.objectRow < len(items) && items[s.preview.objectRow].kind == "表" {
				s.openData(items[s.preview.objectRow].name)
			}
		}
	})
}

func (s *State) dataPreview(c *ui.Context) {
	palette := theme.Colors(c)

	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).AlignItems(ui.Center).Padding(2, 6).Gap(8).
			Background(palette.Header).Children(func() {
			ui.Text(c, locales.Text("数据预览")).FontSize(theme.Font(c, theme.FontSize))
			ui.Text(c, locales.Text("模拟数据 · 100,000 行 / 50 列")).TextColor(palette.Muted).FontSize(theme.Font(c, theme.SmallFontSize)).SingleLine().Grow(1).MinWidth(0)
		})
		theme.Table(c, &s.preview.grid, s.preview.columns, PreviewRows, func(row, col int) {
			ui.Text(c, PreviewCell(row, col)).SingleLine().FontSize(theme.Font(c, 12))
		}).Grow(1)
	})
}

func (s *State) currentObjectKind() string {
	if s.profileMode {
		return s.objectWorkspace.Category(s.sessions.Current().ProfileID)
	}
	return s.preview.kind
}
