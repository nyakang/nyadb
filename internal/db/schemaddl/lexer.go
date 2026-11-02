package schemaddl

import (
	"errors"
	"strings"
	"unicode/utf8"
)

type token struct {
	start, end int
	text       string
	kind       byte // w: word, i: quoted identifier, s: literal, p: punctuation
}

func (t token) word(s string) bool { return t.kind == 'w' && strings.EqualFold(t.text, s) }
func wordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c >= 128
}

// lex preserves byte boundaries. Ordinary SQL strings do not have MySQL
// backslash escapes. PostgreSQL E/dollar strings, nested comments and SQLite
// bracket/backtick identifiers remain indivisible tokens.
func lex(src string) ([]token, error) {
	return lexDialect(src, false)
}

func postgresLex(src string) ([]token, error) {
	ts, err := lexDialect(src, true)
	for _, t := range ts {
		if t.kind == 'i' && src[t.start] != '"' {
			return nil, errors.New("PostgreSQL identifiers must use double quotes")
		}
	}
	return ts, err
}

func lexDialect(src string, postgres bool) ([]token, error) {
	if len(src) > 1<<20 || !utf8.ValidString(src) || strings.ContainsRune(src, 0) {
		return nil, errors.New("invalid or oversized schema SQL")
	}
	var out []token
	for i := 0; i < len(src); {
		if len(out) >= 65536 {
			return nil, errors.New("schema token limit exceeded")
		}
		c, start := src[i], i
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			i++
			continue
		}
		if strings.HasPrefix(src[i:], "--") {
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		}
		if strings.HasPrefix(src[i:], "/*") {
			i += 2
			depth := 1
			for i < len(src) && depth > 0 {
				if strings.HasPrefix(src[i:], "/*") {
					depth++
					i += 2
				} else if strings.HasPrefix(src[i:], "*/") {
					depth--
					i += 2
				} else {
					i++
				}
				if depth > 128 {
					return nil, errors.New("schema comment depth exceeded")
				}
			}
			if depth != 0 {
				return nil, errors.New("unterminated schema comment")
			}
			continue
		}
		escape := (c == 'E' || c == 'e') && i+1 < len(src) && src[i+1] == '\''
		if escape {
			i++
			c = src[i]
		}
		if c == '\'' || c == '"' || c == '`' || c == '[' && !postgres {
			endQuote := c
			kind := byte('i')
			if c == '[' {
				endQuote = ']'
			}
			if c == '\'' {
				kind = 's'
			}
			i++
			var b strings.Builder
			closed := false
			for i < len(src) {
				if escape && src[i] == '\\' && i+1 < len(src) {
					b.WriteString(src[i : i+2])
					i += 2
					continue
				}
				if src[i] == endQuote {
					if c != '[' && i+1 < len(src) && src[i+1] == endQuote {
						b.WriteByte(endQuote)
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				b.WriteByte(src[i])
				i++
			}
			if !closed {
				return nil, errors.New("unterminated schema quote")
			}
			out = append(out, token{start, i, b.String(), kind})
			continue
		}
		if c == '$' {
			j := i + 1
			for j < len(src) && (wordByte(src[j]) && src[j] < 128) {
				j++
			}
			if j < len(src) && src[j] == '$' && (j == i+1 || !(src[i+1] >= '0' && src[i+1] <= '9')) {
				tag := src[i : j+1]
				n := strings.Index(src[j+1:], tag)
				if n < 0 {
					return nil, errors.New("unterminated schema dollar string")
				}
				i = j + 1 + n + len(tag)
				out = append(out, token{start, i, src[start:i], 's'})
				continue
			}
		}
		if wordByte(c) {
			for i < len(src) && (wordByte(src[i]) || src[i] == '$') {
				i++
			}
			out = append(out, token{start, i, src[start:i], 'w'})
			continue
		}
		i++
		out = append(out, token{start, i, src[start:i], 'p'})
	}
	return out, nil
}

func identifierToken(t token) bool { return t.kind == 'i' || t.kind == 'w' }
func qualifiedName(ts []token, at int) (schema, name string, next int, err error) {
	if at >= len(ts) || !identifierToken(ts[at]) {
		return "", "", at, errors.New("expected schema identifier")
	}
	name, next = ts[at].text, at+1
	if next < len(ts) && ts[next].text == "." {
		if next+1 >= len(ts) || !identifierToken(ts[next+1]) {
			return "", "", at, errors.New("expected qualified schema identifier")
		}
		schema, name, next = name, ts[next+1].text, next+2
	}
	return schema, name, next, nil
}

// single rejects extra statements, including those hidden after a trigger END.
func single(ts []token, trigger bool) error {
	depth, body := 0, 0
	for i, t := range ts {
		if t.kind == 'p' && t.text == "(" {
			depth++
		}
		if t.kind == 'p' && t.text == ")" {
			depth--
		}
		if depth < 0 || depth > 128 {
			return errors.New("invalid schema expression depth")
		}
		if trigger && (t.word("BEGIN") || t.word("CASE")) {
			body++
		}
		if trigger && t.word("END") {
			body--
			if body < 0 {
				return errors.New("invalid trigger body")
			}
		}
		if t.kind == 'p' && t.text == ";" && (body == 0 || !trigger) && i != len(ts)-1 {
			return errors.New("schema definition contains multiple statements")
		}
	}
	if depth != 0 || body != 0 {
		return errors.New("unbalanced schema definition")
	}
	return nil
}
