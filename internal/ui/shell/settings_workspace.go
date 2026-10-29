package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

const sqlThemePreview = "-- SQL syntax preview\nSELECT u.id, u.name, COUNT(*) AS total\nFROM users AS u\nWHERE u.active = TRUE AND u.score >= 10\n  AND u.name LIKE 'A%'\nORDER BY total DESC;\n"

func (s *State) openSettings() {
	for i, tab := range s.tabs {
		if tab.kind == tabSettings {
			s.activeTab = i
			return
		}
	}
	s.nextTabID++
	s.tabs = append(s.tabs, &tab{id: s.nextTabID, title: "外观设置", kind: tabSettings})
	s.activeTab = len(s.tabs) - 1
}

func (s *State) settingsWorkspace(c *ui.Context) {
	palette := theme.Colors(c)

	ui.Column(c).Fill().Background(palette.App).Children(func() {
		ui.Row(c).Height(46).Padding(8, 16).AlignItems(ui.Center).
			Background(palette.Header).Gap(9).Children(func() {
			theme.Icon(c, "settings", 18, palette.Blue)
			ui.Text(c, locales.Text("外观设置")).FontSize(theme.Font(c, 16)).Bold()
			ui.Box(c).Grow(1)
			ui.Text(c, locales.Text("修改后立即应用并自动保存")).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(16).Gap(13).FillWidth().Children(func() {
				ui.Text(c, locales.Text("选择主题")).Bold().FontSize(theme.Font(c, 14))
				ui.Text(c, locales.Text("预览深色和浅色配色，选择适合当前工作环境的外观。")).
					FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
				ui.Row(c).FillWidth().Gap(10).Children(func() {
					s.themePreviewCard(c, theme.ModeDark, "深色", true)
					s.themePreviewCard(c, theme.ModeLight, "浅色", false)
				})
				ui.Row(c).AlignItems(ui.Center).Gap(9).Children(func() {
					chosen := theme.CurrentMode() == theme.ModeSystem
					if ui.Button(c, locales.Text("跟随系统")).Border(1, palette.Border).
						Background(palette.Header).Clicked() {
						s.changeAppearance(theme.ModeSystem)
					}
					if chosen {
						ui.Text(c, "✓ "+locales.Text("当前主题")).TextColor(palette.Blue).FontSize(theme.Font(c, 12))
					}
					ui.Text(c, locales.Text("随操作系统的深浅色设置自动切换")).
						TextColor(palette.Muted).FontSize(theme.Font(c, 11))
				})
				ui.Box(c).Height(1).FillWidth().Background(palette.Border)
				s.settingsSQLPreview(c)
				ui.Text(c, locales.Text("原生控件预览")).FontSize(theme.Font(c, 14)).Bold()
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(9).Children(func() {
					ui.PrimaryButton(c, locales.Text("主要操作"))
					ui.Button(c, locales.Text("次要操作")).Border(1, palette.Border)
					ui.Button(c, locales.Text("禁用操作")).Disabled(true)
					ui.TextInput(c, &s.settings.sampleInput).Label(locales.Text("示例输入")).
						Placeholder(locales.Text("输入示例文本")).Width(170)
					ui.Checkbox(c, &s.settings.sampleCheck, locales.Text("示例复选框"))
				})
				ui.Text(c, locales.Text("数据表格预览")).FontSize(theme.Font(c, 12)).Bold()
				s.settings.sampleTable.Selected = &s.settings.sampleRow
				s.settings.sampleTable.TableStyle = &ui.TableStyle{
					AlternateRowColor: palette.GridAlternate, GridColor: palette.Border,
					RowLines: true, ColumnLines: true,
				}
				theme.Table(c, &s.settings.sampleTable,
					[]ui.TableColumn{
						{Title: "ID", Width: 70},
						{Title: locales.Text("名称"), Width: 150},
						{Title: locales.Text("状态"), Width: 135},
					}, 3, func(row, col int) {
						names := []string{"users", "orders", "products"}
						switch col {
						case 0:
							ui.Text(c, []string{"1", "2", "3"}[row]).FontSize(theme.Font(c, 12))
						case 1:
							ui.Text(c, names[row]).FontSize(theme.Font(c, 12))
						default:
							if row == 1 {
								ui.Text(c, "NULL").FontSize(theme.Font(c, 12)).TextColor(theme.CellText(c, &s.settings.sampleTable, row, col, palette.GridNull))
								return
							}
							ui.Text(c, locales.Text("正常")).FontSize(theme.Font(c, 12)).TextColor(theme.CellText(c, &s.settings.sampleTable, row, col, palette.Success))
						}
					}).Height(105).FillWidth()
			})
		})
	})
}

