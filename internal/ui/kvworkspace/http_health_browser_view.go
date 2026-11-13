package kvworkspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func healthCellText(present, nulls []string, name, text string) string {
	if !slices.Contains(present, name) {
		return locales.Text("（未返回）")
	}
	if slices.Contains(nulls, name) {
		return "null"
	}
	if text == "" {
		return `""`
	}
	if len(text) <= 256 {
		return text
	}
	end := 256
	for !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + "…"
}

func healthCheckCell(c contract.KVHTTPHealthCheck, column int) string {
	if column == 8 {
		return healthCellText(c.PresentFields, c.NullFields, "Namespace", c.Namespace)
	}
	if column == 9 {
		return healthCellText(c.PresentFields, c.NullFields, "Partition", c.Partition)
	}
	if column == 7 {
		if c.Maintenance() {
			return locales.Text("维护检查")
		}
		return ""
	}
	names := []string{"Node", "CheckID", "Name", "Status", "ServiceID", "CreateIndex", "ModifyIndex"}
	values := []string{c.Node, c.CheckID, c.Name, c.Status, c.ServiceID, strconv.FormatUint(c.CreateIndex, 10), strconv.FormatUint(c.ModifyIndex, 10)}
	name := names[column]
	if name == "CheckID" && !slices.Contains(c.PresentFields, name) && slices.Contains(c.PresentFields, "CheckId") {
		name = "CheckId"
	}
	if name == "ServiceID" && !slices.Contains(c.PresentFields, name) && slices.Contains(c.PresentFields, "ServiceId") {
		name = "ServiceId"
	}
	return healthCellText(c.PresentFields, c.NullFields, name, values[column])
}

func healthInstanceCell(v contract.KVHTTPHealthInstance, column int) string {
	if column == 8 {
		return healthCellText(v.Service.PresentFields, v.Service.NullFields, "Namespace", v.Service.Namespace)
	}
	if column == 9 {
		return healthCellText(v.Service.PresentFields, v.Service.NullFields, "Partition", v.Service.Partition)
	}
	if column == 0 {
		return healthCellText(v.Node.PresentFields, v.Node.NullFields, "Node", v.Node.Node)
	}
	if column == 5 {
		return healthCellText(v.PresentFields, v.NullFields, "Checks", strconv.Itoa(len(v.Checks)))
	}
	names := map[int]string{1: "Service", 2: "ID", 3: "Address", 4: "Port", 6: "CreateIndex", 7: "ModifyIndex"}
	values := map[int]string{1: v.Service.Service, 2: v.Service.ID, 3: v.Service.Address, 4: strconv.FormatUint(v.Service.Port, 10), 6: strconv.FormatUint(v.Service.CreateIndex, 10), 7: strconv.FormatUint(v.Service.ModifyIndex, 10)}
	name := names[column]
	if name == "ID" && !slices.Contains(v.Service.PresentFields, name) && slices.Contains(v.Service.PresentFields, "Id") {
		name = "Id"
	}
	return healthCellText(v.Service.PresentFields, v.Service.NullFields, name, values[column])
}

