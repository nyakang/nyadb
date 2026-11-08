package objectworkspace

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

var keyColumns = []ui.TableColumn{
	{ID: "name", Title: "密钥名称", Width: 150}, {ID: "key", Title: "API 密钥", Width: 90}, {ID: "uid", Title: "密钥 UID", Width: 290},
	{ID: "actions", Title: "密钥权限", Width: 220}, {ID: "indexes", Title: "索引范围", Width: 170}, {ID: "expires", Title: "到期时间", Width: 200},
	{ID: "description", Title: "密钥描述", Width: 220}, {ID: "created", Title: "创建时间", Width: 220}, {ID: "updated", Title: "修改时间", Width: 220},
}

func keyCell(r contract.KeyRecord, column int) string {
	switch column {
	case 0:
		return r.Name
	case 1:
		return "••••••••"
	case 2:
		return r.UID
	case 3:
		return strings.Join(r.Actions, ", ")
	case 4:
		return strings.Join(r.Indexes, ", ")
	case 5:
		if r.ExpiresAt == "" {
			return locales.Text("永不过期")
		}
		return r.ExpiresAt
	case 6:
		return r.Description
	case 7:
		return r.CreatedAt
	case 8:
		return r.UpdatedAt
	}
	return ""
}
func (s *State) keysView(c *ui.Context, d *Document) {
	x := d.keys
	p := theme.Colors(c)
	ready, modify := s.keysReady(d), s.CanModifyKey(d)
	ui.Column(c).Fill().Background(p.App).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Shrink(0).Padding(2, 8).Gap(8).AlignItems(ui.Center).Background(p.Header).Children(func() {
			name := "Meilisearch · " + locales.Text("API 密钥")
			if s.deps.ProfileName != nil {
				name += " · " + s.deps.ProfileName(d.profileID)
			}
			ui.Text(c, name).SingleLine().Grow(1).MinWidth(0).Tooltip(name)
			ui.Toolbar(c, func() {
				if ui.Button(c, locales.Text("刷新密钥列表")).Disabled(!ready || x.editing || x.preview).Clicked() {
					s.LoadKeys(d, c.Services().Invalidate, true)
				}
				if ui.Button(c, locales.Text("新建 API 密钥")).Disabled(!s.CanCreateKey(d) || x.editing || x.preview).Clicked() {
					s.EditKey(d, true)
				}
				if ui.Button(c, locales.Text("编辑密钥元数据")).Disabled(!modify || x.editing || x.preview).Clicked() {
					s.EditKey(d, false)
				}
				if ui.Button(c, locales.Text("预览删除密钥")).Disabled(!modify || x.editing || x.preview).Clicked() {
					_ = s.PrepareKey(d, true)
				}
				if ui.Button(c, locales.Text("读取选中密钥")).Disabled(!ready || x.record.Token == "" || x.preview).Clicked() {
					s.ReadKey(d, c.Services().Invalidate)
				}
				if ui.Button(c, locales.Text("恢复密钥连接")).Disabled(d.loading || s.session().ProfileID != d.profileID || !s.sessionFor(d.profileID).Connected).Clicked() {
					s.RecoverKeys(d)
				}
				if d.loading && ui.Button(c, locales.Text("停止密钥等待")).Disabled(x.cancelRequested).Clicked() {
					s.CancelKeys(d)
				}
				if ui.Button(c, locales.Text("返回")).Label(locales.Text("返回对象列表")).Clicked() && s.deps.Back != nil {
					x.hideSecret()
					if !s.guardKeysLeave(d, func(ui.Services) { s.deps.Back() }) {
						s.deps.Back()
					}
				}
			}).Label(locales.Text("密钥操作")).Grow(1).MinWidth(0).Gap(4)
		})
		if d.loading {
			message := locales.Text("正在处理原密钥请求…")
			if x.cancelRequested {
				message = locales.Text("正在等待原密钥请求停止…")
			}
			ui.Text(c, message).SingleLine().Padding(2, 8).Shrink(0).TextColor(p.Muted)
		}
		if d.err != "" {
			ui.Text(c, locales.Text(d.err)).SingleLine().Padding(2, 8).Shrink(0).TextColor(p.Muted).Tooltip(locales.Text(d.err))
		}
		if x.uncertain {
			ui.Text(c, locales.Text("密钥提交结果未知；原请求和草稿保留，不能重放。请检查原服务器密钥。")).SingleLine().Tooltip(locales.Text("密钥提交结果未知；原请求和草稿保留，不能重放。请检查原服务器密钥。")).Padding(2, 8).Shrink(0).TextColor(p.Error)
		}
		before := x.tab
		ui.Tabs(c, &x.tab, locales.Text("密钥列表"), locales.Text("密钥详情"), locales.Text("密钥草稿"), locales.Text("密钥请求与响应")).FillWidth().Shrink(0)
		if before != x.tab {
			x.hideSecret()
		}
		switch x.tab {
		case 0:
			s.keyListView(c, d)
		case 1:
			s.keyJSONView(c, d, x.record.Raw, "原始密钥详情预览")
		case 2:
			s.keyFormView(c, d)
		default:
			status := x.last.State
			label := locales.Text("未提交")
			switch status {
			case "completed":
				label = locales.Text("同步完成")
			case "rejected":
				label = locales.Text("请求被拒绝")
			case "uncertain":
				label = locales.Text("结果未知")
			}
			ui.Text(c, label+" · HTTP "+strconv.Itoa(x.last.Status)).SingleLine().Padding(2, 8).Shrink(0)
			raw := x.last.Request + "\n\n" + x.last.Response
			if x.diagnostic != "" && x.diagnostic != x.last.Response {
				raw += "\n\n" + x.diagnostic
			}
			s.keyJSONView(c, d, raw, "原始密钥请求与脱敏响应预览")
		}
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Shrink(0).Padding(2, 8).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Toolbar(c, func() {
				if ui.Button(c, locales.Text("查看原始密钥")).Disabled(!ready || x.record.Token == "" || x.editing || x.preview).Clicked() {
					s.RevealKey(d)
				}
				if ui.Button(c, locales.Text("复制原始密钥")).Disabled(!ready || x.record.Token == "" || x.editing || x.preview).Clicked() {
					if secret, err := s.KeySecret(d); err == nil {
						c.WriteClipboard(secret)
					} else {
						d.err = err.Error()
					}
				}
				if ui.Button(c, locales.Text("复制脱敏密钥详情")).Disabled(x.record.Raw == "").Clicked() {
					c.WriteClipboard(x.record.Raw)
				}
				if ui.Button(c, locales.Text("复制密钥请求")).Disabled(x.last.Request == "").Clicked() {
					c.WriteClipboard(x.last.Request)
				}
				if ui.Button(c, locales.Text("复制脱敏密钥响应")).Disabled(x.last.Response == "" && x.diagnostic == "").Clicked() {
					raw := x.last.Response
					if x.diagnostic != "" {
						raw = x.diagnostic
					}
					c.WriteClipboard(raw)
				}
			}).Label(locales.Text("密钥证据操作")).Grow(1).MinWidth(0).Gap(4)
		})
	})
	s.keyModals(c, d)
}
func (s *State) keyListView(c *ui.Context, d *Document) {
	x := d.keys
	rows := 0
	if x.page != nil {
		rows = len(x.page.Results)
	}
	if rows == 0 {
		theme.EmptyTable(c, &d.columns, keyColumns, locales.Text("没有已加载的密钥"))
	} else {
		old := x.selected
		d.columns.Selected, d.columns.Selection, d.columns.Key = &x.selected, nil, nil
		theme.Table(c, &d.columns, keyColumns, rows, func(row, col int) {
			text := keyCell(x.page.Results[row], col)
			ui.Text(c, text).SingleLine().Tooltip(text).FontSize(theme.Font(c, theme.SmallFontSize))
		}).Grow(1).MinHeight(0).Label(locales.Text("原连接密钥表格"))
		if old != x.selected {
			if d.loading || x.editing || x.preview {
				x.selected = old
			} else {
				s.SelectKey(d, x.selected)
			}
		}
	}
	ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Shrink(0).Padding(2, 8).Gap(6).AlignItems(ui.Center).Children(func() {
		ready := s.keysReady(d) && !x.editing && !x.preview
		previous, next := false, false
		label := locales.Text("实时分页；总数来自最近一次列表读取。")
		if x.page != nil {
			previous = x.page.Offset > 0
			next = x.page.Offset+uint64(rows) < x.page.Total
			label = fmt.Sprintf("OFFSET %d · %d / %d · %s", x.page.Offset, rows, x.page.Total, label)
		}
		ui.Text(c, label).SingleLine().Tooltip(label).Grow(1).MinWidth(0)
		choices := []string{"20", "50", "100"}
		limit := strconv.FormatUint(x.limit, 10)
		ui.Select(c, &limit, choices).Width(65).Label(locales.Text("密钥页大小")).Disabled(!ready)
		if value, e := strconv.ParseUint(limit, 10, 64); e == nil && value != x.limit {
			x.limit = value
			s.loadKeysPage(d, c.Services().Invalidate, 0)
		}
		if ui.Button(c, locales.Text("上一页密钥")).Disabled(!ready || !previous).Clicked() {
			s.PageKeys(d, c.Services().Invalidate, -1)
		}
		if ui.Button(c, locales.Text("下一页密钥")).Disabled(!ready || !next).Clicked() {
			s.PageKeys(d, c.Services().Invalidate, 1)
		}
	})
}
func (s *State) keyJSONView(c *ui.Context, d *Document, raw, label string) {
	preview, truncated := taskPreview(raw)
	if truncated {
		ui.Text(c, locales.Text("密钥 JSON 预览已截断；可复制完整脱敏内容。")).SingleLine().Padding(2, 8).Shrink(0)
	}
	area := ui.TextArea(c.Key(fmt.Sprintf("keys-json/%d/%d", d.id, d.keys.tab)), &preview).Label(locales.Text(label)).ReadOnly(true).Fill().MinHeight(0)
	seen := ui.Local(area, "keys-json-text", func() string { return "" })
	if *seen != preview {
		area.SetTextSelection(0, 0)
		*seen = preview
	}
}
func (s *State) keyFormView(c *ui.Context, d *Document) {
	x := d.keys
	ui.Scroll(c).Fill().MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Padding(8).Gap(6).Children(func() {
			for _, entry := range []struct {
				value  *string
				label  string
				policy bool
			}{{&x.form.UID, "密钥 UID（新建时可留空）", true}, {&x.form.Name, "密钥名称", false}, {&x.form.Description, "密钥描述", false}, {&x.form.Actions, "密钥权限（逗号分隔）", true}, {&x.form.Indexes, "索引范围（逗号分隔）", true}, {&x.form.ExpiresAt, "到期时间（RFC3339，可留空）", true}} {
				entry := entry
				theme.Field(c, locales.Text(entry.label), func() {
					editable := s.keysReady(d) && x.editing && !x.preview && !x.uncertain && (!entry.policy || x.action == "create")
					area := ui.TextInput(c.Key(fmt.Sprintf("keys-field/%d/%s", d.id, entry.label)), entry.value).Label(locales.Text(entry.label)).ReadOnly(!editable).FillWidth()
					theme.BindNativeEdit(c, area, fmt.Sprintf("keys/%d/%p", d.id, entry.value), s.deps.EditFocus, s.deps.EditTarget, func() bool { return d.KeysMode() }, editable, false)
				}).FillWidth()
			}
			ui.Text(c, locales.Text("权限与索引支持原生通配符；修改只允许名称和描述，到期时间留空表示永不过期。")).Tooltip(locales.Text("权限与索引支持原生通配符；修改只允许名称和描述，到期时间留空表示永不过期。")).SingleLine()
		})
	})
	ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Shrink(0).Padding(2, 8).Gap(6).Justify(ui.End).Children(func() {
		if ui.Button(c, locales.Text("放弃密钥草稿")).Disabled(d.loading || !x.editing).Clicked() {
			s.DiscardKeyDraft(d)
		}
		if ui.PrimaryButton(c, locales.Text("预览密钥更改")).Disabled(!s.CanCreateKey(d) || !x.dirty() || x.preview).Clicked() {
			_ = s.PrepareKey(d, false)
		}
	})
}
func (s *State) keyModals(c *ui.Context, d *Document) {
	x := d.keys
	p := theme.Colors(c)
	if x.preview && x.plan != nil {
		theme.Modal(c, &x.preview, func() {
			width, height := c.Size()
			ui.Column(c).Width(min(float32(760), width-60)).MaxHeight(height - 60).Gap(8).Children(func() {
				ui.Text(c, locales.Text("密钥操作预览")).Bold()
				ui.Text(c, locales.Text("提交前会再次检查原密钥；服务器没有原子比较与交换，并发修改仍可能发生。删除会使使用该密钥的连接失去权限。")).TextColor(p.Muted)
				raw := x.plan.Request + "\n" + x.plan.Body
				preview, truncated := taskPreview(raw)
				if truncated {
					ui.Text(c, locales.Text("密钥请求预览已截断；请先复制完整请求检查。"))
				}
				bodyHeight := height - 300
				if x.plan.Action == "delete" {
					bodyHeight -= 60
				}
				ui.TextArea(c.Key(fmt.Sprintf("keys-plan/%d/%d", d.id, x.plan.Sequence)), &preview).ReadOnly(true).Label(locales.Text("将提交的密钥请求")).Height(min(float32(260), bodyHeight)).MinHeight(50)
				if x.plan.Action == "delete" {
					theme.Field(c, locales.Text("输入原密钥 UID 确认删除"), func() {
						ui.TextInput(c, &x.confirmation).Label(locales.Text("输入原密钥 UID 确认删除")).FillWidth()
					}).FillWidth()
				}
				ui.Row(c).Justify(ui.End).Gap(8).Shrink(0).Children(func() {
					if ui.Button(c, locales.Text("复制密钥预览请求")).Clicked() && x.plan != nil {
						c.WriteClipboard(x.plan.Request + "\n" + x.plan.Body)
					}
					if ui.Button(c, locales.Text("返回编辑")).Clicked() {
						x.preview, x.plan = false, nil
					}
					if ui.PrimaryButton(c, locales.Text("确认提交密钥操作")).Disabled(!s.CanConfirmKey(d)).Clicked() {
						s.ApplyKey(d, c.Services().Invalidate)
					}
				})
			})
		})
		if !x.preview {
			x.plan = nil
		}
	}
	if x.secretOpen {
		theme.Modal(c, &x.secretOpen, func() {
			width, _ := c.Size()
			ui.Column(c).Width(min(float32(650), width-60)).Gap(10).Children(func() {
				ui.Text(c, locales.Text("原始密钥临时显示")).Bold()
				ui.Text(c, x.record.UID).SingleLine()
				ui.Text(c, x.secret).SingleLine().FontSize(theme.Font(c, theme.SmallFontSize))
				ui.Text(c, locales.Text("这是原观察的临时密钥；不代表当前仍有效，关闭窗口即隐藏。"))
				ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("复制原始密钥")).Clicked() {
						c.WriteClipboard(x.secret)
					}
					if ui.PrimaryButton(c, locales.Text("隐藏密钥")).Clicked() {
						x.hideSecret()
					}
				})
			})
		})
		if !x.secretOpen {
			x.hideSecret()
		}
	}
	if x.guard {
		theme.Modal(c, &x.guard, func() {
			width, _ := c.Size()
			ui.Column(c).Width(min(float32(600), width-60)).Gap(10).Children(func() {
				ui.Text(c, locales.Text("保留原密钥草稿与证据？")).Bold()
				ui.Text(c, locales.Text("离开将放弃本地草稿与证据。已发送的密钥操作不会回滚；结果未知的请求不能重放。"))
				ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("继续处理密钥")).Clicked() {
						x.guard, x.next = false, nil
					}
					if ui.Button(c, locales.Text("放弃本地密钥草稿并离开")).Disabled(d.loading).Clicked() {
						next := x.next
						x.guard, x.next, x.leaveAcknowledged = false, nil, true
						s.DiscardKeyDraft(d)
						if next != nil {
							next(c.Services())
						}
					}
				})
			})
		})
		if !x.guard {
			x.next = nil
		}
	}
}