func (s *State) settingsSQLPreview(c *ui.Context) {
	palette := theme.Colors(c)

	ui.Text(c, locales.Text("SQL 语法高亮预览")).FontSize(theme.Font(c, 14)).Bold()
	ui.Text(c, locales.Text("关键字、函数、数字、字符串、注释与编辑器背景将随主题变化。")).
		TextColor(palette.Muted).FontSize(theme.Font(c, 11))
	if s.settings.preview == nil {
		buf := editor.NewBuffer(sqlThemePreview)
		buf.SetLanguage(editor.LanguageNamed("SQL"))
		s.settings.preview = &editor.State{
			Buffer: buf, ReadOnly: true, Label: locales.Text("SQL 语法高亮预览"),
			WrapGuides: []int{80}, NoScrollPastEnd: true,
		}
	}
	s.settings.preview.Theme = theme.SQLThemeFor(c)
	ui.Box(c).Height(212).FillWidth().Border(1, palette.Border).Children(func() {
		editor.View(c, s.settings.preview).Fill()
	})
}

func (s *State) themePreviewCard(c *ui.Context, mode theme.Mode, label string, dark bool) {
	palette := theme.Colors(c)

	p := theme.PaletteFor(mode, dark)
	selected := theme.CurrentMode() == mode
	frame := ui.ButtonBase(c).Label(locales.Text(label)+" "+locales.Text("主题预览")).
		Grow(1).MinWidth(0).Height(150).Padding(0).Radius(5).
		Background(p.App).Border(2, p.Border)
	if selected {
		frame.Border(2, palette.Accent)
	}
	frame.Children(func() {
		ui.Column(c).Fill().Gap(0).Children(func() {
			ui.Row(c).Height(theme.RowHeight(c, 27)).Padding(3, 10).AlignItems(ui.Center).
				Background(p.Menu).Children(func() {
				ui.Text(c, locales.Text(label)).FontSize(theme.Font(c, 12)).Bold().TextColor(p.Text)
				ui.Box(c).Grow(1)
				if selected {
					ui.Text(c, "✓").TextColor(p.Blue).FontSize(theme.Font(c, 14))
				}
			})
			ui.Row(c).Grow(1).Children(func() {
				ui.Column(c).Width(56).Background(p.Sidebar).Padding(8, 5).Gap(7).Children(func() {
					ui.Box(c).Height(5).FillWidth().Radius(2).Background(p.Muted)
					ui.Box(c).Height(5).FillWidth().Radius(2).Background(p.Border)
					ui.Box(c).Height(5).FillWidth().Radius(2).Background(p.Border)
				})
				syntax := theme.SQLTheme(&ui.Theme{Dark: dark})
				ui.Column(c).Grow(1).Padding(9, 11).Gap(5).Children(func() {
					ui.Row(c).Gap(5).Children(func() {
						ui.Text(c, "SELECT").FontSize(theme.Font(c, 11)).TextColor(syntax.TokenColor(editor.TokenKeyword))
						ui.Text(c, "name, id").FontSize(theme.Font(c, 11)).TextColor(p.Text)
					})
					ui.Row(c).Gap(5).Children(func() {
						ui.Text(c, "FROM").FontSize(theme.Font(c, 11)).TextColor(syntax.TokenColor(editor.TokenKeyword))
						ui.Text(c, "users").FontSize(theme.Font(c, 11)).TextColor(p.Text)
					})
					ui.Row(c).Gap(5).Children(func() {
						ui.Text(c, "WHERE").FontSize(theme.Font(c, 11)).TextColor(syntax.TokenColor(editor.TokenKeyword))
						ui.Text(c, "id >").FontSize(theme.Font(c, 11)).TextColor(p.Text)
						ui.Text(c, "10").FontSize(theme.Font(c, 11)).TextColor(syntax.TokenColor(editor.TokenNumber))
					})
					ui.Text(c, "-- preview").FontSize(theme.Font(c, 11)).
						TextColor(syntax.TokenColor(editor.TokenComment))
				})
			})
			ui.Row(c).Height(23).Background(p.Header).Padding(3, 9).
				AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Box(c).Width(30).Height(6).Radius(2).Background(p.Accent)
				ui.Text(c, locales.Text("文本和边框预览")).FontSize(theme.Font(c, 10)).TextColor(p.Muted)
			})
		})
	})
	if frame.Clicked() {
		s.changeAppearance(mode)
		c.Invalidate() // redraw the current native context after its color tokens change
	}
}

type settingsState struct {
	preview     *editor.State
	sampleInput string
	sampleCheck bool
	sampleRow   int
	sampleTable ui.ListState
}
