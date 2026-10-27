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
	if bitmap := s.bundledIcons[asset]; bitmap != nil {
		ui.Image(c, bitmap).Size(size, size).Fit(ui.Contain)
		return
	}
	theme.Icon(c, fallback, size, color)
}

// The navigator displays a connection's lifecycle on its icon rather than
// adding a tiny trailing dot. The connection chooser keeps its brand icon.
func (s *State) profileNavigationIcon(c *ui.Context, kind connections.Kind, status string, size float32) {
	if kind == connections.MySQL {
		palette := theme.Colors(c)
		switch status {
		case "已连接":
			theme.Icon(c, "connection-active", size, palette.Success)
		case "连接中":
			theme.Icon(c, "connection-idle", size, palette.Warning)
		case "连接失败":
			theme.Icon(c, "connection-idle", size, palette.Error)
		default:
			theme.Icon(c, "connection-idle", size, palette.Muted)
		}
		return
	}
	s.profileBrandIcon(c, kind, size)
}
