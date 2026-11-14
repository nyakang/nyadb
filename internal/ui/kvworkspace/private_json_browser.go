package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/locales"
)

// Rows retain exact JSON tokens, including unsigned integers and secret fields.
// Only the explicit private reveal flow renders this browser.
type privateJSONRow struct {
	name string
	raw  json.RawMessage
}
type privateJSONBrowser struct {
	identity, filter string
	rows             []privateJSONRow
	page, selected   int
}

func (b *privateJSONBrowser) set(identity string, raw []byte) {
	if b.identity == identity {
		return
	}
	*b = privateJSONBrowser{identity: identity, selected: -1}
	trim := bytes.TrimSpace(raw)
	if len(trim) > 0 && trim[0] == '[' {
		var items []json.RawMessage
		if json.Unmarshal(trim, &items) == nil {
			for i, item := range items {
				b.rows = append(b.rows, privateJSONRow{fmt.Sprint(i + 1), item})
			}
			return
		}
	}
	if len(trim) > 0 && trim[0] == '{' {
		var items map[string]json.RawMessage
		if json.Unmarshal(trim, &items) == nil {
			keys := make([]string, 0, len(items))
			for key := range items {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				b.rows = append(b.rows, privateJSONRow{key, items[key]})
			}
			return
		}
	}
	if len(raw) > 0 {
		b.rows = []privateJSONRow{{"response", bytes.Clone(raw)}}
	}
}

func (b *privateJSONBrowser) indices() []int {
	var indices []int
	query := strings.ToLower(b.filter)
	for i, row := range b.rows {
		if query == "" || strings.Contains(strings.ToLower(row.name), query) || strings.Contains(strings.ToLower(string(row.raw)), query) {
			indices = append(indices, i)
		}
	}
	return indices
}
func pageBounds(page, count, size int) (int, int, int) {
	pages := max(1, (count+size-1)/size)
	page = max(0, min(page, pages-1))
	start := min(page*size, count)
	return page, start, min(start+size, count)
}
func (b *privateJSONBrowser) selectedRaw() []byte {
	if b.selected < 0 || b.selected >= len(b.rows) {
		return nil
	}
	return bytes.Clone(b.rows[b.selected].raw)
}
func (s *State) privateJSONView(c *ui.Context, d *Document, b *privateJSONBrowser, kind string) {
	if b.selected >= 0 {
		if ui.Button(c, locales.Text("返回原响应列表")).Clicked() {
			b.selected = -1
		}
		if raw := b.selectedRaw(); len(raw) > 0 {
			var pretty bytes.Buffer
			text := string(raw)
			if json.Indent(&pretty, raw, "", "  ") == nil {
				text = pretty.String()
			}
			ui.TextArea(c, &text).Label(locales.Text("完整原响应详情")).Height(160).FillWidth().ReadOnly(true)
			ui.Row(c).Gap(5).Children(func() {
				if ui.Button(c, locales.Text("复制完整原响应详情")).Disabled(d.Busy() || d.FileBusy()).Clicked() {
					s.fileJob(d, "copy-private-detail", func(ctx context.Context) fileResult { return fileResult{clipboard: string(raw), err: ctx.Err()} })
					d.io.clipboard = c.Services().WriteClipboard
				}
				if kind == "management" && ui.Button(c, locales.Text("导出完整原响应详情")).Disabled(d.Busy() || d.FileBusy() || !d.httpManagement.exportConsent).Clicked() {
					s.managementFile(d, "management-detail-export")
				}
			})
		}
		return
	}
	if ui.TextInput(c, &b.filter).Label(locales.Text("筛选原响应")).FillWidth().Changed() {
		b.page = 0
		b.selected = -1
	}
	indices := b.indices()
	page, start, end := pageBounds(b.page, len(indices), 25)
	b.page = page
	ui.Text(c, fmt.Sprintf(locales.Text("原响应 %d 项 · 第 %d 页"), len(indices), page+1))
	for _, index := range indices[start:end] {
		row := b.rows[index]
		label := row.name + " · " + clipped(string(row.raw[:min(120, len(row.raw))]))
		if ui.Button(c.Key(fmt.Sprintf("private-row-%d", index)), label).FillWidth().Clicked() {
			b.selected = index
		}
	}
	ui.Row(c).Gap(5).Children(func() {
		if ui.Button(c, locales.Text("上一页")).Disabled(page == 0).Clicked() {
			b.page--
		}
		if ui.Button(c, locales.Text("下一页")).Disabled(end >= len(indices)).Clicked() {
			b.page++
		}
	})

}
