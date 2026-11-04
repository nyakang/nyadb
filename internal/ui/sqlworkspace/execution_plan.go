package sqlworkspace

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type resultReading struct {
	Result *contract.SQLResult
	Views  map[int]*resultView
	Set    int
}

func (d *Document) captureReading() *resultReading {
	d.saveResultView()
	return &resultReading{d.result, d.resultViews, d.resultSet}
}
func (d *Document) restoreReading(r *resultReading) {
	if r == nil {
		return
	}
	d.result = r.Result
	d.resultViews = r.Views
	d.resultSet = r.Set
	if v := r.Views[r.Set]; v != nil {
		d.grid, d.selection, d.selectedRow = v.Grid, v.Selection, v.Row
		d.hiddenResultColumns, d.resultFieldOpen, d.resultFieldMode = v.Hidden, v.FieldOpen, v.FieldMode
	}
}
func (d *Document) showOrdinaryResult(show bool) {
	if d.showOrdinary == show {
		return
	}
	if show {
		d.explainReading = d.captureReading()
		d.restoreReading(d.ordinaryReading)
	} else {
		d.ordinaryReading = d.captureReading()
		d.restoreReading(d.explainReading)
	}
	d.showOrdinary = show
}
func (s *State) planNavigation(c *ui.Context, d *Document) {
	if d.explainReading == nil && d.plan == nil && d.planError == "" {
		return
	}
	ui.Row(c).Gap(6).Padding(3, 8).Children(func() {
		if ui.Button(c, locales.Text("结构计划")).Disabled(d.plan == nil).Clicked() {
			d.showOrdinaryResult(false)
			d.planVisible = true
		}
		if ui.Button(c, locales.Text("原始计划")).Clicked() {
			d.showOrdinaryResult(false)
			d.planVisible = false
		}
		if ui.Button(c, locales.Text("查询结果")).Disabled(d.ordinaryReading == nil || d.ordinaryReading.Result == nil).Clicked() {
			d.showOrdinaryResult(true)
			d.planVisible = false
		}
	})
	if d.planError != "" {
		ui.Text(c, locales.Text("无法解析结构计划，请查看原始结果或使用普通 EXPLAIN")+": "+d.planError).Padding(3, 8)
	}
}
func (s *State) planView(c *ui.Context, d *Document) {
	ui.Scroll(c).Fill().Padding(8).Children(func() {
		var render func(contract.PlanNode, int)
		render = func(node contract.PlanNode, depth int) {
			ui.Column(c).Padding(2, float32(depth*16)).Gap(2).Children(func() {
				ui.Text(c, node.Label).Bold()
				rows, cost := locales.Text("未知"), locales.Text("未知")
				if node.Rows != nil {
					rows = fmt.Sprintf("≈%g", *node.Rows)
				}
				if node.Cost != nil {
					cost = fmt.Sprintf("%g", *node.Cost)
				}
				ui.Text(c, locales.Text("估算行数")+": "+rows+" · "+locales.Text("成本")+": "+cost).TextColor(theme.Colors(c).Muted)
				if node.Detail != "" {
					ui.Text(c, node.Detail)
				}
			})
			for _, child := range node.Children {
				render(child, depth+1)
			}
		}
		for _, node := range d.plan.Nodes {
			render(node, 0)
		}
	})
}
