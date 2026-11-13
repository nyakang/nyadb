package kvworkspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func agentCellText(e contract.KVHTTPAgentEntry, kind string, column int) string {
	if column == 0 {
		return healthCellText([]string{"MapKey"}, nil, "MapKey", e.Key)
	}
	names := []string{"MapKey", "ID", "Service", "Address", "Port", "Kind", "Namespace", "Partition"}
	values := []string{e.Key, e.ID, e.Service, e.Address, strconv.FormatUint(e.Port, 10), e.Kind, e.Namespace, e.Partition}
	if kind == "checks" {
		names = []string{"MapKey", "CheckID", "Name", "ServiceName", "Status", "Type", "Namespace", "Partition"}
		values = []string{e.Key, e.ID, e.Name, e.Service, e.Status, e.Type, e.Namespace, e.Partition}
	}
	name := names[column]
	if (name == "ID" || name == "CheckID") && !slices.Contains(e.PresentFields, name) {
		if name == "ID" {
			name = "Id"
		} else {
			name = "CheckId"
		}
	}
	return healthCellText(e.PresentFields, e.NullFields, name, values[column])
}

func (s *State) httpAgentBrowserView(c *ui.Context, d *Document, r *Receipt) {
	if d.httpAgentBrowser == nil {
		d.httpAgentBrowser = &httpAgentBrowserState{}
	}
	b := d.httpAgentBrowser
	b.bind(r)
	projection := agentReceiptProjection(r)
	if projection == nil {
		return
	}
	p := theme.Colors(c)
	if !projection.Complete {
		ui.Text(c, locales.Text("Agent 响应受 ACL 过滤或不完整；仅展示原 map 返回的条目")).Wrap().FillWidth().TextColor(p.Warning)
	}
	if b.detail {
		ui.Toolbar(c, func() {
			if ui.Button(c, locales.Text("返回 Agent 列表")).Clicked() {
				b.detail, b.preview = false, ""
			}
		}).FillWidth()
		ui.Text(c, locales.Text("此详情来自所选原始响应；预览最多 64 KiB，完整内容保留在 HTTP 回执")).SingleLine().Tooltip(locales.Text("此详情来自所选原始响应；预览最多 64 KiB，完整内容保留在 HTTP 回执")).TextColor(p.Muted)
		ui.TextArea(c, &b.preview).Label(locales.Text("本机 Agent 原 map 值详情")).ReadOnly(true).Fill().MinHeight(0)
		return
	}
	ui.Row(c).FillWidth().MinWidth(0).Gap(6).Children(func() {
		ui.TextInput(c, &b.query).Label(locales.Text("筛选已返回的 Agent 条目")).Placeholder(locales.Text("筛选已返回的 Agent 条目")).Grow(1).MinWidth(0)
		if projection.Kind == "checks" {
			options := []string{locales.Text("全部检查状态"), "passing", "warning", "critical", locales.Text("维护检查"), locales.Text("其它或未返回状态")}
			choice := options[int(b.status)]
			if ui.Select(c, &choice, options).Label(locales.Text("检查状态筛选")).Width(185).Changed() {
				for i, v := range options {
					if choice == v {
						b.status = httpHealthStatusFilter(i)
					}
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
		ui.Text(c, fmt.Sprintf(locales.Text("第 %d/%d 页 · 匹配 %d/%d 个 Agent 条目"), b.page+1, b.pageCount(), len(b.indices), len(projection.Rows))).SingleLine()
	}).FillWidth().MinWidth(0).Gap(5)
	_, selectedOK := b.selectedIndex(r)
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("查看所选原始详情")).Disabled(!selectedOK).Clicked() {
			if index, ok := b.selectedIndex(r); ok {
				raw := projection.Rows[index].Raw
				if len(raw) != 0 {
					var out bytes.Buffer
					if json.Indent(&out, raw, "", "  ") == nil {
						b.preview, b.detail = clipped(out.String()), true
					}
				}
			}
		}
		if ui.Button(c, locales.Text("复制所选原 map 值 JSON")).Disabled(!selectedOK || d.FileBusy() || d.Busy()).Clicked() {
			s.CopySelectedHTTPAgentValue(c.Services(), d)
		}
		if ui.Button(c, locales.Text("导出所选原 map 值 JSON")).Disabled(!selectedOK || d.FileBusy() || d.Busy() || s.deps.Files.SavePath == nil || s.deps.Files.WriteStream == nil).Clicked() {
			s.ExportSelectedHTTPAgentValue(d)
		}
	}).FillWidth().MinWidth(0).Gap(5)
	ui.Text(c, locales.Text("本地筛选与翻页不会重新读取")).SingleLine().Tooltip(locales.Text("本地筛选与翻页不会重新读取")).TextColor(p.Muted)
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
	columns := []ui.TableColumn{{Title: "MapKey", Width: 220}, {Title: "ID", Width: 220}, {Title: "Service", Width: 180}, {Title: "Address", Width: 160}, {Title: "Port", Width: 80}, {Title: "Kind", Width: 100}}
	if projection.Kind == "checks" {
		columns = []ui.TableColumn{{Title: "MapKey", Width: 220}, {Title: "CheckID", Width: 220}, {Title: "Name", Width: 180}, {Title: "ServiceName", Width: 180}, {Title: "Status", Width: 95}, {Title: "Type", Width: 100}}
	}
	columns = append(columns, ui.TableColumn{Title: "Namespace", Width: 170}, ui.TableColumn{Title: "Partition", Width: 150})
	page := b.page
	theme.Table(c, &b.columns, columns, len(indices), func(row, column int) {
		text := agentCellText(projection.Rows[indices[row]], projection.Kind, column)
		ui.Text(c, text).SingleLine().Tooltip(text)
	}).Label(locales.Text("已返回的 Agent 服务或检查")).Fill().MinHeight(0)
	if selected >= 0 {
		b.selectRow(r, page, selected)
	}
}

func (s *State) httpAgentBrowserModals(c *ui.Context) {
	for _, d := range s.documents {
		if !s.active(d) || !d.httpAgent || s.leave != nil {
			continue
		}
		r := s.selectedReceipt(d)
		if agentReceiptProjection(r) == nil || r.Scope.Namespace.ProfileID != d.profileID {
			d.httpAgent = false
			continue
		}
		theme.Modal(c.Key(fmt.Sprintf("kv-agent-browser-%d", d.id)), &d.httpAgent, func() {
			ui.Column(c).Width(660).Height(440).Gap(6).Children(func() {
				ui.Row(c).FillWidth().Gap(6).Children(func() {
					ui.Text(c, locales.Text("本机 Agent 服务与检查")).Bold().Grow(1)
					if ui.Button(c, locales.Text("返回原 HTTP 回执")).Clicked() {
						d.httpAgent = false
					}
				})
				ui.Text(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), r.Scope.Namespace.ProfileID, r.Scope.Generation)).SingleLine().TextColor(theme.Colors(c).Muted)
				s.httpAgentBrowserView(c, d, r)
			})
		})
	}
}
