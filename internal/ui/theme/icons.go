package theme

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/assets/lucide"
)

// Database-brand fallback symbols are independently authored for NyaDB.
// Common operation and object icons come from pinned Lucide assets.
func symbol(body string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" stroke-linecap="round">` + body + `</svg>`))
}

// lucideSymbol fails during initialization if a checked-in SVG is missing
// or invalid, rather than silently rendering an empty button.
func lucideSymbol(name string) *ui.SVG {
	data, err := lucide.Read(name)
	if err != nil {
		panic(err)
	}
	return ui.MustParseSVG(data)
}

var Symbols = map[string]*ui.SVG{
	"connection":     lucideSymbol("plug-zap"),
	"query":          lucideSymbol("file-code-2"),
	"table":          lucideSymbol("table-2"),
	"view":           lucideSymbol("eye"),
	"function":       lucideSymbol("square-function"),
	"user":           lucideSymbol("user-round"),
	"tools":          lucideSymbol("wrench"),
	"backup":         lucideSymbol("database-backup"),
	"automation":     lucideSymbol("calendar-clock"),
	"model":          lucideSymbol("workflow"),
	"info":           lucideSymbol("info"),
	"folder":         lucideSymbol("folder"),
	"database":       lucideSymbol("database"),
	"catalog-grid":   lucideSymbol("layout-grid"),
	"catalog-list":   lucideSymbol("list"),
	"catalog-filter": lucideSymbol("list-filter"),
	"catalog-vendor": lucideSymbol("building-2"),
	"mysql-type":     symbol(`<path d="M3 16c1-5 5-10 10-11 3-1 6 0 8 3-4-1-7 1-8 5-1 3-4 6-10 7"/><path d="m12 9 4 2m-9 5 2 3"/>`),
	"postgres-type":  symbol(`<path d="m12 2 8 5v10l-8 5-8-5V7Z"/><path d="M7 9h10m-10 5h10m-5-5v10"/>`),
	"sqlite-type":    symbol(`<path d="M4 20C4 10 9 3 21 3c-1 9-7 16-17 17Z"/><path d="M6 18c3-4 7-7 12-10m-8 4-2-4m6 1 4 1"/>`),
	"mssql-type":     symbol(`<path d="M3 5h18v14H3zM3 10h18m-12-5v14m6-9v9"/>`),
	"oracle-type":    symbol(`<rect x="2.5" y="7" width="19" height="10" rx="5"/><path d="M8 12h8"/>`),
	"maria-type":     symbol(`<path d="M2 15c4-7 8-2 12-8 2-2 5-2 8-1-3 4-6 4-9 9-3 5-7 5-11 3"/><path d="m8 12 2 2m6-6 1 3"/>`),
	"mongo-type":     symbol(`<path d="M12 2C6 7 5 12 7 17c1 3 3 4 5 5 2-1 4-2 5-5 2-5 1-10-5-15Z"/><path d="M12 4v17"/>`),
	"redis-type":     symbol(`<path d="m3 8 9-4 9 4-9 4-9-4Zm0 5 9 4 9-4M3 17l9 4 9-4M3 8v9m18-9v9"/>`),
	"snowflake-type": symbol(`<path d="M12 2v20M2 12h20M5 5l14 14M19 5 5 19M9 4l3 3 3-3M9 20l3-3 3 3"/>`),
	"dm-type":        symbol(`<circle cx="12" cy="12" r="9"/><path d="M4 13c4-7 8-7 16-2M5 17c7-6 10-4 14-3"/>`),
	"ocean-type":     symbol(`<path d="M2 8c3-3 5-3 8 0s5 3 8 0 4-3 4-3M2 14c3-3 5-3 8 0s5 3 8 0 4-3 4-3M2 20c3-3 5-3 8 0s5 3 8 0 4-3 4-3"/>`),
	"cloud-type":     symbol(`<path d="M7 19h11a4 4 0 0 0 1-8 7 7 0 0 0-13-1 4.5 4.5 0 0 0 1 9Z"/><path d="M8 15h8"/>`),
	"plus":           lucideSymbol("plus"),
	"close":          lucideSymbol("x"),
	"refresh":        lucideSymbol("refresh-cw"),
}

func Icon(c *ui.Context, name string, size float32, color ui.Color) {
	if svg := Symbols[name]; svg != nil {
		ui.Icon(c, svg).Size(size, size).TextColor(color)
	}
}
