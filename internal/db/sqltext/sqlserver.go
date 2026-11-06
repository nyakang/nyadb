package sqltext

import (
	"errors"
	"strings"
)

// SQLServerTokens recognizes T-SQL brackets, Unicode strings, variables and
// nested comments. Backslashes never escape a T-SQL quote.
func SQLServerTokens(source string) ([]Token, error) {
	if len(source) > 8<<20 {
		return nil, errors.New("T-SQL exceeds 8 MiB")
	}
	var out []Token
	for i := 0; i < len(source); {
		if strings.ContainsRune(" \t\r\n", rune(source[i])) {
			i++
			continue
		}
		start, quoted, comment := i, false, false
		switch {
		case strings.HasPrefix(source[i:], "--"):
			comment = true
			for i < len(source) && source[i] != '\n' {
				i++
			}
		case strings.HasPrefix(source[i:], "/*"):
			comment = true
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
				return nil, errors.New("unterminated T-SQL comment")
			}
		case source[i] == '[' || source[i] == '\'' || source[i] == '"' || (source[i] == 'N' || source[i] == 'n') && i+1 < len(source) && source[i+1] == '\'':
			quoted = true
			if source[i] == 'N' || source[i] == 'n' {
				i++
			}
			end := source[i]
			if end == '[' {
				end = ']'
			}
			i++
			closed := false
			for i < len(source) {
				if source[i] == end {
					i++
					if i < len(source) && source[i] == end {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, errors.New("unterminated T-SQL quote")
			}
		default:
			i++
			if sqlServerWord(source[start]) {
				for i < len(source) && sqlServerWord(source[i]) {
					i++
				}
			}
		}
		out = append(out, Token{Text: source[start:i], Start: start, End: i, Quoted: quoted, Comment: comment})
	}
	return out, nil
}
func sqlServerWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '@' || c == '#' || c == '$' || c >= 128
}

// SplitSQLServer sends whole batches so DECLARE variables, TRY/CATCH blocks
// and procedure definitions retain server semantics. Only standalone GO lines
// separate batches. GO repeat counts are rejected to prevent accidental replay.
func SplitSQLServer(source string) ([]string, error) {
	spans, err := sqlServerBatches(source)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, span := range spans {
		out = append(out, strings.TrimSpace(source[span[0]:span[1]]))
	}
	return out, nil
}
func sqlServerBatches(source string) ([][2]int, error) {
	tokens, err := SQLServerTokens(source)
	if err != nil {
		return nil, err
	}
	var spans [][2]int
	start, code := 0, false
	for i, t := range tokens {
		if t.Comment {
			continue
		}
		if Word(t, "GO") {
			lineStart := strings.LastIndexByte(source[:t.Start], '\n') + 1
			lineEnd := len(source)
			if n := strings.IndexByte(source[t.End:], '\n'); n >= 0 {
				lineEnd = t.End + n
			}
			if strings.TrimSpace(source[lineStart:t.Start]) == "" {
				nextStart := min(len(source), lineEnd+1)
				for _, next := range tokens[i+1:] {
					if next.Start >= lineEnd {
						break
					}
					if !next.Comment {
						return nil, errors.New("GO repeat counts or trailing syntax are unsupported")
					}
					nextStart = max(nextStart, next.End)
				}
				if code {
					spans = append(spans, [2]int{start, lineStart})
				}
				start = nextStart
				code = false
				continue
			}
		}
		code = true
	}
	if code {
		spans = append(spans, [2]int{start, len(source)})
	}
	if len(spans) > 256 {
		return nil, errors.New("T-SQL exceeds 256 batches")
	}
	return spans, nil
}
func SQLServerStatementAt(source string, offset int) string {
	spans, err := sqlServerBatches(source)
	if err != nil {
		return source
	}
	for _, span := range spans {
		if offset <= span[1] {
			return strings.TrimSpace(source[span[0]:span[1]])
		}
	}
	if len(spans) > 0 {
		span := spans[len(spans)-1]
		return strings.TrimSpace(source[span[0]:span[1]])
	}
	return ""
}

func SingleSQLServerStatement(source string) error {
	parts, err := SplitSQLServer(source)
	if err != nil {
		return err
	}
	if len(parts) != 1 {
		return errors.New("EXPLAIN requires one T-SQL batch")
	}
	tokens, err := SQLServerTokens(parts[0])
	if err != nil {
		return err
	}
	code, ended := false, false
	for _, t := range tokens {
		if t.Comment {
			continue
		}
		if ended {
			return errors.New("EXPLAIN requires one T-SQL statement")
		}
		if !code && !Word(t, "SELECT") && !Word(t, "WITH") {
			return errors.New("SQL Server EXPLAIN supports SELECT and WITH")
		}
		code = true
		if !t.Quoted && t.Text == ";" {
			ended = true
		}
		if Word(t, "SET") || Word(t, "EXEC") || Word(t, "EXECUTE") || Word(t, "GO") {
			return errors.New("unsupported T-SQL EXPLAIN syntax")
		}
	}
	if !code {
		return errors.New("empty T-SQL statement")
	}
	return nil
}
func FormatSQLServer(source string) (string, error) {
	if _, err := SplitSQLServer(source); err != nil {
		return "", err
	}
	tokens, err := SQLServerTokens(source)
	if err != nil {
		return "", err
	}
	// Preserve newlines: line-sensitive GO and -- comments must not move.
	keywords := " select from where order by group having offset fetch next rows only top insert into values update set delete join left right inner outer on as and or not null is create alter table view index primary key foreign references unique default check constraint begin end when then else case union all distinct asc desc declare exec execute procedure function return try catch throw transaction commit rollback use go output over partition "
	var b strings.Builder
	last := 0
	for _, t := range tokens {
		b.WriteString(source[last:t.Start])
		v := t.Text
		if !t.Quoted && !t.Comment && strings.Contains(keywords, " "+strings.ToLower(v)+" ") {
			v = strings.ToUpper(v)
		}
		b.WriteString(v)
		last = t.End
	}
	b.WriteString(source[last:])
	return b.String(), nil
}
