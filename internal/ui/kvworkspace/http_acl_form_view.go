package kvworkspace

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) httpACLFormModals(c *ui.Context) {
	for _, d := range s.documents {
		if !s.active(d) || d.httpACL == nil || d.httpACL.form == nil || !d.httpACL.form.open || s.leave != nil {
			continue
		}
		form := d.httpACL.form
		theme.Modal(c.Key(fmt.Sprintf("kv-acl-read-%d", d.id)), &form.open, func() {
			ui.Column(c).Width(660).Height(440).Gap(6).Children(func() {
				ui.Text(c, locales.Text("读取 ACL 资源")).Bold()
				ui.Text(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), d.profileID, d.scope.Generation)).SingleLine()
				ui.Toolbar(c, func() {
					if ui.Button(c, locales.Text("打开 ACL 读取输入")).Disabled(d.Busy() || d.FileBusy() || s.deps.Files.OpenPath == nil || s.deps.Files.OpenReader == nil).Clicked() {
						s.httpACLFormFile(d, "open-acl-input", c.Services())
					}
					if ui.Button(c, locales.Text("保存 ACL 读取输入")).Disabled(d.Busy() || d.FileBusy() || form.path == "" && s.deps.Files.SavePath == nil || s.deps.Files.WriteStream == nil).Clicked() {
						s.httpACLFormFile(d, "save-acl-input", c.Services())
					}
					if ui.Button(c, locales.Text("另存 ACL 读取输入")).Disabled(d.Busy() || d.FileBusy() || s.deps.Files.SavePath == nil || s.deps.Files.WriteStream == nil).Clicked() {
						s.httpACLFormFile(d, "save-as-acl-input", c.Services())
					}
					if ui.Button(c, locales.Text("复制 ACL 读取输入")).Disabled(d.Busy() || d.FileBusy()).Clicked() {
						s.httpACLFormFile(d, "copy-acl-input", c.Services())
					}
				}).FillWidth().MinWidth(0).Gap(5)
				input := form.input
				disabled := d.Busy() || d.FileBusy()
				ui.Scroll(c).Fill().MinHeight(0).Children(func() {
					ui.Column(c).FillWidth().Gap(5).Children(func() {
						ui.Select(c, &input.Resource, []string{"token", "policy", "role", "auth-method", "binding-rule", "templated-policy"}).Label(locales.Text("ACL 资源类型")).FillWidth().Disabled(disabled)
						ui.Select(c, &input.Lookup, []string{"list", "id", "name", "self"}).Label(locales.Text("ACL 查询方式")).FillWidth().Disabled(disabled)
						ui.TextInput(c, &input.Identity).Label(locales.Text("ACL 原标识或名称")).Placeholder(locales.Text("ACL 原标识或名称")).FillWidth().Disabled(disabled)
						ui.Text(c, locales.Text("self 使用原连接令牌；token 的 id 是 AccessorID。list 和 self 不填写标识。")).Wrap().FillWidth().TextColor(theme.Colors(c).Muted)
						for _, option := range []struct {
							name    string
							include *bool
							value   *string
						}{
							{"policy", &input.IncludePolicy, &input.Policy}, {"role", &input.IncludeRole, &input.Role},
							{"authmethod", &input.IncludeAuthMethod, &input.AuthMethod}, {"servicename", &input.IncludeServiceName, &input.ServiceName},
							{"index", &input.IncludeIndex, &input.Index}, {"wait", &input.IncludeWait, &input.Wait},
						} {
							ui.Row(c).FillWidth().Gap(6).Children(func() {
								ui.Checkbox(c, option.include, option.name).Width(140).Disabled(disabled)
								ui.TextInput(c, option.value).Label("ACL " + option.name).Grow(1).MinWidth(0).Disabled(disabled || !*option.include)
							})
						}
						ui.Checkbox(c, &input.Expanded, "expanded").Disabled(disabled)
						ui.Text(c, locales.Text("保留勾选参数的空值；不支持的资源、查询方式或参数组合将被拒绝。")).Wrap().FillWidth().TextColor(theme.Colors(c).Muted)
					})
				})
				s.SetHTTPACLReadInput(d, input)
				if d.error != "" {
					ui.Text(c, d.error).SingleLine().Tooltip(d.error).TextColor(theme.Colors(c).Warning)
				}
				ui.Toolbar(c, func() {
					if ui.Button(c, locales.Text("读取原连接 ACL")).Disabled(disabled || d.NeedsRecovery()).Clicked() {
						s.RunHTTPACLReadForm(d)
					}
					if d.FileBusy() && ui.Button(c, locales.Text("取消文件操作")).Clicked() {
						s.CancelFile(d)
					}
					if ui.Button(c, locales.Text("返回请求文本")).Clicked() {
						form.open = false
					}
				}).FillWidth().MinWidth(0).Gap(5)
			})
		})
	}
}
