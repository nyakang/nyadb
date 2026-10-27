package shell

import "testing"

func TestSQLAtCaretStatementsAndQuotedSemicolons(t *testing.T) {
	tests := []struct {
		name, src string
		cursor    int
		want      string
	}{
		{"first", "SELECT 1;\nSELECT 2;", 3, "SELECT 1;"},
		{"second", "SELECT 1;\nSELECT 2;", 13, "SELECT 2;"},
		{"buffer-end", "SELECT 1;\nSELECT 2;", 19, "SELECT 2;"},
		{"trailing-semicolon", "SELECT 1;\n", 10, "SELECT 1;"},
		{"empty", " \t  ", 1, ""},
		{"single-quoted", "SELECT 'a; b' AS c; SELECT 2;", 10, "SELECT 'a; b' AS c;"},
		{"double-quoted", "SELECT \"a; b\"; SELECT 2;", 24, "SELECT 2;"},
		{"quoted-name", "SELECT `t;1` FROM x; SELECT 2;", 9, "SELECT `t;1` FROM x;"},
		{"block-comment", "SELECT /* ; */ 1; SELECT 2;", 11, "SELECT /* ; */ 1;"},
		{"line-comment", "-- ; ignored\nSELECT 1;\nSELECT 2;", 17, "-- ; ignored\nSELECT 1;"},
		{"unicode", "SELECT '中文;';\nSELECT 2;", 8, "SELECT '中文;';"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sqlAtCaret(tc.src, tc.cursor); got != tc.want {
				t.Fatalf("cursor=%d SQL=%q got=%q want=%q", tc.cursor, tc.src, got, tc.want)
			}
		})
	}
}
