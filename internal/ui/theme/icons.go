package theme

import "github.com/egoist/mygo/ui"

// These simple geometric icons are authored for NyaDB, not copied from
// another application's assets. MyGo parses them once at initialization.
func symbol(body string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" stroke-linecap="round">` + body + `</svg>`))
}

var Symbols = map[string]*ui.SVG{
	"connection": symbol(`<path d="m8 3 4 4m-8 5 4 4m5-11-8 8m7 0 7-7"/><path d="M4 16 2 20l4-2 3 3 4-4"/>`),
	"query":      symbol(`<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 9h18M8 13h5m-5 4h8"/>`),
	"table":      symbol(`<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 10h18M10 4v16m-7-5h18"/>`),
	"view":       symbol(`<path d="M2 12s4-6 10-6 10 6 10 6-4 6-10 6S2 12 2 12Z"/><circle cx="12" cy="12" r="3"/>`),
	"function":   symbol(`<path d="M14 3c-4 0-4 6-5 10s-2 8-6 8m2-11h11m-1 4 6 6m0-6-6 6"/>`),
	"user":       symbol(`<circle cx="12" cy="8" r="4"/><path d="M4 21c0-5 3-7 8-7s8 2 8 7"/>`),
	"tools":      symbol(`<path d="M21 6a5 5 0 0 1-7 5L6 19l-3-3 8-8a5 5 0 0 1 5-7l-3 4 3 3Z"/>`),
	"backup":     symbol(`<rect x="4" y="3" width="16" height="18" rx="2"/><path d="M8 7h8M8 11h6m-2 4 3 3 4-4"/>`),
	"automation": symbol(`<rect x="3" y="5" width="18" height="14" rx="3"/><path d="M8 3v2m8-2v2M7 10h1m8 0h1m-10 5 3-3 2 2 4-4"/>`),
	"model":      symbol(`<rect x="2" y="2" width="7" height="7" rx="1"/><rect x="15" y="2" width="7" height="7" rx="1"/><rect x="9" y="15" width="7" height="7" rx="1"/><path d="M5 9v3h7v3m7-6v3h-7"/>`),
	"info":       symbol(`<circle cx="12" cy="12" r="9"/><path d="M12 11v5m0-9v1"/>`),
	"folder":     symbol(`<path d="M3 6h7l2 2h9v12H3Z"/>`),
	"database":   symbol(`<ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v14c0 4 18 4 18 0V5M3 12c0 4 18 4 18 0"/>`),
	"plus":       symbol(`<path d="M12 4v16M4 12h16"/>`),
	"close":      symbol(`<path d="m5 5 14 14M19 5 5 19"/>`),
	"refresh":    symbol(`<path d="M20 6v5h-5M4 18v-5h5"/><path d="M6 9a7 7 0 0 1 12-2l2 4M4 13l2 4a7 7 0 0 0 12-2"/>`),
}

func Icon(c *ui.Context, name string, size float32, color ui.Color) {
	if svg := Symbols[name]; svg != nil {
		ui.Icon(c, svg).Size(size, size).TextColor(color)
	}
}
