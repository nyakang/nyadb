package rqlite

import (
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// SQLite quoting differs from PostgreSQL: brackets quote identifiers, dollar
// names are identifiers/parameters, and block comments do not nest. Trigger
// bodies keep their own semicolons inside one native statement.
func splitSQL(source string) ([]string, error) {
	if !utf8.ValidString(source) || strings.ContainsRune(source, 0) || len(source) > maxRequestBytes {
		return nil, connections.ErrInvalid
	}
	var out []string
	start, depth := 0, 0
	var words []string
	hasToken, trigger := false, false
	appendPart := func(end int) {
		if hasToken {
			out = append(out, strings.TrimSpace(source[start:end]))
		}
		words, hasToken, trigger, depth = nil, false, false, 0
	}
	for i := 0; i < len(source); {
		c := source[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			i++
			continue
		}
		if strings.HasPrefix(source[i:], "--") {
			end := strings.IndexByte(source[i:], '\n')
			if end < 0 {
				i = len(source)
			} else {
				i += end
			}
			continue
		}
		if strings.HasPrefix(source[i:], "/*") {
			end := strings.Index(source[i+2:], "*/")
			if end < 0 {
				return nil, connections.ErrInvalid
			}
			i += end + 4
			continue
		}
		if c == ';' && depth == 0 {
			appendPart(i)
			i++
			start = i
			continue
		}
		hasToken = true
		if c == '\'' || c == '"' || c == '`' || c == '[' {
			quote := c
			if c == '[' {
				quote = ']'
			}
			i++
			closed := false
			for i < len(source) {
				if source[i] == quote {
					i++
					if c != '[' && i < len(source) && source[i] == quote {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, connections.ErrInvalid
			}
			continue
		}
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '$' || c >= 128 {
			j := i + 1
			for j < len(source) {
				b := source[j]
				if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '$' || b >= 128) {
					break
				}
				j++
			}
			word := strings.ToUpper(source[i:j])
			i = j
			if len(words) == 0 {
				switch word {
				case "BEGIN", "END", "COMMIT", "ROLLBACK", "SAVEPOINT", "RELEASE":
					return nil, contract.ErrUnsupported
				}
			}
			if len(words) < 3 {
				words = append(words, word)
			}
			if len(words) == 2 && words[0] == "CREATE" && words[1] == "TRIGGER" || len(words) == 3 && words[0] == "CREATE" && (words[1] == "TEMP" || words[1] == "TEMPORARY") && words[2] == "TRIGGER" {
				trigger = true
			}
			if trigger {
				if word == "BEGIN" || word == "CASE" {
					depth++
				} else if word == "END" && depth > 0 {
					depth--
				}
			}
			continue
		}
		i++
	}
	if depth != 0 {
		return nil, connections.ErrInvalid
	}
	appendPart(len(source))
	if len(out) == 0 || len(out) > 1024 {
		return nil, connections.ErrInvalid
	}
	return out, nil
}
