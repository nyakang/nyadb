package objectworkspace

import (
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

var snapshotActions = []struct{ action, label string }{{"create", "创建快照"}, {"delete", "删除快照"}, {"download", "下载快照"}, {"upload", "从本地归档恢复"}, {"recover", "从服务器位置恢复"}, {"stream", "直接下载分片"}}

func snapshotRequestText(r contract.SnapshotRequest) string {
	return r.Method + " " + r.Path + "\n" + string(r.Body)
}
func (s *State) snapshotsView(c *ui.Context, d *Document) {
	x := d.snapshots
	p := theme.Colors(c)
	ui.Column(c).Fill().MinHeight(0).Background(p.App).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Shrink(0).Padding(2, 8).Gap(8).AlignItems(ui.Center).Background(p.Header).Children(func() {
			title := locales.Text("快照管理")
			if s.deps.ProfileName != nil {
				title += " · " + s.deps.ProfileName(d.profileID)
			}
			ui.Text(c, title).SingleLine().Grow(1).MinWidth(0).Tooltip(title)
			if x.pending {
				if ui.Button(c, locales.Text("核验原快照操作")).Shrink(0).Disabled(!s.snapshotsReady(d)).Clicked() {
					s.VerifySnapshotManagement(d, c.Services().Invalidate)
				}
			}
			ui.Toolbar(c, func() {
				if !x.pending {
					if ui.Button(c, locales.Text("读取快照列表")).Disabled(!s.snapshotsReady(d) || x.guarding || x.preview).Clicked() {
						s.ReadSnapshotManagement(d, c.Services().Invalidate)
					}
					if ui.Button(c, locales.Text("预览快照操作")).Disabled(!s.snapshotsReady(d) || x.guarding || x.preview || x.observation == nil).Clicked() {
						_ = s.PrepareSnapshotManagement(d, c.Services().Invalidate)
					}
				}
				if d.loading && ui.Button(c, locales.Text("停止快照等待")).Disabled(x.cancelRequested).Clicked() {
					s.CancelSnapshotManagement(d)
				}
				if ui.Button(c, locales.Text("返回对象列表")).Clicked() && s.deps.Back != nil {
					if !s.guardSnapshotsLeave(d, func(ui.Services) { s.deps.Back() }) {
						s.deps.Back()
					}
				}
			}).Label(locales.Text("快照管理操作")).Grow(1).MinWidth(0).Gap(4)
		})
		message := d.err
		if d.loading {
			message = "正在等待原快照请求…"
			if x.cancelRequested {
				message = "正在等待原快照请求及文件关闭…"
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
		ui.Tabs(c, &x.tab, locales.Text("快照操作"), locales.Text("归档列表"), locales.Text("原请求与核验证据")).FillWidth().Shrink(0)
		switch x.tab {
		case 0:
			s.snapshotFormView(c, d)
		case 1:
			s.snapshotListView(c, d)
		default:
			status := "未提交"
			switch x.last.State {
			case "response":
				status = "收到原生回执，等待核验"
			case "uncertain", "indeterminate", "enqueued":
				status = "核验未完成"
			case "applied":
				status = "已核验操作生效"
			case "unchanged":
				status = "已核验，原生回执为 false"
			case "observed":
				status = "已观察恢复后集合，请核对向量与业务数据"
			case "transferred":
				status = "归档下载完成"
			case "transfer_incomplete":
				status = "归档下载未完成"
			}
			result := ""
			if x.last.Receipt.HasResult {
				result = " · result=" + strconv.FormatBool(x.last.Receipt.Result)
			}
			if x.last.Receipt.HasNullResult {
				result = " · result=null"
			}
			ui.Text(c, locales.Text(status)+result).SingleLine().Tooltip(locales.Text(status)+result).Padding(2, 8).Shrink(0)
			if x.last.HasCount {
				ui.Text(c, locales.Text("物理集合实时点数")+": "+strconv.FormatUint(x.last.Count, 10)).SingleLine().Padding(2, 8).Shrink(0)
				ui.Text(c, locales.Text("实时集合计数不是分片计数，也不能证明完整归档与全部数据等价。")).SingleLine().Tooltip(locales.Text("实时集合计数不是分片计数，也不能证明完整归档与全部数据等价。")).Padding(2, 8).Shrink(0)
			}
			if x.file.Partial != "" {
				text := locales.Text("部分归档保留在") + ": " + x.file.Partial
				ui.Text(c, text).SingleLine().Tooltip(text).TextColor(p.Error).Padding(2, 8).Shrink(0)
			}
			if x.file.Published {
				text := locales.Text("归档已保存到") + ": " + x.file.Destination
				ui.Text(c, text).SingleLine().Tooltip(text).Padding(2, 8).Shrink(0)
			}
			s.snapshotJSONView(c, d, snapshotDisplayEvidence(x), "快照原请求与证据")
		}
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Shrink(0).Padding(2, 8).Children(func() {
			ui.Toolbar(c, func() {
				if ui.Button(c, locales.Text("复制完整快照请求")).Disabled(x.request.Method == "").Clicked() {
					c.WriteClipboard(snapshotRequestText(x.request))
				}
				if ui.Button(c, locales.Text("复制原快照回执")).Disabled(len(x.last.Receipt.Response.Body) == 0).Clicked() {
					c.WriteClipboard(string(x.last.Receipt.Response.Body))
				}
				if ui.Button(c, locales.Text("复制完整核验证据")).Disabled(x.observation == nil && x.request.Method == "" && len(x.readEvidence.Reads) == 0).Clicked() {
					c.WriteClipboard(snapshotEvidence(x))
				}
			}).Label(locales.Text("快照证据操作")).Grow(1).MinWidth(0).Gap(4)
		})
	})
	s.snapshotModals(c, d)
}
func (s *State) snapshotFormView(c *ui.Context, d *Document) {
	x := d.snapshots
	f := x.form
	editable := s.snapshotsReady(d) && !x.pending && !x.preview && !x.guarding
	ui.Scroll(c).Fill().MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Padding(8).Gap(6).Children(func() {
			theme.Field(c, locales.Text("快照范围"), func() {
				choices := []string{locales.Text("集合快照"), locales.Text("分片快照"), locales.Text("整库快照")}
				scopes := []contract.SnapshotScope{contract.SnapshotCollection, contract.SnapshotShard, contract.SnapshotStorage}
				selected := choices[0]
				for i, v := range scopes {
					if v == f.Scope {
						selected = choices[i]
					}
				}
				ui.Select(c, &selected, choices).Disabled(!editable).FillWidth().Label(locales.Text("快照范围"))
				for i, label := range choices {
					if label == selected {
						f.Scope = scopes[i]
					}
				}
			}).FillWidth()
			theme.Field(c, locales.Text("管理操作"), func() {
				choices := []string{}
				selected := ""
				for _, entry := range snapshotActions {
					if f.Scope == contract.SnapshotStorage && (entry.action == "upload" || entry.action == "recover" || entry.action == "stream") || f.Scope != contract.SnapshotShard && entry.action == "stream" {
						continue
					}
					label := locales.Text(entry.label)
					choices = append(choices, label)
					if entry.action == f.Action {
						selected = label
					}
				}
				if selected == "" {
					selected = locales.Text("创建快照")
					f.Action = "create"
				}
				ui.Select(c, &selected, choices).Disabled(!editable).FillWidth().Label(locales.Text("管理操作"))
				for _, entry := range snapshotActions {
					if locales.Text(entry.label) == selected {
						f.Action = entry.action
					}
				}
			}).FillWidth()
			fields := []struct {
				value *string
				label string
			}{{&f.Limit, "归档大小上限（MiB）"}, {&f.Timeout, "快照等待超时（秒）"}}
			if f.Scope != contract.SnapshotStorage {
				fields = append([]struct {
					value *string
					label string
				}{{&f.Collection, "物理集合名称"}}, fields...)
			} else {
				ui.Text(c, locales.Text("整库归档仅支持创建、下载和删除；恢复需在 Qdrant 停机启动时加载归档。")).SingleLine().Tooltip(locales.Text("整库归档仅支持创建、下载和删除；恢复需在 Qdrant 停机启动时加载归档。"))
			}
			if f.Scope == contract.SnapshotShard {
				fields = append(fields, struct {
					value *string
					label string
				}{&f.Shard, "分片编号"})
			}
			if f.Action == "recover" {
				fields = append(fields, struct {
					value *string
					label string
				}{&f.Location, "服务器位置或 URL"}, struct {
					value *string
					label string
				}{&f.Checksum, "源归档 SHA-256（可选）"})
			}
			for _, field := range fields {
				field := field
				theme.Field(c, locales.Text(field.label), func() {
					e := ui.TextInput(c.Key(fmt.Sprintf("snapshots/%d/%s", d.id, field.label)), field.value).Label(locales.Text(field.label)).ReadOnly(!editable).FillWidth()
					theme.BindNativeEdit(c, e, fmt.Sprintf("snapshots/%d/%s", d.id, field.label), s.deps.EditFocus, s.deps.EditTarget, func() bool { return s.snapshotsReady(d) && !x.pending && !x.preview && !x.guarding }, editable, false)
				}).FillWidth()
			}
			if f.Action == "upload" || f.Action == "recover" {
				theme.Field(c, locales.Text("恢复优先级"), func() {
					ui.Select(c, &f.Priority, []string{"snapshot", "replica", "no_sync"}).Disabled(!editable).FillWidth().Label(locales.Text("恢复优先级"))
				}).FillWidth()
			}
			if f.Action == "upload" || f.Action == "download" || f.Action == "stream" {
				save := f.Action != "upload"
				label, path := "选择源归档", f.Source
				if save {
					label, path = "选择下载目标", f.Destination
				}
				ui.Row(c).Gap(8).FillWidth().Children(func() {
					ui.Text(c, path).Grow(1).MinWidth(0).SingleLine().Tooltip(path)
					if ui.Button(c, locales.Text(label)).Disabled(!editable).Clicked() {
						s.SetSnapshotManagementForm(d, f)
						s.SnapshotFilePicker(d, save)
						f = x.form
					}
				})
			}
			if f.Action == "delete" || f.Action == "download" {
				selected := s.snapshotSelected(d)
				ui.Text(c, locales.Text("选定归档")+": "+selected.Name+" · "+strconv.FormatUint(selected.Size, 10)+" bytes").SingleLine().Tooltip(selected.Name)
			}
		})
	})
	if editable && f != x.form {
		s.SetSnapshotManagementForm(d, f)
	}
}
func (s *State) snapshotListView(c *ui.Context, d *Document) {
	x := d.snapshots
	columns := []ui.TableColumn{{ID: "name", Title: locales.Text("归档名称"), Width: 360}, {ID: "size", Title: locales.Text("大小（字节）"), Width: 150}, {ID: "created", Title: locales.Text("创建时间"), Width: 240}, {ID: "checksum", Title: "SHA-256", Width: 420}}
	if len(x.snapshot.Snapshots) == 0 {
		theme.EmptyTable(c, &d.columns, columns, locales.Text("没有已加载的快照归档"))
		return
	}
	old := x.selected
	d.columns.Selected, d.columns.Selection, d.columns.Key = &x.selected, nil, nil
	theme.Table(c, &d.columns, columns, len(x.snapshot.Snapshots), func(row, col int) {
		entry := x.snapshot.Snapshots[row]
		text := ""
		switch col {
		case 0:
			text = entry.Name
		case 1:
			text = strconv.FormatUint(entry.Size, 10)
		case 2:
			text = entry.CreationTime
		case 3:
			text = entry.Checksum
		}
		ui.Text(c, text).SingleLine().Tooltip(text).FontSize(theme.Font(c, theme.SmallFontSize))
	}).Grow(1).MinHeight(0).Label(locales.Text("原会话快照归档表格"))
	if old != x.selected {
		row := x.selected
		x.selected = old
		s.SelectSnapshot(d, row)
	}
}
func (s *State) snapshotJSONView(c *ui.Context, d *Document, raw, label string) {
	preview, truncated := taskPreview(raw)
	if truncated {
		ui.Text(c, locales.Text("信息预览已截断，可复制完整核验证据。")).SingleLine().Padding(2, 8).Shrink(0)
	}
	ui.TextArea(c.Key(fmt.Sprintf("snapshots-json/%d/%d", d.id, d.snapshots.tab)), &preview).ReadOnly(true).Label(locales.Text(label)).Fill().MinHeight(0)
}
func (s *State) snapshotModals(c *ui.Context, d *Document) {
	x := d.snapshots
	if x.preview && x.plan != nil {
		theme.Modal(c, &x.preview, func() {
			width, height := c.Size()
			ui.Column(c).Width(min(float32(760), width-60)).MaxHeight(height - 60).Gap(8).Children(func() {
				ui.Text(c, locales.Text("快照操作预览")).Bold()
				if x.plan.RestoresData() {
					ui.Text(c, locales.Text("恢复会替换目标集合内容；提交前处理原 REST 与相关数据文档，并重新核对原信息。"))
				}
				preview, truncated := taskPreview(snapshotRequestText(x.request))
				ui.TextArea(c.Key(fmt.Sprintf("snapshots-plan/%d/%d", d.id, x.revision)), &preview).ReadOnly(true).Label(locales.Text("将提交的快照请求")).Height(min(float32(190), height-365)).MinHeight(40)
				if x.source.Name != "" {
					ui.Text(c, x.form.Source+" · "+strconv.FormatUint(x.source.Size, 10)+" bytes").SingleLine().Tooltip(x.form.Source)
					ui.Text(c, "SHA-256: "+x.source.SHA256).SingleLine().Tooltip(x.source.SHA256)
				}
				if x.output != nil {
					ui.Text(c, x.output.Path()).SingleLine().Tooltip(x.output.Path())
					ui.Text(c, locales.Text("下载完成后保存到目标文件；目标变化时保留临时归档并停止发布。"))
				}
				if truncated {
					ui.Checkbox(c, &x.reviewed, locales.Text("已复制并检查完整快照请求"))
				}
				if snapshotConfirmation(x.form, s.snapshotSelected(d)) != "" {
					theme.Field(c, locales.Text("输入原名称确认操作"), func() {
						ui.TextInput(c, &x.confirmation).Label(locales.Text("输入原名称确认操作")).FillWidth()
					}).FillWidth()
				}
				ui.Row(c).Justify(ui.End).Gap(8).Shrink(0).Children(func() {
					if ui.Button(c, locales.Text("复制完整快照请求")).Clicked() {
						c.WriteClipboard(snapshotRequestText(x.request))
					}
					if ui.Button(c, locales.Text("返回编辑")).Clicked() {
						x.preview, x.plan, x.guarding = false, nil, false
						x.revision++
					}
					if ui.PrimaryButton(c, locales.Text("确认提交快照操作")).Disabled(!s.CanSubmitSnapshotManagement(d)).Clicked() {
						s.SubmitSnapshotManagement(d, c.Services())
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
				ui.Text(c, locales.Text("保留原快照草稿与证据？")).Bold()
				ui.Text(c, locales.Text("离开将放弃本地草稿与证据。已发送操作不会回滚，结果未知的原请求不能重放。"))
				ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("继续处理快照操作")).Clicked() {
						x.guard, x.next = false, nil
					}
					if d.loading && ui.Button(c, locales.Text("停止快照等待")).Disabled(x.cancelRequested).Clicked() {
						s.CancelSnapshotManagement(d)
					}
					if ui.Button(c, locales.Text("放弃原快照草稿并离开")).Disabled(d.loading).Clicked() {
						next := x.next
						s.abandonSnapshots(d)
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
