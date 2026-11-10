package kvworkspace

import (
	"encoding/base64"
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) leaseBrowserReceiptView(c *ui.Context, d *Document) {
	p := theme.Colors(c)
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("租约列表")).Clicked() {
			d.browserReceiptChoice = 1
		}
		if ui.Button(c, locales.Text("租约详情")).Clicked() {
			d.browserReceiptChoice = 2
		}
		if ui.Button(c, locales.Text("本次浏览回执")).Clicked() {
			d.browserReceiptChoice = 0
		}
		if ui.Button(c, locales.Text("清除浏览回执")).Disabled(d.Busy() || d.FileBusy()).Clicked() {
			s.ClearLeaseBrowser(d)
		}
	}).Label(locales.Text("租约浏览操作")).FillWidth().MinWidth(0).Padding(0).Gap(5)
	ui.Toolbar(c, func() {
		r := s.selectedLeaseBrowserReceipt(d)
		if ui.Button(c, locales.Text("复制完整租约记录")).Disabled(r == nil || d.FileBusy()).Clicked() {
			s.CopyLeaseBrowserRecord(c.Services(), d)
		}
		if ui.Button(c, locales.Text("导出租约记录")).Disabled(r == nil || d.FileBusy() || s.deps.Files.SavePath == nil).Clicked() {
			s.ExportLeaseBrowserRecord(d)
		}
		s.leaseManagementMenu(c, d)
	}).Label(locales.Text("KV 回执操作")).FillWidth().MinWidth(0).Padding(0).Gap(5)
	ui.Text(c, locales.Text("集群租约观察；TTL 为逐项读回值，翻页会重新读取列表")).FontSize(theme.Font(c, 11)).TextColor(p.Muted).SingleLine().Tooltip(locales.Text("集群租约观察；TTL 为逐项读回值，翻页会重新读取列表"))
	if d.browserReceiptChoice == 0 {
		r := d.browserLast
		if r == nil {
			ui.Text(c, locales.Text("运行租约浏览后显示原始记录")).TextColor(p.Muted)
			return
		}
		preview := r.preview
		ui.TextArea(c, &preview).Label(locales.Text("租约观察预览")).ReadOnly(true).Fill().MinHeight(0)
		return
	}
	if d.browserReceiptChoice == 2 {
		s.leaseDetailView(c, d)
		return
	}
	if d.browserPage == nil || d.browserPage.Page == nil {
		ui.Text(c, locales.Text("运行租约浏览后显示原始记录")).TextColor(p.Muted)
		return
	}
	page := d.browserPage.Page
	ready := !d.Busy() && !d.needsRecovery && sameEventScope(d.browserPage.Scope, d.scope)
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("读取所选租约详情")).Disabled(!ready || d.browserSelectedID == 0).Clicked() {
			s.ReadSelectedLease(d)
		}
		if ui.Button(c, locales.Text("下一个 ID 窗口")).Disabled(!ready || !page.Complete || !page.More || page.NextAfter == nil).Clicked() {
			s.NextLeasePage(d)
		}
		ui.Text(c, fmt.Sprintf(locales.Text("%d 个租约观察"), len(page.Rows))).TextColor(p.Muted)
	}).Label(locales.Text("租约列表操作")).FillWidth().MinWidth(0).Padding(0).Gap(5)
	d.browserColumns.Selected = &d.browserRow
	theme.Table(c, &d.browserColumns, []ui.TableColumn{{Title: locales.Text("租约 ID"), Width: 230}, {Title: locales.Text("观察 TTL（秒）"), Width: 140}, {Title: locales.Text("授予 TTL（秒）"), Width: 140}}, len(page.Rows), func(row, col int) {
		r := page.Rows[row]
		l := r.Result.Reply.Lease
		text := strconv.FormatInt(r.ID, 10)
		if col == 1 {
			text = strconv.FormatInt(l.TTL, 10)
		}
		if col == 2 {
			text = strconv.FormatInt(l.GrantedTTL, 10)
		}
		ui.Text(c, text).SingleLine().Tooltip(text)
	}).Label(locales.Text("集群租约列表")).Fill().MinHeight(0)
	if d.browserRow >= 0 && d.browserRow < len(page.Rows) {
		d.browserSelectedID = page.Rows[d.browserRow].ID
	}
}
func (s *State) leaseDetailView(c *ui.Context, d *Document) {
	p := theme.Colors(c)
	if d.browserDetail == nil || d.browserDetail.Detail == nil {
		ui.Text(c, locales.Text("运行租约浏览后显示原始记录")).TextColor(p.Muted)
		return
	}
	r := d.browserDetail.Detail
	l := r.Result.Reply.Lease

	ui.Text(c, fmt.Sprintf("ID %d · TTL %d · GrantedTTL %d", l.ID, l.TTL, l.GrantedTTL)).Bold().SingleLine()
	ui.Text(c, fmt.Sprintf("revision %d · cluster %d · member %d", r.Result.Header.Revision, r.Result.Header.ClusterID, r.Result.Header.MemberID)).SingleLine().TextColor(p.Muted)
	if !r.Request.IncludeKeys {
		ui.Text(c, locales.Text("本次详情未请求关联键")).TextColor(p.Muted)
		return
	}
	ui.Toolbar(c, func() {
		ui.Text(c, fmt.Sprintf(locales.Text("%d 个完整关联键"), len(l.Keys))).TextColor(p.Muted)
		if ui.Button(c, locales.Text("复制完整键 Base64")).Disabled(d.browserKeyRow < 0 || d.browserKeyRow >= len(l.Keys)).Clicked() {
			c.WriteClipboard(base64.StdEncoding.EncodeToString(l.Keys[d.browserKeyRow]))
		}
	}).Label(locales.Text("关联键操作")).FillWidth().Padding(0).Gap(5)
	d.browserKeysColumns.Selected = &d.browserKeyRow
	theme.Table(c, &d.browserKeysColumns, []ui.TableColumn{{Title: locales.Text("字节数"), Width: 85}, {Title: locales.Text("键 Base64 预览"), Width: 560}}, len(l.Keys), func(row, col int) {
		key := l.Keys[row]
		text := strconv.Itoa(len(key))
		if col == 1 {
			text = base64.StdEncoding.EncodeToString(key[:min(len(key), 256)])
			if len(key) > 256 {
				text += "…"
			}
		}
		ui.Text(c, text).SingleLine().Tooltip(text)
	}).Label(locales.Text("租约完整关联键")).Fill().MinHeight(0)
}
