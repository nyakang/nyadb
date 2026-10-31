package theme

import "github.com/egoist/mygo/ui"

// Font reads the frame's UI and desktop text scaling without compounding.
// Display/DPI scaling is separately applied by MyGo.
func Font(c *ui.Context, size float32) float32 {
	state := ui.Local(c.Root(), snapshotKey{}, func() windowSnapshot { return windowSnapshot{} })
	if state.applied {
		return size * state.value.UIZoom
	}
	scale := c.Preferences().TextScale
	if scale <= 0 {
		scale = 1
	}
	return size * scale * float32(CurrentMetrics().UIFontSize) / 12
}

// RowHeight preserves normal compact geometry and makes room for larger text.
func RowHeight(c *ui.Context, height float32) float32 {
	return height * max(float32(1), Font(c, FontSize)/FontSize)
}

// Shared desktop measurements in device-independent pixels. These describe
// the compact Linux reference; MyGo applies the actual display scale.
const (
	FontSize          float32 = 12
	SmallFontSize     float32 = 11
	MenuHeight        float32 = 26
	ToolbarHeight     float32 = 70
	ToolbarItemWidth  float32 = 64
	ToolbarItemHeight float32 = 64
	ToolbarIconSize   float32 = 28
	TabHeight         float32 = 27
	CommandBarHeight  float32 = 30
	StatusBarHeight   float32 = 22
	TreeRowHeight     float32 = 19
	SmallIconSize     float32 = 16
	SidebarWidth      float32 = 250
	InspectorWidth    float32 = 200
	SplitterBorder    float32 = 3 // plus the native one-DIP draggable divider
	ControlHeight     float32 = 26
	BorderWidth       float32 = 1
	ControlRadius     float32 = 2
)

// TableRowExtra is added to natural text height, never to analysis/header rows.
func TableRowExtra(c *ui.Context) float32 {
	s := Frame(c)
	switch s.Metrics.TableDensity {
	case DensityStandard:
		return 4 * s.UIZoom
	case DensityRelaxed:
		return 8 * s.UIZoom
	default:
		return 0
	}
}
