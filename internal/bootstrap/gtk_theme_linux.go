//go:build linux

package bootstrap

import "os"

// NyaDB uses a dark workspace. GTK3's native GtkMenu may ignore
// gtk-application-prefer-dark-theme when the desktop's current GTK theme
// has no effective dark variant (observed with Yaru on headless X11).
// Select a standard GTK native dark theme before GTK initializes; leave
// explicit GTK_THEME choices untouched, including an intentionally empty one.
// No MyGo table/menu controls or GTK widgets are replaced.
func configureNativeGTKTheme() {
	if _, explicit := os.LookupEnv("GTK_THEME"); explicit {
		return
	}
	_ = os.Setenv("GTK_THEME", "Adwaita:dark")
}
