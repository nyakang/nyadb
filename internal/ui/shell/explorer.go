package shell

import (
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
	query := strings.TrimSpace(strings.ToLower(s.search))
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
	if s.profileMode {
		s.profileSidebar(c)
		return
	}
	ui.Column(c).Fill().Background(theme.Sidebar).Children(func() {
		ui.Row(c).Height(30).Padding(4, 10).AlignItems(ui.Center).
			Background(theme.Accent).Gap(8).Children(func() {
			theme.Icon(c, "connection", 17, theme.Text)
			ui.Text(c, locales.Text("我的连接")).FontSize(12).Bold()
			ui.Box(c).Grow(1)
			if ui.Button(c, "+").Label("添加连接").Padding(1, 6).Clicked() {
				s.clickToolbar("连接")
			}
		})
		ui.Column(c).Grow(1).Padding(4, 5).Children(func() {
			roots := s.filteredRoots()
			if len(roots) == 0 {
				ui.Text(c, "没有匹配的连接").Padding(12).TextColor(theme.Muted)
				return
			}
			s.explorerTree.List.Selected = &s.explorerRow
			ui.Outline(c, &s.explorerTree, roots, demoChildren, func(name string) {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
					switch name {
					case "测试库", "个人库", "公司库", "开发库", "生产库", "app_db", "personal", "office", "dev", "production":
						theme.Icon(c, "folder", 15, theme.Gold)
					case "customers", "orders", "products", "audit_logs":
						theme.Icon(c, "table", 15, theme.Blue)
					default:
						theme.Icon(c, "database", 15, theme.Muted)
					}
					ui.Text(c, name).SingleLine().FontSize(12)
				}).ContextMenu(func(m *ui.Menu) {
					if m.Item(locales.Text("选择 ") + name).Chosen() {
						s.explorerName = name
						s.status = "已选择：" + name + "（模拟连接）"
					}
					if m.Item(locales.Text("新建查询")).Chosen() {
						s.explorerName = name
						s.openQuery()
					}
				})
			}).Grow(1)
			if s.explorerRow >= 0 && s.explorerRow < s.explorerTree.Rows() {
				name := s.explorerTree.Item(s.explorerRow)
				if name != s.explorerName {
					s.explorerName = name
					s.objectRow = 0
					s.status = "已选择：" + name + "（模拟连接）"
				}
			}
		})
		ui.Column(c).Padding(6).BorderWidth(1, 0, 0, 0).BorderColor(theme.Border).Children(func() {
			ui.SearchField(c, &s.search).Label(locales.Text("搜索连接")).Placeholder(locales.Text("搜索连接"))
		})
	})
}
