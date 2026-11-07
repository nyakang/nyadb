package navigation

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// The main navigator reuses the branded, pinned PNGs already shipped by the
// connection chooser. The symbols are an offline fallback, never Navicat assets.
func profileBrand(kind connections.Kind, p theme.Palette) (asset, fallback string, color ui.Color) {
	switch kind {
	case connections.DuckDB:
		return "DuckDB", "duckdb-type", p.Gold
	case connections.MySQL:
		return "MySQL", "mysql-type", p.Blue
	case connections.MariaDB:
		return "MariaDB", "maria-type", p.Gold
	case connections.OceanBase:
		return "OceanBase", "ocean-type", p.Blue
	case connections.TiDB:
		return "TiDB", "database", p.Error
	case connections.Redis:
		return "Redis", "redis-type", p.Error
	case connections.MongoDB:
		return "MongoDB", "mongo-type", p.Green
	case connections.Oracle:
		return "Oracle", "oracle-type", p.Error
	case connections.SQLServer:
		return "SQL Server", "mssql-type", p.Gold
	case connections.CockroachDB:
		return "CockroachDB", "database", p.Purple
	case connections.OpenGauss:
		return "openGauss", "database", p.Blue
	case connections.GaussDB:
		return "GaussDB", "database", p.Blue
	case connections.Kingbase:
		return "KingbaseES", "database", p.Error
	case connections.PostgreSQL:
		return "PostgreSQL", "postgres-type", p.Blue
	case connections.SQLite:
		return "SQLite", "sqlite-type", p.Gold
	case connections.ClickHouse:
		return "", "database", p.Gold
	default:
		return "", "database", p.Muted
	}
}

func BrandIcon(c *ui.Context, kind connections.Kind, size float32) {
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
func NavigationIcon(c *ui.Context, kind connections.Kind, status string, size float32) {
	if kind.IsMySQLFamily() {
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
			BrandIcon(c, kind, size)
		}).DrawOver(func(p *ui.Painter, r ui.Rect) {
			diameter := size * .34
			badge := ui.Rect{X: r.X + r.W - diameter, Y: r.Y + r.H - diameter, W: diameter, H: diameter}
			p.Fill(badge, ink, diameter/2)
			p.Stroke(badge, palette.Sidebar, diameter/2, 1)
		})
		return
	}
	BrandIcon(c, kind, size)
}

// ConfigureDisclosure uses the MyGo fork's solid triangle with its own hit target.
func ConfigureDisclosure(state *ui.OutlineState[string]) {
	if state == nil {
		return
	}
	state.Disclosure = ui.OutlineTriangle
	state.DisclosureSize = 10 // independent hit target; visible triangle ~6 DIPs
}
