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

func sessionCellPreview(row contract.KVHTTPSession, column int) string {
	name := []string{"ID", "Name", "Node", "TTL", "Behavior", "CreateIndex", "ModifyIndex"}[column]
	if !slices.Contains(row.PresentFields, name) {
		return locales.Text("（未返回）")
	}
	if slices.Contains(row.NullFields, name) {
		return "null"
	}
	text := []string{row.ID, row.Name, row.Node, row.TTL, row.Behavior, strconv.FormatUint(row.CreateIndex, 10), strconv.FormatUint(row.ModifyIndex, 10)}[column]
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

// Decode one original row only on an explicit detail action. The preview is
// bounded; the complete original response remains in the receipt for export.
func originalSessionPreview(r *Receipt, id string) string {
	if sessionReceiptRows(r) == nil {
		return ""
	}
	d := json.NewDecoder(bytes.NewReader(r.Result.HTTP.Body))
	if token, err := d.Token(); err != nil || token != json.Delim('[') {
		return ""
	}
	for d.More() {
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return ""
		}
		var fields map[string]json.RawMessage
		var originalID string
		if json.Unmarshal(raw, &fields) != nil || json.Unmarshal(fields["ID"], &originalID) != nil {
			return ""
		}
		if originalID == id {
			var b bytes.Buffer
			if json.Indent(&b, raw, "", "  ") != nil {
				return ""
			}
			return clipped(b.String())
		}
	}
	return ""
}

func (s *State) httpSessionBrowserView(c *ui.Context, d *Document, receipt *Receipt) {
	if d.httpSessionBrowser == nil {
		d.httpSessionBrowser = &httpSessionBrowserState{}
	}
	b := d.httpSessionBrowser
	b.bind(receipt)
	p := theme.Colors(c)
	if receipt.Result.HTTP.Metadata.ACLFiltered != nil && *receipt.Result.HTTP.Metadata.ACLFiltered {
		ui.Text(c, locales.Text("此 Session 响应受 ACL 过滤，仅显示返回的观察")).SingleLine().Tooltip(locales.Text("此 Session 响应受 ACL 过滤，仅显示返回的观察")).TextColor(p.Warning)
	}
	if b.detail != "" {
		ui.Toolbar(c, func() {
			if ui.Button(c, locales.Text("返回 Session 列表")).Clicked() {
				b.detail, b.preview = "", ""
			}
			ui.Text(c, b.selectedID).SingleLine().Tooltip(b.selectedID)
		}).FillWidth().MinWidth(0).Gap(5)
		ui.Text(c, locales.Text("此详情来自所选原始响应；预览最多 64 KiB，完整内容保留在 HTTP 回执")).SingleLine().Tooltip(locales.Text("此详情来自所选原始响应；预览最多 64 KiB，完整内容保留在 HTTP 回执")).TextColor(p.Muted)
		ui.TextArea(c, &b.preview).Label(locales.Text("Session 原始详情")).ReadOnly(true).Fill().MinHeight(0)
		return
	}
	ui.Row(c).FillWidth().MinWidth(0).Gap(6).Children(func() {
		ui.TextInput(c, &b.query).Label(locales.Text("筛选已返回的 Session")).Placeholder(locales.Text("筛选已返回的 Session")).Grow(1).MinWidth(0)
		options := []string{locales.Text("全部 TTL 状态"), locales.Text("有 TTL 配置"), locales.Text("无 TTL 配置"), locales.Text("TTL 未返回或不可识别")}
		choice := options[int(b.ttl)]
		if ui.Select(c, &choice, options).Label(locales.Text("Session TTL 筛选")).Width(190).Changed() {
			for i, option := range options {
				if choice == option {
					b.ttl = httpSessionTTLFilter(i)
				}
			}
		}
	})
	b.bind(receipt)
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("上一页")).Disabled(b.page == 0).Clicked() {
			b.movePage(receipt, b.page-1)
		}
		if ui.Button(c, locales.Text("下一页")).Disabled(b.page+1 >= b.pageCount()).Clicked() {
			b.movePage(receipt, b.page+1)
		}
		ui.Text(c, fmt.Sprintf(locales.Text("第 %d/%d 页 · 匹配 %d/%d 个 Session"), b.page+1, b.pageCount(), len(b.indices), len(sessionReceiptRows(receipt)))).SingleLine()
	}).FillWidth().MinWidth(0).Gap(5)
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("查看所选原始详情")).Disabled(b.selectedID == "").Clicked() {
			b.preview = originalSessionPreview(receipt, b.selectedID)
			b.detail = b.selectedID
		}
		if ui.Button(c, locales.Text("复制所选 Session UUID")).Disabled(b.selectedID == "" || d.FileBusy()).Clicked() {
			if row, ok := b.selected(receipt); ok {
				c.WriteClipboard(row.ID)
			}
		}
	}).FillWidth().MinWidth(0).Gap(5)
	ui.Text(c, locales.Text("本地筛选与翻页不会重新读取")).SingleLine().Tooltip(locales.Text("本地筛选与翻页不会重新读取")).TextColor(p.Muted)
	rows, indices := sessionReceiptRows(receipt), b.pageIndices()
	selected := -1
	for i, original := range indices {
		if rows[original].ID == b.selectedID {
			selected = i
		}
	}
	b.columns.Selected = &selected
	b.columns.Key = func(row int) any { return rows[indices[row]].ID }
	page := b.page
	theme.Table(c, &b.columns, []ui.TableColumn{{Title: "Session UUID", Width: 280}, {Title: "Name", Width: 160}, {Title: "Node", Width: 190}, {Title: "TTL", Width: 95}, {Title: "Behavior", Width: 95}, {Title: "CreateIndex", Width: 170}, {Title: "ModifyIndex", Width: 170}}, len(indices), func(row, column int) {
		value := rows[indices[row]]
		text := sessionCellPreview(value, column)
		ui.Text(c, text).SingleLine().Tooltip(text)
	}).Label(locales.Text("已返回的 Session 列表")).Fill().MinHeight(0)
	if selected >= 0 {
		b.selectRow(receipt, page, selected)
	}
}

func (s *State) httpSessionBrowserModals(c *ui.Context) {
	for _, d := range s.documents {
		if !s.active(d) || !d.httpSessions || s.leave != nil {
			continue
		}
		r := s.selectedReceipt(d)
		if r == nil || r.Result.HTTP == nil || r.Result.HTTP.Sessions == nil || r.Scope.Namespace.ProfileID != d.profileID {
			d.httpSessions = false
			continue
		}
		theme.Modal(c.Key(fmt.Sprintf("kv-session-browser-%d", d.id)), &d.httpSessions, func() {
			ui.Column(c).Width(660).Height(440).Gap(6).Children(func() {
				ui.Row(c).FillWidth().Gap(6).Children(func() {
					ui.Text(c, locales.Text("Session 列表")).Bold().Grow(1)
					if ui.Button(c, locales.Text("返回原 HTTP 回执")).Clicked() {
						d.httpSessions = false
					}
				})
				ui.Text(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), r.Scope.Namespace.ProfileID, r.Scope.Generation)).SingleLine().TextColor(theme.Colors(c).Muted)
				s.httpSessionBrowserView(c, d, r)
			})
		})
	}
}
