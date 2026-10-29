// Package theme defines NyaDB's desktop colors, metrics and vector symbols.
package theme

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/locales"
)

// Snapshot is a value owned by a frame. Resolving another window or changing
// the saved mode never mutates an existing snapshot.
type Snapshot struct {
	Dark      bool
	Palette   Palette
	TextScale float32
}

func Resolve(mode Mode, systemDark bool, prefs ui.Preferences) Snapshot {
	mode = ParseMode(string(mode))
	return snapshot(mode == ModeDark || (mode == ModeSystem && systemDark), prefs)
}

func snapshot(dark bool, prefs ui.Preferences) Snapshot {
	p := lightPalette
	if dark {
		p = darkPalette
	}
	if prefs.Accent.A != 0 {
		p.Accent = prefs.Accent
		p.Accent.A = 255
		p.AccentText = onColor(p.Accent)
		p.AccentHover = readableBackground(p.Accent.Mix(ui.Hex("#000000"), 0.12), p.AccentText)
		p.AccentPressed = readableBackground(p.Accent.Mix(ui.Hex("#000000"), 0.25), p.AccentText)
		p.Focus = p.Accent
		p.Selection = p.Accent.Mix(p.App, 0.25)
	}
	p.SelectionText = onColor(p.Selection)
	if prefs.HighContrast {
		p.Border = p.Border.Mix(p.Text, 0.65)
		p.Muted = p.Muted.Mix(p.Text, 0.65)
		p.Focus = p.Text
	}
	p.InputBorder = p.Border
	if dark && !prefs.HighContrast {
		p.InputBorder = p.Panel
	}
	p.GridAlternate = p.App.Mix(p.Header, 0.5)
	p.GridNull = p.Muted
	scale := prefs.TextScale
	if scale <= 0 {
		scale = 1
	}
	return Snapshot{Dark: dark, Palette: p, TextScale: scale}
}

// Colors uses the frame's resolved appearance, independent of the process's
// saved mode. Local widget theme overrides do not change workbench tokens.
func Colors(c *ui.Context) Palette { return snapshot(c.Theme().Dark, c.Preferences()).Palette }

// Apply maps the frame's snapshot to native MyGo widgets. System preferences
// are applied once to NyaDB's baseline, so repeated calls do not compound them.
func Apply(c *ui.Context) Snapshot {
	s := Resolve(CurrentMode(), c.Theme().Dark, c.Preferences())
	p := s.Palette
	t := *c.Theme()
	t.Dark = s.Dark
	t.Background, t.Surface = p.App, p.Header
	t.SurfaceHover, t.SurfacePressed = p.SurfaceHover, p.SurfacePressed
	t.Border, t.Text, t.TextMuted = p.Border, p.Text, p.Muted
	t.Accent, t.AccentHover, t.AccentPressed, t.AccentText = p.Accent, p.AccentHover, p.AccentPressed, p.AccentText
	t.Selection, t.Focus = p.Selection, p.Focus
	t.Danger, t.Warning, t.Success = p.Error, p.Warning, p.Success
	t.Inverse, t.InverseText = p.Header, p.Text
	if !s.Dark {
		t.Inverse, t.InverseText = ui.Hex("#30353b"), ui.Hex("#ffffff")
	}
	t.Spacing, t.FontSize, t.Radius = 3, FontSize*s.TextScale, ControlRadius
	c.SetTheme(&t)
	return s
}

// SelectionTheme gives native lists/grids the same foreground/background pair
// as custom navigation rows, without changing primary-button colors.
func SelectionTheme(c *ui.Context) *ui.Theme {
	t := *c.Theme()
	p := Colors(c)
	t.Accent, t.AccentText = p.Selection, p.SelectionText
	return &t
}

// InputTheme keeps the connection form's focused border while pairing its
// accent ink correctly even when high contrast replaces Focus with Text.
func InputTheme(c *ui.Context) *ui.Theme {
	t := *c.Theme()
	p := Colors(c)
	t.Surface, t.Border = p.Panel, p.InputBorder
	t.Accent, t.AccentText = p.Focus, onColor(p.Focus)
	t.AccentHover = readableBackground(p.AccentHover, t.AccentText)
	t.AccentPressed = readableBackground(p.AccentPressed, t.AccentText)
	return &t
}

