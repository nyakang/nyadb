package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// The main navigator reuses the branded, pinned PNGs already shipped by the
// connection chooser. The symbols are an offline fallback, never Navicat assets.
func profileBrand(kind connections.Kind, p theme.Palette) (asset, fallback string, color ui.Color) {
	switch kind {
	case connections.MySQL:
		return "MySQL", "mysql-type", p.Blue
	case connections.PostgreSQL:
		return "PostgreSQL", "postgres-type", p.Blue
	case connections.SQLite:
		return "SQLite", "sqlite-type", p.Gold
	default:
		return "", "database", p.Muted
	}
}

func (s *State) profileBrandIcon(c *ui.Context, kind connections.Kind, size float32) {
	palette := theme.Colors(c)

	asset, fallback, color := profileBrand(kind, palette)
	icons, _ := theme.BundledConnectionIcons()
	if bitmap := icons[asset]; bitmap != nil {
		ui.Image(c, bitmap).Size(size, size).Fit(ui.Contain)
		return
	}
	theme.Icon(c, fallback, size, color)
}

// Keep the engine's recognizable artwork in every lifecycle state. A native
// corner badge conveys status without replacing or tinting the brand bitmap.
func (s *State) profileNavigationIcon(c *ui.Context, kind connections.Kind, status string, size float32) {
	if kind == connections.MySQL {
		palette := theme.Colors(c)
		ink := palette.Muted
		switch status {
		case "已连接":
			ink = palette.Success
		case "连接中":
			ink = palette.Warning
		case "连接失败":
			ink = palette.Error
		}
		ui.Box(c).Size(size, size).Children(func() {
			s.profileBrandIcon(c, kind, size)
		}).DrawOver(func(p *ui.Painter, r ui.Rect) {
			diameter := size * .34
			badge := ui.Rect{X: r.X + r.W - diameter, Y: r.Y + r.H - diameter, W: diameter, H: diameter}
			p.Fill(badge, ink, diameter/2)
			p.Stroke(badge, palette.Sidebar, diameter/2, 1)
		})
		return
	}
	s.profileBrandIcon(c, kind, size)
}
