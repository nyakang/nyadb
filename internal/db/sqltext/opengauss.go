package sqltext

import (
	"errors"
	"strings"
)

// SplitOpenGauss preserves native AS/IS ... BEGIN ... END; routine bodies.
// A following slash on its own line is a client delimiter, never a replay.
// PostgreSQL dollar strings, escape strings and nested comments remain opaque.
func SplitOpenGauss(source string) ([]string, error) {
	if len(source) > 8<<20 {
		return nil, errors.New("openGauss SQL exceeds 8 MiB")
	}
	ts, err := openGaussTokens(source)
	if err != nil {
		return nil, err
	}
	var out []string
	start, head, parens := 0, 0, 0
	routine, native, began, complete, code, slashAllowed := false, false, false, false, false, false
	previous := ""
	var stack []string
	flush := func(end, next int) {
		if code {
			out = append(out, strings.TrimSpace(source[start:end]))
		}
		start, head, parens = next, 0, 0
		previous = ""
		slashAllowed = routine
		routine, native, began, complete, code = false, false, false, false, false
		stack = nil
	}
	for i := 0; i < len(ts); i++ {
		t := ts[i]
		if t.Comment {
			continue
		}
		if !t.Quoted && t.Text == "/" && openGaussSlashLine(source, t) && slashAllowed && !code {
			start, slashAllowed = t.End, false
			continue
		}
		code = true
		if t.Quoted {
			previous = "quoted"
			continue
		}
		word := strings.ToUpper(t.Text)
		// Match only the actual CREATE [OR REPLACE] routine prefix. A column
		// named procedure or EXECUTE PROCEDURE in a trigger is not a body.
		switch head {
		case 0:
			if word == "CREATE" {
				head = 1
			} else {
				head = -1
			}
		case 1, 3:
			switch word {
			case "OR":
				if head == 1 {
					head = 2
				} else {
					head = -1
				}
			case "FUNCTION", "PROCEDURE":
				routine, head = true, -1
			case "PACKAGE":
				return nil, errors.New("openGauss package scripts are unsupported")
			default:
				head = -1
			}
		case 2:
			if word == "REPLACE" {
				head = 3
			} else {
				head = -1
			}
		}
		if routine && !native && parens == 0 && (word == "AS" || word == "IS") {
			j := i + 1
			for j < len(ts) && ts[j].Comment {
				j++
			}
			// SQL/plpgsql functions with a quoted body use ordinary PG splitting.
			if j < len(ts) && !ts[j].Quoted {
				native = true
			}
		}
		if native {
			switch word {
			case "BEGIN", "CASE", "LOOP":
				stack = append(stack, word)
				began = began || word == "BEGIN"
			case "IF":
				// Procedural IF begins a body statement. SQL IF EXISTS within
				// DDL does not introduce another block or need lookahead.
				if previous == ";" || previous == "BEGIN" || previous == "THEN" || previous == "ELSE" {
					stack = append(stack, "IF")
				}
			case "FUNCTION", "PROCEDURE", "PACKAGE":
				return nil, errors.New("openGauss local routine declarations are unsupported")
			case "END":
				if len(stack) == 0 {
					return nil, errors.New("unexpected openGauss END")
				}
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				j := i + 1
				for j < len(ts) && ts[j].Comment {
					j++
				}
				suffix := ""
				if j < len(ts) && (Word(ts[j], "IF") || Word(ts[j], "LOOP") || Word(ts[j], "CASE")) {
					suffix, i = strings.ToUpper(ts[j].Text), j
				}
				if suffix != "" && suffix != top || suffix == "" && (top == "IF" || top == "LOOP") {
					return nil, errors.New("mismatched openGauss block ending")
				}
				complete = began && len(stack) == 0
			}
		}
		if t.Text == "(" {
			parens++
		} else if t.Text == ")" {
			parens--
		}
		if t.Text == ";" && (!native || complete) {
			end := t.Start
			if native {
				end = t.End // The native grammar requires the final END semicolon.
			}
			flush(end, t.End)
		} else {
			previous = word
		}
	}
	if native {
		return nil, errors.New("incomplete openGauss routine; expected END;")
	}
	flush(len(source), len(source))
	if len(out) > 1024 {
		return nil, errors.New("openGauss script exceeds 1024 statements")
	}
	return out, nil
}

func openGaussSlashLine(source string, t Token) bool {
	left := strings.LastIndex(source[:t.Start], "\n") + 1
	right := strings.Index(source[t.End:], "\n")
	if right < 0 {
		right = len(source)
	} else {
		right += t.End
	}
	return strings.TrimSpace(source[left:right]) == "/"
}

func OpenGaussStatementAt(source string, offset int) string {
	parts, err := SplitOpenGauss(source)
	if err != nil {
		return source // Execute validates the whole script before sending SQL.
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

// Preserve native routine whitespace and body bytes during formatting.
func FormatOpenGauss(source string) (string, error) {
	parts, err := SplitOpenGauss(source)
	if err != nil {
		return "", err
	}
	for _, q := range parts {
		if strings.HasSuffix(q, ";") {
			return source, nil
		}
	}
	return FormatPortable(source)
}

func openGaussTokens(source string) ([]Token, error) {
	var out []Token
	for i := 0; i < len(source); {
		if source[i] <= ' ' {
			i++
			continue
		}
		start := i
		t := Token{Start: i}
		switch {
		case strings.HasPrefix(source[i:], "--"):
			t.Comment = true
			for i < len(source) && source[i] != '\n' {
				i++
			}
		case strings.HasPrefix(source[i:], "/*"):
			t.Comment = true
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
				return nil, errors.New("unterminated openGauss comment")
			}
		case source[i] == '\'' || source[i] == '"':
			t.Quoted = true
			delimiter := source[i]
			escape := delimiter == '\'' && i > 0 && (source[i-1] == 'E' || source[i-1] == 'e') && (i < 2 || !isOracleWord(source[i-2]))
			i++
			closed := false
			for i < len(source) {
				if escape && source[i] == '\\' && i+1 < len(source) {
					i += 2
				} else if source[i] == delimiter {
					i++
					if i < len(source) && source[i] == delimiter {
						i++
						continue
					}
					closed = true
					break
				} else {
					i++
				}
			}
			if !closed {
				return nil, errors.New("unterminated openGauss quote")
			}
		case clickHouseDollarTag(source, i) != "":
			t.Quoted = true
			tag := clickHouseDollarTag(source, i)
			end := strings.Index(source[i+len(tag):], tag)
			if end < 0 {
				return nil, errors.New("unterminated openGauss dollar string")
			}
			i += 2*len(tag) + end
		default:
			i++
			if isOracleWord(source[start]) && source[start] != '$' && source[start] != '#' {
				for i < len(source) && isOracleWord(source[i]) && source[i] != '#' {
					i++
				}
			}
		}
		t.Text, t.End = source[start:i], i
		out = append(out, t)
	}
	return out, nil
}
