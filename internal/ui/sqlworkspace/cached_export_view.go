package sqlworkspace

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type cachedExportDraft struct {
	Open             bool
	Format           string
	Selection        bool
	Cached, Selected contract.ResultSet
}

func (s *State) cachedExportView(c *ui.Context, d *Document) {
	if !d.cachedExport.Open {
		d.cachedExport = cachedExportDraft{}
		return
	}
	x := &d.cachedExport
	theme.Modal(c, &x.Open, func() {
		ui.Column(c).Width(440).MaxWidth(440).Gap(10).Children(func() {
			ui.Text(c, locales.Text("导出缓存的查询结果")).Bold()
			theme.Field(c, locales.Text("导出格式"), func() { ui.Select(c, &x.Format, cachedResultFormats()).Label(locales.Text("导出格式")) })
			theme.Field(c, locales.Text("导出范围"), func() {
				theme.Choice(c, &x.Selection, []bool{false, true}, func(selection bool) string {
					if selection {
						return locales.Text("结果选区")
					}
					return locales.Text("缓存的当前结果")
				}).Label(locales.Text("导出范围"))
			})
			set := x.Cached
			if x.Selection {
				set = x.Selected
			}
			ui.Text(c, fmt.Sprintf(locales.Text("%d 行 / %d 列"), len(set.Rows), len(set.Columns)))
			ui.Text(c, locales.Text("仅导出已缓存的数据；不会重新执行 SQL")).TextColor(theme.Colors(c).Muted)
			if len(set.Truncation) > 0 {
				ui.Text(c, truncationText(set.Truncation)).TextColor(theme.Colors(c).Warning)
			}
			ui.Row(c).Wrap().Gap(8).Justify(ui.End).Shrink(0).Children(func() {
				if ui.Button(c, locales.Text("取消")).Clicked() {
					x.Open = false
				}
				if ui.PrimaryButton(c, locales.Text("导出")).Disabled(len(set.Columns) == 0 || x.Selection && len(set.Rows) == 0).Clicked() {
					x.Open = false
					s.exportCachedSet(d, set, x.Format)
				}
			})
		})
	})
	if !x.Open {
		d.cachedExport = cachedExportDraft{}
	}
}

func cloneCachedResultSet(set contract.ResultSet) contract.ResultSet {
	out := contract.ResultSet{AffectedRows: set.AffectedRows, Columns: append([]contract.Column(nil), set.Columns...), Truncation: append([]string(nil), set.Truncation...), DefaultHiddenColumns: append([]int(nil), set.DefaultHiddenColumns...), LayoutKey: set.LayoutKey}
	for _, row := range set.Rows {
		cells := make([]contract.Cell, len(row))
		for i, c := range row {
			cells[i] = c.Clone()
		}
		out.Rows = append(out.Rows, cells)
	}
	return out
}
func (s *State) openCachedExport(d *Document) {
	if d == nil || d.closed || d.result == nil {
		return
	}
	d.cachedExport = cachedExportDraft{Open: true, Format: "CSV", Cached: cloneCachedResultSet(resultData(d, false)), Selected: cloneCachedResultSet(resultData(d, true))}
}
