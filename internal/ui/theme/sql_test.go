package theme

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

func TestSQLThemeLightAndDarkSyntaxAreDistinct(t *testing.T) {
	light := SQLTheme(&ui.Theme{Dark: false})
	dark := SQLTheme(&ui.Theme{Dark: true})
	if light.Background == dark.Background || light.Text == dark.Text {
		t.Fatal("SQL editor background/text ignored appearance")
	}
	for _, kind := range []editor.TokenKind{
		editor.TokenKeyword, editor.TokenFunction, editor.TokenNumber,
		editor.TokenString, editor.TokenComment, editor.TokenInvalid,
	} {
		if light.TokenColor(kind) == dark.TokenColor(kind) {
			t.Fatalf("token %d is not theme-dependent", kind)
		}
		if light.TokenColor(kind) == light.Background || dark.TokenColor(kind) == dark.Background {
			t.Fatalf("token %d is invisible", kind)
		}
	}
	if !light.Tokens[editor.TokenComment].Italic || !dark.Tokens[editor.TokenComment].Italic {
		t.Fatal("comments lost their style")
	}
	if light.ActiveLine == dark.ActiveLine || light.Selection == dark.Selection {
		t.Fatal("selection or active line not themed")
	}
}

func TestPalettePreviewsIndependentFromSelectedMode(t *testing.T) {
	before := CurrentMode()
	defer SetMode(before)
	SetMode(ModeLight)
	if PaletteFor(ModeDark, false).App != darkPalette.App {
		t.Fatal("dark preview incorrectly uses selected light palette")
	}
	SetMode(ModeDark)
	if PaletteFor(ModeLight, true).App != lightPalette.App {
		t.Fatal("light preview incorrectly uses selected dark palette")
	}
	if PaletteFor(ModeSystem, false).App != lightPalette.App ||
		PaletteFor(ModeSystem, true).App != darkPalette.App {
		t.Fatal("system preview doesn't resolve effective appearance")
	}
}
