package kvworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/consulspec"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
	"strconv"
)

func httpKVSearchInput(x *httpKVToolsState) (consulspec.SearchRequest, error) {
	a, e := strconv.Atoi(x.limit)
	if e != nil {
		return consulspec.SearchRequest{}, e
	}
	b, e := strconv.Atoi(x.maxScan)
	r := consulspec.SearchRequest{Prefix: x.prefix, Query: x.query, Keys: x.keys, Values: x.values, CaseSensitive: x.caseSensitive, Limit: a, MaxScan: b}
	if e == nil {
		e = consulspec.ValidateSearch(r)
	}
	return r, e
}
func (s *State) httpKVToolsModals(c *ui.Context) {
	for _, d := range s.documents {
		if !s.active(d) || d.httpKVTools == nil || !d.httpKVTools.open || s.leave != nil {
			continue
		}
		x := d.httpKVTools
		width, height := c.Size()
		theme.Modal(c.Key(fmt.Sprintf("consul-kv-tools-%d", d.id)), &x.open, func() {
			ui.Column(c).Width(min(float32(700), max(float32(1), width-68))).Height(min(float32(550), max(float32(1), height-68))).Gap(6).Children(func() {
				ui.Text(c, locales.Text("Consul KV 搜索与 Bundle")).Bold()
				busy := d.Busy() || d.FileBusy()
				ui.Scroll(c).Fill().MinHeight(0).Children(func() {
					ui.Column(c).FillWidth().Gap(5).Children(func() {
						ui.Text(c, locales.Text("原键或前缀"))
						ui.TextInput(c, &x.prefix).Label(locales.Text("原键或前缀")).FillWidth().Disabled(busy)
						ui.Checkbox(c, &x.prefixMode, locales.Text("前缀")).Disabled(busy)
						ui.Text(c, locales.Text("搜索文本"))
						ui.TextInput(c, &x.query).Label(locales.Text("搜索文本")).FillWidth().Disabled(busy)
						ui.Row(c).Gap(6).Children(func() {
							ui.Checkbox(c, &x.keys, locales.Text("键")).Disabled(busy)
							ui.Checkbox(c, &x.values, locales.Text("值")).Disabled(busy)
							ui.Checkbox(c, &x.caseSensitive, locales.Text("区分大小写")).Disabled(busy)
							ui.Column(c).Width(130).Children(func() {
								ui.Text(c, locales.Text("匹配限额"))
								ui.TextInput(c, &x.limit).Label(locales.Text("匹配限额")).FillWidth().Disabled(busy)
							})
							ui.Column(c).Width(130).Children(func() {
								ui.Text(c, locales.Text("扫描限额"))
								ui.TextInput(c, &x.maxScan).Label(locales.Text("扫描限额")).FillWidth().Disabled(busy)
							})
						})
						ui.Text(c, locales.Text("搜索读取完整原前缀响应；二进制值保留原字节，值搜索仅匹配 UTF-8 文本")).Wrap()
						if x.progress != nil && busy {
							ui.Text(c, fmt.Sprintf(locales.Text("已扫描 %d 项；取消等待原执行器结束"), x.progress.Load())).Wrap()
						}
						if r := x.search; r != nil {
							ui.Text(c, fmt.Sprintf(locales.Text("扫描 %d · 匹配 %d · 达到限额 %t · 已取消 %t"), r.Scanned, r.Matched, r.Limited, r.Canceled)).Wrap()
							s.kvToolRows(c, d, x, true)

						}
						if x.bundle != nil {
							ui.Text(c, fmt.Sprintf(locales.Text("Bundle %d 个原键 · 来源 DC %s · Namespace %s · Partition %s"), len(x.bundle.Entries), x.bundle.Datacenter, x.bundle.Namespace, x.bundle.Partition)).Wrap()
						}
						ui.Select(c, &x.policy, []string{"abort", "skip", "cas"}).Label(locales.Text("导入冲突策略")).Disabled(busy)
						if p := x.preview; p != nil {
							ui.Text(c, fmt.Sprintf(locales.Text("可导入 %t · 独立事务批次 %d · 已完成 %d"), p.CanApply, len(p.Requests), x.batch)).Wrap()
							s.kvToolRows(c, d, x, false)

							ui.Text(c, locales.Text("每批最多 64 个原生操作；各批独立确认与提交，后续读取不会确认未知写入")).Wrap()
						}
						if x.plan != nil {
							ui.Text(c, fmt.Sprintf(locales.Text("确认原连接 #%d 的第 %d 批完整事务"), d.profileID, x.batch+1)).Wrap()
							text := x.planRequest.JSON
							ui.TextArea(c, &text).Label(locales.Text("完整原事务")).Height(140).FillWidth().ReadOnly(true)
							ui.Checkbox(c, &x.checked, locales.Text("我确认执行此独立原生事务批次"))
							if ui.Button(c, locales.Text("确认导入此批")).Disabled(busy || !x.checked).Clicked() {
								s.ConfirmHTTPKVImportBatch(d)
							}
						}
						for i, r := range x.batches {
							ui.Text(c, fmt.Sprintf("%d · %s", i+1, ReceiptStatus(r.Result))).Wrap()
						}
						ui.Checkbox(c, &x.consent, locales.Text("允许将完整原键值 Bundle 保存到文件"))
					})
				})
				if d.error != "" {
					ui.Text(c, clipped(d.error)).Wrap().TextColor(theme.Colors(c).Warning)
				}
				ui.Toolbar(c, func() {
					if ui.Button(c, locales.Text("搜索")).Disabled(busy || d.NeedsRecovery()).Clicked() {
						s.RunHTTPKVSearch(d, false)
					}
					if ui.Button(c, locales.Text("捕获导出 Bundle")).Disabled(busy || d.NeedsRecovery() || x.preview != nil && x.batch < len(x.preview.Requests)).Clicked() {
						s.RunHTTPKVSearch(d, true)
					}
					if ui.Button(c, locales.Text("打开 Bundle")).Disabled(busy || len(x.batches) > 0).Clicked() {
						s.OpenHTTPKVBundle(d)
					}
				}).FillWidth()
				ui.Toolbar(c, func() {
					if ui.Button(c, locales.Text("保存 Bundle")).Disabled(busy || x.bundle == nil || !x.consent).Clicked() {
						s.SaveHTTPKVBundle(d)
					}
					if ui.Button(c, locales.Text("预览 Bundle 导入")).Disabled(busy || d.NeedsRecovery() || x.bundle == nil || len(x.batches) > 0).Clicked() {
						s.PreviewHTTPKVImport(d)
					}
				}).FillWidth()
				ui.Toolbar(c, func() {
					if ui.Button(c, locales.Text("预览下一独立事务")).Disabled(busy || d.NeedsRecovery() || x.preview == nil || d.hasUnknownWrites()).Clicked() {
						s.PrepareHTTPKVImportBatch(d)
					}
					if ui.Button(c, locales.Text("导出完整工具记录")).Disabled(busy || !x.consent).Clicked() {
						s.ExportHTTPKVTools(d)
					}
					if ui.Button(c, locales.Text("取消")).Disabled(!busy).Clicked() {
						s.RequestCancel(d)
					}
				}).FillWidth()
			})
		})
	}
}
func (s *State) ExportHTTPKVTools(d *Document) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.httpKVTools == nil || !d.httpKVTools.consent {
		return
	}
	x := d.httpKVTools
	f := s.deps.Files
	if f.SavePath == nil || f.WriteStream == nil {
		return
	}
	x.consent = false
	raw, err := json.Marshal(struct {
		ExportOriginal *contract.KVResult
		Search         *consulspec.SearchResult
		Bundle         *consulspec.Bundle
		Preview        *consulspec.ImportPreview
		Observations   []contract.KVResult
		Batches        []Receipt
	}{x.exportOriginal, x.search, x.bundle, x.preview, x.observations, x.batches})
	if err != nil || len(raw) > 128<<20 {
		return
	}
	s.fileJob(d, "http-kv-record-save", func(ctx context.Context) fileResult {
		path, err := f.SavePath("consul-kv-original-record.json")
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{err: writeCaptured(ctx, f, path, raw)}
	})
}

