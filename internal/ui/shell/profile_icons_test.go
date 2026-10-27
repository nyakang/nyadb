package shell

import (
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestWorkbenchProfileBrandsReuseEmbeddedCatalogAssets(t *testing.T) {
	state := New()
	for _, tc := range []struct {
		kind            connections.Kind
		asset, fallback string
	}{
		{connections.MySQL, "MySQL", "mysql-type"},
		{connections.PostgreSQL, "PostgreSQL", "postgres-type"},
		{connections.SQLite, "SQLite", "sqlite-type"},
	} {
		asset, fallback, _ := profileBrand(tc.kind, theme.PaletteFor(theme.ModeDark, true))
		if asset != tc.asset || fallback != tc.fallback {
			t.Fatalf("%s icon mapping: %s, %s", tc.kind, asset, fallback)
		}
		if state.bundledIcons[asset] == nil {
			t.Errorf("%s has no embedded brand bitmap", asset)
		}
		if theme.Symbols[fallback] == nil {
			t.Errorf("%s has no offline fallback glyph", asset)
		}
	}
	asset, fallback, _ := profileBrand(connections.Kind("unrecognized"), theme.PaletteFor(theme.ModeDark, true))
	if asset != "" || fallback != "database" {
		t.Errorf("unknown vendor must use generic fallback: %q %q", asset, fallback)
	}
}
