//go:build linux && (amd64 || arm64)

package bootstrap

import (
	"github.com/ebitengine/purego"
	"github.com/egoist/mygo"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"os"
)

// GTK_THEME pins a variant for the process lifetime. Use a reversible GTK
// settings override instead; MyGo owns gtk-application-prefer-dark-theme.
// An explicit GTK_THEME or a desktop contrast theme always takes precedence.
type gtkThemeController struct{ overridden bool }

type gtkThemeSettings interface {
	setName(string)
	resetName()
}

func (c *gtkThemeController) apply(mode theme.Mode, explicit, highContrast bool, settings gtkThemeSettings) {
	fallback := mode == theme.ModeDark && !explicit && !highContrast
	if fallback && !c.overridden {
		c.overridden = true
		settings.setName("Adwaita")
	} else if !fallback && c.overridden {
		c.overridden = false
		settings.resetName()
	}
}

var nativeGTKTheme gtkThemeController

func applyNativeGTKTheme(mode theme.Mode) {
	settings := loadGTKThemeSettings()
	if settings == nil {
		return
	}
	_, explicit := os.LookupEnv("GTK_THEME")
	nativeGTKTheme.apply(mode, explicit, mygo.Theme.Preferences().HighContrast, settings)
}

type nativeGTKSettings struct {
	settings uintptr
	set      func(uintptr, *byte, *byte, uintptr)
	reset    func(uintptr, *byte)
}

func (s *nativeGTKSettings) setName(name string) {
	property, value := append([]byte("gtk-theme-name"), 0), append([]byte(name), 0)
	s.set(s.settings, &property[0], &value[0], 0)
}

func (s *nativeGTKSettings) resetName() {
	property := append([]byte("gtk-theme-name"), 0)
	s.reset(s.settings, &property[0])
}

var gtkSettingsAPI *nativeGTKSettings

func loadGTKThemeSettings() *nativeGTKSettings {
	if gtkSettingsAPI != nil {
		return gtkSettingsAPI
	}
	gtk, err := purego.Dlopen("libgtk-3.so.0", purego.RTLD_NOW)
	if err != nil {
		return nil
	}
	gobject, err := purego.Dlopen("libgobject-2.0.so.0", purego.RTLD_NOW)
	if err != nil {
		return nil
	}
	var getDefault func() uintptr
	s := &nativeGTKSettings{}
	purego.RegisterLibFunc(&getDefault, gtk, "gtk_settings_get_default")
	purego.RegisterLibFunc(&s.set, gobject, "g_object_set")
	purego.RegisterLibFunc(&s.reset, gtk, "gtk_settings_reset_property")
	s.settings = getDefault()
	if s.settings == 0 {
		return nil
	}
	gtkSettingsAPI = s
	return s
}
