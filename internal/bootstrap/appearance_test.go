package bootstrap

import (
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestLoadAppearanceRestoresColorFamilyAndMode(t *testing.T) {
	mode, scheme := theme.CurrentMode(), theme.CurrentScheme()
	t.Cleanup(func() { theme.SetMode(mode); theme.SetScheme(scheme) })
	for _, item := range theme.Schemes() {
		for _, mode := range []theme.Mode{theme.ModeLight, theme.ModeDark, theme.ModeSystem} {
			path := filepath.Join(t.TempDir(), "config.sqlite")
			db, err := localdb.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			for key, value := range map[string]string{theme.ModePreferenceKey: string(mode), theme.SchemePreferenceKey: string(item.ID)} {
				if err := localdb.SetPreference(db, key, value); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = localdb.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			theme.SetMode(theme.ModeDark)
			theme.SetScheme(theme.SchemeClassic)
			err = loadAppearance(db)
			if closeErr := db.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			if err != nil || theme.CurrentMode() != mode || theme.CurrentScheme() != item.ID {
				t.Fatalf("restore %s %s: %s %s, %v", item.ID, mode, theme.CurrentScheme(), theme.CurrentMode(), err)
			}
		}
	}
}

func TestLoadAppearancePreservesLegacyModeWithMissingOrUnknownScheme(t *testing.T) {
	mode, scheme := theme.CurrentMode(), theme.CurrentScheme()
	t.Cleanup(func() { theme.SetMode(mode); theme.SetScheme(scheme) })
	db, err := localdb.Open(filepath.Join(t.TempDir(), "config.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := localdb.SetPreference(db, theme.ModePreferenceKey, "light"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "future-scheme"} {
		if value != "" {
			if err := localdb.SetPreference(db, theme.SchemePreferenceKey, value); err != nil {
				t.Fatal(err)
			}
		}
		theme.SetScheme(theme.SchemeRose)
		if err := loadAppearance(db); err != nil {
			t.Fatal(err)
		}
		if theme.CurrentMode() != theme.ModeLight || theme.CurrentScheme() != theme.SchemeClassic {
			t.Fatal("legacy light appearance was reset")
		}
	}
}
