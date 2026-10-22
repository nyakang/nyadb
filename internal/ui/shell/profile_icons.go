package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// The main navigator reuses the branded, pinned PNGs already shipped by the
// connection chooser. The symbols are an offline fallback, never Navicat assets.
func profileBrand(kind connections.Kind) (asset, fallback string, color ui.Color) {
	switch kind {
	case connections.MySQL:
		return "MySQL", "mysql-type", theme.Blue
	case connections.PostgreSQL:
		return "PostgreSQL", "postgres-type", theme.Blue
	case connections.SQLite:
		return "SQLite", "sqlite-type", theme.Gold
	default:
		return "", "database", theme.Muted
	}
}

func (s *State) profileBrandIcon(c *ui.Context, kind connections.Kind, size float32) {
	asset, fallback, color := profileBrand(kind)
	if bitmap := s.bundledIcons[asset]; bitmap != nil {
		ui.Image(c, bitmap).Size(size, size).Fit(ui.Contain)
		return
	}
	theme.Icon(c, fallback, size, color)
}
