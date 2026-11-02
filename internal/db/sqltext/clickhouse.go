package sqltext

import (
	"errors"
	"strings"
)

func clickHouseDollarTag(source string, at int) string {
	if at >= len(source) || source[at] != '$' {
		return ""
	}
	j := at + 1
	for j < len(source) && ((source[j] >= 'a' && source[j] <= 'z') || (source[j] >= 'A' && source[j] <= 'Z') || (source[j] >= '0' && source[j] <= '9') || source[j] == '_') {
		j++
	}
	if j < len(source) && source[j] == '$' {
		return source[at : j+1]
	}
	return ""
}

// SplitClickHouse protects native literals/identifiers, heredocs and --
// comments without MySQL's following-space requirement. Client DELIMITER
// directives are not interpreted as ClickHouse syntax.
func SplitClickHouse(source string) ([]string, error) {
	if len(source) > 8<<20 {
		return nil, errors.New("ClickHouse SQL exceeds 8 MiB")
	}
	var parts []string
	start, quoteByte := 0, byte(0)
	line, block, code := false, false, false
	dollar := ""
	appendPart := func(end int) error {
		if code {
			if len(parts) >= 256 {
				return errors.New("too many ClickHouse statements")
			}
			parts = append(parts, strings.TrimSpace(source[start:end]))
		}
		code = false
		return nil
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
			if strings.HasPrefix(source[i:], "/*") {
				return nil, errors.New("nested ClickHouse comments are unsupported")
			}
			if strings.HasPrefix(source[i:], "*/") {
				block = false
				i++
			}
			continue
		}
		if dollar != "" {
			if strings.HasPrefix(source[i:], dollar) {
				i += len(dollar) - 1
				dollar = ""
			}
			continue
		}
		if quoteByte != 0 {
			if c == '\\' && i+1 < len(source) {
				i++
				continue
			}
			if c == quoteByte {
				if i+1 < len(source) && source[i+1] == c {
					i++
				} else {
					quoteByte = 0
				}
			}
			continue
		}
		if c == '#' || strings.HasPrefix(source[i:], "--") {
			line = true
			continue
		}
		if strings.HasPrefix(source[i:], "/*") {
			block = true
			i++
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quoteByte = c
			code = true
			continue
		}
		if tag := clickHouseDollarTag(source, i); tag != "" {
			dollar = tag
			i += len(tag) - 1
			code = true
			continue
		}
		if c == ';' {
			if err := appendPart(i); err != nil {
				return nil, err
			}
			start = i + 1
			continue
		}
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			code = true
		}
	}
	if quoteByte != 0 || block || dollar != "" {
		return nil, errors.New("unterminated ClickHouse literal or comment")
	}
	if err := appendPart(len(source)); err != nil {
		return nil, err
	}
	return parts, nil
}
func ClickHouseTokens(source string) ([]Token, error) { return tokens(source, true) }
func FormatClickHouse(source string) (string, error) {
	if _, err := SplitClickHouse(source); err != nil {
		return "", err
	}
	tokens, err := ClickHouseTokens(source)
	if err != nil {
		return "", err
	}
	return formatTokens(source, tokens), nil
}
func ClickHouseStatementAt(source string, offset int) string {
	parts, err := SplitClickHouse(source)
	if err != nil {
		return source
	}
	at := 0
	for _, q := range parts {
		start := strings.Index(source[at:], q)
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
func SingleClickHouseStatement(source string) error {
	parts, err := SplitClickHouse(source)
	if err != nil {
		return err
	}
	if len(parts) != 1 {
		return errors.New("EXPLAIN requires one ClickHouse statement")
	}
	return nil
}
