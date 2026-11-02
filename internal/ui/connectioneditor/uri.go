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
func (s *State) URIActive() bool { return s.uriOpen }

// ImportURI prepares a fresh editor only after the entire URI validates. A
// malformed URI leaves the current masked input available for correction.
func (s *State) ImportURI(raw string) error {
	parsed, err := connections.ParseURI(raw)
	if err != nil {
		return err
	}
	group := s.uriGroup
	s.Close()
	s.connectionDraft = parsed.Profile
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
		ui.Text(c, locales.Text("支持 MySQL、PostgreSQL 和 SQLite；导入后检查配置，不会自动连接。"))
		ui.TextInput(c, &s.uriText).Password().Label(locales.Text("连接 URI")).Placeholder("mysql://host:3306/database").FillWidth()
		ui.Text(c, locales.Text("URI 输入隐藏，密码仅进入临时编辑器；是否保存到密钥环由连接表单决定。"))
		if s.uriError != "" {
			ui.Text(c, s.uriError).TextColor(theme.Colors(c).Error)
		}
		ui.Box(c).Grow(1)
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, locales.Text("返回连接类型")).Clicked() {
				s.Close()
				if s.back != nil {
					s.back()
				}
			}
			if ui.Button(c, locales.Text("取消")).Label(locales.Text("取消 URI 导入")).Clicked() {
				s.Cancel()
			}
			if ui.PrimaryButton(c, locales.Text("导入到连接表单")).Disabled(s.uriText == "").Clicked() {
				if err := s.ImportURI(s.uriText); err != nil {
					s.uriError = locales.Text("连接 URI 无效，请检查格式、主机、端口和数据库。")
					if errors.Is(err, connections.ErrURIOptions) {
						s.uriError = locales.Text("URI 含有当前引擎不支持的选项，请在连接表单中配置。")
					}
				} else if imported != nil {
					imported()
				}
			}
		})
	})
}
