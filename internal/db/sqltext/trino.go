package sqltext

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Trino uses doubled SQL quotes, ordinary backslashes, -- comments regardless
// of following whitespace, and non-nested /* */ comments. It has no MySQL
// DELIMITER, # comments, backticks or PostgreSQL dollar/E-string rules.
func SplitTrino(source string) ([]string, error) {
	if !utf8.ValidString(source) || strings.ContainsRune(source, 0) || len(source) > 8<<20 {
		return nil, errors.New("invalid or oversized Trino SQL source")
	}
	var parts []string
	start := 0
	var quote byte
	line, block := false, false
	function, routine := false, false
	appendPart := func(end int) {
		if q := strings.TrimSpace(source[start:end]); q != "" {
			parts = append(parts, q)
		}
		start = end + 1
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
				return nil, errors.New("Trino does not support nested block comments")
			}
			if i+1 < len(source) && source[i:i+2] == "*/" {
				block = false
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
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if c == '`' || c == '#' || c == '$' {
			return nil, errors.New("unsupported non-Trino SQL syntax")
		}
		if i+1 < len(source) && source[i:i+2] == "--" {
			line = true
			i++
			continue
		}
		if i+1 < len(source) && source[i:i+2] == "/*" {
			block = true
			i++
			continue
		}
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' {
			j := i + 1
			for j < len(source) && (source[j] >= 'a' && source[j] <= 'z' || source[j] >= 'A' && source[j] <= 'Z' || source[j] >= '0' && source[j] <= '9' || source[j] == '_') {
				j++
			}
			word := strings.ToUpper(source[i:j])
			if word == "FUNCTION" {
				function = true
			}
			if word == "BEGIN" && function {
				routine = true
			}
			i = j - 1
			continue
		}
		if c == ';' {
			appendPart(i)
			function = false
		}
	}
	if quote != 0 || block {
		return nil, errors.New("unterminated Trino quote or comment")
	}
	appendPart(len(source))
	// A native SQL routine may contain its own semicolons. Submit its complete
	// source once; the server validates the entire routine/query before running
	// it. Mixed scripts containing such a routine are not partially executed.
	if routine {
		q := strings.TrimSpace(source)
		q = strings.TrimSuffix(q, ";")
		parts = []string{q}
	}
	if len(parts) == 0 || len(parts) > 1024 {
		return nil, errors.New("Trino SQL requires 1 to 1024 statements")
	}
	for _, p := range parts {
		if len(p) > 1<<20 {
			return nil, errors.New("Trino statement exceeds 1 MiB")
		}
	}
	return parts, nil
}

func TrinoStatementAt(source string, offset int) string {
	parts, err := SplitTrino(source)
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

func FormatTrino(source string) (string, error) {
	if _, err := SplitTrino(source); err != nil {
		return "", err
	}
	ts, err := tokensWithRules(source, false, true)
	if err != nil {
		return "", err
	}
	return formatTokens(source, ts), nil
}
