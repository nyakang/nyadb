package connectioneditor

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) editorDuckDBFields(c *ui.Context, labelWidth, fieldWidth float32) {
	a := &s.connectionDraft.DuckDBOptions
	theme.Field(c, locales.Text("数据库文件："), func() {
		ui.Row(c).Gap(0).FillWidth().AlignItems(ui.Center).Children(func() {
			s.formTextInput(c, &s.connectionDraft.Database, "数据库文件", fieldWidth-connectionFormBrowseGap-connectionFormBrowseSize, false, false)
			ui.Box(c).Width(connectionFormBrowseGap)
			connectionFormBrowse(c, "浏览文件", s.engineFilePicker == nil, func() { s.engineFilePicker(DuckDBMainFile, -1, s.engineFileVersion) })
		})
	})
	ui.Row(c).Gap(0).Children(func() {
		ui.Box(c).Width(labelWidth)
		connectionFormCheckbox(c, &a.RequireExisting, "现有数据库文件", a.ReadOnly || s.connectionDraft.Database == ":memory:")
	})
	ui.Text(c, locales.Text("输入 :memory: 创建临时内存数据库；断开连接后数据会释放。")).FontSize(theme.Font(c, 11)).TextColor(theme.Colors(c).Muted)
}
func (s *State) duckDBAdvanced(c *ui.Context, width float32) {
	a := &s.connectionDraft.DuckDBOptions
	formWidth := connectionScaledFormWidth(c, width)
	labelWidth, fieldWidth := connectionFormLabelWidth, formWidth-connectionFormLabelWidth
	theme.Form(c, formWidth, func() {
		connectionFormCheckbox(c, &a.ReadOnly, "只读连接", s.connectionDraft.Database == ":memory:")
		s.editorInputRow(c, "内存限制（MiB）", "DuckDB 内存限制", &s.engineNumbers[0], labelWidth, 120)
		s.editorInputRow(c, "工作线程数", "DuckDB 工作线程数", &s.engineNumbers[1], labelWidth, 120)
		s.editorInputRow(c, "后台连接上限", "后台连接上限", &s.engineNumbers[2], labelWidth, 120)
		s.editorInputRow(c, "SQL 会话上限", "SQL 会话上限", &s.engineNumbers[3], labelWidth, 120)
		s.editorInputRow(c, "默认模式：", "默认模式", &a.DefaultSchema, labelWidth, fieldWidth)
		s.editorInputRow(c, "DuckDB 运行库", "DuckDB 运行库", &a.LibraryFile, labelWidth, fieldWidth)
		ui.Text(c, locales.Text("运行库路径可留空，使用随应用安装的 DuckDB 运行库。")).FontSize(theme.Font(c, 11)).TextColor(theme.Colors(c).Muted)
	})
}
