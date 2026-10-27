//go:build linux && (amd64 || arm64)

package bootstrap

import (
	"fmt"

	"github.com/ebitengine/purego"
	"github.com/egoist/mygo"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// GTK's stock Adwaita menu spacing is looser than the compact navigation
// menus observed in Navicat 18. Apply only geometry and typography; colours
// continue to come from the selected native GTK theme (light or dark).
const compactMenuCSS = `
menu { padding: 1px 0; }
menu menuitem { min-height: 0; padding: 3px 11px; }
menu menuitem label { font-size: %gpx; }
menu separator { min-height: 1px; margin: 2px 1px; }
`

func init() {
	// Register before bootstrap.Run creates a window. WhenReady runs on the
	// native UI thread after GTK has been initialized.
	mygo.App.WhenReady(func() {
		installCompactGTKMenus()
		mygo.Theme.OnUpdated(installCompactGTKMenus)
	})
}

// Reuse one provider across preference updates; the screen owns its reference.
var updateCompactGTKMenus func() bool

func installCompactGTKMenus() {
	if updateCompactGTKMenus != nil {
		updateCompactGTKMenus()
		return
	}
	gtk, err := purego.Dlopen("libgtk-3.so.0", purego.RTLD_NOW)
	if err != nil {
		return
	}
	gdk, err := purego.Dlopen("libgdk-3.so.0", purego.RTLD_NOW)
	if err != nil {
		return
	}
	gobject, err := purego.Dlopen("libgobject-2.0.so.0", purego.RTLD_NOW)
	if err != nil {
		return
	}
	var newProvider func() uintptr
	var loadCSS func(uintptr, *byte, int, uintptr) bool
	var addForScreen func(uintptr, uintptr, uint32)
	var defaultScreen func() uintptr
	var unref func(uintptr)
	purego.RegisterLibFunc(&newProvider, gtk, "gtk_css_provider_new")
	purego.RegisterLibFunc(&loadCSS, gtk, "gtk_css_provider_load_from_data")
	purego.RegisterLibFunc(&addForScreen, gtk, "gtk_style_context_add_provider_for_screen")
	purego.RegisterLibFunc(&defaultScreen, gdk, "gdk_screen_get_default")
	purego.RegisterLibFunc(&unref, gobject, "g_object_unref")

	screen := defaultScreen()
	if screen == 0 {
		return
	}
	provider := newProvider()
	if provider == 0 {
		return
	}
	defer unref(provider)
	update := func() bool {
		scale := mygo.Theme.Preferences().TextScale
		if scale <= 0 {
			scale = 1
		}
		css := fmt.Sprintf(compactMenuCSS, float64(theme.FontSize)*scale)
		data := append([]byte(css), 0)
		return loadCSS(provider, &data[0], len(css), 0)
	}
	if update() {
		addForScreen(screen, provider, 600) // GTK_STYLE_PROVIDER_PRIORITY_APPLICATION
		updateCompactGTKMenus = update
	}
}
