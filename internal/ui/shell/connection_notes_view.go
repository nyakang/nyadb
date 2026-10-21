package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// connectionNotesEditor reproduces the full-width multiline Notes page.
// The draft is persisted only when the connection editor's Confirm action
// succeeds; Cancel discards it along with the other draft changes.
func (s *State) connectionNotesEditor(c *ui.Context, width float32) {
	_, height := c.Size()
	formWidth := max(float32(480), width-40)
	editorHeight := max(float32(180), min(float32(475), height-226))
	ui.Column(c).Width(formWidth).Gap(4).Children(func() {
		ui.Row(c).Height(22).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "备注：*").FontSize(12)
		})
		ui.TextArea(c, &s.connectionDraft.Notes).
			Label("连接备注").
			Width(formWidth).Height(editorHeight).
			Padding(7, 6).FontSize(12).
			Background(theme.App).Border(1, theme.App)
		ui.Row(c).Height(18).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "* 更改将应用于所有连接配置文件。").
				FontSize(11).TextColor(theme.Muted)
		})
	})
}
