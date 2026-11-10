package kvworkspace

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func authActionLabel(method string) string {
	switch method {
	case authPath("UserAdd"):
		return locales.Text("创建用户")
	case authPath("UserDelete"):
		return locales.Text("删除用户")
	case authPath("UserChangePassword"):
		return locales.Text("修改用户密码")
	case authPath("UserGrantRole"):
		return locales.Text("授予用户角色")
	case authPath("UserRevokeRole"):
		return locales.Text("撤销用户角色")
	case authPath("RoleAdd"):
		return locales.Text("创建角色")
	case authPath("RoleDelete"):
		return locales.Text("删除角色")
	case authPath("RoleGrantPermission"):
		return locales.Text("授予角色权限")
	case authPath("RoleRevokePermission"):
		return locales.Text("撤销角色权限")
	}
	return method
}

func authHint(c *ui.Context, text string) {
	ui.Text(c, text).FontSize(theme.Font(c, 11)).SingleLine().Tooltip(text).TextColor(theme.Colors(c).Muted)
}

func (s *State) authManagementView(c *ui.Context, d *Document) {
	a := d.authState()
	if view, err := authRequestView(d.Request()); err == nil {
		a.category = view
	}
	ready := !d.Busy() && !d.needsRecovery && d.AuthManagementAvailable()
	ui.Column(c).Fill().MinHeight(0).Padding(4, 8).Gap(4).Children(func() {
		ui.Toolbar(c, func() {
			if ui.Button(c, locales.Text("用户目录")).Disabled(d.Busy()).Clicked() {
				s.SetAuthCategory(d, "users")
			}
			if ui.Button(c, locales.Text("角色目录")).Disabled(d.Busy()).Clicked() {
				s.SetAuthCategory(d, "roles")
			}
			if ui.Button(c, locales.Text("读取认证状态")).Disabled(!ready || !d.authSupports(authPath("AuthStatus"))).Clicked() {
				s.ReadAuth(d, "AuthStatus", "")
			}
			if ui.Button(c, locales.Text("清除权限目录观察")).Disabled(d.Busy() || d.FileBusy()).Clicked() {
				s.ClearAuthBrowser(d)
			}
			ui.Button(c, locales.Text("管理用户与角色")).Disabled(!ready || d.hasUnknownWrites()).Menu(func(m *ui.Menu) {
				for _, action := range []string{"UserAdd", "UserDelete", "UserChangePassword", "UserGrantRole", "UserRevokeRole", "RoleAdd", "RoleDelete", "RoleGrantPermission", "RoleRevokePermission"} {
					if m.Item(authActionLabel(authPath(action))).Disabled(!d.authSupports(authPath(action))).Chosen() {
						s.OpenAuthAction(d, action)
					}
				}
			})
		}).Label(locales.Text("Auth 管理操作")).FillWidth().MinWidth(0).Padding(0).Gap(5)
		if a.status != nil && a.status.Result.Reply != nil && a.status.Result.Reply.Auth != nil {
			r := a.status.Result.Reply.Auth
			ui.Text(c, fmt.Sprintf(locales.Text("认证启用：%t · Auth 版本：%s"), r.Enabled, strconv.FormatUint(r.Revision, 10))).SingleLine().TextColor(theme.Colors(c).Muted)
		}
		ui.Row(c).FillWidth().Gap(6).Children(func() {
			ui.TextInput(c, &a.filter).Label(locales.Text("筛选权限目录")).Placeholder(locales.Text("筛选权限目录")).Grow(1).MinWidth(0)
			ui.TextInput(c, &a.target).Label(locales.Text("详情名称")).Placeholder(locales.Text("详情名称")).Grow(1).MinWidth(0).Disabled(d.Busy())
			if ui.Button(c, locales.Text("读取名称详情")).Disabled(!ready || !validAuthFormName(a.target)).Clicked() {
				method := "UserGet"
				if a.category == "roles" {
					method = "RoleGet"
					a.selectedRole = a.target
				} else {
					a.selectedUser = a.target
				}
				s.ReadAuth(d, method, a.target)
			}
		})
		authHint(c, locales.Text("用户与角色属于集群；每次 RPC 由服务端判断权限"))
		ui.SplitPercent(c, &a.split, func() { s.authNamesView(c, d) }, func() { s.authDetailView(c, d) }).Fill().MinHeight(0)
	})
}

