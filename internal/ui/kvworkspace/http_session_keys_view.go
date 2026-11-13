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

func originalHTTPSessionKeyPreview(r *Receipt, key string) string {
	if r == nil || r.HTTPSessionKeys == nil || !r.HTTPSessionKeys.ProjectionComplete || r.Result.HTTP == nil {
		return ""
	}
	d := json.NewDecoder(bytes.NewReader(r.Result.HTTP.Body))
	if token, err := d.Token(); err != nil || token != json.Delim('[') {
		return ""
	}
	for d.More() {
		var raw json.RawMessage
		var fields map[string]json.RawMessage
		var originalKey string
		if d.Decode(&raw) != nil || json.Unmarshal(raw, &fields) != nil || json.Unmarshal(fields["Key"], &originalKey) != nil {
			return ""
		}
		if originalKey == key {
			var out bytes.Buffer
			if json.Indent(&out, raw, "", "  ") != nil {
				return ""
			}
			return clipped(out.String())
		}
	}
	return ""
}

func sessionKeyCellPreview(row contract.KVHTTPEntry, column int) string {
	field := []string{"Key", "Value", "Flags", "ModifyIndex", "LockIndex"}[column]
	if !slices.Contains(row.PresentFields, field) {
		return locales.Text("（未返回）")
	}
	if column == 1 {
		if !row.ValuePresent {
			return locales.Text("（未返回）")
		}
		if row.ValueNull {
			return "null"
		}
		return strconv.Itoa(len(row.Value)) + " bytes"
	}
	text := []string{row.Key, "", strconv.FormatUint(row.Flags, 10), strconv.FormatUint(row.ModifyIndex, 10), strconv.FormatUint(row.LockIndex, 10)}[column]
	if len(text) <= 256 {
		return text
	}
	end := 256
	for !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + "…"
}

func (s *State) httpSessionKeysView(c *ui.Context, d *Document, r *Receipt) {
	if d.httpSessionKeysBrowser == nil {
		d.httpSessionKeysBrowser = &httpSessionKeysBrowserState{}
	}
	b := d.httpSessionKeysBrowser
	b.bind(r)
	p := theme.Colors(c)
	ui.Text(c, "Session UUID: "+r.HTTPSessionKeys.Request.ID).SingleLine().Tooltip(r.HTTPSessionKeys.Request.ID)
	if !r.HTTPSessionKeys.Complete {
		ui.Text(c, locales.Text("ACL 已过滤；此持有键观察不完整")).SingleLine().Tooltip(locales.Text("ACL 已过滤；此持有键观察不完整")).TextColor(p.Warning)
	}
	notice := locales.Text("读取全根目录，包含配置前缀之外的键；最多 10000 行、32 MiB 观察预算")
	ui.Text(c, notice).SingleLine().Tooltip(notice).TextColor(p.Muted)
	if b.detail {
		if ui.Button(c, locales.Text("返回持有键列表")).Clicked() {
			b.detail = false
			b.preview = ""
		}
		ui.Text(c, locales.Text("预览最多 64 KiB；复制与导出保留完整内容")).SingleLine().Tooltip(locales.Text("预览最多 64 KiB；复制与导出保留完整内容")).TextColor(p.Muted)
		ui.TextArea(c, &b.preview).Label(locales.Text("持有键原始详情")).ReadOnly(true).Fill().MinHeight(0)
		return
	}
	ui.TextInput(c, &b.query).Label(locales.Text("筛选已返回的持有键")).Placeholder(locales.Text("筛选已返回的持有键")).FillWidth().MinWidth(0)
	b.bind(r)
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("上一页")).Disabled(b.page == 0).Clicked() {
			b.movePage(r, b.page-1)
		}
		if ui.Button(c, locales.Text("下一页")).Disabled(b.page+1 >= b.pageCount()).Clicked() {
			b.movePage(r, b.page+1)
		}
		ui.Text(c, fmt.Sprintf(locales.Text("第 %d/%d 页 · 匹配 %d/%d 个持有键"), b.page+1, b.pageCount(), len(b.indices), len(r.HTTPSessionKeys.Matches))).SingleLine()
	}).FillWidth().MinWidth(0).Gap(5)
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("查看所选原始详情")).Disabled(b.selectedKey == "").Clicked() {
			if row, ok := b.selected(r); ok {
				b.preview = originalHTTPSessionKeyPreview(r, row.Key)
				b.detail = true
			}
		}
		if ui.Button(c, locales.Text("复制所选持有键名称")).Disabled(b.selectedKey == "" || d.FileBusy()).Clicked() {
			if row, ok := b.selected(r); ok {
				c.WriteClipboard(row.Key)
			}
		}
	}).FillWidth().MinWidth(0).Gap(5)
	rows, indices := r.Result.HTTP.Entries, b.pageIndices()
	selected := -1
	for i, index := range indices {
		if rows[index].Key == b.selectedKey {
			selected = i
		}
	}
	b.columns.Selected = &selected
	b.columns.Key = func(row int) any { return rows[indices[row]].Key }
	page := b.page
	theme.Table(c, &b.columns, []ui.TableColumn{{Title: "Key", Width: 290}, {Title: "Value", Width: 100}, {Title: "Flags", Width: 170}, {Title: "ModifyIndex", Width: 170}, {Title: "LockIndex", Width: 170}}, len(indices), func(row, column int) {
		text := sessionKeyCellPreview(rows[indices[row]], column)
		ui.Text(c, text).SingleLine().Tooltip(text)
	}).Label(locales.Text("已返回的持有键列表")).Fill().MinHeight(0)
	if selected >= 0 {
		b.selectRow(r, page, selected)
	}
}

func (s *State) httpSessionKeysModals(c *ui.Context) {
	for _, d := range s.documents {
		if !s.active(d) || !d.httpSessionKeys || s.leave != nil {
			continue
		}
		r := s.selectedReceipt(d)
		if r == nil || r.HTTPSessionKeys == nil || !r.HTTPSessionKeys.ProjectionComplete || r.Result.HTTP == nil || r.Scope.Namespace.ProfileID != d.profileID {
			d.httpSessionKeys = false
			continue
		}
		theme.Modal(c.Key(fmt.Sprintf("kv-session-keys-%d", d.id)), &d.httpSessionKeys, func() {
			ui.Column(c).Width(660).Height(440).Gap(6).Children(func() {
				ui.Row(c).FillWidth().Gap(6).Children(func() {
					ui.Text(c, locales.Text("Session 持有键")).Bold().Grow(1)
					if ui.Button(c, locales.Text("返回原 HTTP 回执")).Clicked() {
						d.httpSessionKeys = false
					}
				})
				ui.Text(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), r.Scope.Namespace.ProfileID, r.Scope.Generation)).SingleLine().TextColor(theme.Colors(c).Muted)
				ui.Text(c, locales.Text("此观察不能保证销毁时的完整影响；Session 锁可能随时变化")).SingleLine().Tooltip(locales.Text("此观察不能保证销毁时的完整影响；Session 锁可能随时变化")).TextColor(theme.Colors(c).Muted)
				s.httpSessionKeysView(c, d, r)
			})
		})
	}
}
