package shell

import "strings"

// sqlAtCaret isolates the statement under a SQL editor's byte-offset cursor.
// Semicolons inside string literals, quoted names and comments are ignored.
// SQL is not rewritten, and explicit text selection always takes precedence.
func sqlAtCaret(source string, cursor int) string {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(source) {
		cursor = len(source)
	}
	type segment struct{ start, end int }
	parts := make([]segment, 0, 8)
	start := 0
	mode := byte(0) // 0=SQL, ' or " or backtick=quoted, L=line comment, B=block comment
	for i := 0; i < len(source); i++ {
		ch := source[i]
		switch mode {
		case 'L':
			if ch == '\n' {
				mode = 0
			}
		case 'B':
			if ch == '*' && i+1 < len(source) && source[i+1] == '/' {
				i++
				mode = 0
			}
		case '\'', '"', '`':
			if ch == '\\' && i+1 < len(source) {
				i++
				continue
			}
			if ch == mode {
				if i+1 < len(source) && source[i+1] == mode {
					i++
					continue
				}
				mode = 0
			}
		default:
			if ch == '\'' || ch == '"' || ch == '`' {
				mode = ch
			} else if ch == '#' || ch == '-' && i+2 < len(source) && source[i+1] == '-' &&
				(source[i+2] == ' ' || source[i+2] == '\t' || source[i+2] == '\n') {
				mode = 'L'
			} else if ch == '/' && i+1 < len(source) && source[i+1] == '*' {
				i++
				mode = 'B'
			} else if ch == ';' {
				parts = append(parts, segment{start, i + 1})
				start = i + 1
			}
		}
	}
	parts = append(parts, segment{start, len(source)})
	// Prefer a nonempty statement containing the cursor. At the exact end
	// of a SQL buffer, use its final statement rather than an empty segment.
	for _, part := range parts {
		if cursor >= part.start && cursor < part.end {
			if text := strings.TrimSpace(source[part.start:part.end]); text != "" {
				return text
			}
		}
	}
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i].end <= cursor {
			if text := strings.TrimSpace(source[parts[i].start:parts[i].end]); text != "" {
				return text
			}
		}
	}
	for _, part := range parts {
		if text := strings.TrimSpace(source[part.start:part.end]); text != "" {
			return text
		}
	}
	return ""
}
