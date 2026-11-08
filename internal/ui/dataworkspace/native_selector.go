package dataworkspace

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

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
	if f.Dialect != "mango" {
		return
	}
	raw := strings.TrimSpace(d.selectorDraft)
	if err := f.Validate(raw); err != nil {
		d.selectorError = locales.Text("请输入有效的 Mango selector JSON 对象")
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
	if d.selectorHistory {
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
	if !d.selectorOpen || s.selectorFor(d).Dialect != "mango" {
		return
	}
	p := theme.Colors(c)
	ui.Column(c).FillWidth().Height(170).Shrink(0).Padding(4, 8).Gap(4).Background(p.Header).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "Mango").Bold()
			ui.Text(c, locales.Text("输入 selector 对象；查询排除设计文档，文档总数包含设计文档。")).FontSize(theme.Font(c, theme.SmallFontSize)).SingleLine().Grow(1).MinWidth(0)
		})
		ui.TextArea(c, &d.selectorDraft).Label(locales.Text("Mango selector JSON")).FillWidth().Grow(1).MinHeight(0).ReadOnly(d.loading)
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, locales.Text("应用 Mango 查询")).Disabled(d.loading).Clicked() {
				s.applyDataSelector(c.Services(), d)
			}
			if ui.Button(c, locales.Text("清除 Mango 查询")).Label(locales.Text("清除 Mango 查询")).Disabled(d.loading).Clicked() {
				s.clearMySQLDataFilter(c.Services(), d)
			}
			if ui.Button(c, locales.Text("收起 Mango 查询")).Label(locales.Text("收起 Mango 查询")).Clicked() {
				d.selectorOpen = false
			}
			if d.selectorError != "" {
				ui.Text(c, d.selectorError).TextColor(p.Error).SingleLine().Grow(1).MinWidth(0).Tooltip(d.selectorError)
			}
		})
	})
}
