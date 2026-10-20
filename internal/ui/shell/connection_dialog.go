package shell

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func (s *State) openConnectionDialog() {
	s.connectionDialog = true
	s.connectionStep = 0
	s.connectionKind = ""
	s.connectionDraft = connections.Profile{}
	s.connectionPort = ""
	s.connectionSecret = ""
	s.connectionError = ""
}

func (s *State) closeConnectionDialog() {
	s.connectionDialog = false
	s.connectionSecret = ""
	s.connectionError = ""
}

func (s *State) chooseConnectionKind(kind connections.Kind) {
	s.connectionKind = kind
	s.connectionError = ""
}

func (s *State) nextConnectionStep() {
	if s.connectionKind == "" {
		s.connectionError = "请先选择数据库类型"
		return
	}
	draft, err := connections.Defaults(s.connectionKind)
	if err != nil {
		s.connectionError = err.Error()
		return
	}
	s.connectionDraft = draft
	s.connectionPort = strconv.Itoa(int(draft.Port))
	if draft.Kind == connections.SQLite {
		s.connectionPort = ""
	}
	s.connectionError = ""
	s.connectionStep = 1
}

func (s *State) draftProfile() (connections.Profile, error) {
	p := s.connectionDraft
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return p, fmt.Errorf("请输入连接名称")
	}
	if p.Kind == connections.SQLite {
		if strings.TrimSpace(p.Database) == "" {
			return p, fmt.Errorf("请选择 SQLite 数据库文件")
		}
		return p, nil
	}
	port, err := strconv.ParseUint(strings.TrimSpace(s.connectionPort), 10, 16)
	if err != nil || port == 0 {
		return p, fmt.Errorf("端口必须是 1–65535 之间的数字")
	}
	p.Port = uint16(port)
	if strings.TrimSpace(p.Host) == "" {
		return p, fmt.Errorf("请输入数据库主机")
	}
	return p, nil
}

func (s *State) saveConnectionDraft() {
	p, err := s.draftProfile()
	if err != nil {
		s.connectionError = err.Error()
		return
	}
	if s.profileService == nil {
		s.connectionError = "连接档案尚未接入本地存储"
		return
	}
	var saved connections.Profile
	if p.ID > 0 {
		saved, err = s.profileService.Update(context.Background(), p)
	} else {
		saved, err = s.profileService.Create(context.Background(), p)
	}
	if err != nil {
		s.connectionError = "保存失败（连接名称可能重复或配置无效）：" + err.Error()
		return
	}
	s.selectedProfileID = saved.ID
	s.loadProfiles()
	s.closeConnectionDialog()
	s.status = "已保存连接：" + saved.Name + "（未建立数据库会话）"
}

// connectionModal is a native MyGo modal, not a WebView or imported UI asset.
func (s *State) connectionModal(c *ui.Context) {
	if !s.connectionDialog {
		return
	}
	ui.Modal(c, &s.connectionDialog, func() {
		ui.Column(c).Width(720).Gap(12).Children(func() {
			title := "新建连接 · 选择数据库"
			if s.connectionStep == 1 {
				title = "新建连接 · " + connectionKindName(s.connectionKind)
				if s.connectionDraft.ID != 0 {
					title = "编辑连接 · " + connectionKindName(s.connectionKind)
				}
			}
			ui.Text(c, title).FontSize(19).Bold()
			ui.Box(c).Height(1).Background(theme.Border)
			if s.connectionStep == 0 {
				s.connectionTypes(c)
			} else {
				s.connectionFields(c)
			}
			if s.connectionError != "" {
				ui.Text(c, s.connectionError).FontSize(12).TextColor(theme.Gold)
			}
			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				if s.connectionStep == 1 && s.connectionDraft.ID == 0 {
					if ui.Button(c, "上一步").Clicked() {
						s.connectionStep = 0
						s.connectionError = ""
						s.connectionSecret = ""
					}
				}
				if ui.Button(c, "取消连接向导").Clicked() {
					s.closeConnectionDialog()
				}
				if s.connectionStep == 0 {
					if ui.PrimaryButton(c, "下一步").Clicked() {
						s.nextConnectionStep()
					}
				} else if ui.PrimaryButton(c, "保存连接").Clicked() {
					s.saveConnectionDraft()
				}
			})
		})
	})
}

func connectionKindName(kind connections.Kind) string {
	switch kind {
	case connections.MySQL:
		return "MySQL"
	case connections.PostgreSQL:
		return "PostgreSQL"
	case connections.SQLite:
		return "SQLite"
	default:
		return "未知类型"
	}
}

func (s *State) connectionTypes(c *ui.Context) {
	ui.Text(c, "选择一个连接类型：").TextColor(theme.Muted)
	ui.Row(c).Gap(10).Children(func() {
		for _, kind := range []connections.Kind{connections.MySQL, connections.PostgreSQL, connections.SQLite} {
			choice := kind
			ui.Column(c).Width(205).Gap(8).Padding(14).Radius(5).
				Border(1, theme.Border).Background(theme.Panel).Children(func() {
				icon := "database"
				if choice == connections.SQLite {
					icon = "table"
				}
				theme.Icon(c, icon, 28, theme.Blue)
				ui.Text(c, connectionKindName(choice)).Bold()
				button := "选择 " + connectionKindName(choice)
				if s.connectionKind == choice {
					button = "已选择 " + connectionKindName(choice)
				}
				if ui.Button(c, button).Clicked() {
					s.chooseConnectionKind(choice)
				}
			})
		}
	})
	ui.Text(c, "仅提供 MySQL、PostgreSQL 与 SQLite 的连接档案；本阶段不会访问数据库。").
		FontSize(12).TextColor(theme.Muted)
}

func (s *State) connectionFields(c *ui.Context) {
	p := &s.connectionDraft
	ui.Column(c).Gap(10).Children(func() {
		ui.Row(c).Gap(14).Children(func() {
			ui.Column(c).Width(235).Children(func() {
				ui.Text(c, "连接名称").FontSize(12)
				ui.TextInput(c, &p.Name).Label("连接名称").Placeholder("例如：本地开发库").FillWidth()
			})
			ui.Column(c).Width(235).Children(func() {
				ui.Text(c, "数据库类型").FontSize(12)
				ui.Text(c, connectionKindName(p.Kind)).Bold()
			})
		})
		if p.Kind == connections.SQLite {
			ui.Text(c, "已有 SQLite 数据库文件").FontSize(12)
			ui.TextInput(c, &p.Database).Label("数据库文件").Placeholder("/path/to/database.sqlite").FillWidth()
			ui.Text(c, "数据库文件必须由用户自行指定。创建数据库文件将在后续版本实现。").
				FontSize(11).TextColor(theme.Muted)
		} else {
			ui.Text(c, "主机").FontSize(12)
			ui.TextInput(c, &p.Host).Label("主机").FillWidth()
			ui.Row(c).Gap(12).Children(func() {
				ui.Column(c).Width(160).Children(func() {
					ui.Text(c, "端口").FontSize(12)
					ui.TextInput(c, &s.connectionPort).Label("端口").FillWidth()
				})
				ui.Column(c).Width(300).Children(func() {
					ui.Text(c, "用户名").FontSize(12)
					ui.TextInput(c, &p.Username).Label("用户名").FillWidth()
				})
			})
			ui.Text(c, "默认数据库（可选）").FontSize(12)
			ui.TextInput(c, &p.Database).Label("默认数据库").FillWidth()
			ui.Text(c, "连接凭据在本阶段不持久化；实际连接与安全凭据管理将在后续版本提供。").
				FontSize(11).TextColor(theme.Muted)
		}
	})
}
