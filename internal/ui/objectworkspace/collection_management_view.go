package objectworkspace

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

var collectionActions = []struct{ action, label string }{
	{"collection_create", "创建集合"}, {"collection_update", "修改集合配置"}, {"collection_delete", "删除集合"},
	{"alias_create", "创建别名"}, {"alias_delete", "删除别名"}, {"alias_rename", "重命名别名"}, {"alias_retarget", "重定向别名"},
}

func collectionRequestText(r contract.CollectionManagementRequest) string {
	return r.Method + " " + r.Path + "\n" + string(r.Body)
}

// Display native collection/configuration and alias names. Full original-profile
// identity and bounded HTTP evidence are retained by the explicit copy action.
func collectionSnapshotText(snapshot contract.CollectionManagementSnapshot) string {
	type configuration struct {
		Name          string          `json:"name"`
		Exists        bool            `json:"exists"`
		Configuration json.RawMessage `json:"configuration,omitempty"`
	}
	type alias struct {
		Alias      string `json:"alias"`
		Collection string `json:"collection"`
	}
	output := struct {
		Collections []configuration `json:"collections"`
		Aliases     []alias         `json:"aliases"`
	}{Collections: []configuration{}, Aliases: []alias{}}
	targets := snapshot.Targets
	if len(targets) == 0 {
		targets = snapshot.Collections
	}
	for _, ref := range targets {
		entry := configuration{Name: ref.Catalog}
		for _, existing := range snapshot.Collections {
			if existing == ref {
				entry.Exists = true
			}
		}
		for _, cfg := range snapshot.Configurations {
			if cfg.Collection == ref && json.Valid([]byte(cfg.JSON)) {
				entry.Configuration = json.RawMessage(cfg.JSON)
			}
		}
		output.Collections = append(output.Collections, entry)
	}
	for _, a := range snapshot.Aliases {
		for _, ref := range targets {
			if a.Collection == ref {
				output.Aliases = append(output.Aliases, alias{a.Alias.Catalog, a.Collection.Catalog})
				break
			}
		}
	}
	raw, _ := json.MarshalIndent(output, "", "  ")
	return string(raw)
}
func (s *State) collectionsView(c *ui.Context, d *Document) {
	x := d.collections
	p := theme.Colors(c)
	ui.Column(c).Fill().MinHeight(0).Background(p.App).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Shrink(0).Padding(2, 8).Gap(8).AlignItems(ui.Center).Background(p.Header).Children(func() {
			title := locales.Text("集合与别名管理")
			if s.deps.ProfileName != nil {
				title += " · " + s.deps.ProfileName(d.profileID)
			}
			ui.Text(c, title).SingleLine().Grow(1).MinWidth(0).Tooltip(title)
			ui.Toolbar(c, func() {
				if !x.pending {
					if ui.Button(c, locales.Text("读取原集合信息")).Disabled(!s.collectionsReady(d) || x.guarding || x.preview).Clicked() {
						s.ReadCollectionManagement(d, c.Services().Invalidate)
					}
					if ui.Button(c, locales.Text("预览集合操作")).Disabled(!s.collectionsReady(d) || x.guarding || x.observation == nil || x.preview).Clicked() {
						_ = s.PrepareCollectionManagement(d)
					}
				} else {
					if ui.Button(c, locales.Text("核验原集合操作")).Disabled(!s.collectionsReady(d)).Clicked() {
						s.VerifyCollectionManagement(d, c.Services().Invalidate)
					}
				}
				if d.loading && ui.Button(c, locales.Text("停止管理等待")).Disabled(x.cancelRequested).Clicked() {
					s.CancelCollectionManagement(d)
				}
				if ui.Button(c, locales.Text("返回对象列表")).Clicked() && s.deps.Back != nil {
					if !s.guardCollectionsLeave(d, func(ui.Services) { s.deps.Back() }) {
						s.deps.Back()
					}
				}
			}).Label(locales.Text("集合管理操作")).Grow(1).MinWidth(0).Gap(4)
		})
		message := d.err
		if d.loading {
			message = "正在等待原集合管理请求…"
			if x.cancelRequested {
				message = "正在等待原集合管理请求停止…"
			}
		}
		if x.guarding {
			message = "请先处理原连接的相关文档；原操作尚未提交"
		}
		if x.abandoned {
			message = "此原操作已放弃，仅供查看；请关闭此页后重新管理"
		}
		if message != "" {
			ui.Text(c, locales.Text(message)).SingleLine().Tooltip(locales.Text(message)).TextColor(p.Muted).Padding(2, 8).Shrink(0)
		}
		if x.pending {
			ui.Text(c, locales.Text("原操作尚未核验；保留草稿与回执，不可重放。核验只读取原会话。")).SingleLine().Tooltip(locales.Text("原操作尚未核验；保留草稿与回执，不可重放。核验只读取原会话。")).TextColor(p.Error).Padding(2, 8).Shrink(0)
		}
		ui.Tabs(c, &x.tab, locales.Text("管理草稿"), locales.Text("原集合与别名"), locales.Text("原请求与核验证据")).FillWidth().Shrink(0)
		switch x.tab {
		case 0:
			s.collectionFormView(c, d)
		case 1:
			s.collectionJSONView(c, d, collectionSnapshotText(x.snapshot), "原集合与别名信息预览")
		default:
			status := locales.Text("未提交")
			switch x.last.State {
			case "response":
				status = locales.Text("收到原生回执，等待核验")
			case "uncertain", "indeterminate":
				status = locales.Text("核验未完成")
			case "applied":
				status = locales.Text("已核验操作生效")
			case "unchanged":
				status = locales.Text("已核验，原生回执为 false")
			}
			boolean := ""
			if x.last.Receipt.HasResult {
				boolean = " · result=" + strconv.FormatBool(x.last.Receipt.Result)
			}
			ui.Text(c, status+" · HTTP "+strconv.Itoa(x.last.Receipt.Response.Status)+boolean).SingleLine().Padding(2, 8).Shrink(0)
			s.collectionJSONView(c, d, collectionRequestText(x.request)+"\n\n"+string(x.last.Receipt.Response.Body)+"\n\n"+collectionSnapshotText(x.last.Snapshot), "原集合请求与回执预览")
		}
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Shrink(0).Padding(2, 8).Gap(6).Children(func() {
			ui.Toolbar(c, func() {
				if ui.Button(c, locales.Text("复制完整集合请求")).Disabled(x.request.Method == "").Clicked() {
					c.WriteClipboard(collectionRequestText(x.request))
				}
				if ui.Button(c, locales.Text("复制原集合回执")).Disabled(len(x.last.Receipt.Response.Body) == 0).Clicked() {
					c.WriteClipboard(string(x.last.Receipt.Response.Body))
				}
				if ui.Button(c, locales.Text("复制完整核验证据")).Disabled(x.observation == nil && len(x.readEvidence.Reads) == 0 && x.request.Method == "").Clicked() {
					c.WriteClipboard(collectionEvidence(x))
				}
			}).Label(locales.Text("集合证据操作")).Grow(1).MinWidth(0).Gap(4)
		})
	})
	s.collectionModals(c, d)
}
func (s *State) collectionFormView(c *ui.Context, d *Document) {
	x := d.collections
	before := x.form
	editable := s.collectionsReady(d) && !x.pending && !x.guarding && !x.preview
	ui.Scroll(c).Fill().MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Padding(8).Gap(6).Children(func() {
			theme.Field(c, locales.Text("管理操作"), func() {
				choices := []string{}
				action := ""
				for _, entry := range collectionActions {
					label := locales.Text(entry.label)
					choices = append(choices, label)
					if entry.action == x.form.Action {
						action = label
					}
				}
				ui.Select(c, &action, choices).Disabled(!editable).FillWidth().Label(locales.Text("管理操作"))
				for _, entry := range collectionActions {
					if action == locales.Text(entry.label) {
						x.form.Action = entry.action
					}
				}
			}).FillWidth()
			fields := []struct {
				value *string
				label string
			}{{&x.form.Collection, "物理集合名称"}, {&x.form.Timeout, "服务器提交超时（秒）"}}
			if x.form.Action == "alias_create" || x.form.Action == "alias_delete" || x.form.Action == "alias_rename" || x.form.Action == "alias_retarget" {
				fields = append(fields, struct {
					value *string
					label string
				}{&x.form.Alias, "原别名"})
			}
			if x.form.Action == "alias_retarget" {
				fields = append(fields, struct {
					value *string
					label string
				}{&x.form.DestinationCollection, "目标物理集合名称"})
			}
			if x.form.Action == "alias_rename" {
				fields = append(fields, struct {
					value *string
					label string
				}{&x.form.DestinationAlias, "目标别名"})
			}
			for _, field := range fields {
				field := field
				theme.Field(c, locales.Text(field.label), func() {
					e := ui.TextInput(c.Key(fmt.Sprintf("collections/%d/%s", d.id, field.label)), field.value).Label(locales.Text(field.label)).ReadOnly(!editable).FillWidth()
					theme.BindNativeEdit(c, e, fmt.Sprintf("collections/%d/%p", d.id, field.value), s.deps.EditFocus, s.deps.EditTarget, func() bool {
						return s.collectionsReady(d) && !d.collections.pending && !d.collections.preview && !d.collections.guarding
					}, editable, false)
				}).FillWidth()
			}
			if x.form.Action == "collection_create" || x.form.Action == "collection_update" {
				ui.Text(c, locales.Text("原生配置 JSON；创建时填写向量配置，更新只填写要修改的字段。")).SingleLine().Tooltip(locales.Text("原生配置 JSON；创建时填写向量配置，更新只填写要修改的字段。"))
				if x.form.Action == "collection_create" {
					ui.Text(c, `{"vectors":{"size":3,"distance":"Cosine"}}`).SingleLine().Tooltip(`{"vectors":{"size":3,"distance":"Cosine"}}`)
				}
				e := ui.TextArea(c.Key(fmt.Sprintf("collections-config/%d", d.id)), &x.form.Configuration).Label(locales.Text("原生集合配置 JSON")).ReadOnly(!editable).FillWidth().Height(180)
				theme.BindNativeEdit(c, e, fmt.Sprintf("collections-config/%d", d.id), s.deps.EditFocus, s.deps.EditTarget, func() bool {
					return s.collectionsReady(d) && !d.collections.pending && !d.collections.preview && !d.collections.guarding
				}, editable, false)
				ui.Text(c, locales.Text("metadata 更新按顶层键合并，null 删除键，嵌套对象整体替换；首次设置保留 null 键。")).SingleLine().Tooltip(locales.Text("metadata 更新按顶层键合并，null 删除键，嵌套对象整体替换；首次设置保留 null 键。"))
			}
			if x.form.Action == "alias_retarget" {
				ui.Text(c, locales.Text("重定向在一次原生请求中按顺序删除并重建别名。")).SingleLine()
			}
		})
	})
	if before != x.form {
		x.plan = nil
		x.preview = false
		x.revision++
		x.leaveAcknowledged = false
		x.confirmation = ""
		x.reviewed = false
	}
}
func (s *State) collectionJSONView(c *ui.Context, d *Document, raw, label string) {
	preview, truncated := taskPreview(raw)
	if truncated {
		ui.Text(c, locales.Text("集合信息预览已截断，可复制完整内容。")).SingleLine().Padding(2, 8).Shrink(0)
	}
	area := ui.TextArea(c.Key(fmt.Sprintf("collections-json/%d/%d", d.id, d.collections.tab)), &preview).ReadOnly(true).Label(locales.Text(label)).Fill().MinHeight(0)
	seen := ui.Local(area, "collections-json-text", func() string { return "" })
	if *seen != preview {
		area.SetTextSelection(0, 0)
		*seen = preview
	}
}
func (s *State) collectionModals(c *ui.Context, d *Document) {
	x := d.collections
	p := theme.Colors(c)
	if x.preview && x.plan != nil {
		theme.Modal(c, &x.preview, func() {
			width, height := c.Size()
			ui.Column(c).Width(min(float32(760), width-60)).MaxHeight(height - 60).Gap(8).Children(func() {
				ui.Text(c, locales.Text("集合操作预览")).Bold()
				ui.Text(c, locales.Text("提交前会关闭原 REST 及相关数据文档，并重新核对原信息；并发修改仍可能发生。")).TextColor(p.Muted)
				preview, truncated := taskPreview(collectionRequestText(x.request))
				ui.TextArea(c.Key(fmt.Sprintf("collections-plan/%d/%d", d.id, x.revision)), &preview).ReadOnly(true).Label(locales.Text("将提交的集合请求")).Height(min(float32(230), height-325)).MinHeight(40)
				if truncated {
					ui.Checkbox(c, &x.reviewed, locales.Text("已复制并检查完整集合请求"))
				}
				if confirmation := collectionConfirmation(x.form); confirmation != "" {
					theme.Field(c, locales.Text("输入原名称确认操作"), func() {
						ui.TextInput(c, &x.confirmation).Label(locales.Text("输入原名称确认操作")).FillWidth()
					}).FillWidth()
				}
				ui.Row(c).Justify(ui.End).Gap(8).Shrink(0).Children(func() {
					if ui.Button(c, locales.Text("复制完整集合请求")).Clicked() {
						c.WriteClipboard(collectionRequestText(x.request))
					}
					if ui.Button(c, locales.Text("返回编辑")).Clicked() {
						x.preview, x.plan, x.guarding = false, nil, false
						x.revision++
					}
					if ui.PrimaryButton(c, locales.Text("确认提交集合操作")).Disabled(!s.CanSubmitCollectionManagement(d)).Clicked() {
						s.SubmitCollectionManagement(d, c.Services())
					}
				})
			})
		})
		if !x.preview {
			x.plan = nil
			x.guarding = false
			x.revision++
		}
	}
	if x.guard {
		theme.Modal(c, &x.guard, func() {
			width, _ := c.Size()
			ui.Column(c).Width(min(float32(600), width-60)).Gap(10).Children(func() {
				ui.Text(c, locales.Text("保留原集合草稿与证据？")).Bold()
				ui.Text(c, locales.Text("离开将放弃本地草稿与证据。已发送操作不会回滚，结果未知的原请求不能重放。"))
				ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("继续处理集合操作")).Clicked() {
						x.guard, x.next = false, nil
					}
					if d.loading && ui.Button(c, locales.Text("停止管理等待")).Disabled(x.cancelRequested).Clicked() {
						s.CancelCollectionManagement(d)
					}
					if ui.Button(c, locales.Text("放弃原集合草稿并离开")).Disabled(d.loading).Clicked() {
						next := x.next
						s.abandonCollections(d)
						x.guard, x.next = false, nil
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
