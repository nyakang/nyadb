package kvworkspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func catalogNodeCells(n contract.KVHTTPCatalogNode) []string {
	fields := []string{"Node", "Node", "ID", "Address", "Datacenter", "Partition", "PeerName", "ModifyIndex"}
	values := []string{n.Node, n.Node, n.ID, n.Address, n.Datacenter, n.Partition, n.PeerName, strconv.FormatUint(n.ModifyIndex, 10)}
	for i, key := range fields {
		if key == "ID" && !slices.Contains(n.PresentFields, key) {
			key = "Id"
		}
		values[i] = healthCellText(n.PresentFields, n.NullFields, key, values[i])
	}
	return []string{"Node", values[0], values[1], values[2], values[3], "—", values[4], "—", values[5], values[6], values[7]}
}

func catalogRowCells(p *contract.KVHTTPCatalogList, index int) []string {
	switch p.Kind {
	case "datacenters":
		return []string{"Datacenter", p.Datacenters[index]}
	case "nodes":
		return catalogNodeCells(p.Nodes[index])
	case "services":
		s := p.Services[index]
		tags := strings.Join(s.Tags, ", ")
		if s.TagsNull {
			tags = "null"
		}
		return []string{"Service", s.Name, tags}
	case "service":
		i := p.Instances[index]
		fields := []string{"ServiceName", "Node", "ServiceID", "ServiceAddress", "ServicePort", "Datacenter", "Namespace", "Partition", "PeerName", "ModifyIndex"}
		values := []string{i.ServiceName, i.Node, i.ServiceID, i.ServiceAddress, strconv.FormatUint(i.ServicePort, 10), i.Datacenter, i.Namespace, i.Partition, i.PeerName, strconv.FormatUint(i.ModifyIndex, 10)}
		for k, key := range fields {
			if key == "ServiceID" && !slices.Contains(i.PresentFields, key) {
				key = "ServiceId"
			}
			values[k] = healthCellText(i.PresentFields, i.NullFields, key, values[k])
		}
		return append([]string{"ServiceNode"}, values...)
	case "node":
		if index == 0 {
			return catalogNodeCells(p.NodeServices.Node)
		}
		s := p.NodeServices.Services[index-1]
		v := s.Service
		fields := []string{"Service", "ID", "Address", "Port", "Namespace", "Partition", "ModifyIndex"}
		values := []string{v.Service, v.ID, v.Address, strconv.FormatUint(v.Port, 10), v.Namespace, v.Partition, strconv.FormatUint(v.ModifyIndex, 10)}
		for i, key := range fields {
			if key == "ID" && !slices.Contains(v.PresentFields, key) {
				key = "Id"
			}
			values[i] = healthCellText(v.PresentFields, v.NullFields, key, values[i])
		}
		return []string{"NodeService", values[0], p.NodeServices.Node.Node, s.Key, values[2], values[3], p.NodeServices.Node.Datacenter, values[4], values[5], p.NodeServices.Node.PeerName, values[6]}
	}
	return nil
}

func catalogColumns(kind string) []ui.TableColumn {
	names := []string{"Type", "Name", "Node", "ID / MapKey", "Address", "Port", "Datacenter", "Namespace", "Partition", "PeerName", "ModifyIndex"}
	switch kind {
	case "datacenters":
		names = []string{"Type", "Datacenter"}
	case "services":
		names = []string{"Type", "Service / MapKey", "Tags"}
	case "service":
		names = []string{"Type", "ServiceName", "Node", "ServiceID", "ServiceAddress", "ServicePort", "Datacenter", "Namespace", "Partition", "PeerName", "ModifyIndex"}
	case "node":
		names = []string{"Type", "Service / Node", "Node", "ID / MapKey", "Address", "Port", "Datacenter", "Namespace", "Partition", "PeerName", "ModifyIndex"}
	}
	columns := make([]ui.TableColumn, len(names))
	for i, name := range names {
		columns[i] = ui.TableColumn{Title: name, Width: 170}
		if i == 0 {
			columns[i].Width = 110
		}
	}
	return columns
}

