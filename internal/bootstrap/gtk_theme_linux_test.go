//go:build linux && (amd64 || arm64)

package bootstrap

import (
	"github.com/nyakang/nyadb/internal/ui/theme"
	"reflect"
	"testing"
)

type recordingGTKSettings struct{ changes []string }

func (s *recordingGTKSettings) setName(name string) { s.changes = append(s.changes, name) }
func (s *recordingGTKSettings) resetName()          { s.changes = append(s.changes, "desktop") }

func TestNativeGTKThemeRestoresDesktopAcrossModeChanges(t *testing.T) {
	var controller gtkThemeController
	settings := &recordingGTKSettings{}
	for _, mode := range []theme.Mode{theme.ModeDark, theme.ModeDark, theme.ModeLight, theme.ModeDark, theme.ModeSystem, theme.ModeLight} {
		controller.apply(mode, false, false, settings)
	}
	if want := []string{"Adwaita", "desktop", "Adwaita", "desktop"}; !reflect.DeepEqual(settings.changes, want) {
		t.Fatalf("GTK changes = %v, want %v", settings.changes, want)
	}
}

func TestNativeGTKThemeRespectsExplicitAndContrastThemes(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		explicit, highContrast bool
	}{
		{"explicit GTK_THEME including empty", true, false},
		{"desktop contrast theme", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var controller gtkThemeController
			settings := &recordingGTKSettings{}
			controller.apply(theme.ModeDark, tc.explicit, tc.highContrast, settings)
			if len(settings.changes) != 0 {
				t.Fatalf("overrode desktop/user theme: %v", settings.changes)
			}
			controller.apply(theme.ModeDark, false, false, settings)
			controller.apply(theme.ModeDark, tc.explicit, tc.highContrast, settings)
			if want := []string{"Adwaita", "desktop"}; !reflect.DeepEqual(settings.changes, want) {
				t.Fatalf("failed to release override: %v", settings.changes)
			}
		})
	}
}
