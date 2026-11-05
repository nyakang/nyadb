package connectioneditor

import (
	"errors"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) BeginURI(group string) {
	s.Close()
	s.uriOpen, s.uriGroup = true, group
}

// BeginURIFromEditor parks the form in place, including invalid raw inputs and
// temporary secrets. No configuration or vault state changes until import.
func (s *State) BeginURIFromEditor() {
	if !s.active {
		return
	}
	s.cancelMySQLConnectionTest()
	s.engineFileVersion++
	s.uriOpen, s.uriReturn = true, true
	s.uriGroup = s.connectionDraft.GroupName
	s.uriText, s.uriError = "", ""
	s.uriConfirm = false
}

func (s *State) ReturnFromURI() {
	if !s.uriReturn {
		s.Back()
		return
	}
	s.uriOpen, s.uriReturn, s.uriConfirm = false, false, false
	s.uriText, s.uriError, s.uriGroup = "", "", ""
}
func (s *State) URIActive() bool { return s.uriOpen }

// ImportURI prepares a fresh editor only after the entire URI validates. A
// malformed URI leaves the current masked input available for correction.
func (s *State) ImportURI(raw string) error {
	parsed, err := connections.ParseURI(raw)
	if err != nil {
		return err
	}
	group := s.uriGroup
	original, returning := s.connectionDraft, s.uriReturn
	s.Close()
	s.connectionDraft = parsed.Profile
	if returning {
		s.connectionDraft.ID, s.connectionDraft.Name = original.ID, original.Name
		s.connectionDraft.GroupName, s.connectionDraft.Starred = original.GroupName, original.Starred
		s.connectionDraft.Notes, s.connectionDraft.Color = original.Notes, original.Color
		s.connectionDraft.CreatedAt, s.connectionDraft.UpdatedAt = original.CreatedAt, original.UpdatedAt
	}
	s.connectionDraft.GroupName = group
	s.initializeInputs()
	s.connectionSecret = parsed.Password
	s.connectionSavePassword = false
	s.connectionEditorTab = 0
	s.connectionNameFocusPending = true
	s.active = true
	return nil
}

func (s *State) URIView(c *ui.Context, width, height float32, imported func()) {
	if !s.uriOpen {
		return
	}
	ui.Column(c).Width(width).Height(height).Padding(20).Gap(12).Children(func() {
		ui.Text(c, locales.Text("通过 URI 新建连接")).Bold()
		ui.Text(c, locales.Text("支持 MySQL、PostgreSQL、SQLite 和 ClickHouse；导入后检查配置，不会自动连接。"))
		uri := ui.TextInput(c, &s.uriText).Password().Label(locales.Text("连接 URI")).Placeholder("mysql://host:3306/database").FillWidth()
		theme.BindNativeEdit(c, uri, "connection/uri", s.editFocus, s.editTarget, func() bool { return s.uriOpen }, true, true)
		ui.Text(c, locales.Text("URI 输入隐藏，密码仅进入临时编辑器；是否保存到密钥环由连接表单决定。"))
		if s.uriError != "" {
			ui.Text(c, s.uriError).TextColor(theme.Colors(c).Error)
		}
		ui.Box(c).Grow(1)
		ui.Row(c).Wrap().Gap(8).Justify(ui.End).Shrink(0).Children(func() {
			back := "返回连接类型"
			if s.uriReturn {
				back = "返回连接表单"
			}
			if ui.Button(c, locales.Text(back)).Clicked() {
				s.ReturnFromURI()
			}
			if ui.Button(c, locales.Text("取消")).Label(locales.Text("取消 URI 导入")).Clicked() {
				if s.uriReturn {
					s.ReturnFromURI()
				} else {
					s.Cancel()
				}
			}
			if ui.PrimaryButton(c, locales.Text("导入到连接表单")).Disabled(s.uriText == "").Clicked() {
				if _, err := connections.ParseURI(s.uriText); err != nil {
					s.uriError = locales.Text("连接 URI 无效，请检查格式、主机、端口和数据库。")
					if errors.Is(err, connections.ErrURIOptions) {
						s.uriError = locales.Text("URI 含有当前引擎不支持的选项，请在连接表单中配置。")
					}
				} else if s.uriReturn {
					s.uriConfirm = true
				} else if err := s.ImportURI(s.uriText); err == nil && imported != nil {
					imported()
				}
			}
		})
	})
	theme.Modal(c, &s.uriConfirm, func() {
		ui.Column(c).Width(440).MaxWidthPercent(95).Gap(12).Children(func() {
			ui.Text(c, locales.Text("替换连接配置？")).Bold()
			ui.Text(c, locales.Text("URI 将替换连接参数、高级选项和备用配置。名称与备注保留，保存前不会修改已有连接或密码。"))
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, locales.Text("取消")).Clicked() {
					s.uriConfirm = false
				}
				if ui.PrimaryButton(c, locales.Text("替换配置")).Clicked() {
					if err := s.ImportURI(s.uriText); err == nil && imported != nil {
						imported()
					}
				}
			})
		})
	})
}
