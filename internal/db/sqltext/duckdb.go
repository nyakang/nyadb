package sqltext

import (
	"errors"
	"strings"
)

// DuckDBTokens follows DuckDB's PostgreSQL-style strings, quoted identifiers,
// dollar strings and nested comments. Ordinary strings never use backslash
// escapes; E strings do. Tokens retain byte positions for caret and formatting.
func DuckDBTokens(source string) ([]Token, error) {
	if len(source) > 8<<20 {
		return nil, errors.New("DuckDB SQL exceeds 8 MiB")
	}
	var out []Token
	for i := 0; i < len(source); {
		if strings.ContainsRune(" \t\r\n", rune(source[i])) {
			i++
			continue
		}
		start, quoted, comment := i, false, false
		switch {
		case strings.HasPrefix(source[i:], "--"):
			comment = true
			for i < len(source) && source[i] != '\n' {
				i++
			}
		case strings.HasPrefix(source[i:], "/*"):
			comment = true
			i += 2
			depth := 1
			for i < len(source) && depth > 0 {
				if strings.HasPrefix(source[i:], "/*") {
					depth++
					i += 2
				} else if strings.HasPrefix(source[i:], "*/") {
					depth--
					i += 2
				} else {
					i++
				}
			}
			if depth != 0 {
				return nil, errors.New("unterminated DuckDB comment")
			}
		case source[i] == '$' && clickHouseDollarTag(source, i) != "":
			quoted = true
			tag := clickHouseDollarTag(source, i)
			end := strings.Index(source[i+len(tag):], tag)
			if end < 0 {
				return nil, errors.New("unterminated DuckDB dollar string")
			}
			i += len(tag)*2 + end
		case source[i] == '\'' || source[i] == '"' || (source[i] == 'E' || source[i] == 'e') && i+1 < len(source) && source[i+1] == '\'':
			quoted = true
			escape := source[i] == 'E' || source[i] == 'e'
			if escape {
				i++
			}
			end := source[i]
			i++
			closed := false
			for i < len(source) {
				if escape && source[i] == '\\' && i+1 < len(source) {
					i += 2
					continue
				}
				if source[i] == end {
					i++
					if i < len(source) && source[i] == end {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, errors.New("unterminated DuckDB quote")
			}
		default:
			i++
			if sqlServerWord(source[start]) {
				for i < len(source) && sqlServerWord(source[i]) {
					i++
				}
			}
		}
		out = append(out, Token{Text: source[start:i], Start: start, End: i, Quoted: quoted, Comment: comment})
	}
	return out, nil
}
func SplitDuckDB(source string) ([]string, error) {
	tokens, err := DuckDBTokens(source)
	if err != nil {
		return nil, err
	}
	var out []string
	start, code := 0, false
	for _, t := range tokens {
		if t.Comment {
			continue
		}
		if !t.Quoted && t.Text == ";" {
			if code {
				out = append(out, strings.TrimSpace(source[start:t.Start]))
			}
			start = t.End
			code = false
		} else {
			code = true
		}
	}
	if code {
		out = append(out, strings.TrimSpace(source[start:]))
	}
	if len(out) > 1024 {
		return nil, errors.New("DuckDB SQL exceeds 1024 statements")
	}
	return out, nil
}
func DuckDBStatementAt(source string, offset int) string {
	parts, err := SplitDuckDB(source)
	if err != nil {
		return source
	}
	at := 0
	for _, q := range parts {
		start := strings.Index(source[at:], q) + at
		end := start + len(q)
		if offset <= end {
			return q
		}
		at = end
	}
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}
func FormatDuckDB(source string) (string, error) {
	t, err := DuckDBTokens(source)
	if err != nil {
		return "", err
	}
	return formatTokens(source, t), nil
}
func SingleDuckDBStatement(source string) error {
	parts, err := SplitDuckDB(source)
	if err != nil {
		return err
	}
	if len(parts) != 1 {
		return errors.New("EXPLAIN requires one DuckDB statement")
	}
	tokens, err := DuckDBTokens(parts[0])
	if err != nil {
		return err
	}
	for _, t := range tokens {
		if t.Comment {
			continue
		}
		if Word(t, "SELECT") || Word(t, "WITH") || Word(t, "FROM") || Word(t, "VALUES") {
			return nil
		}
		return errors.New("DuckDB EXPLAIN supports queries")
	}
	return errors.New("empty DuckDB statement")
}