func (s *State) authNamesView(c *ui.Context, d *Document) {
	a := d.authState()
	r := a.users
	title := locales.Text("用户目录")
	if a.category == "roles" {
		r = a.roles
		title = locales.Text("角色目录")
	}
	ui.Column(c).Fill().MinHeight(0).Gap(3).Children(func() {
		ui.Text(c, title).Bold()
		if r == nil || r.Result.Reply == nil || r.Result.Reply.Auth == nil {
			authHint(c, locales.Text("显式刷新后显示完整目录观察"))
			return
		}
		if !sameEventScope(r.Scope, d.scope) {
			authHint(c, locales.Text("这是原会话观察，请重新读取后再选择"))
		}
		names := r.Result.Reply.Auth.Users
		if a.category == "roles" {
			names = r.Result.Reply.Auth.Roles
		}
		filtered := make([]string, 0, len(names))
		for _, name := range names {
			if a.filter == "" || strings.Contains(strings.ToLower(name), strings.ToLower(a.filter)) {
				filtered = append(filtered, name)
			}
		}
		selected := a.selectedUser
		if a.category == "roles" {
			selected = a.selectedRole
		}
		row := -1
		for i, name := range filtered {
			if name == selected {
				row = i
				break
			}
		}
		a.row = row
		a.columns.Selected = &a.row
		theme.Table(c, &a.columns, []ui.TableColumn{{Title: locales.Text("完整名称"), ID: "name"}}, len(filtered), func(row, col int) {
			name := filtered[row]
			ui.Text(c, clipped(name)).SingleLine().FillWidth().MinWidth(0).Tooltip(clipped(name))
		}).Label(title).Fill().MinHeight(0)
		if a.row >= 0 && a.row < len(filtered) && filtered[a.row] != selected {
			s.SelectAuthName(d, filtered[a.row])
		}
		if ui.Button(c, locales.Text("复制完整名称")).Disabled(selected == "").Clicked() {
			c.WriteClipboard(selected)
		}
	})
}

func authPermissionGeometry(p contract.KVAuthPermission) string {
	if len(p.End) == 0 {
		return locales.Text("精确键")
	}
	if bytes.Equal(p.End, []byte{0}) {
		return locales.Text("无上界区间")
	}
	return locales.Text("右侧不含的区间")
}

func authBytesPreview(value []byte) string {
	text := base64.StdEncoding.EncodeToString(value[:min(len(value), 256)])
	if len(value) > 256 {
		text += "…"
	}
	return text
}

func (s *State) authDetailView(c *ui.Context, d *Document) {
	a := d.authState()
	r := a.userDetail
	users := a.category == "users"
	name := a.selectedUser
	if !users {
		r = a.roleDetail
		name = a.selectedRole
	}
	ui.Column(c).Fill().MinHeight(0).Gap(4).Children(func() {
		if r == nil || r.Result.Reply == nil || r.Result.Reply.Auth == nil {
			authHint(c, locales.Text("选择或输入名称，再显式读取详情"))
			return
		}
		var fields map[string]string
		if json.Unmarshal([]byte(r.Authored.JSON), &fields) != nil {
			return
		}
		target := fields["name"]
		if !users {
			target = fields["role"]
		}
		if target != name {
			authHint(c, locales.Text("选择或输入名称，再显式读取详情"))
			return
		}
		ui.Text(c, clipped(target)).Bold().SingleLine().Tooltip(clipped(target))
		authHint(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), r.Scope.Namespace.ProfileID, r.Scope.Generation))
		if !sameEventScope(r.Scope, d.scope) {
			authHint(c, locales.Text("这是原会话观察，请重新读取后再选择"))
		}
		if users {
			roles := r.Result.Reply.Auth.Roles
			a.detailColumns.Selected = &a.detailRow
			theme.Table(c, &a.detailColumns, []ui.TableColumn{{Title: locales.Text("已授予角色"), Width: 330}}, len(roles), func(row, col int) { ui.Text(c, clipped(roles[row])).SingleLine().Tooltip(clipped(roles[row])) }).Label(locales.Text("用户角色观察")).Fill().MinHeight(0)
			if ui.Button(c, locales.Text("复制完整角色名称")).Disabled(a.detailRow < 0 || a.detailRow >= len(roles)).Clicked() {
				c.WriteClipboard(roles[a.detailRow])
			}
		} else {
			permissions := r.Result.Reply.Auth.Permissions
			a.detailColumns.Selected = &a.detailRow
			theme.Table(c, &a.detailColumns, []ui.TableColumn{{Title: locales.Text("权限"), Width: 90}, {Title: locales.Text("区间语义"), Width: 135}, {Title: locales.Text("键 Base64 预览"), Width: 180}, {Title: locales.Text("终点 Base64 预览"), Width: 180}}, len(permissions), func(row, col int) {
				p := permissions[row]
				text := p.Access
				if col == 1 {
					text = authPermissionGeometry(p)
				} else if col == 2 {
					text = authBytesPreview(p.Key)
				} else if col == 3 {
					text = authBytesPreview(p.End)
				}
				ui.Text(c, text).SingleLine().Tooltip(text)
			}).Label(locales.Text("角色权限观察")).Fill().MinHeight(0)
			ui.Toolbar(c, func() {
				valid := a.detailRow >= 0 && a.detailRow < len(permissions)
				if ui.Button(c, locales.Text("复制完整权限")).Disabled(!valid).Clicked() {
					raw, _ := json.MarshalIndent(permissions[a.detailRow], "", "  ")
					c.WriteClipboard(string(raw))
				}
				if ui.Button(c, locales.Text("预览撤销所选权限")).Disabled(!valid || d.Busy() || d.needsRecovery || d.hasUnknownWrites() || !sameEventScope(r.Scope, d.scope)).Clicked() {
					s.OpenSelectedAuthPermission(d, true)
				}
			}).FillWidth().MinWidth(0).Padding(0).Gap(5)
		}
		if ui.Button(c, locales.Text("复制完整详情回执")).Clicked() {
			raw, err := encodeOperationReceipt(*r)
			if err == nil {
				c.WriteClipboard(string(raw))
			}
		}
	})
}

