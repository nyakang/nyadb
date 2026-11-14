package kvworkspace

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type httpACLBrowser struct {
	open, detail bool
	query        string
	page         int
	selected     int
	preview      string
	columns      ui.ListState
}

func aclBrowserIndices(p *contract.KVHTTPACLProjection, query string) []int {
	if p == nil || !p.ProjectionComplete {
		return nil
	}
	var indices []int
	query = strings.ToLower(query)
	for i, row := range p.Rows {
		// SecretID, Config and raw unknown fields never enter search or cells.
		if query == "" || strings.Contains(strings.ToLower(row.Identity+"\n"+row.Name), query) {
			indices = append(indices, i)
		}
	}
	return indices
}

func aclCellState(state string) string {
	switch state {
	case "absent":
		return locales.Text("未返回")
	case "null":
		return "null"
	case "empty":
		return locales.Text("空值")
	case "redacted":
		return locales.Text("服务端已遮蔽")
	case "value":
		return locales.Text("已隐藏")
	}
	return "—"
}

func (s *State) openHTTPACLBrowser(d *Document) {
	if !s.active(d) || !d.httpACLContent() {
		return
	}
	s.HideHTTPACLReceipt(d)
	if d.httpACL.browser == nil {
		d.httpACL.browser = &httpACLBrowser{selected: -1}
	}
	d.httpACL.browser.open = true
}

func (s *State) httpACLBrowserView(c *ui.Context, d *Document) {
	x, r := d.httpACL, d.httpACL.last
	b := x.browser
	p := theme.Colors(c)
	ui.Text(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), r.Scope.Namespace.ProfileID, r.Scope.Generation)).SingleLine().TextColor(p.Muted)
	ui.Text(c, r.Request.Resource+" · "+r.Request.Lookup+" · "+r.Request.Identity).SingleLine().Tooltip(r.Request.Identity)
	if x.transfer != nil {
		s.httpACLTransferView(c, d)
		return
	}
	if h := r.Receipt.HTTP; h != nil {
		ui.Text(c, "HTTP "+strconv.Itoa(h.Status)+" · "+ReceiptStatus(r.Receipt)).SingleLine()
	}
	if !x.valid || r.Projection == nil {
		ui.Text(c, locales.Text("ACL 条目不可用；原始回执仍可显式查看或导出")).Wrap().FillWidth().TextColor(p.Warning)
	} else if !r.Projection.Complete || r.Projection.ContainsRedaction {
		ui.Text(c, locales.Text("仅展示原响应返回的 ACL 条目；过滤、null 或遮蔽不表示资源不存在")).Wrap().FillWidth().TextColor(p.Warning)
	}
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("显示原 ACL 正文（含秘密）")).Disabled(r.Receipt.HTTP == nil || d.Busy()).Clicked() {
			if s.RevealHTTPACLReceipt(d, r.LocalRecordID) {
				b.preview, b.detail = clipped(string(r.Receipt.HTTP.Body)), true
			}
		}
		if ui.Button(c, locales.Text("导出完整 ACL 回执…")).Disabled(d.Busy() || d.FileBusy() || s.deps.Files.SavePath == nil || s.deps.Files.WriteStream == nil).Clicked() {
			s.prepareHTTPACLTransfer(d, false)
		}
	}).FillWidth().MinWidth(0).Gap(5)
	if b.detail && x.revealed == r.LocalRecordID {
		if ui.Button(c, locales.Text("隐藏秘密并返回 ACL 列表")).Clicked() {
			s.HideHTTPACLReceipt(d)
		}
		ui.Text(c, locales.Text("预览最多 64 KiB；导出保留原始回执和完整字节")).SingleLine().TextColor(p.Muted)
		ui.TextArea(c, &b.preview).Label(locales.Text("ACL 原始秘密详情")).ReadOnly(true).Fill().MinHeight(0)
		return
	}
	ui.Text(c, locales.Text("秘密默认隐藏；显示、复制和导出仅使用此原回执")).SingleLine().TextColor(p.Muted)
	if ui.TextInput(c, &b.query).Label(locales.Text("筛选已返回的 ACL 名称或标识")).Placeholder(locales.Text("筛选已返回的 ACL 名称或标识")).FillWidth().Changed() {
		b.page, b.selected = 0, -1
	}
	indices := aclBrowserIndices(r.Projection, b.query)
	pages := max(1, (len(indices)+49)/50)
	b.page = min(b.page, pages-1)
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("上一页")).Disabled(b.page == 0).Clicked() {
			b.page--
			b.selected = -1
		}
		if ui.Button(c, locales.Text("下一页")).Disabled(b.page+1 >= pages).Clicked() {
			b.page++
			b.selected = -1
		}
		ui.Text(c, fmt.Sprintf(locales.Text("第 %d/%d 页 · %d 个 ACL 条目"), b.page+1, pages, len(indices))).SingleLine()
	}).FillWidth().MinWidth(0).Gap(5)
	selectedOK := x.valid && r.Projection != nil && b.selected >= 0 && b.selected < len(r.Projection.Rows)
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("显示所选 ACL 原值（含秘密）")).Disabled(!selectedOK || d.Busy()).Clicked() {
			if s.RevealHTTPACLReceipt(d, r.LocalRecordID) {
				b.preview, b.detail = clipped(string(r.Projection.Rows[b.selected].Raw)), true
			}
		}
		if ui.Button(c, locales.Text("复制所选 ACL 原值…")).Disabled(!selectedOK || d.Busy() || d.FileBusy()).Clicked() {
			s.prepareHTTPACLTransfer(d, true)
		}
	}).FillWidth().MinWidth(0).Gap(5)
	page := indices[min(b.page*50, len(indices)):min((b.page+1)*50, len(indices))]
	selected := -1
	for i, original := range page {
		if b.selected == original {
			selected = i
		}
	}
	b.columns.Selected = &selected
	b.columns.Key = func(row int) any {
		return struct {
			record string
			index  int
		}{r.LocalRecordID, page[row]}
	}
	columns := []ui.TableColumn{{Title: "ID / Name", Width: 260}, {Title: locales.Text("名称"), Width: 160}, {Title: "SecretID", Width: 130}, {Title: "Config", Width: 130}}
	theme.Table(c, &b.columns, columns, len(page), func(row, col int) {
		entry := r.Projection.Rows[page[row]]
		cells := []string{entry.Identity, entry.Name, aclCellState(entry.SecretState), aclCellState(entry.ConfigState)}
		ui.Text(c, cells[col]).SingleLine().Tooltip(cells[col])
	}).Label(locales.Text("已返回的 ACL 条目")).Fill().MinHeight(0)
	if selected >= 0 && selected < len(page) {
		b.selected = page[selected]
	}
}

