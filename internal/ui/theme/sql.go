package theme

import (
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// SQLTheme is the NyaDB SQL editor's semantic syntax palette. It retains the
// native MyGo editor's selection, caret, folding and virtualized rendering.
// Colors depend on the effective window appearance, not only the saved mode.
func SQLTheme(window *ui.Theme) *editor.Theme {
	base := editor.DefaultTheme(window)
	dark := window.Dark
	base.Background = ui.Hex("#ffffff")
	base.Text = ui.Hex("#25303d")
	base.LineNumber = ui.Hex("#858c96")
	base.ActiveLineNumber = ui.Hex("#364253")
	base.ActiveLine = ui.Hex("#edf3fa")
	base.Cursor = ui.Hex("#0879d1")
	base.Selection = ui.Hex("#b6d8fa")
	base.InactiveSelection = ui.Hex("#dce8f4")
	base.IndentGuide = ui.Hex("#e4e8ed")
	base.ActiveIndentGuide = ui.Hex("#a9b9cb")
	base.WrapGuide = ui.Hex("#e5e9ee")
	base.MatchingBracket = ui.Hex("#0879d1")
	if dark {
		base.Background = ui.Hex("#242424")
		base.Text = ui.Hex("#e2e5e9")
		base.LineNumber = ui.Hex("#818991")
		base.ActiveLineNumber = ui.Hex("#e3e8ec")
		base.ActiveLine = ui.Hex("#303844")
		base.Cursor = ui.Hex("#67bfff")
		base.Selection = ui.Hex("#215885")
		base.InactiveSelection = ui.Hex("#384657")
		base.IndentGuide = ui.Hex("#3c4149")
		base.ActiveIndentGuide = ui.Hex("#63768b")
		base.WrapGuide = ui.Hex("#383d44")
		base.MatchingBracket = ui.Hex("#67bfff")
	}
	tokens := map[editor.TokenKind]string{
		editor.TokenKeyword: "#0550ae", editor.TokenType: "#8250df",
		editor.TokenFunction: "#8250df", editor.TokenVariable: "#20242a",
		editor.TokenProperty: "#0550ae", editor.TokenConstant: "#0550ae",
		editor.TokenNumber: "#0969da", editor.TokenString: "#0a7a50",
		editor.TokenEscape: "#a34200", editor.TokenComment: "#6a737d",
		editor.TokenOperator: "#3c444d", editor.TokenPunctuation: "#57606a",
		editor.TokenInvalid: "#cf222e",
	}
	if dark {
		tokens = map[editor.TokenKind]string{
			editor.TokenKeyword: "#75baff", editor.TokenType: "#c3a0fa",
			editor.TokenFunction: "#d1a8ff", editor.TokenVariable: "#e2e5e9",
			editor.TokenProperty: "#9ecbff", editor.TokenConstant: "#98c6ff",
			editor.TokenNumber: "#efbb84", editor.TokenString: "#9edca7",
			editor.TokenEscape: "#ffd08b", editor.TokenComment: "#929ba5",
			editor.TokenOperator: "#ced2d8", editor.TokenPunctuation: "#bec6d0",
			editor.TokenInvalid: "#ff8d8d",
		}
	}
	for kind, color := range tokens {
		base.Tokens[kind] = editor.TokenStyle{Color: ui.Hex(color)}
	}
	base.Tokens[editor.TokenComment] = editor.TokenStyle{Color: base.Tokens[editor.TokenComment].Color, Italic: true}
	return base
}

// PaletteFor provides immutable read-only swatches for the settings preview.
// System mode is resolved through the current native appearance.
func PaletteFor(mode Mode, systemDark bool) Palette {
	if mode == ModeSystem {
		mode = ModeLight
		if systemDark {
			mode = ModeDark
		}
	}
	if mode == ModeLight {
		return lightPalette
	}
	return darkPalette
}
