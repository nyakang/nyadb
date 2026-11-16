package kvworkspace

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) znodeReceiptView(c *ui.Context, d *Document, r *Receipt) {
	p := theme.Colors(c)
	ui.Text(c, ReceiptStatus(r.Result)+" · "+r.Result.Code).Bold().SingleLine()
	ui.Text(c, r.Result.Request.Method+" · "+r.Scope.ZNode.Root).SingleLine().Tooltip(r.Scope.Version + "\n" + r.Scope.ZNode.ConnectString)
	if r.Result.ProjectionError != "" {
		ui.Text(c, r.Result.ProjectionError).TextColor(p.Warning).SingleLine().Tooltip(r.Result.ProjectionError)
	}
	if ui.Button(c, locales.Text("复制原请求")).Clicked() {
		c.WriteClipboard(r.Result.Request.Method + "\n" + r.Result.Request.JSON)
	}
	// Native error envelopes may contain secrets; ordinary previews use the
	// sanitized error. Complete native bytes remain available by explicit export.
	text := r.Result.JSON
	if text == "" {
		text = r.Error
	}
	if text == "" {
		text = ReceiptStatus(r.Result)
	}
	if len(text) > previewLimit {
		ui.Text(c, locales.Text("预览最多 64 KiB；复制与导出保留完整内容")).FontSize(theme.Font(c, 11)).TextColor(p.Muted)
	}
	preview := clipped(text)
	area := ui.TextArea(c, &preview).Label(locales.Text("KV 原生回执")).ReadOnly(true).Fill().MinHeight(0)
	theme.BindNativeEdit(c, area, fmt.Sprintf("kv/receipt/%d", d.id), s.deps.EditFocus, s.deps.EditTarget, func() bool { return s.active(d) }, false, false)
}