func (s *State) httpCatalogBrowserView(c *ui.Context, d *Document, r *Receipt) {
	if d.httpCatalogBrowser == nil {
		d.httpCatalogBrowser = &httpCatalogBrowserState{}
	}
	b := d.httpCatalogBrowser
	b.bind(r)
	projection := catalogReceiptProjection(r)
	if projection == nil {
		return
	}
	p := theme.Colors(c)
	if !projection.Complete {
		ui.Text(c, locales.Text("Catalog 响应受 ACL 过滤或不完整；仅展示原响应返回的条目")).Wrap().FillWidth().TextColor(p.Warning)
	}
	if projection.Null {
		ui.Text(c, locales.Text("原节点查询返回 null；此观察不提供写入权限")).Wrap().FillWidth().TextColor(p.Muted)
	}
	if b.detail {
		ui.Toolbar(c, func() {
			if ui.Button(c, locales.Text("返回 Catalog 列表")).Clicked() {
				b.detail, b.preview = false, ""
			}
		}).FillWidth()
		ui.Text(c, locales.Text("此详情来自所选原始响应；预览最多 64 KiB，完整内容保留在 HTTP 回执")).SingleLine().Tooltip(locales.Text("此详情来自所选原始响应；预览最多 64 KiB，完整内容保留在 HTTP 回执")).TextColor(p.Muted)
		ui.TextArea(c, &b.preview).Label(locales.Text("Catalog 原值详情")).ReadOnly(true).Fill().MinHeight(0)
		return
	}
	ui.TextInput(c, &b.query).Label(locales.Text("筛选已返回的 Catalog 条目")).Placeholder(locales.Text("筛选已返回的 Catalog 条目")).FillWidth().MinWidth(0)
	b.bind(r)
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("上一页")).Disabled(b.page == 0).Clicked() {
			b.movePage(r, b.page-1)
		}
		if ui.Button(c, locales.Text("下一页")).Disabled(b.page+1 >= b.pageCount()).Clicked() {
			b.movePage(r, b.page+1)
		}
		ui.Text(c, fmt.Sprintf(locales.Text("第 %d/%d 页 · 匹配 %d/%d 个 Catalog 条目"), b.page+1, b.pageCount(), len(b.indices), catalogRowCount(projection))).SingleLine()
	}).FillWidth().MinWidth(0).Gap(5)
	_, selectedOK := b.selectedIndex(r)
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("查看所选原始详情")).Disabled(!selectedOK).Clicked() {
			if index, ok := b.selectedIndex(r); ok {
				if raw, ok := catalogRowRaw(r, index); ok {
					var out bytes.Buffer
					if json.Indent(&out, raw, "", "  ") == nil {
						b.preview, b.detail = clipped(out.String()), true
					}
				}
			}
		}
		if ui.Button(c, locales.Text("复制所选 Catalog 原值 JSON")).Disabled(!selectedOK || d.FileBusy() || d.Busy()).Clicked() {
			s.CopySelectedHTTPCatalogValue(c.Services(), d)
		}
		if ui.Button(c, locales.Text("导出所选 Catalog 原值 JSON")).Disabled(!selectedOK || d.FileBusy() || d.Busy() || s.deps.Files.SavePath == nil || s.deps.Files.WriteStream == nil).Clicked() {
			s.ExportSelectedHTTPCatalogValue(d)
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
	page := b.page
	theme.Table(c, &b.columns, catalogColumns(projection.Kind), len(indices), func(row, column int) {
		cells := catalogRowCells(projection, indices[row])
		text := cells[column]
		ui.Text(c, text).SingleLine().Tooltip(text)
	}).Label(locales.Text("已返回的 Catalog 条目")).Fill().MinHeight(0)
	if selected >= 0 {
		b.selectRow(r, page, selected)
	}
}

func (s *State) httpCatalogBrowserModals(c *ui.Context) {
	for _, d := range s.documents {
		if !s.active(d) || !d.httpCatalog || s.leave != nil {
			continue
		}
		r := s.selectedReceipt(d)
		if catalogReceiptProjection(r) == nil || r.Scope.Namespace.ProfileID != d.profileID {
			d.httpCatalog = false
			continue
		}
		theme.Modal(c.Key(fmt.Sprintf("kv-catalog-browser-%d", d.id)), &d.httpCatalog, func() {
			ui.Column(c).Width(660).Height(440).Gap(6).Children(func() {
				ui.Row(c).FillWidth().Gap(6).Children(func() {
					ui.Text(c, locales.Text("Catalog 原响应浏览器")).Bold().Grow(1)
					if ui.Button(c, locales.Text("返回原 HTTP 回执")).Clicked() {
						d.httpCatalog = false
					}
				})
				ui.Text(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), r.Scope.Namespace.ProfileID, r.Scope.Generation)).SingleLine().TextColor(theme.Colors(c).Muted)
				s.httpCatalogBrowserView(c, d, r)
			})
		})
	}
}
