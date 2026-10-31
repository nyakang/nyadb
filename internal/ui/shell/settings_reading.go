package shell

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type readingSizeInput struct {
	value   string
	focused bool
	reveal  bool
}

func (s *State) SetMetricsHandler(changed func(theme.MetricsPreferences)) {
	s.settings.metricsChanged = changed
}

func (s *State) changeMetrics(metrics theme.MetricsPreferences) bool {
	metrics = theme.NormalizeMetrics(metrics)
	if !s.saveThemePreference(theme.MetricsPreferenceKey, theme.EncodeMetrics(metrics)) {
		return false
	}
	theme.SetMetrics(metrics)
	if s.settings.metricsChanged != nil {
		s.settings.metricsChanged(metrics)
	}
	return true
}

func (s *State) readingSettings(c *ui.Context) {
	ui.Text(c, locales.Text("阅读设置")).Bold().FontSize(theme.Font(c, 14))
	ui.Text(c, locales.Text("字号在按 Enter 或离开输入框后应用。SQL 字号独立，系统文字缩放仍然生效。")).FontSize(theme.Font(c, 11)).TextColor(theme.Colors(c).Muted)
	s.readingSizeControl(c, "界面基准字号", &s.settings.uiSizeInput, false)
	s.readingSizeControl(c, "SQL 字号", &s.settings.sqlSizeInput, true)
	ui.Text(c, locales.Text("表格行高")).Bold().FontSize(theme.Font(c, 12))
	ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
		for _, item := range []struct {
			density theme.TableDensity
			label   string
		}{
			{theme.DensityCompact, "紧凑"}, {theme.DensityStandard, "标准"}, {theme.DensityRelaxed, "宽松"},
		} {
			metrics := theme.Frame(c).Metrics
			if ui.Button(c, locales.Text(item.label)).Selected(metrics.TableDensity == item.density).Clicked() {
				metrics = theme.CurrentMetrics()
				metrics.TableDensity = item.density
				s.changeMetrics(metrics)
				c.Invalidate()
			}
		}
		if ui.Button(c, locales.Text("恢复默认")).Label(locales.Text("恢复默认行高")).Clicked() {
			metrics := theme.CurrentMetrics()
			metrics.TableDensity = theme.DensityCompact
			s.changeMetrics(metrics)
			c.Invalidate()
		}
	})
}

func (s *State) readingSizeControl(c *ui.Context, label string, input *readingSizeInput, sql bool) {
	reveal := input.reveal
	input.reveal = false
	metrics := theme.Frame(c).Metrics
	current, low, high, fallback, reset := metrics.UIFontSize, 11, 16, 12, "恢复默认界面字号"
	if sql {
		current, low, high, fallback, reset = metrics.SQLFontSize, 10, 24, 13, "恢复默认 SQL 字号"
	}
	if !input.focused {
		input.value = strconv.Itoa(current)
	}
	apply := func(value int) {
		m := theme.CurrentMetrics()
		if sql {
			m.SQLFontSize = value
		} else {
			m.UIFontSize = value
		}
		if !s.changeMetrics(m) {
			input.value = strconv.Itoa(current)
		}
		input.reveal = true
		c.Invalidate()
	}
	ui.Row(c).Key(label).FillWidth().Wrap().AlignItems(ui.Center).Gap(8).Children(func() {
		ui.Text(c, locales.Text(label)).FontSize(theme.Font(c, 12)).MinWidth(theme.Font(c, 110))
		ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
			inputLabel := "设置界面字号"
			if sql {
				inputLabel = "设置 SQL 字号"
			}
			field := ui.TextInput(c, &input.value).Label(locales.Text(inputLabel)).Width(theme.Font(c, 56)).Height(theme.RowHeight(c, 30))
			if reveal {
				field.ScrollIntoView()
			}
			focused := field.Focused()
			if field.Submitted() || (input.focused && !focused) {
				value, err := strconv.Atoi(strings.TrimSpace(input.value))
				if err == nil && value >= low && value <= high {
					if value != current {
						apply(value)
					}
				} else {
					input.value = strconv.Itoa(current)
				}
			}
			input.focused = focused
			for _, step := range []int{-1, 1} {
				text, action := "−", "减小界面字号"
				if step > 0 {
					text, action = "+", "增大界面字号"
				}
				if sql {
					action = "减小 SQL 字号"
					if step > 0 {
						action = "增大 SQL 字号"
					}
				}
				if ui.Button(c, text).Label(locales.Text(action)).Disabled((step < 0 && current <= low) || (step > 0 && current >= high)).Clicked() {
					value := theme.CurrentMetrics().UIFontSize
					if sql {
						value = theme.CurrentMetrics().SQLFontSize
					}
					value = min(high, max(low, value+step))
					input.value = strconv.Itoa(value)
					apply(value)
				}
			}
		})
		if ui.Button(c, locales.Text("恢复默认")).Label(locales.Text(reset)).Clicked() {
			input.value = strconv.Itoa(fallback)
			apply(fallback)
		}
	})
}
