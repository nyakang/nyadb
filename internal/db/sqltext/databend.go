package sqltext

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var errDatabendSQL = errors.New("invalid or oversized Databend SQL source")

// Databend uses backslash-escaped SQL strings, doubled identifier quotes,
// line/block comments and dollar-delimited procedure bodies. The shared lexer
// enables these lexical forms without using another product's execution rules.
func DatabendTokens(source string) ([]Token, error) {
	if len(source) > 8<<20 || !utf8.ValidString(source) || strings.ContainsRune(source, 0) {
		return nil, errDatabendSQL
	}
	return tokensWithRules(source, true, false)
}

func SplitDatabend(source string) ([]string, error) {
	tokens, err := DatabendTokens(source)
	if err != nil {
		return nil, err
	}
	var out []string
	start, substantive := 0, false
	for _, token := range tokens {
		if token.Comment {
			continue
		}
		if !token.Quoted && token.Text == ";" {
			if substantive {
				out = append(out, strings.TrimSpace(source[start:token.Start]))
			}
			start, substantive = token.End, false
		} else {
			substantive = true
		}
	}
	if substantive {
		out = append(out, strings.TrimSpace(source[start:]))
	}
	if len(out) > 1024 {
		return nil, errDatabendSQL
	}
	return out, nil
}

// DatabendMutationKind captures the original top-level INSERT/UPDATE/DELETE,
// including WITH bodies. It never infers an operation from returned labels.
func DatabendMutationKind(source string) string {
	tokens, err := DatabendTokens(source)
	if err != nil {
		return ""
	}
	var terms []Token
	for _, token := range tokens {
		if !token.Comment {
			terms = append(terms, token)
		}
	}
	kind := func(t Token) string {
		if t.Quoted {
			return ""
		}
		switch strings.ToLower(t.Text) {
		case "insert":
			return "insert"
		case "update":
			return "update"
		case "delete":
			return "delete"
		}
		return ""
	}
	if len(terms) == 0 {
		return ""
	}
	if !strings.EqualFold(terms[0].Text, "WITH") {
		return kind(terms[0])
	}
	i := 1
	if i < len(terms) && strings.EqualFold(terms[i].Text, "RECURSIVE") {
		i++
	}
	skipBody := func() bool {
		if i >= len(terms) || terms[i].Quoted || terms[i].Text != "(" {
			return false
		}
		depth := 0
		for ; i < len(terms); i++ {
			if terms[i].Quoted {
				continue
			}
			if terms[i].Text == "(" {
				depth++
			}
			if terms[i].Text == ")" {
				depth--
				if depth == 0 {
					i++
					return true
				}
			}
		}
		return false
	}
	for i < len(terms) {
		i++ // Captured CTE name, optionally quoted.
		if i < len(terms) && terms[i].Text == "(" && !skipBody() {
			return ""
		}
		if i >= len(terms) || !strings.EqualFold(terms[i].Text, "AS") {
			return ""
		}
		i++
		if !skipBody() || i >= len(terms) {
			return ""
		}
		if terms[i].Text == "," {
			i++
			continue
		}
		return kind(terms[i])
	}
	return ""
}

// DatabendStatementAt uses the same lexical boundaries as execution.
func DatabendStatementAt(source string, offset int) string {
	parts, err := SplitDatabend(source)
	if err != nil {
		return source
	}
	at := 0
	for _, part := range parts {
		start := strings.Index(source[at:], part) + at
		end := start + len(part)
		if offset <= end {
			return part
		}
		at = end
	}
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

func FormatDatabend(source string) (string, error) {
	tokens, err := DatabendTokens(source)
	if err != nil {
		return "", err
	}
	return formatTokens(source, tokens), nil
}
