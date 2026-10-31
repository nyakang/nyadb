// Package theme defines NyaDB's desktop colors, metrics and vector symbols.
package theme

import (
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/locales"
)

// Snapshot is a value owned by a frame. Resolving another window or changing
// the saved mode never mutates an existing snapshot.
type Snapshot struct {
	Scheme      Scheme
	Dark        bool
	Palette     Palette
	SQL         editor.Theme
	TextScale   float32 // Desktop scaling, retained for compatibility.
	Metrics     MetricsPreferences
	UIZoom      float32
	SQLFontSize float32
}

// Resolve preserves the classic palette API for existing callers and previews.
func Resolve(mode Mode, systemDark bool, prefs ui.Preferences) Snapshot {
	return ResolveScheme(SchemeClassic, mode, systemDark, prefs)
}

// ResolveScheme is pure: previewing a scheme never changes the current choice.
func ResolveScheme(scheme Scheme, mode Mode, systemDark bool, prefs ui.Preferences) Snapshot {
	return ResolveAppearance(scheme, mode, systemDark, prefs, DefaultMetrics())
}

// ResolveAppearance resolves colors and reading preferences without global state.
func ResolveAppearance(scheme Scheme, mode Mode, systemDark bool, prefs ui.Preferences, metrics MetricsPreferences) Snapshot {
	mode = ParseMode(string(mode))
	return snapshot(ParseScheme(string(scheme)), mode == ModeDark || (mode == ModeSystem && systemDark), prefs, metrics)
}

func snapshot(scheme Scheme, dark bool, prefs ui.Preferences, metrics MetricsPreferences) Snapshot {
	p := schemePalette(scheme, dark)
	// Classic integrates with the desktop accent. Named color families keep
	// their designed accents; contrast and text scaling apply to every scheme.
	if scheme == SchemeClassic && prefs.Accent.A != 0 {
		p.Accent = prefs.Accent
		p.Accent.A = 255
		p.AccentText = onColor(p.Accent)
		p.AccentHover = readableBackground(p.Accent.Mix(ui.Hex("#000000"), 0.12), p.AccentText)
		p.AccentPressed = readableBackground(p.Accent.Mix(ui.Hex("#000000"), 0.25), p.AccentText)
		p.Focus = p.Accent
		p.Selection = p.App.Mix(p.Accent, 0.22)
	}
	if scheme == SchemeClassic {
		p.Selection = p.App.Mix(p.Accent, 0.22)
	}
	p.SelectionText = onColor(p.Selection)
	p.NavigationSelection, p.NavigationSelectionText = p.Selection, p.SelectionText
	if scheme == SchemeClassic && dark && prefs.Accent.A == 0 && !prefs.HighContrast {
		p.NavigationSelection, p.NavigationSelectionText = ui.Hex("#0878ce"), ui.Hex("#ffffff")
	}
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
	p.Notice = p.App.Mix(p.Warning, 0.16)
	p.GridModified = p.App.Mix(p.Warning, 0.12)
	backgrounds := []ui.Color{p.App, p.Panel, p.Header, p.Sidebar, p.Toolbar, p.GridAlternate, p.Notice, p.GridModified}
	p.Text = readableInk(p.Text, backgrounds...)
	p.Muted = readableInk(p.Muted, backgrounds...)
	p.GridNull = p.Muted
	p.NoticeText, p.GridModifiedText = p.Text, p.Text
	p.Error, p.Warning, p.Success = readableInk(p.Error, backgrounds...), readableInk(p.Warning, backgrounds...), readableInk(p.Success, backgrounds...)
	p.Focus = readableInkMinimum(p.Focus, 3, p.App, p.GridAlternate, p.Selection, p.Header)
	p.GridModifiedMark = readableInkMinimum(p.Warning, 3, p.App, p.GridAlternate, p.GridModified)
	p.GridInsertedMark = readableInkMinimum(p.Success, 3, p.App, p.GridAlternate)
	p.GridDeletedMark = readableInkMinimum(p.Error, 3, p.App, p.GridAlternate)
	p.Chart = p.Accent
	if scheme == SchemeClassic {
		p.Chart = ui.Hex("#168397")
		if dark {
			p.Chart = ui.Hex("#28b0c8")
		}
	}
	scale := prefs.TextScale
	if scale <= 0 {
		scale = 1
	}
	metrics = NormalizeMetrics(metrics)
	return Snapshot{Scheme: scheme, Dark: dark, Palette: p, SQL: *sqlTheme(scheme, dark, p, prefs), TextScale: scale,
		Metrics: metrics, UIZoom: scale * float32(metrics.UIFontSize) / 12, SQLFontSize: scale * float32(metrics.SQLFontSize)}
}