func authFormField(c *ui.Context, label string, target *string, secret bool) {
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label+"：").Width(145)
		field := ui.TextInput(c, target).Label(label).Grow(1).MinWidth(0)
		if secret {
			field.Password()
		}
	})
}

func (s *State) authPermissionForm(c *ui.Context, p *authDraft, revoke bool) {
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		if !revoke {
			ui.Select(c, &p.access, []string{"READ", "WRITE", "READWRITE"}).Label(locales.Text("权限")).Width(145)
		}
		ranges := []struct{ key, label string }{{"exact", locales.Text("精确键")}, {"range", locales.Text("右侧不含的区间")}, {"prefix", locales.Text("键前缀")}, {"all", locales.Text("所有键")}, {"unbounded", locales.Text("无上界区间")}}
		labels := []string{}
		choice := ""
		for _, r := range ranges {
			labels = append(labels, r.label)
			if r.key == p.rangeKind {
				choice = r.label
			}
		}
		if ui.Select(c, &choice, labels).Label(locales.Text("权限范围")).Grow(1).Changed() {
			for _, r := range ranges {
				if r.label == choice {
					p.rangeKind = r.key
				}
			}
		}
		encoding := locales.Text("文本")
		if p.encoding == "base64" {
			encoding = "Base64"
		}
		if ui.Select(c, &encoding, []string{locales.Text("文本"), "Base64"}).Label(locales.Text("键编码")).Width(130).Changed() {
			p.encoding = "text"
			if encoding == "Base64" {
				p.encoding = "base64"
			}
		}
	})
	if p.rangeKind != "all" {
		authFormField(c, locales.Text("权限键"), &p.key, false)
	}
	if p.rangeKind == "range" {
		authFormField(c, locales.Text("独占终点"), &p.end, false)
	}
	authHint(c, locales.Text("区间不含终点；无上界区间使用单 NUL 终点"))
}