func (s *State) httpACLTransferView(c *ui.Context, d *Document) {
	x := d.httpACL
	p := x.transfer
	label := locales.Text("导出完整 ACL 回执…")
	if p.copy {
		label = locales.Text("复制所选 ACL 原值…")
	}
	ui.Text(c, label).Bold()
	ui.Text(c, locales.Text("包含原响应中的令牌秘密和认证配置。确认后将完整内容发送到所选文件或系统剪贴板。")).Wrap().FillWidth()
	ui.Text(c, fmt.Sprintf(locales.Text("原回执快照：%d 字节"), len(p.raw))).SingleLine()
	ui.Checkbox(c, &p.checked, locales.Text("我确认复制或导出此原回执中的秘密"))
	ui.Row(c).FillWidth().Gap(6).Children(func() {
		if ui.Button(c, locales.Text("确认传出 ACL 内容")).Disabled(!p.checked || d.Busy() || d.FileBusy()).Clicked() {
			s.confirmHTTPACLTransfer(c.Services(), d, p.checked)
		}
		if ui.Button(c, locales.Text("取消")).Clicked() {
			x.transfer = nil
		}
	})
}

func (s *State) httpACLBrowserModals(c *ui.Context) {
	for _, d := range s.documents {
		if !s.active(d) || !d.httpACLContent() || d.httpACL.browser == nil || !d.httpACL.browser.open || s.leave != nil {
			continue
		}
		b := d.httpACL.browser
		theme.Modal(c.Key(fmt.Sprintf("kv-acl-browser-%d", d.id)), &b.open, func() {
			ui.Column(c).Width(660).Height(440).Gap(5).Children(func() {
				ui.Row(c).FillWidth().Gap(6).Children(func() {
					ui.Text(c, locales.Text("ACL 原响应浏览器")).Bold().Grow(1)
					if ui.Button(c, locales.Text("返回原 HTTP 回执")).Clicked() {
						b.open = false
					}
				})
				s.httpACLBrowserView(c, d)
			})
		})
		if !b.open {
			s.HideHTTPACLReceipt(d)
		}
	}
}
