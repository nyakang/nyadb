package bootstrap

import (
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestReadingPreferencesRestartAndLegacyFallback(t *testing.T) {
	mode, scheme, metrics := theme.CurrentMode(), theme.CurrentScheme(), theme.CurrentMetrics()
	t.Cleanup(func() { theme.SetMode(mode); theme.SetScheme(scheme); theme.SetMetrics(metrics) })
	path := filepath.Join(t.TempDir(), "config.sqlite")
	db, err := localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{theme.ModePreferenceKey: "system", theme.SchemePreferenceKey: "forest"} {
		if err := localdb.SetPreference(db, key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := loadAppearance(db); err != nil {
		t.Fatal(err)
	}
	if theme.CurrentMetrics() != theme.DefaultMetrics() {
		t.Fatal("legacy defaults changed")
	}
	want := theme.MetricsPreferences{UIFontSize: 16, SQLFontSize: 24, TableDensity: theme.DensityRelaxed}
	if err := localdb.SetPreference(db, theme.MetricsPreferenceKey, theme.EncodeMetrics(want)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	theme.SetMetrics(theme.DefaultMetrics())
	if err := loadAppearance(db); err != nil {
		t.Fatal(err)
	}
	if theme.CurrentMetrics() != want || theme.CurrentMode() != theme.ModeSystem || theme.CurrentScheme() != theme.SchemeForest {
		t.Fatal("restart lost appearance choices")
	}
	if err := localdb.SetPreference(db, theme.MetricsPreferenceKey, `{"version":1,"ui_font_size":99,"sql_font_size":18,"table_density":"future"}`); err != nil {
		t.Fatal(err)
	}
	if err := loadAppearance(db); err != nil {
		t.Fatal(err)
	}
	if theme.CurrentMetrics() != (theme.MetricsPreferences{UIFontSize: 12, SQLFontSize: 18, TableDensity: theme.DensityCompact}) {
		t.Fatal("invalid metric did not preserve valid SQL size")
	}
}
