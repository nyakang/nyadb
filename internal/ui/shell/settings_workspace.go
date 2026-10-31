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
		ui.Row(c).Wrap().MinHeight(46).Padding(8, 16).AlignItems(ui.Center).
			Background(palette.Header).Gap(9).Children(func() {
			theme.Icon(c, "settings", 18, palette.Blue)
			ui.Text(c, locales.Text("外观设置")).FontSize(theme.Font(c, 16)).Bold()
			ui.Box(c).Grow(1)
			if s.settings.saveFailed {
				ui.Text(c, locales.Text("无法保存主题设置，请重试。")).FontSize(theme.Font(c, 11)).TextColor(palette.Error)
			}
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(16).Gap(13).FillWidth().Children(func() {
				ui.Text(c, locales.Text("修改后立即应用并自动保存")).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
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
				ui.Column(c).Key("scheme-gallery").FillWidth().Gap(10).Children(func() {
					ui.Text(c, locales.Text("配色方案")).FontSize(theme.Font(c, 12)).Bold()
					schemes := theme.Schemes()
					columns, cardWidth := s.schemeCardLayout(c)
					for start := 0; start < len(schemes); start += columns {
						ui.Row(c).FillWidth().Gap(10).Children(func() {
							for _, scheme := range schemes[start:min(start+columns, len(schemes))] {
								s.schemePreviewCard(c, scheme, cardWidth)
							}
							if columns == 2 && start+1 == len(schemes) {
								ui.Box(c).Grow(1).MinWidth(0)
							}
						})
					}
				})
				ui.Box(c).Height(1).FillWidth().Background(palette.Border)
				ui.Column(c).Key("reading-settings").FillWidth().Gap(13).Children(func() { s.readingSettings(c) })
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
								ui.Text(c, "NULL").Italic().FontSize(theme.Font(c, 12)).TextColor(theme.CellText(c, &s.settings.sampleTable, row, col, palette.GridNull))
								return
							}
							ui.Text(c, locales.Text("正常")).FontSize(theme.Font(c, 12)).TextColor(theme.CellText(c, &s.settings.sampleTable, row, col, palette.Success))
						}
					}).Height(theme.RowHeight(c, 140) + 3*theme.TableRowExtra(c)).FillWidth()
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
	theme.ApplySQLAppearance(c, s.settings.preview)
	ui.Box(c).Height(212).FillWidth().Border(1, palette.Border).Children(func() {
		editor.View(c, s.settings.preview).Fill()
	})
}

// The workbench split percentages are the same source used by native layout.
// Reserve borders, padding and scrollbar before choosing the card arrangement.
func (s *State) schemeCardLayout(c *ui.Context) (int, float32) {
	width, _ := c.Size()
	width = (width - 4) * (1 - s.sidebarPercent/100)
	if s.inspectorOn {
		width = (width - 4) * s.centerPercent / 100
	}
	width = max(float32(100), width-52)
	if width >= 2*theme.Font(c, 360)+10 {
		return 2, (width - 10) / 2
	}
	return 1, width
}

func (s *State) schemePreviewCard(c *ui.Context, scheme theme.SchemeInfo, width float32) {
	palette := theme.Colors(c)
	selected := theme.Frame(c).Scheme == scheme.ID
	frame := ui.ButtonBase(c).Label(locales.Text(scheme.Name)+" "+locales.Text("主题预览")).
		Selected(selected).Tooltip(locales.Text(scheme.Description)).
		Grow(1).MinWidth(0).Padding(0).Radius(5).Background(palette.Panel).Border(2, palette.Border)
	if selected {
		frame.Border(2, palette.Focus)
	}
	frame.Children(func() {
		ui.Column(c).FillWidth().MinWidth(0).Children(func() {
			ui.Row(c).Padding(5, 10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.Text(scheme.Name)).FontSize(theme.Font(c, 12)).Bold().TextColor(palette.Text)
				ui.Box(c).Grow(1)
				if selected {
					ui.Text(c, "✓").TextColor(palette.Focus)
				}
			})
			variants := ui.Row(c).FillWidth().MinWidth(0).Gap(1)
			if width < theme.Font(c, 360) {
				variants.Column()
			}
			variants.Children(func() {
				for _, mode := range []theme.Mode{theme.ModeLight, theme.ModeDark} {
					snapshot := theme.ResolveAppearance(scheme.ID, mode, false, c.Preferences(), theme.Frame(c).Metrics)
					schemeVariantPreview(c, snapshot)
				}
			})
		})
	})
	if frame.Clicked() {
		s.changeColorScheme(scheme.ID)
		c.Invalidate()
	}
}

// Static swatches use resolved values, never a local Apply or a live document.
func schemeVariantPreview(c *ui.Context, snapshot theme.Snapshot) {
	p, syntax := snapshot.Palette, snapshot.SQL
	label := "浅色"
	if snapshot.Dark {
		label = "深色"
	}
	ui.Column(c).Grow(1).MinWidth(0).Background(p.App).TextColor(p.Text).Children(func() {
		ui.Row(c).FillWidth().Padding(3, 7).Background(p.Header).Children(func() {
			ui.Text(c, locales.Text(label)).FontSize(theme.Font(c, 10)).TextColor(p.Muted)
		})
		ui.Row(c).FillWidth().Children(func() {
			ui.Box(c).Width(12).Height(theme.Font(c, 50)).Background(p.Sidebar)
			ui.Column(c).Grow(1).MinWidth(0).Padding(5).Background(syntax.Background).Children(func() {
				ui.Row(c).Gap(4).Children(func() {
					ui.Text(c, "SELECT").FontSize(theme.Font(c, 10)).TextColor(syntax.TokenColor(editor.TokenKeyword))
					ui.Text(c, "id").FontSize(theme.Font(c, 10)).TextColor(syntax.Text)
				})
				ui.Text(c, "'NyaDB'").FontSize(theme.Font(c, 10)).TextColor(syntax.TokenColor(editor.TokenString))
			})
		})
		for row, text := range []string{"1   users", "2   orders", "3   NULL", "*   products"} {
			bg, ink := p.App, p.Text
			switch row {
			case 1:
				bg, ink = p.Selection, p.SelectionText
			case 2:
				bg, ink = p.GridAlternate, p.GridNull
			case 3:
				bg, ink = p.GridModified, p.GridModifiedText
			}
			ui.Row(c).FillWidth().Padding(2, 7).Background(bg).Children(func() {
				t := ui.Text(c, text).SingleLine().FontSize(theme.Font(c, 10)).TextColor(ink)
				if row == 2 {
					t.Italic()
				}
			})
		}
	})
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
	metricsChanged func(theme.MetricsPreferences)
	uiSizeInput    readingSizeInput
	sqlSizeInput   readingSizeInput
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
