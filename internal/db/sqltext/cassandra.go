package sqltext

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// SplitCassandra preserves native BEGIN [UNLOGGED|COUNTER] BATCH...APPLY BATCH,
// doubled quotes, $$ function bodies and CQL comments as indivisible statements.
func SplitCassandra(source string) ([]string, error) {
	bad := errors.New("invalid or unsupported Cassandra CQL source")
	if len(source) > 8<<20 || !utf8.ValidString(source) || strings.ContainsRune(source, 0) {
		return nil, bad
	}
	var parts []string
	start := 0
	var quote byte
	line, block, dollar, code, batch := false, false, false, false, false
	var words []string
	last := ""
	appendPart := func(end int) {
		if code {
			parts = append(parts, strings.TrimSpace(source[start:end]))
		}
		start = end + 1
		code = false
		words = nil
		last = ""
	}
	for i := 0; i < len(source); i++ {
		c := source[i]
		if line {
			if c == '\n' {
				line = false
			}
			continue
		}
		if block {
			if i+1 < len(source) && source[i:i+2] == "/*" {
				return nil, bad
			}
			if i+1 < len(source) && source[i:i+2] == "*/" {
				block = false
				i++
			}
			continue
		}
		if dollar {
			if i+1 < len(source) && source[i:i+2] == "$$" {
				dollar = false
				i++
			}
			continue
		}
		if quote != 0 {
			if c == quote {
				if i+1 < len(source) && source[i+1] == quote {
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		if i+1 < len(source) {
			two := source[i : i+2]
			if two == "--" || two == "//" {
				line = true
				i++
				continue
			}
			if two == "/*" {
				block = true
				i++
				continue
			}
			if two == "$$" {
				dollar = true
				code = true
				i++
				continue
			}
		}
		if c == '\'' || c == '"' {
			quote = c
			code = true
			continue
		}
		if c == '`' || c == '#' || c == '$' {
			return nil, bad
		}
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' {
			j := i + 1
			for j < len(source) && (source[j] >= 'a' && source[j] <= 'z' || source[j] >= 'A' && source[j] <= 'Z' || source[j] >= '0' && source[j] <= '9' || source[j] == '_') {
				j++
			}
			word := strings.ToUpper(source[i:j])
			code = true
			if len(words) < 3 {
				words = append(words, word)
			}
			if len(words) >= 2 && words[0] == "BEGIN" && (words[1] == "BATCH" || len(words) == 3 && (words[1] == "UNLOGGED" || words[1] == "COUNTER") && words[2] == "BATCH") {
				batch = true
				words[0] = ""
			}
			if batch && last == "APPLY" && word == "BATCH" {
				batch = false
			}
			last = word
			i = j - 1
			continue
		}
		if c == ';' && !batch {
			appendPart(i)
			if len(parts) > 1024 {
				return nil, bad
			}
		} else if c != ' ' && c != '\n' && c != '\r' && c != '\t' && c != ';' {
			code = true
		}
	}
	if quote != 0 || block || dollar || batch {
		return nil, bad
	}
	appendPart(len(source))
	if len(parts) == 0 || len(parts) > 1024 {
		return nil, bad
	}
	return parts, nil
}

func CassandraStatementAt(source string, offset int) string {
	parts, err := SplitCassandra(source)
	if err != nil {
		return source
	}
	at := 0
	for _, p := range parts {
		start := strings.Index(source[at:], p)
		if start < 0 {
			continue
		}
		start += at
		end := start + len(p)
		if offset <= end {
			return p
		}
		at = end
	}
	return parts[len(parts)-1]
}
