package sqltext

import (
	"fmt"
	"strings"
)

// splitSQL recognizes PostgreSQL dollar strings as well as SQL quote/comment
// forms. Semicolons in function bodies never become another request.
func SplitPortable(src string) ([]string, error) {
	var out []string
	start := 0
	quoteByte := byte(0)
	escapeQuote := false
	triggerDepth := 0
	trigger := false
	create := false
	dollar := ""
	block := 0
	line := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if line {
			if c == '\n' {
				line = false
			}
			continue
		}
		if block > 0 {
			if i+1 < len(src) && src[i:i+2] == "/*" {
				block++
				i++
			} else if i+1 < len(src) && src[i:i+2] == "*/" {
				block--
				i++
			}
			continue
		}
		if dollar != "" {
			if strings.HasPrefix(src[i:], dollar) {
				i += len(dollar) - 1
				dollar = ""
			}
			continue
		}
		if quoteByte != 0 {
			if c == '\\' && escapeQuote && i+1 < len(src) {
				i++
				continue
			}
			if c == quoteByte {
				if i+1 < len(src) && src[i+1] == c {
					i++
				} else {
					quoteByte = 0
				}
			}
			continue
		}
		if i+1 < len(src) && src[i:i+2] == "--" {
			line = true
			i++
			continue
		}
		if i+1 < len(src) && src[i:i+2] == "/*" {
			block = 1
			i++
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quoteByte = c
			escapeQuote = c == '\'' && i > 0 && (src[i-1] == 'E' || src[i-1] == 'e') && (i < 2 || !((src[i-2] >= 'a' && src[i-2] <= 'z') || (src[i-2] >= 'A' && src[i-2] <= 'Z') || src[i-2] == '_'))
			continue
		}
		if c == '$' {
			j := i + 1
			for j < len(src) && ((src[j] >= 'a' && src[j] <= 'z') || (src[j] >= 'A' && src[j] <= 'Z') || (src[j] >= '0' && src[j] <= '9') || src[j] == '_') {
				j++
			}
			if j < len(src) && src[j] == '$' {
				dollar = src[i : j+1]
				i = j
				continue
			}
		}
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			j := i + 1
			for j < len(src) && ((src[j] >= 'a' && src[j] <= 'z') || (src[j] >= 'A' && src[j] <= 'Z') || src[j] == '_') {
				j++
			}
			word := strings.ToUpper(src[i:j])
			if word == "CREATE" {
				create = true
			}
			if create && word == "TRIGGER" {
				trigger = true
			}
			if trigger {
				if word == "BEGIN" || word == "CASE" {
					triggerDepth++
				} else if word == "END" && triggerDepth > 0 {
					triggerDepth--
				}
			}
			i = j - 1
			continue
		}
		if c == ';' && triggerDepth == 0 {
			if q := strings.TrimSpace(src[start:i]); q != "" {
				out = append(out, q)
			}
			start = i + 1
			trigger = false
			create = false
		}
	}
	if quoteByte != 0 || dollar != "" || block > 0 {
		return nil, fmt.Errorf("unterminated SQL string or comment")
	}
	if q := strings.TrimSpace(src[start:]); q != "" {
		out = append(out, q)
	}
	if len(out) > 1024 {
		return nil, fmt.Errorf("SQL script exceeds 1024 statements")
	}
	return out, nil
}

func PortableStatementAt(src string, offset int) string {
	parts, e := SplitPortable(src)
	if e != nil {
		return src
	}
	at := 0
	for _, q := range parts {
		start := strings.Index(src[at:], q)
		if start < 0 {
			continue
		}
		start += at
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

// FormatPortable protects PostgreSQL dollar strings and nested comments before
// sharing the keyword formatter. Protected bytes are restored verbatim.
func FormatPortable(source string) (string, error) {
	if _, e := SplitPortable(source); e != nil {
		return "", e
	}
	type replacement struct{ token, raw string }
	var replacements []replacement
	var b strings.Builder
	for i := 0; i < len(source); {
		start := i
		end := i

		if strings.HasPrefix(source[i:], "/*") {
			depth := 1
			end = i + 2
			for end < len(source) && depth > 0 {
				if strings.HasPrefix(source[end:], "/*") {
					depth++
					end += 2
				} else if strings.HasPrefix(source[end:], "*/") {
					depth--
					end += 2
				} else {
					end++
				}
			}
		} else if strings.HasPrefix(source[i:], "--") {
			end = i + 2
			for end < len(source) && source[end] != '\n' {
				end++
			}
		} else if source[i] == '$' {
			j := i + 1
			for j < len(source) && ((source[j] >= 'a' && source[j] <= 'z') || (source[j] >= 'A' && source[j] <= 'Z') || (source[j] >= '0' && source[j] <= '9') || source[j] == '_') {
				j++
			}
			if j < len(source) && source[j] == '$' {
				tag := source[i : j+1]
				if n := strings.Index(source[j+1:], tag); n >= 0 {
					end = j + 1 + n + len(tag)
				}
			}
		} else if source[i] == '\'' || source[i] == '"' || source[i] == '`' {
			q := source[i]
			escape := q == '\'' && i > 0 && (source[i-1] == 'E' || source[i-1] == 'e')
			end = i + 1
			for end < len(source) {
				if escape && source[end] == '\\' && end+1 < len(source) {
					end += 2
					continue
				}
				if source[end] == q {
					end++
					if end < len(source) && source[end] == q {
						end++
						continue
					}
					break
				}
				end++
			}

		}
		if end > i {
			raw := source[start:end]
			{
				n := len(replacements)
				token := fmt.Sprintf("'__nyadb_portable_%d__'", n)
				for strings.Contains(source, token) {
					n++
					token = fmt.Sprintf("'__nyadb_portable_%d__'", n)
				}
				replacements = append(replacements, replacement{token, raw})
				b.WriteString(token)
			}
			i = end
		} else {
			b.WriteByte(source[i])
			i++
		}
	}
	formatted, e := Format(b.String())
	if e != nil {
		return "", e
	}
	for _, r := range replacements {
		formatted = strings.ReplaceAll(formatted, r.token, r.raw)
	}
	return formatted, nil
}