// Table keeps MyGo's selection, virtualization and interaction behavior.
func Table(c *ui.Context, state *ui.ListState, columns []ui.TableColumn, rows int, cell func(row, col int)) ui.Element {
	base := c.Theme()
	c.SetTheme(SelectionTheme(c))
	defer c.SetTheme(base)
	return ui.Table(c, state, columns, rows, cell)
}

// Outline applies the same selected-row colors to native navigation trees.
func Outline[K comparable](c *ui.Context, state *ui.OutlineState[K], roots []K, children func(K) []K, row func(K)) ui.Element {
	base := c.Theme()
	c.SetTheme(SelectionTheme(c))
	defer c.SetTheme(base)
	return ui.Outline(c, state, roots, children, row)
}

// NavigationOutline keeps the database explorer's selection distinct from
// data grids. Navicat-like deep blue is used for focused selection in the
// dark tree only; high-contrast and light modes retain semantic defaults.
func NavigationOutline[K comparable](c *ui.Context, state *ui.OutlineState[K], roots []K, children func(K) []K, row func(K)) ui.Element {
	base := c.Theme()
	selected := SelectionTheme(c)
	if base.Dark && !c.Preferences().HighContrast {
		selected.Accent = ui.Hex("#0878ce")
		selected.AccentText = ui.Hex("#ffffff")
	}
	c.SetTheme(selected)
	defer c.SetTheme(base)
	return ui.Outline(c, state, roots, children, row)
}

// CellText preserves readable ink on selected cells, including semantic
// secondary text. MyGo resolves selection after column reordering/hiding.
func CellText(c *ui.Context, state *ui.ListState, row, col int, normal ui.Color) ui.Color {
	if state.CellSelected(row, col) {
		return Colors(c).SelectionText
	}
	return normal
}

// EmptyTable renders headers without registering a second native list. The
// pinned MyGo version reuses a zero-row Table's ListState within a frame.
// Preserve the real column widths/order without registering a second list or
// inventing a data row. Once rows arrive the native Table resumes ownership.
func EmptyTable(c *ui.Context, state *ui.ListState, columns []ui.TableColumn, message string) {
	palette := Colors(c)

	ordered := make([]ui.TableColumn, 0, len(columns))
	seen := map[string]bool{}
	id := func(col ui.TableColumn) string {
		if col.ID != "" {
			return col.ID
		}
		return col.Title
	}
	for _, key := range state.Columns.Order {
		for _, col := range columns {
			if id(col) == key && !seen[key] && !col.Fixed {
				ordered = append(ordered, col)
				seen[key] = true
			}
		}
	}
	for _, col := range columns {
		if !seen[id(col)] && !col.Fixed {
			ordered = append(ordered, col)
		}
	}
	for i, col := range columns {
		if col.Fixed {
			ordered = append(ordered, ui.TableColumn{})
			copy(ordered[i+1:], ordered[i:])
			ordered[i] = col
		}
	}
	height := c.Theme().Space(8)
	for _, col := range columns {
		if col.Subtitle != "" {
			height = c.Theme().Space(15)
		}
	}
	ui.Column(c).Grow(1).MinHeight(0).Children(func() {
		ui.ScrollHorizontal(c).Height(height + 1).Children(func() {
			ui.Row(c).Height(height).Gap(0).BorderWidth(0, 0, 1, 0).BorderColor(palette.Border).Children(func() {
				for _, col := range ordered {
					width := col.Width
					if saved, ok := state.Columns.Widths[id(col)]; ok {
						width = saved
					}
					if width <= 0 {
						width = 120
					}
					ui.Column(c).Width(width).Height(height).Padding(3, 6).Gap(2).BorderWidth(0, 1, 0, 0).BorderColor(palette.Border).Children(func() {
						ui.Text(c, locales.Text(col.Title)).SingleLine().FontSize(Font(c, FontSize)).TextColor(palette.Muted).Bold()
						if col.Subtitle != "" {
							ui.Text(c, col.Subtitle).SingleLine().FontSize(Font(c, SmallFontSize)).TextColor(palette.Muted)
						}
					})
				}
			})
		})
		if message != "" {
			ui.Text(c, locales.Text(message)).Padding(8).FontSize(Font(c, SmallFontSize)).TextColor(palette.Muted)
		}
		ui.Box(c).Grow(1)
	})
}
