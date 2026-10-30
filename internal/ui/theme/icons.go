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
	"connection":   lucideSymbol("plug-zap"),
	"query":        lucideSymbol("file-code-2"),
	"new-query":    lucideSymbol("file-plus-2"),
	"open-table":   lucideSymbol("folder-open"),
	"design-table": lucideSymbol("table-properties"),
	"new-table":    lucideSymbol("file-plus-2"),
	"delete-table": lucideSymbol("trash-2"),
	"import":       lucideSymbol("file-down"),
	"export":       lucideSymbol("file-up"),
	"new-group":    lucideSymbol("folder-plus"),
	"edit":         lucideSymbol("pencil"),
	"copy":         lucideSymbol("copy"),
	"delete":       lucideSymbol("trash-2"),
	"table":        lucideSymbol("table-2"),
	"view":         lucideSymbol("binoculars"),
	"function":     lucideSymbol("square-function"),
	"user":         lucideSymbol("user-round"),
	"tools":        lucideSymbol("wrench"),
	"backup":       lucideSymbol("database-backup"),
	"automation":   lucideSymbol("bot"),
	"model":        lucideSymbol("workflow"),
	"info":         lucideSymbol("info"),
	"folder":       lucideSymbol("folder"),
	// An independently drawn, solid folder is more legible in the compact tree.
	"folder-filled": symbol(`<path d="M2.3 7a2.2 2.2 0 0 1 2.2-2.2h5.1l2.1 2.1h7.8a2.2 2.2 0 0 1 2.2 2.2v9.4a2.2 2.2 0 0 1-2.2 2.2h-15a2.2 2.2 0 0 1-2.2-2.2Z" fill="currentColor" stroke="none"/><path d="M3.4 9h17.2" stroke="#98731a" stroke-width=".8" opacity=".6"/>`),
	"database":      lucideSymbol("database"),
	// NyaDB's own solid, segmented database cylinder stays legible at 15 DIPs.
	"database-solid": symbol(`<ellipse cx="12" cy="4.7" rx="8.7" ry="3.1" fill="currentColor" stroke="none" opacity=".95"/>
		<path d="M3.3 5.5v3.1c0 1.8 3.9 3.2 8.7 3.2s8.7-1.4 8.7-3.2V5.5c-.9 2-4.6 3.1-8.7 3.1S4.2 7.5 3.3 5.5Z" fill="currentColor" stroke="none" opacity=".77"/>
		<path d="M3.3 10.3v3.2c0 1.8 3.9 3.2 8.7 3.2s8.7-1.4 8.7-3.2v-3.2c-.9 1.9-4.6 3-8.7 3s-7.8-1.1-8.7-3Z" fill="currentColor" stroke="none" opacity=".86"/>
		<path d="M3.3 15.4v3.2c0 1.8 3.9 3.2 8.7 3.2s8.7-1.4 8.7-3.2v-3.2c-.9 1.9-4.6 3-8.7 3s-7.8-1.1-8.7-3Z" fill="currentColor" stroke="none" opacity=".94"/>`),
	// Lifecycle icons are independent assets, not copied proprietary imagery.
	"connection-idle":   symbol(`<path d="M7 5.5 4.5 8 9 12.5l-2 2 2.5 2.5 2-2L16 19.5l2.5-2.5L14 12.5l2-2L13.5 8l-2 2Z" fill="currentColor" stroke="none"/><path d="M5.5 6.5 9 3m6 8 3.5-3.5" stroke="currentColor" stroke-width="2.2"/>`),
	"connection-active": symbol(`<rect x="2" y="2" width="20" height="20" rx="3" fill="currentColor" stroke="none"/><path d="m9 5 5 5-3 3 4 4-2 2-4-4-3 3-2-2 3-3-3-3Z" fill="#fff" stroke="none"/>`),
	"ssh-server":        symbol(`<rect x="5" y="2" width="14" height="6" rx="1"/><rect x="5" y="10" width="14" height="6" rx="1"/><rect x="5" y="18" width="14" height="4" rx="1"/><path d="M8 5h.5M8 13h.5M8 20h.5"/>`),
	"catalog-grid":      lucideSymbol("layout-grid"),
	"catalog-list":      lucideSymbol("list"),
	"catalog-filter":    lucideSymbol("list-filter"),
	"catalog-vendor":    lucideSymbol("building-2"),
	"mysql-type":        symbol(`<path d="M3 16c1-5 5-10 10-11 3-1 6 0 8 3-4-1-7 1-8 5-1 3-4 6-10 7"/><path d="m12 9 4 2m-9 5 2 3"/>`),
	"postgres-type":     symbol(`<path d="m12 2 8 5v10l-8 5-8-5V7Z"/><path d="M7 9h10m-10 5h10m-5-5v10"/>`),
	"sqlite-type":       symbol(`<path d="M4 20C4 10 9 3 21 3c-1 9-7 16-17 17Z"/><path d="M6 18c3-4 7-7 12-10m-8 4-2-4m6 1 4 1"/>`),
	"mssql-type":        symbol(`<path d="M3 5h18v14H3zM3 10h18m-12-5v14m6-9v9"/>`),
	"oracle-type":       symbol(`<rect x="2.5" y="7" width="19" height="10" rx="5"/><path d="M8 12h8"/>`),
	"maria-type":        symbol(`<path d="M2 15c4-7 8-2 12-8 2-2 5-2 8-1-3 4-6 4-9 9-3 5-7 5-11 3"/><path d="m8 12 2 2m6-6 1 3"/>`),
	"mongo-type":        symbol(`<path d="M12 2C6 7 5 12 7 17c1 3 3 4 5 5 2-1 4-2 5-5 2-5 1-10-5-15Z"/><path d="M12 4v17"/>`),
	"redis-type":        symbol(`<path d="m3 8 9-4 9 4-9 4-9-4Zm0 5 9 4 9-4M3 17l9 4 9-4M3 8v9m18-9v9"/>`),
	"snowflake-type":    symbol(`<path d="M12 2v20M2 12h20M5 5l14 14M19 5 5 19M9 4l3 3 3-3M9 20l3-3 3 3"/>`),
	"dm-type":           symbol(`<circle cx="12" cy="12" r="9"/><path d="M4 13c4-7 8-7 16-2M5 17c7-6 10-4 14-3"/>`),
	"ocean-type":        symbol(`<path d="M2 8c3-3 5-3 8 0s5 3 8 0 4-3 4-3M2 14c3-3 5-3 8 0s5 3 8 0 4-3 4-3M2 20c3-3 5-3 8 0s5 3 8 0 4-3 4-3"/>`),
	"cloud-type":        symbol(`<path d="M7 19h11a4 4 0 0 0 1-8 7 7 0 0 0-13-1 4.5 4.5 0 0 0 1 9Z"/><path d="M8 15h8"/>`),
	"plus":              lucideSymbol("plus"),
	"close":             lucideSymbol("x"),
	"check":             symbol(`<path d="m5 12 4 4L19 6"/>`),
	"refresh":           lucideSymbol("refresh-cw"),
	"history":           symbol(`<path d="M3 11a9 9 0 1 1 3 7M3 4v7h7M12 7v5l3 2"/>`),
	"chart":             symbol(`<path d="M4 20V4m0 16h17M8 16V9m5 7V5m5 11v-4"/>`),
	"settings":          lucideSymbol("settings-2"),
	"star":              lucideSymbol("star"),
	"navigate-active":   lucideSymbol("locate-fixed"),
	"collapse-all":      lucideSymbol("chevrons-up"),
}

func Icon(c *ui.Context, name string, size float32, color ui.Color) {
	if svg := Symbols[name]; svg != nil {
		ui.Icon(c, svg).Size(size, size).TextColor(color)
	}
}