func (s *State) authManagementModals(c *ui.Context) {
	for _, d := range s.documents {
		p := d.authDraft
		if !s.active(d) || p == nil || s.leave != nil {
			continue
		}
		theme.Modal(c.Key(fmt.Sprintf("kv-auth-form-%d", d.id)), &p.open, func() {
			ui.Column(c).Width(540).Gap(7).Children(func() {
				ui.Text(c, authActionLabel(p.method)).Bold()
				authHint(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), p.scope.Namespace.ProfileID, p.scope.Generation))
				authFormField(c, locales.Text("原始名称"), &p.name, false)
				if p.method == authPath("UserAdd") || p.method == authPath("UserChangePassword") {
					if p.method == authPath("UserAdd") && ui.Checkbox(c, &p.noPassword, locales.Text("无密码用户（证书身份）")).Changed() {
						if p.noPassword {
							p.password = ""
						}
					}
					if !p.noPassword {
						authFormField(c, locales.Text("临时密码"), &p.password, true)
					}
					authHint(c, locales.Text("密码只用于原执行计划；预览和回执明确省略密码"))
					if p.method == authPath("UserAdd") {
						ui.Text(c, locales.Text("初始角色（每行一个，保留名称空格）"))
						ui.TextArea(c, &p.initialRoles).Label(locales.Text("初始角色")).Height(65).FillWidth()
					}
				}
				if p.method == authPath("UserGrantRole") || p.method == authPath("UserRevokeRole") {
					authFormField(c, locales.Text("原始角色名称"), &p.role, false)
				}
				if p.method == authPath("RoleAdd") {
					ui.Checkbox(c, &p.initialPermission, locales.Text("创建后单独授予初始权限"))
				}
				if p.method == authPath("RoleGrantPermission") || p.method == authPath("RoleRevokePermission") || p.method == authPath("RoleAdd") && p.initialPermission {
					s.authPermissionForm(c, p, p.method == authPath("RoleRevokePermission"))
				}
				if d.error != "" {
					ui.Text(c, clipped(d.error)).TextColor(theme.Colors(c).Error).SingleLine().Tooltip(clipped(d.error))
				}
				ui.Row(c).Gap(6).Children(func() {
					if ui.Button(c, locales.Text("取消")).Clicked() {
						p.password = ""
						s.CancelConfirmation(d)
					}
					if ui.Button(c, locales.Text("预览 Auth 写入步骤")).Clicked() {
						s.PrepareAuthAction(d)
					}
				})
			})
		})
		if !p.open && d.authDraft == p {
			p.password = ""
			s.CancelConfirmation(d)
		}
	}
}

func (s *State) authConfirmationView(c *ui.Context, d *Document, p *confirmation) {
	r := p.auth.receipt
	ui.Column(c).Width(540).Gap(7).Children(func() {
		ui.Text(c, locales.Text("确认 Auth 原生写入步骤")).Bold()
		authHint(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), r.Scope.Namespace.ProfileID, r.Scope.Generation))
		authHint(c, locales.Text("各步骤是独立 RPC；失败或未知步骤会停止后续写入"))
		authHint(c, locales.Text("按名称和区间执行，没有 CAS 或身份重用保护"))
		var steps []string
		for i, request := range r.Requests {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal([]byte(request.Request.JSON), &fields)
			var name string
			for _, key := range []string{"name", "user", "role"} {
				if json.Unmarshal(fields[key], &name) == nil {
					break
				}
			}
			steps = append(steps, fmt.Sprintf("%d. %s: %s", i+1, authActionLabel(request.Request.Method), clipped(name)))
		}
		ui.Text(c, strings.Join(steps, " → ")).SingleLine().FillWidth().MinWidth(0).Tooltip(strings.Join(steps, "\n"))
		raw, _ := json.MarshalIndent(authRecord(r), "", "  ")
		preview := clipped(string(raw))
		area := ui.TextArea(c, &preview).Label(locales.Text("Auth 完整步骤预览")).ReadOnly(true).Height(160).FillWidth()
		seen := ui.Local(area, "auth-preview-text", func() string { return "" })
		if *seen != preview {
			area.SetTextSelection(0, 0)
			*seen = preview
		}
		authHint(c, locales.Text("密码已省略的预览记录不能直接作为执行请求"))
		ui.Toolbar(c, func() {
			if ui.Button(c, locales.Text("复制完整 Auth 预览")).Clicked() {
				c.WriteClipboard(string(raw))
			}
			if ui.Button(c, locales.Text("保存 Auth 预览记录")).Disabled(d.FileBusy() || s.deps.Files.SavePath == nil || s.deps.Files.WriteStream == nil).Clicked() {
				s.ExportAuthPreview(d)
			}
		}).FillWidth().MinWidth(0).Padding(0).Gap(5)
		ui.Checkbox(c, &p.checked, locales.Text("我确认顺序执行这些原生写入步骤"))
		ui.Row(c).Gap(6).Children(func() {
			if ui.Button(c, locales.Text("取消")).Clicked() {
				s.CancelConfirmation(d)
			}
			if ui.Button(c, locales.Text("确认执行 Auth 写入")).Disabled(!p.checked || d.Busy()).Clicked() {
				s.Confirm(d, p.checked)
			}
		})
	})
}