func (s *State) httpHealthBrowserView(c *ui.Context, d *Document, r *Receipt) {
	if d.httpHealthBrowser == nil {
		d.httpHealthBrowser = &httpHealthBrowserState{}
	}
	b := d.httpHealthBrowser
	b.bind(r)
	projection := healthReceiptProjection(r)
	if projection == nil {
		return
	}
	p := theme.Colors(c)
	if !projection.Complete {
		ui.Text(c, locales.Text("健康响应不完整或受 ACL 过滤；仅展示原响应返回的观察")).SingleLine().Tooltip(locales.Text("健康响应不完整或受 ACL 过滤；仅展示原响应返回的观察")).TextColor(p.Warning)
	}
	if b.detail {
		ui.Toolbar(c, func() {
			if ui.Button(c, locales.Text("返回健康列表")).Clicked() {
				b.detail, b.preview = false, ""
			}
		}).FillWidth()
		ui.Text(c, locales.Text("此详情来自所选原始响应；预览最多 64 KiB，完整内容保留在 HTTP 回执")).SingleLine().Tooltip(locales.Text("此详情来自所选原始响应；预览最多 64 KiB，完整内容保留在 HTTP 回执")).TextColor(p.Muted)
		ui.TextArea(c, &b.preview).Label(locales.Text("健康检查或实例原始详情")).ReadOnly(true).Fill().MinHeight(0)
		return
	}
	ui.Row(c).FillWidth().MinWidth(0).Gap(6).Children(func() {
		ui.TextInput(c, &b.query).Label(locales.Text("筛选已返回的健康观察")).Placeholder(locales.Text("筛选已返回的健康观察")).Grow(1).MinWidth(0)
		options := []string{locales.Text("全部检查状态"), "passing", "warning", "critical", locales.Text("维护检查"), locales.Text("其它或未返回状态")}
		choice := options[int(b.status)]
		if ui.Select(c, &choice, options).Label(locales.Text("检查状态筛选")).Width(185).Changed() {
			for i, v := range options {
				if choice == v {
					b.status = httpHealthStatusFilter(i)
				}
			}
		}
	})
	b.bind(r)
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("上一页")).Disabled(b.page == 0).Clicked() {
			b.movePage(r, b.page-1)
		}
		if ui.Button(c, locales.Text("下一页")).Disabled(b.page+1 >= b.pageCount()).Clicked() {
			b.movePage(r, b.page+1)
		}
		ui.Text(c, fmt.Sprintf(locales.Text("第 %d/%d 页 · 匹配 %d/%d 个健康观察"), b.page+1, b.pageCount(), len(b.indices), healthRowCount(projection))).SingleLine()
	}).FillWidth().MinWidth(0).Gap(5)
	_, selectedOK := b.selectedIndex(r)
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("查看所选原始详情")).Disabled(!selectedOK).Clicked() {
			if index, ok := b.selectedIndex(r); ok {
				raw, err := originalHealthRow(r.Result.HTTP.Body, index)
				if err == nil {
					var out bytes.Buffer
					if json.Indent(&out, raw, "", "  ") == nil {
						b.preview, b.detail = clipped(out.String()), true
					}
				}
			}
		}
		if ui.Button(c, locales.Text("复制所选原始 JSON")).Disabled(!selectedOK || d.FileBusy() || d.Busy()).Clicked() {
			s.CopySelectedHTTPHealthRow(c.Services(), d)
		}
		if ui.Button(c, locales.Text("导出所选原始 JSON")).Disabled(!selectedOK || d.FileBusy() || d.Busy() || s.deps.Files.SavePath == nil || s.deps.Files.WriteStream == nil).Clicked() {
			s.ExportSelectedHTTPHealthRow(d)
		}
	}).FillWidth().MinWidth(0).Gap(5)
	ui.Text(c, locales.Text("本地筛选与翻页不会重新读取")).SingleLine().Tooltip(locales.Text("本地筛选与翻页不会重新读取")).TextColor(p.Muted)
	if projection.Kind == "service-instances" {
		ui.Text(c, locales.Text("实例筛选按返回的检查状态匹配")).SingleLine().Tooltip(locales.Text("实例筛选按返回的检查状态匹配")).TextColor(p.Muted)
	}
	indices := b.pageIndices()
	selected := -1
	for i, original := range indices {
		if original == b.selectedOriginal {
			selected = i
		}
	}
	b.columns.Selected = &selected
	b.columns.Key = func(row int) any {
		return struct {
			receipt *Receipt
			index   int
		}{r, indices[row]}
	}
	columns := []ui.TableColumn{{Title: "Node", Width: 180}, {Title: "CheckID", Width: 220}, {Title: "Name", Width: 160}, {Title: "Status", Width: 95}, {Title: "ServiceID", Width: 170}, {Title: "CreateIndex", Width: 170}, {Title: "ModifyIndex", Width: 170}, {Title: locales.Text("维护检查"), Width: 100}}
	if projection.Kind == "service-instances" {
		columns = []ui.TableColumn{{Title: "Node", Width: 180}, {Title: "Service", Width: 180}, {Title: "ServiceID", Width: 220}, {Title: "Address", Width: 160}, {Title: "Port", Width: 80}, {Title: locales.Text("返回的检查数"), Width: 110}, {Title: "CreateIndex", Width: 170}, {Title: "ModifyIndex", Width: 170}}
	}
	columns = append(columns, ui.TableColumn{Title: "Namespace", Width: 170}, ui.TableColumn{Title: "Partition", Width: 150})
	page := b.page
	theme.Table(c, &b.columns, columns, len(indices), func(row, column int) {
		text := ""
		if projection.Kind == "checks" {
			text = healthCheckCell(projection.Checks[indices[row]], column)
		} else {
			text = healthInstanceCell(projection.Instances[indices[row]], column)
		}
		ui.Text(c, text).SingleLine().Tooltip(text)
	}).Label(locales.Text("已返回的健康检查或服务实例")).Fill().MinHeight(0)
	if selected >= 0 {
		b.selectRow(r, page, selected)
	}
}

func (s *State) httpHealthBrowserModals(c *ui.Context) {
	for _, d := range s.documents {
		if !s.active(d) || !d.httpHealth || s.leave != nil {
			continue
		}
		r := s.healthBrowserReceipt(d)
		if healthReceiptProjection(r) == nil || r.Scope.Namespace.ProfileID != d.profileID {
			d.httpHealth = false
			continue
		}
		theme.Modal(c.Key(fmt.Sprintf("kv-health-browser-%d", d.id)), &d.httpHealth, func() {
			ui.Column(c).Width(660).Height(440).Gap(6).Children(func() {
				ui.Row(c).FillWidth().Gap(6).Children(func() {
					ui.Text(c, locales.Text("健康检查与服务实例")).Bold().Grow(1)
					if ui.Button(c, locales.Text("返回原 HTTP 回执")).Clicked() {
						d.httpHealth = false
					}
				})
				ui.Text(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), r.Scope.Namespace.ProfileID, r.Scope.Generation)).SingleLine().TextColor(theme.Colors(c).Muted)
				s.httpHealthBrowserView(c, d, r)
			})
		})
	}
}
