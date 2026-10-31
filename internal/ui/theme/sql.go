package theme

import (
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// SQLTheme is the NyaDB SQL editor's semantic syntax palette. It retains the
// native MyGo editor's selection, caret, folding and virtualized rendering.
// Colors depend on the effective window appearance, not only the saved mode.
func SQLTheme(window *ui.Theme) *editor.Theme {
	mode := ModeLight
	if window.Dark {
		mode = ModeDark
	}
	s := Resolve(mode, window.Dark, ui.Preferences{})
	return &s.SQL
}

func classicSQLTheme(window *ui.Theme) *editor.Theme {
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

func sqlTheme(scheme Scheme, dark bool, p Palette, prefs ui.Preferences) *editor.Theme {
	t := classicSQLTheme(&ui.Theme{Dark: dark, Background: p.App, Text: p.Text, TextMuted: p.Muted, Border: p.Border})
	if scheme != SchemeClassic {
		v := variantFor(scheme, dark)
		t.Background, t.Text = p.App, p.Text
		t.LineNumber, t.ActiveLineNumber = p.Muted, p.Text
		t.ActiveLine = p.App.Mix(p.Header, 0.65)
		// Editor selection keeps syntax ink, unlike tables which switch to
		// SelectionText. Use a subtle tint so all token colors remain legible.
		t.Selection = p.App.Mix(p.Accent, 0.16)
		t.InactiveSelection = p.App.Mix(p.Accent, 0.08)
		t.Cursor, t.MatchingBracket = p.Focus, p.Focus
		t.IndentGuide, t.WrapGuide, t.ActiveIndentGuide = p.Border, p.Border, p.Muted
		t.Whitespace = p.Muted
		for _, token := range []struct {
			kind  editor.TokenKind
			color ui.Color
		}{
			{editor.TokenKeyword, ui.Hex(v.keyword)}, {editor.TokenProperty, ui.Hex(v.keyword)},
			{editor.TokenConstant, ui.Hex(v.keyword)}, {editor.TokenFunction, ui.Hex(v.function)},
			{editor.TokenType, ui.Hex(v.function)}, {editor.TokenString, ui.Hex(v.str)},
			{editor.TokenEscape, ui.Hex(v.number)}, {editor.TokenNumber, ui.Hex(v.number)},
			{editor.TokenVariable, p.Text}, {editor.TokenOperator, p.Text},
			{editor.TokenPunctuation, p.Muted}, {editor.TokenComment, p.Muted}, {editor.TokenInvalid, p.Error},
		} {
			t.Tokens[token.kind] = editor.TokenStyle{Color: token.color}
		}
		t.Tokens[editor.TokenComment].Italic = true
	}
	t.Error, t.Warning = p.Error, p.Warning
	t.Info, t.Hint = p.Blue, p.Muted
	t.Added, t.Deleted, t.Modified = p.Success, p.Error, p.Blue
	if scheme == SchemeClassic && prefs.Accent.A != 0 {
		t.Cursor, t.MatchingBracket = p.Focus, p.Focus
	}
	if prefs.HighContrast {
		t.LineNumber = p.Muted
		t.Tokens[editor.TokenComment] = editor.TokenStyle{Color: p.Muted, Italic: true}
		t.IndentGuide, t.WrapGuide = p.Border, p.Border
	}
	if scheme != SchemeClassic {
		for i := range t.Tokens {
			if t.Tokens[i].Color.A != 0 {
				t.Tokens[i].Color = readableInk(t.Tokens[i].Color, t.Background, t.ActiveLine, t.Selection)
			}
		}
	}
	return t
}

// SQLThemeFor uses exactly the same frame snapshot as the workbench widgets.
func SQLThemeFor(c *ui.Context) *editor.Theme {
	s := Frame(c)
	return &s.SQL
}
