package programviewer

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func (d *Document) View(c *ui.Context) {
	if d == nil || d.closed {
		return
	}
	palette := theme.Colors(c)
	obj := d.data
	ui.Column(c).Fill().Background(palette.App).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(3, 8).Gap(8).AlignItems(ui.Center).
			Background(palette.Header).Children(func() {
			theme.Icon(c, "function", 14, palette.Muted)
			ui.Text(c, d.database+" / "+obj.Name).FontSize(theme.Font(c, 12)).Grow(1).MinWidth(0).SingleLine()
			ui.Text(c, obj.Kind+" · 只读").TextColor(palette.Muted).FontSize(theme.Font(c, 11))
		})
		ui.ScrollBoth(c).Grow(1).Children(func() {
			ui.Column(c).Padding(12, 14).Gap(7).MinWidth(480).Children(func() {
				for _, field := range []struct{ label, value string }{
					{"类型", obj.Kind}, {"名称", obj.Name}, {"定义者", obj.Definer},
					{"参数", obj.Parameters}, {"返回类型", obj.ReturnType},
					{"SQL SECURITY", obj.SQLSecurity}, {"状态", obj.Status},
					{"调度", obj.Schedule}, {"开始时间", obj.Starts}, {"结束时间", obj.Ends},
					{"ON COMPLETION", obj.OnCompletion},
					{"创建时间", obj.CreatedAt}, {"修改时间", obj.ModifiedAt}, {"注释", obj.Comment},
				} {
					if field.value == "" {
						continue
					}
					f := field
					ui.Row(c).AlignItems(ui.Start).Gap(12).Children(func() {
						ui.Text(c, f.label).Width(115).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
						ui.Text(c, f.value).FontSize(theme.Font(c, 12)).Grow(1).MinWidth(0)
					})
				}
				ui.Text(c, "对象定义（由服务器返回）").FontSize(theme.Font(c, 12)).Bold()
				for _, line := range definitionLines(obj.Definition) {
					ui.Text(c, line).FontSize(theme.Font(c, 11))
				}
			})
		})
	})
}

// Bound native rows and intermediate slices while retaining the full definition
// in the document snapshot. The final row reports the existing 500-line limit.
func definitionLines(definition string) []string {
	definition = strings.TrimSpace(definition)
	if definition == "" {
		definition = "当前账号未获取到定义，可能受到服务器权限限制"
	}
	lines := strings.SplitN(definition, "\n", 501)
	if len(lines) > 500 {
		lines[500] = "… (定义过长，仅展示前 500 行)"
	}
	return lines
}
