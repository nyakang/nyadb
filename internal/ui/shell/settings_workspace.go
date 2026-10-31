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
		ui.Row(c).MinHeight(46).Padding(8, 16).AlignItems(ui.Center).
			Background(palette.Header).Gap(9).Children(func() {
			theme.Icon(c, "settings", 18, palette.Blue)
			ui.Text(c, locales.Text("外观设置")).FontSize(theme.Font(c, 16)).Bold()
			ui.Box(c).Grow(1)
			if s.settings.saveFailed {
				ui.Text(c, locales.Text("无法保存主题设置，请重试。")).FontSize(theme.Font(c, 11)).TextColor(palette.Error)
			} else {
				ui.Text(c, locales.Text("修改后立即应用并自动保存")).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
			}
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(16).Gap(13).FillWidth().Children(func() {
				ui.Text(c, locales.Text("选择主题")).Bold().FontSize(theme.Font(c, 14))
				ui.Text(c, locales.Text("选择配色方案，再搭配喜欢的明暗模式。所有窗口将同步更新。")).
					FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
				ui.Text(c, locales.Text("明暗模式")).FontSize(theme.Font(c, 12)).Bold()
				mode := theme.CurrentMode()
				ui.RadioGroup(c, func() {
					for _, choice := range []struct {
						mode theme.Mode
						name string
					}{{theme.ModeSystem, "跟随系统"}, {theme.ModeLight, "浅色"}, {theme.ModeDark, "深色"}} {
						button := ui.RadioBase(c, &mode, choice.mode).Label(locales.Text(choice.name)).
							Grow(1).MinWidth(0).Height(theme.RowHeight(c, 30)).Radius(3).Border(1, palette.Border).Center()
						background, ink := palette.Header, palette.Text
						if theme.CurrentMode() == choice.mode {
							background, ink = palette.Selection, palette.SelectionText
						}
						button.Background(background).Children(func() {
							ui.Text(c, locales.Text(choice.name)).FontSize(theme.Font(c, 12)).TextColor(ink)
						})
					}
				}).Row().FillWidth().Gap(8).Label(locales.Text("明暗模式"))
				if mode != theme.CurrentMode() {
					s.changeAppearance(mode)
					c.Invalidate()
				}
				if theme.CurrentMode() == theme.ModeSystem {
					label := "当前使用浅色配色"
					if theme.Frame(c).Dark {
						label = "当前使用深色配色"
					}
					ui.Text(c, locales.Text(label)+" · "+locales.Text("随操作系统的深浅色设置自动切换")).
						TextColor(palette.Muted).FontSize(theme.Font(c, 11))
				}
				ui.Text(c, locales.Text("配色方案")).FontSize(theme.Font(c, 12)).Bold()
				schemes := theme.Schemes()
				for start := 0; start < len(schemes); start += 2 {
					ui.Row(c).FillWidth().Gap(10).Children(func() {
						for _, scheme := range schemes[start:min(start+2, len(schemes))] {
							s.schemePreviewCard(c, scheme)
						}
						if start+1 == len(schemes) {
							ui.Box(c).Grow(1).MinWidth(0)
						}
					})
				}
				ui.Box(c).Height(1).FillWidth().Background(palette.Border)
				s.settingsSQLPreview(c)
				ui.Text(c, locales.Text("原生控件预览")).FontSize(theme.Font(c, 14)).Bold()
				ui.Row(c).FillWidth().Wrap().AlignItems(ui.Center).Gap(9).Children(func() {
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

func (s *State) schemePreviewCard(c *ui.Context, scheme theme.SchemeInfo) {
	palette := theme.Colors(c)
	preview := theme.ResolveScheme(scheme.ID, theme.CurrentMode(), theme.Frame(c).Dark, c.Preferences())
	p, syntax := preview.Palette, preview.SQL
	selected := theme.CurrentScheme() == scheme.ID
	frame := ui.ButtonBase(c).Label(locales.Text(scheme.Name)+" "+locales.Text("主题预览")).
		Selected(selected).
		Tooltip(locales.Text(scheme.Description)).
		Grow(1).MinWidth(0).Height(theme.RowHeight(c, 120)).Padding(0).Radius(5).
		Background(p.App).Border(2, p.Border)
	if selected {
		frame.Border(2, palette.Accent)
	}
	frame.Children(func() {
		ui.Column(c).Fill().Gap(0).Children(func() {
			ui.Row(c).Height(theme.RowHeight(c, 27)).Padding(3, 10).AlignItems(ui.Center).
				Background(p.Menu).Children(func() {
				ui.Text(c, locales.Text(scheme.Name)).FontSize(theme.Font(c, 12)).Bold().TextColor(p.Text)
				ui.Box(c).Grow(1)
				if selected {
					ui.Text(c, "✓").TextColor(p.Blue).FontSize(theme.Font(c, 14))
				}
			})
			ui.Row(c).Grow(1).Children(func() {
				ui.Column(c).Width(36).Background(p.Sidebar).Padding(8, 5).Gap(7).Children(func() {
					ui.Box(c).Height(5).FillWidth().Radius(2).Background(p.Muted)
					ui.Box(c).Height(5).FillWidth().Radius(2).Background(p.Border)
					ui.Box(c).Height(5).FillWidth().Radius(2).Background(p.Border)
				})
				ui.Column(c).Grow(1).MinWidth(0).Background(syntax.Background).Padding(6, 8).Gap(4).Children(func() {
					ui.Row(c).Gap(5).Children(func() {
						ui.Text(c, "SELECT").FontSize(theme.Font(c, 11)).TextColor(syntax.TokenColor(editor.TokenKeyword))
						ui.Text(c, "name").FontSize(theme.Font(c, 11)).TextColor(syntax.Text)
					})
					ui.Row(c).Gap(5).Children(func() {
						ui.Text(c, "FROM").FontSize(theme.Font(c, 11)).TextColor(syntax.TokenColor(editor.TokenKeyword))
						ui.Text(c, "users").FontSize(theme.Font(c, 11)).TextColor(p.Text)
					})
				})
			})
			ui.Row(c).Height(23).Background(p.Header).Padding(3, 9).
				AlignItems(ui.Center).Gap(6).Children(func() {
				for _, color := range []ui.Color{p.Accent, syntax.TokenColor(editor.TokenFunction), syntax.TokenColor(editor.TokenString), syntax.TokenColor(editor.TokenNumber)} {
					ui.Box(c).Grow(1).MinWidth(0).Height(5).Radius(2).Background(color)
				}
			})
		})
	})
	if frame.Clicked() {
		s.changeColorScheme(scheme.ID)
		c.Invalidate() // redraw the current native context after its color tokens change
	}
}

type settingsState struct {
	preview        *editor.State
	sampleInput    string
	sampleCheck    bool
	sampleRow      int
	sampleTable    ui.ListState
	savePreference func(key, value string) error
	schemeChanged  func(theme.Scheme)
	saveFailed     bool
}

// SetThemePreferenceSaver keeps persistence out of shell. A failed write
// retains the active choice and offers a retry by selecting the card again.
func (s *State) SetThemePreferenceSaver(save func(key, value string) error) {
	s.settings.savePreference = save
}

func (s *State) SetColorSchemeHandler(changed func(theme.Scheme)) {
	s.settings.schemeChanged = changed
}

func (s *State) saveThemePreference(key, value string) bool {
	s.settings.saveFailed = false
	if save := s.settings.savePreference; save != nil {
		if err := save(key, value); err != nil {
			s.settings.saveFailed = true
			s.status = "无法保存主题设置，请重试。"
			return false
		}
	}
	if s.status == "无法保存主题设置，请重试。" {
		s.status = "主题设置已保存"
	}
	return true
}

func (s *State) changeColorScheme(scheme theme.Scheme) {
	scheme = theme.ParseScheme(string(scheme))
	if !s.saveThemePreference(theme.SchemePreferenceKey, string(scheme)) {
		return
	}
	theme.SetScheme(scheme)
	if s.settings.schemeChanged != nil {
		s.settings.schemeChanged(scheme)
	}
}
