// Package sqltext preserves quoted SQL while scanning public SQL definitions.
package sqltext

import (
	"errors"
	"strings"
	"unicode"
)

type Token struct {
	Text            string
	Start, End      int
	Quoted, Comment bool
}

func Tokens(source string) ([]Token, error) {
	var out []Token
	for i := 0; i < len(source); {
		if source[i] == ' ' || source[i] == '\n' || source[i] == '\r' || source[i] == '\t' {
			i++
			continue
		}
		start := i
		q, c := false, false
		switch {
		case source[i] == '\'' || source[i] == '"' || source[i] == '`':
			q = true
			quote := source[i]
			i++
			closed := false
			for i < len(source) {
				if source[i] == '\\' && i+1 < len(source) {
					i += 2
					continue
				}
				if source[i] == quote {
					if i+1 < len(source) && source[i+1] == quote {
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, errors.New("unterminated SQL quote")
			}
		case strings.HasPrefix(source[i:], "/*"):
			c = true
			end := strings.Index(source[i+2:], "*/")
			if end < 0 {
				return nil, errors.New("unterminated SQL comment")
			}
			i += end + 4
		case source[i] == '#' || strings.HasPrefix(source[i:], "--") && (i+2 == len(source) || source[i+2] <= ' '):
			c = true
			for i < len(source) && source[i] != '\n' {
				i++
			}
		default:
			if source[i] == '_' || source[i] >= 'a' && source[i] <= 'z' || source[i] >= 'A' && source[i] <= 'Z' || source[i] >= '0' && source[i] <= '9' || source[i] >= 128 {
				i++
				for i < len(source) && (source[i] == '_' || source[i] >= 'a' && source[i] <= 'z' || source[i] >= 'A' && source[i] <= 'Z' || source[i] >= '0' && source[i] <= '9' || source[i] >= 128) {
					i++
				}
			} else {
				i++
			}
		}
		out = append(out, Token{Text: source[start:i], Start: start, End: i, Quoted: q, Comment: c})
	}
	return out, nil
}
func Format(source string) (string, error) {
	tokens, e := Tokens(source)
	if e != nil {
		return "", e
	}
	keywords := strings.Fields("select from where order by group having limit offset insert into values update set delete join left right inner outer on as and or not null is create alter table view index primary key foreign references unique default check constraint begin end when then else case union all distinct asc desc explain returning")
	known := map[string]bool{}
	for _, v := range keywords {
		known[v] = true
	}
	var b strings.Builder
	last := 0
	depth := 0
	for _, t := range tokens {
		gap := source[last:t.Start]
		if depth == 0 && !t.Quoted && !t.Comment && b.Len() > 0 {
			switch strings.ToUpper(t.Text) {
			case "SELECT", "FROM", "WHERE", "GROUP", "ORDER", "HAVING", "LIMIT", "UNION", "SET", "VALUES", "RETURNING":
				gap = "\n"
			}
		}
		b.WriteString(gap)
		v := t.Text
		if !t.Quoted && !t.Comment && known[strings.ToLower(v)] {
			v = strings.ToUpper(v)
		}
		b.WriteString(v)
		if !t.Quoted && !t.Comment {
			if t.Text == "(" {
				depth++
			}
			if t.Text == ")" {
				depth = max(0, depth-1)
			}
		}
		last = t.End
	}
	b.WriteString(source[last:])
	return b.String(), nil
}

// Identifier quotes a database object name for a supported SQL dialect.
func Identifier(value string, mysql bool) (string, error) {
	if value == "" || strings.ContainsRune(value, 0) {
		return "", errors.New("invalid SQL identifier")
	}
	quote := "\""
	if mysql {
		quote = "`"
	}
	return quote + strings.ReplaceAll(value, quote, quote+quote) + quote, nil
}
func Literal(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
func Word(t Token, value string) bool {
	return !t.Quoted && !t.Comment && strings.EqualFold(t.Text, value)
}
func ValidName(s string) bool {
	return strings.TrimSpace(s) != "" && !strings.ContainsRune(s, 0) && !strings.ContainsFunc(s, unicode.IsControl)
}
