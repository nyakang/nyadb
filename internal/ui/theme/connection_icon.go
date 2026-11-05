package theme

import "github.com/egoist/mygo/ui"

// ConnectionActionIcon shares the toolbar's Lucide glyphs and blue ink.
func ConnectionActionIcon(c *ui.Context, size float32) {
	Icon(c, "connection", size, Colors(c).Blue)
}
