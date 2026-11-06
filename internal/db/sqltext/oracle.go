package sqltext

import (
	"errors"
	"strings"
)

// OracleTokens recognizes ordinary/national strings and alternative q quoting.
// A slash alone on a line is a client block delimiter, never sent to Oracle.
func OracleTokens(source string) ([]Token, error) {
	var out []Token
	for i := 0; i < len(source); {
		if strings.ContainsRune(" \t\r\n", rune(source[i])) {
			i++
			continue
		}
		start := i
		quoted, comment := false, false
		switch {
		case strings.HasPrefix(source[i:], "--"):
			comment = true
			i += 2
			for i < len(source) && source[i] != '\n' {
				i++
			}
		case strings.HasPrefix(source[i:], "/*"):
			comment = true
			i += 2
			end := strings.Index(source[i:], "*/")
			if end < 0 {
				return nil, errors.New("unterminated Oracle comment")
			}
			i += end + 2
		case (source[i] == 'q' || source[i] == 'Q') && i+2 < len(source) && source[i+1] == '\'' || (source[i] == 'n' || source[i] == 'N') && i+3 < len(source) && (source[i+1] == 'q' || source[i+1] == 'Q') && source[i+2] == '\'':
			quoted = true
			offset := 2
			if source[i] == 'n' || source[i] == 'N' {
				offset = 3
			}
			open := source[i+offset]
			close := open
			switch open {
			case '[':
				close = ']'
			case '{':
				close = '}'
			case '(':
				close = ')'
			case '<':
				close = '>'
			}
			i += offset + 1
			end := strings.Index(source[i:], string([]byte{close, '\''}))
			if end < 0 {
				return nil, errors.New("unterminated Oracle q string")
			}
			i += end + 2
		case source[i] == '\'' || source[i] == '"':
			quoted = true
			delimiter := source[i]
			i++
			closed := false
			for i < len(source) {
				if source[i] == delimiter {
					i++
					if i < len(source) && source[i] == delimiter {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, errors.New("unterminated Oracle quote")
			}
		case isOracleWord(source[i]):
			i++
			for i < len(source) && isOracleWord(source[i]) {
				i++
			}
		default:
			i++
		}
		out = append(out, Token{Text: source[start:i], Start: start, End: i, Quoted: quoted, Comment: comment})
	}
	return out, nil
}
func isOracleWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$' || c == '#' || c >= 128
}
func oracleBlock(tokens []Token) bool {
	var words []string
	for _, t := range tokens {
		if !t.Comment && !t.Quoted {
			words = append(words, strings.ToUpper(t.Text))
			if len(words) >= 8 {
				break
			}
		}
	}
	if len(words) == 0 {
		return false
	}
	if words[0] == "BEGIN" || words[0] == "DECLARE" {
		return true
	}
	if words[0] != "CREATE" {
		return false
	}
	for _, w := range words[1:] {
		switch w {
		case "FUNCTION", "PROCEDURE", "PACKAGE", "TRIGGER", "TYPE":
			return true
		}
	}
	return false
}
func SplitOracle(source string) ([]string, error) {
	tokens, err := OracleTokens(source)
	if err != nil {
		return nil, err
	}
	var out []string
	start, index := 0, 0
	code := false
	block := oracleBlock(tokens)
	flush := func(end int) {
		if code {
			out = append(out, strings.TrimSpace(source[start:end]))
		}
		code = false
	}
	for i, t := range tokens {
		if t.Comment {
			continue
		}
		slash := false
		if !t.Quoted && t.Text == "/" {
			left := strings.LastIndex(source[:t.Start], "\n") + 1
			right := strings.Index(source[t.End:], "\n")
			if right < 0 {
				right = len(source)
			} else {
				right += t.End
			}
			slash = strings.TrimSpace(source[left:right]) == "/"
		}
		if slash || !block && !t.Quoted && t.Text == ";" {
			flush(t.Start)
			start = t.End
			index = i + 1
			block = oracleBlock(tokens[index:])
			continue
		}
		code = true
	}
	flush(len(source))
	if len(out) > 1024 {
		return nil, errors.New("Oracle batch exceeds 1024 statements")
	}
	return out, nil
}
func OracleStatementAt(source string, offset int) string {
	parts, err := SplitOracle(source)
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
func FormatOracle(source string) (string, error) {
	tokens, err := OracleTokens(source)
	if err != nil {
		return "", err
	}
	if oracleBlock(tokens) {
		return source, nil
	}
	return formatTokens(source, tokens), nil
}
func OracleQuery(source string) bool {
	tokens, err := OracleTokens(source)
	if err != nil {
		return false
	}
	depth := 0
	for _, t := range tokens {
		if t.Comment || t.Quoted {
			continue
		}
		switch t.Text {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth != 0 {
			continue
		}
		if Word(t, "SELECT") {
			return true
		}
		if Word(t, "INSERT") || Word(t, "UPDATE") || Word(t, "DELETE") || Word(t, "MERGE") {
			return false
		}
		if !Word(t, "WITH") && len(t.Text) > 0 && len(tokens) > 0 && t.Start == tokens[0].Start {
			return false
		}
	}
	return false
}
func SingleOracleStatement(source string) error {
	parts, err := SplitOracle(source)
	if err != nil {
		return err
	}
	if len(parts) != 1 || !OracleQuery(parts[0]) {
		return errors.New("Oracle EXPLAIN requires one SELECT statement")
	}
	return nil
}
