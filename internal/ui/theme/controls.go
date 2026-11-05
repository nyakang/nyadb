package theme

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// CommandButton keeps native toolbar styling and command semantics.
func CommandButton(c *ui.Context, caption, label, icon string) ui.Element {
	return ui.Button(c, "").Label(label).Tooltip(label).
		MinHeight(RowHeight(c, ControlHeight)).Padding(2, 5).Radius(ControlRadius).
		Children(func() {
			ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
				if icon != "" {
					Icon(c, icon, SmallIconSize, c.Theme().Text)
				}
				if caption != "" {
					ui.Text(c, caption).SingleLine().FontSize(Font(c, FontSize))
				}
			})
		})
}

func IconButton(c *ui.Context, label, icon string) ui.Element {
	return CommandButton(c, "", label, icon).Size(RowHeight(c, ControlHeight), RowHeight(c, ControlHeight)).Padding(0)
}

// Tabs provides compact native tab semantics. The owner validates changes.
func Tabs(c *ui.Context, selected *int, labels []string, disabled bool, group ...string) ui.Element {
	p := Colors(c)
	parts := ui.TabsBase(c, selected, len(labels))
	parts.List.Gap(2).Disabled(disabled).Children(func() {
		for i, label := range labels {
			name := label
			if len(group) > 0 {
				name = group[0] + "/" + label
			}
			tab := parts.Tab(i).Label(name).MinHeight(RowHeight(c, TabHeight)).Padding(3, 8).Radius(ControlRadius)
			if i == *selected {
				tab.Background(p.Selection).TextColor(p.SelectionText)
			}
			tab.Children(func() { ui.Text(c, label).SingleLine().FontSize(Font(c, FontSize)) })
		}
	})
	return parts.List
}

// Form retains compact spacing, letting MyGo measure the shared label column.
// Narrow forms stack fields so long translated labels cannot squeeze inputs.
func Form(c *ui.Context, width float32, build func()) ui.Element {
	if width < 520*max(float32(1), Font(c, FontSize)/FontSize) {
		return ui.Column(c).Width(width).MinWidth(0).Gap(5).Children(build)
	}
	return ui.Form(c, build).Width(width).MinWidth(0).Gap(5)
}

func Fieldset(c *ui.Context, legend string, build func()) ui.Element {
	return ui.Fieldset(c, legend, build).Gap(5).Margin(0).FillWidth().MinWidth(0)
}

// Choice binds stable values, rather than parsing a translated display label.
func Choice[K comparable](c *ui.Context, value *K, values []K, label func(K) string) ui.Element {
	parts := ui.SelectBase(c, value)
	p := Colors(c)
	parts.Trigger.MinHeight(RowHeight(c, ControlHeight)).Padding(2, 8).
		Background(p.Panel).Border(1, p.InputBorder).Radius(ControlRadius).
		Children(func() { ui.Text(c, label(*value)).SingleLine(); Icon(c, "chevron-down", 12, p.Muted) })
	parts.Popup(func(panel ui.Element) {
		panel.Padding(3).Background(p.Panel).Border(1, p.Border).Radius(ControlRadius)
		for _, v := range values {
			parts.Item(v).Padding(3, 8).Children(func() { ui.Text(c, label(v)).SingleLine() })
		}
	})
	return parts.Trigger
}

// Modal has one padding/surface owner. Existing workflows own dismissal and
// content scrolling, including tables and editors with their own scroll state.
func Modal(c *ui.Context, open *bool, build func()) ui.Element {
	return ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		w, h := c.Size()
		backdrop.Background(ui.RGBA(0, 0, 0, 0.4))
		panel.Padding(14).Gap(8).Radius(ControlRadius).Background(Colors(c).Panel).
			MaxWidth(max(float32(1), w-40)).MaxHeight(max(float32(1), h-40)).
			Shadow(0, 10, 30, 0, ui.RGBA(0, 0, 0, 0.3)).Children(func() {
			ui.Column(c).MinWidth(0).MinHeight(0).MaxWidth(max(float32(1), w-68)).MaxHeight(max(float32(1), h-68)).Children(build)
		})
	})
}

func LabelWidth(c *ui.Context, label string, minimum float32) float32 {
	w, _ := c.MeasureText(0, ui.Span{Text: label, Size: Font(c, FontSize)})
	return max(minimum, w+8)
}

func TextField(c *ui.Context, value *string, label string) ui.Element {
	var input ui.Element
	Field(c, label, func() { input = ui.TextInput(c, value).Label(label).FillWidth() }).MinWidth(0)
	return input
}

// Distinguish the visible caption from the input's accessible name.
func Field(c *ui.Context, label string, build func()) ui.Element {
	if !strings.HasSuffix(label, ":") && !strings.HasSuffix(label, "：") {
		label += ":"
	}
	return ui.Field(c, label, build)
}
