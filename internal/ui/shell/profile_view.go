package shell

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func (s *State) profileSidebar(c *ui.Context) {
	ui.Column(c).Fill().Background(theme.Sidebar).Children(func() {
		ui.Row(c).Height(30).Padding(4, 10).AlignItems(ui.Center).Background(theme.Accent).Gap(8).Children(func() {
			theme.Icon(c, "connection", 17, theme.Text)
			ui.Text(c, "我的连接").FontSize(12).Bold()
			ui.Box(c).Grow(1)
			if ui.Button(c, "+").Label("添加连接").Padding(1, 6).Clicked() {
				s.openConnectionDialog()
			}
		})
		ui.Column(c).Grow(1).Padding(4, 5).Children(func() {
			roots := s.profileRoots()
			if len(roots) == 0 {
				msg := "暂无连接档案，请点击“+”新建"
				if strings.TrimSpace(s.search) != "" {
					msg = "没有匹配的连接"
				}
				ui.Text(c, msg).Padding(12).TextColor(theme.Muted)
				return
			}
			// Preserve selection by database profile ID across sort and refresh.
			if s.explorerRow < 0 && s.selectedProfileID != 0 {
				if current, ok := s.currentProfile(); ok {
					for i, name := range roots {
						if name == current.Name {
							s.explorerRow = i
							break
						}
					}
				}
			}
			s.explorerTree.List.Selected = &s.explorerRow
			ui.Outline(c, &s.explorerTree, roots, func(string) []string { return nil }, func(name string) {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
					theme.Icon(c, "database", 15, theme.Blue)
					ui.Text(c, name).SingleLine().FontSize(12)
				})
			}).Grow(1)
			if s.explorerRow >= 0 && s.explorerRow < s.explorerTree.Rows() {
				name := s.explorerTree.Item(s.explorerRow)
				for _, p := range s.profileEntries {
					if p.Name == name && p.ID != s.selectedProfileID {
						s.selectedProfileID = p.ID
						s.explorerName = p.Name
						s.status = "已选择连接：" + name + "（未连接）"
						break
					}
				}
			}
		})
		ui.Column(c).Padding(6).BorderWidth(1, 0, 0, 0).BorderColor(theme.Border).Gap(5).Children(func() {
			ui.Row(c).Gap(4).Children(func() {
				has := s.selectedProfileID > 0
				if ui.Button(c, "编辑连接").Disabled(!has).Clicked() {
					s.startEditProfile()
				}
				if ui.Button(c, "复制连接").Disabled(!has).Clicked() {
					s.duplicateProfile()
				}
				if ui.Button(c, "删除连接").Disabled(!has).Clicked() {
					s.confirmDeleteProfile()
				}
				if ui.Button(c, "刷新").Clicked() {
					s.loadProfiles()
					s.status = "已刷新连接档案"
				}
			})
			if ui.SearchField(c, &s.search).Label("搜索连接").Placeholder("搜索连接").Changed() {
				s.explorerRow = -1
			}
		})
	})
}

func (s *State) profileRoots() []string {
	filter := strings.ToLower(strings.TrimSpace(s.search))
	names := make([]string, 0, len(s.profileEntries))
	for _, p := range s.profileEntries {
		if filter == "" || strings.Contains(strings.ToLower(p.Name), filter) {
			names = append(names, p.Name)
		}
	}
	return names
}

func (s *State) profileInspector(c *ui.Context) {
	p, ok := s.currentProfile()
	ui.Column(c).Fill().Background(theme.Panel).Children(func() {
		ui.Row(c).Height(31).Padding(4, 10).AlignItems(ui.Center).Background(theme.Header).Children(func() {
			ui.Text(c, "信息").Bold().FontSize(12)
			ui.Box(c).Grow(1)
			if ui.Button(c, "×").Label("隐藏信息面板").Padding(2, 6).Clicked() {
				s.inspectorOn = false
			}
		})
		ui.Column(c).Padding(16).Gap(12).Children(func() {
			if !ok {
				ui.Text(c, "选择连接以查看档案信息").TextColor(theme.Muted)
				return
			}
			theme.Icon(c, "connection", 38, theme.Blue)
			ui.Text(c, p.Name).Bold().FontSize(16)
			ui.Text(c, "配置档案 / 未连接").FontSize(11).TextColor(theme.Muted)
			ui.Box(c).Height(1).Background(theme.Border)
			ui.Text(c, "基本信息").Bold().TextColor(theme.Blue)
			s.property(c, "名称", p.Name)
			s.property(c, "类型", connectionKindName(p.Kind))
			s.property(c, "状态", "未连接")
			if p.Kind == connections.SQLite {
				s.property(c, "文件", p.Database)
			} else {
				s.property(c, "主机", p.Host)
				s.property(c, "端口", fmt.Sprint(p.Port))
				s.property(c, "用户", p.Username)
				s.property(c, "数据库", p.Database)
			}
			ui.Box(c).Height(1).Background(theme.Border)
			ui.Text(c, "连接档案已保存，但未建立数据库会话。").TextColor(theme.Muted).FontSize(11)
		})
	})
}
