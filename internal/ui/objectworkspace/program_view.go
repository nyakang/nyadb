package objectworkspace

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"

	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (d *programDocument) View(c *ui.Context) {
	if d == nil || d.closed {
		return
	}
	palette := theme.Colors(c)
	obj := d.data
	ui.Column(c).Fill().Background(palette.App).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(3, 8).Gap(8).AlignItems(ui.Center).
			Background(palette.Header).Children(func() {
			icon := "function"
			if obj.Kind == "序列" {
				icon = "refresh"
			}
			theme.Icon(c, icon, 14, palette.Muted)
			ui.Text(c, d.database+" / "+obj.Name).FontSize(theme.Font(c, 12)).Grow(1).MinWidth(0).SingleLine()
			ui.Text(c, locales.Text(obj.Kind)+" · "+locales.Text("只读")).TextColor(palette.Muted).FontSize(theme.Font(c, 11))
		})
		ui.ScrollBoth(c).Grow(1).Children(func() {
			ui.Column(c).Padding(12, 14).Gap(7).MinWidth(480).Children(func() {
				for _, field := range programProperties(obj) {
					if field.value == "" {
						continue
					}
					f := field
					ui.Row(c).AlignItems(ui.Start).Gap(12).Children(func() {
						ui.Text(c, locales.Text(f.label)).Width(115).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
						ui.Text(c, f.value).FontSize(theme.Font(c, 12)).Grow(1).MinWidth(0)
					})
				}
				label := "对象定义（由服务器返回）"
				if obj.Kind == "序列" && obj.Sequence != nil {
					label = "序列定义（根据服务器目录参数重建）"
				}
				ui.Text(c, locales.Text(label)).FontSize(theme.Font(c, 12)).Bold()
				for _, line := range definitionLines(obj.Definition) {
					ui.Text(c, line).FontSize(theme.Font(c, 11))
				}
			})
		})
	})
}

func programProperties(obj contract.DBObject) []struct{ label, value string } {
	fields := []struct{ label, value string }{
		{"类型", locales.Text(obj.Kind)}, {"名称", obj.Name}, {"定义者", obj.Definer},
		{"参数", obj.Parameters}, {"返回类型", obj.ReturnType},
		{"SQL SECURITY", obj.SQLSecurity}, {"状态", obj.Status},
		{"调度", obj.Schedule}, {"开始时间", obj.Starts}, {"结束时间", obj.Ends},
		{"ON COMPLETION", obj.OnCompletion},
		{"创建时间", obj.CreatedAt}, {"修改时间", obj.ModifiedAt}, {"注释", obj.Comment},
	}
	if seq := obj.Sequence; seq != nil {
		cycle := "NO CYCLE"
		if seq.Cycle {
			cycle = "CYCLE"
		}
		fields = append(fields, []struct{ label, value string }{
			{"序列类型", seq.DataType}, {"序列起点", strconv.FormatInt(seq.Start, 10)},
			{"序列步长", strconv.FormatInt(seq.Increment, 10)}, {"序列最小值", strconv.FormatInt(seq.Min, 10)},
			{"序列最大值", strconv.FormatInt(seq.Max, 10)}, {"序列缓存", strconv.FormatInt(seq.Cache, 10)}, {"序列循环", cycle},
		}...)
	}
	return fields
}

// Bound native rows and intermediate slices while retaining the full definition
// in the document snapshot. The final row reports the existing 500-line limit.
func definitionLines(definition string) []string {
	definition = strings.TrimSpace(definition)
	if definition == "" {
		definition = locales.Text("当前账号未获取到定义，可能受到服务器权限限制")
	}
	lines := strings.SplitN(definition, "\n", 501)
	if len(lines) > 500 {
		lines[500] = locales.Text("… (定义过长，仅展示前 500 行)")
	}
	return lines
}
