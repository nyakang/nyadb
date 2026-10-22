package theme

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/assets/lucide"
)

func TestGeneralPurposeSymbolsUseCommittedLucide(t *testing.T) {
	// Semantic names are stable across the UI even when the upstream icon
	// filenames differ. Regression-test all toolbar/explorer names.
	expected := map[string]string{
		"connection":   "plug-zap",
		"query":        "file-code-2",
		"new-query":    "file-plus-2",
		"open-table":   "folder-open",
		"design-table": "table-properties",
		"new-table":    "file-plus-2",
		"delete-table": "trash-2",
		"import":       "file-down",
		"export":       "file-up",
		"new-group":    "folder-plus",
		"edit":         "pencil",
		"copy":         "copy",
		"delete":       "trash-2",
		"table":        "table-2",
		"view":         "binoculars",
		"function":     "square-function",
		"user":         "user-round",
		"tools":        "wrench",
		"backup":       "database-backup",
		"automation":   "bot",
		"model":        "workflow",
		"info":         "info",
		"folder":       "folder",
		"database":     "database",
		"plus":         "plus",
		"close":        "x",
		"refresh":      "refresh-cw",
	}
	for semantic, upstream := range expected {
		if Symbols[semantic] == nil {
			t.Errorf("general icon %q is missing", semantic)
		}
		asset, err := lucide.Read(upstream)
		if err != nil {
			t.Fatalf("source SVG missing for %q: %v", semantic, err)
		}
		if parsed := ui.MustParseSVG(asset); parsed == nil {
			t.Fatalf("Lucide %s could not be parsed by MyGo", upstream)
		}
	}
}

func TestConnectionBrandFallbackSymbolsStayAvailable(t *testing.T) {
	if Symbols["folder-filled"] == nil {
		t.Fatal("native filled folder glyph missing")
	}
	// These are *not* replaced by generic Lucide operation glyphs.
	for _, name := range []string{
		"mysql-type", "postgres-type", "sqlite-type", "mssql-type",
		"oracle-type", "maria-type", "mongo-type", "redis-type",
		"snowflake-type", "dm-type", "ocean-type", "cloud-type",
	} {
		if Symbols[name] == nil {
			t.Errorf("brand fallback symbol %q was lost", name)
		}
	}
}