func (s *State) kvToolRows(c *ui.Context, d *Document, x *httpKVToolsState, search bool) {
	var count int
	var page *int
	browser := &x.previewBrowser
	page = &x.previewPage
	if search {
		count = len(x.search.Matches)
		page = &x.searchPage
		browser = &x.searchBrowser
	} else {
		count = len(x.preview.Rows)
	}
	p, start, end := pageBounds(*page, count, 25)
	*page = p
	ui.Text(c, fmt.Sprintf(locales.Text("原响应 %d 项 · 第 %d 页"), count, p+1))
	for index := start; index < end; index++ {
		var label string
		var value any
		if search {
			entry := x.search.Matches[index]
			label = entry.Entry.Key
			value = entry
		} else {
			row := x.preview.Rows[index]
			label = fmt.Sprintf("%s · %s · CAS %d", row.Key, row.Operation, row.ExpectedIndex)
			value = struct {
				Row      consulspec.ImportRow
				Source   consulspec.BundleEntry
				Original contract.KVResult
			}{row, x.preview.Bundle.Entries[index], x.observations[index]}
		}
		if ui.Button(c.Key(fmt.Sprintf("kv-tool-row-%t-%d", search, index)), label).FillWidth().Clicked() {
			raw, _ := json.MarshalIndent(value, "", "  ")
			browser.set(fmt.Sprintf("%d-%t-%d", x.revision, search, index), raw)
		}
	}
	ui.Row(c).Gap(5).Children(func() {
		if ui.Button(c, locales.Text("上一页")).Disabled(p == 0).Clicked() {
			*page--
		}
		if ui.Button(c, locales.Text("下一页")).Disabled(end >= count).Clicked() {
			*page++
		}
	})
	if len(browser.rows) > 0 {
		s.privateJSONView(c, d, browser, "kv-tools")
	}
}