type snapshotKey struct{}
type windowSnapshot struct {
	applied bool
	value   Snapshot
}

// Frame returns the root's immutable value. Apply refreshes it at the start of
// each build. Local widget overrides and clicks during a build cannot recolor
// half of a window, and no context/window pointers are kept in global caches.
// Isolated feature views without Apply resolve the current choice directly.
func Frame(c *ui.Context) Snapshot {
	state := ui.Local(c.Root(), snapshotKey{}, func() windowSnapshot { return windowSnapshot{} })
	if state.applied {
		return state.value
	}
	return snapshot(CurrentScheme(), c.Theme().Dark, c.Preferences(), CurrentMetrics())
}

// Colors uses the frame's resolved appearance, independent of the process's
// saved mode. Local widget theme overrides do not change workbench tokens.
func Colors(c *ui.Context) Palette {
	state := ui.Local(c.Root(), snapshotKey{}, func() windowSnapshot { return windowSnapshot{} })
	if state.applied {
		return state.value.Palette
	}
	return snapshot(CurrentScheme(), c.Theme().Dark, c.Preferences(), CurrentMetrics()).Palette
}

// Apply maps the frame's snapshot to native MyGo widgets. System preferences
// are applied once to NyaDB's baseline, so repeated calls do not compound them.
func Apply(c *ui.Context) Snapshot {
	s := ResolveAppearance(CurrentScheme(), CurrentMode(), c.Theme().Dark, c.Preferences(), CurrentMetrics())
	state := ui.Local(c.Root(), snapshotKey{}, func() windowSnapshot { return windowSnapshot{} })
	*state = windowSnapshot{applied: true, value: s}
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
	t.Spacing, t.FontSize, t.Radius = 3*max(float32(1), s.UIZoom), FontSize*s.UIZoom, ControlRadius
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
	return tableWithFont(c, state, columns, rows, Font(c, FontSize), cell)
}

// TableWithFont also accounts for a document's local data zoom when adding
// row space. Its zero-width sizing element leaves native cell parents intact.
func TableWithFont(c *ui.Context, state *ui.ListState, columns []ui.TableColumn, rows int, font float32, cell func(int, int)) ui.Element {
	base := c.Theme()
	c.SetTheme(SelectionTheme(c))
	defer c.SetTheme(base)
	return tableWithFont(c, state, columns, rows, font, cell)
}

func tableWithFont(c *ui.Context, state *ui.ListState, columns []ui.TableColumn, rows int, font float32, cell func(int, int)) ui.Element {
	style := ui.TableStyle{}
	if state.TableStyle != nil {
		style = *state.TableStyle
	}
	style.AlternateRowColor, style.GridColor = Colors(c).GridAlternate, Colors(c).Border
	state.TableStyle = &style
	extra := TableRowExtra(c)
	var height float32
	if extra > 0 {
		m := (ui.Font{Size: font}).Metrics()
		height = m.Ascent + m.Descent + m.LineGap + extra
	}
	return ui.Table(c, state, columns, rows, func(row, col int) {
		ui.Box(c).Key("theme-row-height").Width(0).MinHeight(height).Shrink(0).Role(ui.RoleNone)
		cell(row, col)
	})
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
	p := Colors(c)
	selected.Accent, selected.AccentText = p.NavigationSelection, p.NavigationSelectionText
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
