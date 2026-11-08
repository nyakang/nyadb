package dataworkspace

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type nativeSelectorLabels struct {
	title, toolbar, input, hint, apply, clear, collapse, invalid, restored string
}

func selectorLabels(dialect string) (nativeSelectorLabels, bool) {
	switch dialect {
	case "qdrant":
		return nativeSelectorLabels{"Qdrant", "Qdrant 过滤", "Qdrant filter JSON", "输入原生 filter 对象；使用 ID 游标翻页。计数和完整 JSONL 导出覆盖整个集合，表格导出使用当前过滤。", "应用 Qdrant 过滤", "清除 Qdrant 过滤", "收起 Qdrant 过滤", "请输入有效的 Qdrant filter JSON 对象", "Qdrant 过滤已恢复；请点击应用 Qdrant 过滤执行"}, true
	case "mango":
		return nativeSelectorLabels{"Mango", "Mango 查询", "Mango selector JSON", "输入 selector 对象；查询排除设计文档，文档总数包含设计文档。", "应用 Mango 查询", "清除 Mango 查询", "收起 Mango 查询", "请输入有效的 Mango selector JSON 对象", "Mango 查询已恢复；请点击应用 Mango 查询执行"}, true
	case "meilisearch":
		return nativeSelectorLabels{"Meilisearch", "Meilisearch 搜索", "Meilisearch 搜索 JSON", "输入 q、filter、sort、facets 等原生搜索参数；使用网格翻页。搜索命中及表格导出受服务器上限限制，完整 JSONL 导出忽略搜索。", "应用 Meilisearch 搜索", "清除 Meilisearch 搜索", "收起 Meilisearch 搜索", "搜索 JSON 必须为有效对象，且不能包含 offset、limit、page 或 hitsPerPage", "Meilisearch 搜索已恢复；请点击应用 Meilisearch 搜索执行"}, true
	}
	return nativeSelectorLabels{}, false
}

func (s *State) selectorFor(d *Document) contract.DataSelector {
	if d == nil || d.closed || !d.capabilities.Documents {
		return contract.DataSelector{}
	}
	f := s.runtimeFor(d).Selector
	if f.Column == "" || f.Mode == "" || f.Validate == nil {
		return contract.DataSelector{}
	}
	return f
}

func (s *State) applyDataSelector(services ui.Services, d *Document) {
	if d == nil || d.closed || d.loading {
		return
	}
	f := s.selectorFor(d)
	labels, supported := selectorLabels(f.Dialect)
	if !supported {
		return
	}
	raw := strings.TrimSpace(d.selectorDraft)
	if err := f.Validate(raw); err != nil {
		d.selectorError = locales.Text(labels.invalid)
		return
	}
	if s.GuardLeave(d, func(next ui.Services) { s.applyDataSelector(next, d) }) {
		return
	}
	d.selectorDraft, d.selectorError = raw, ""
	d.filterRules, d.filterExtraDraft = nil, nil
	d.filterCondition, d.groupDraft = nil, nil
	d.disabledGroupRules = nil
	d.mainFilterDisabled = false
	d.filterColumnDraft, d.filterValueDraft, d.ruleError = "选择字段", "", ""
	d.filterColumn, d.filterMode, d.filterValue = f.Column, f.Mode, raw
	if f.Dialect == "meilisearch" || f.Dialect == "qdrant" {
		d.sortColumn, d.sortDescending, d.sortRules, d.sortDraft, d.sort = "", false, nil, nil, ui.SortOrder{}
	}
	if d.selectorHistory && f.Dialect != "meilisearch" && f.Dialect != "qdrant" {
		d.sortColumn, d.sortDescending = "", false
		d.sortRules = slices.Clone(d.selectorHistorySorts)
		d.sort = ui.SortOrder{}
		if len(d.sortRules) > 0 {
			d.sort = ui.SortOrder{Column: d.sortRules[0].Column, Descending: d.sortRules[0].Descending}
		}
	}
	d.selectorHistory, d.selectorHistorySorts = false, nil
	s.Refresh(services, d)
}

func (s *State) dataSelectorView(c *ui.Context, d *Document) {
	labels, supported := selectorLabels(s.selectorFor(d).Dialect)
	if !d.selectorOpen || !supported {
		return
	}
	p := theme.Colors(c)
	ui.Column(c).FillWidth().Height(170).Shrink(0).Padding(4, 8).Gap(4).Background(p.Header).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, labels.title).Bold()
			ui.Text(c, locales.Text(labels.hint)).FontSize(theme.Font(c, theme.SmallFontSize)).SingleLine().Grow(1).MinWidth(0).Tooltip(locales.Text(labels.hint))
		})
		if d.page != nil && d.page.Search != nil {
			info := d.page.Search
			ui.Text(c, fmt.Sprintf(locales.Text("估算命中：%d · 服务端搜索：%d ms"), info.EstimatedTotalHits, info.ProcessingTimeMS)).FontSize(theme.Font(c, theme.SmallFontSize)).SingleLine()
		}
		if d.selectorError != "" {
			ui.Text(c, d.selectorError).TextColor(p.Error).FontSize(theme.Font(c, theme.SmallFontSize)).SingleLine().Tooltip(d.selectorError)
		}
		if d.selectorResponseOpen && d.page != nil && d.page.Search != nil {
			response := d.page.Search.RawJSON
			count := 0
			for index := range response {
				if count == 20_480 {
					response = response[:index]
					ui.Text(c, locales.Text("响应预览已截断；可复制完整搜索响应。")).FontSize(theme.Font(c, theme.SmallFontSize)).SingleLine()
					break
				}
				count++
			}
			ui.TextArea(c, &response).Label(locales.Text("原生搜索响应预览")).FillWidth().Grow(1).MinHeight(0).ReadOnly(true)
		} else {
			ui.TextArea(c, &d.selectorDraft).Label(locales.Text(labels.input)).FillWidth().Grow(1).MinHeight(0).ReadOnly(d.loading)
		}
		ui.Toolbar(c, func() {
			if ui.Button(c, locales.Text(labels.apply)).Disabled(d.loading).Clicked() {
				s.applyDataSelector(c.Services(), d)
			}
			if ui.Button(c, locales.Text(labels.clear)).Label(locales.Text(labels.clear)).Disabled(d.loading).Clicked() {
				s.clearMySQLDataFilter(c.Services(), d)
			}
			if ui.Button(c, locales.Text(labels.collapse)).Label(locales.Text(labels.collapse)).Clicked() {
				d.selectorOpen = false
			}
			if d.page != nil && d.page.Search != nil {
				if ui.Button(c, locales.Text("搜索响应")).Label(locales.Text("查看原生搜索响应")).Clicked() {
					d.selectorResponseOpen = !d.selectorResponseOpen
				}
				if ui.Button(c, locales.Text("复制完整搜索响应")).Clicked() {
					c.WriteClipboard(d.page.Search.RawJSON)
				}
			}
		}).FillWidth().Height(theme.RowHeight(c, theme.CommandBarHeight)).Gap(6)
	})
}
