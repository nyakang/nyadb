package sqltext

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// DM8 native mode uses conventional PL quotes/comments and a standalone slash
// as a client block delimiter. This shares only a lexical grammar with Oracle;
// product identity, transport, catalogs and execution remain independent.
func SplitDameng(source string) ([]string, error) {
	if len(source) > 8<<20 || !utf8.ValidString(source) || strings.ContainsRune(source, 0) {
		return nil, errors.New("invalid or oversized DM8 SQL source")
	}
	ts, err := OracleTokens(source)
	if err != nil {
		return nil, errors.New("unterminated DM8 SQL quote or comment")
	}
	parens := 0
	for _, t := range ts {
		if !t.Comment && !t.Quoted {
			if t.Text == "(" {
				parens++
			} else if t.Text == ")" {
				parens--
			}
			if parens < 0 {
				return nil, errors.New("unbalanced DM8 SQL parentheses")
			}
		}
	}
	if parens != 0 {
		return nil, errors.New("unbalanced DM8 SQL parentheses")
	}
	var out []string
	start, head := 0, 0
	code := false
	flush := func(end, next int) error {
		if code {
			if end-start > 1<<20 {
				return errors.New("DM8 statement exceeds 1 MiB")
			}
			part := ts[head:]
			for i, t := range part {
				if t.Start >= end {
					part = part[:i]
					break
				}
			}
			if damengBlock(part) && !damengBlockComplete(part) {
				return errors.New("incomplete DM8 procedural block")
			}
			out = append(out, strings.TrimSpace(source[start:end]))
			if len(out) > 1024 {
				return errors.New("DM8 batch exceeds 1024 statements")
			}
		}
		start, code = next, false
		return nil
	}
	block := damengBlock(ts)
	for i, t := range ts {
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
			if err = flush(t.Start, t.End); err != nil {
				return nil, err
			}
			head = i + 1
			block = damengBlock(ts[head:])
			continue
		}
		code = true
	}
	if err = flush(len(source), len(source)); err != nil {
		return nil, err
	}
	return out, nil
}

func DamengStatementAt(source string, offset int) string {
	parts, err := SplitDameng(source)
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

func FormatDameng(source string) (string, error) {
	parts, err := SplitDameng(source)
	if err != nil {
		return "", err
	}
	for _, part := range parts {
		tokens, err := OracleTokens(part)
		if err != nil {
			return "", err
		}
		if damengBlock(tokens) {
			return source, nil
		}
	}
	for line := range strings.SplitSeq(source, "\n") {
		if strings.TrimSpace(line) == "/" {
			return source, nil
		}
	}
	tokens, err := OracleTokens(source)
	if err != nil {
		return "", err
	}
	return formatTokens(source, tokens), nil
}

func damengBlock(ts []Token) bool {
	var words []string
	for _, t := range ts {
		if t.Comment {
			continue
		}
		if t.Quoted {
			break
		}
		words = append(words, strings.ToUpper(t.Text))
		if len(words) == 4 {
			break
		}
	}
	if len(words) == 0 {
		return false
	}
	if words[0] == "BEGIN" || words[0] == "DECLARE" {
		return true
	}
	if words[0] != "CREATE" || len(words) < 2 {
		return false
	}
	i := 1
	if len(words) == 4 && words[1] == "OR" && words[2] == "REPLACE" {
		i = 3
	}
	switch words[i] {
	case "FUNCTION", "PROCEDURE", "PACKAGE", "TRIGGER", "TYPE":
		return true
	}
	return false
}

func damengBlockComplete(ts []Token) bool {
	var stack []string
	previous := ""
	seenBegin, seenEnd := false, false
	for i := 0; i < len(ts); i++ {
		t := ts[i]
		if t.Comment || t.Quoted {
			continue
		}
		word := strings.ToUpper(t.Text)
		switch word {
		case "BEGIN", "CASE", "LOOP":
			stack = append(stack, word)
			seenBegin = seenBegin || word == "BEGIN"
		case "IF":
			if previous == ";" || previous == "BEGIN" || previous == "THEN" || previous == "ELSE" {
				stack = append(stack, word)
			}
		case "END":
			seenEnd = true
			j := i + 1
			for j < len(ts) && ts[j].Comment {
				j++
			}
			suffix := ""
			if j < len(ts) && (Word(ts[j], "IF") || Word(ts[j], "LOOP") || Word(ts[j], "CASE")) {
				suffix, i = strings.ToUpper(ts[j].Text), j
			}
			if len(stack) == 0 {
				if suffix != "" {
					return false
				}
			} else {
				top := stack[len(stack)-1]
				if suffix != "" && top != suffix || suffix == "" && (top == "IF" || top == "LOOP") {
					return false
				}
				stack = stack[:len(stack)-1]
			}
		}
		previous = word
	}
	if len(stack) != 0 {
		return false
	}
	if seenBegin {
		return seenEnd
	}
	for _, t := range ts {
		if Word(t, "PACKAGE") {
			return seenEnd
		}
		if Word(t, "TYPE") {
			return true
		}
	}
	return false
}

func DamengQuery(source string) bool {
	ts, err := OracleTokens(source)
	if err != nil {
		return false
	}
	first, depth := true, 0
	for _, t := range ts {
		if t.Comment || t.Quoted {
			continue
		}
		if first {
			first = false
			if !Word(t, "WITH") {
				return Word(t, "SELECT")
			}
		}
		if t.Text == "(" {
			depth++
		} else if t.Text == ")" {
			depth--
		}
		if depth == 0 {
			if Word(t, "SELECT") {
				return true
			}
			if Word(t, "INSERT") || Word(t, "UPDATE") || Word(t, "DELETE") || Word(t, "MERGE") {
				return false
			}
		}
	}
	return false
}

// DamengDDLColumn is a declaration snapshot read from native GET_DDL. The
// quoted name and type are preserved; keyword-looking quoted defaults never
// become generated/identity flags. This text is never executed by the parser.
type DamengDDLColumn struct {
	Type                string
	Generated, Identity bool
}

func DamengDDLColumns(source string) (map[string]DamengDDLColumn, error) {
	if len(source) > 1<<20 {
		return nil, errors.New("oversized DM8 table definition")
	}
	ts, err := OracleTokens(source)
	// This DM8 build escapes column/header names, but its foreign-key DDL
	// can emit an unescaped quote in the referenced schema. Only fields are
	// parsed here; foreign keys come from qualified native catalog rows.
	// Accept a canonical constraint-line boundary only when the preceding
	// field prefix is lexically complete and ends in a top-level comma.
	for at := 0; at < len(source); {
		next := strings.IndexByte(source[at:], '\n')
		if next < 0 {
			break
		}
		at += next + 1
		line := strings.TrimLeft(source[at:], " \t\r")
		constraint := false
		for _, prefix := range []string{"CONSTRAINT ", "NOT CLUSTER PRIMARY KEY", "PRIMARY KEY", "FOREIGN KEY", "UNIQUE(", "CHECK("} {
			constraint = constraint || strings.HasPrefix(line, prefix)
		}
		if !constraint {
			continue
		}
		prefix, e := OracleTokens(source[:at])
		if e != nil || len(prefix) == 0 || prefix[len(prefix)-1].Text != "," {
			continue
		}
		depth := 0
		for _, t := range prefix {
			if !t.Comment && !t.Quoted {
				if t.Text == "(" {
					depth++
				} else if t.Text == ")" {
					depth--
				}
			}
		}
		if depth == 1 {
			ts = append(prefix, Token{Text: ")", Start: at, End: at})
			err = nil
			break
		}
	}
	if err != nil {
		return nil, errors.New("unreadable DM8 field declarations")
	}
	var out = make(map[string]DamengDDLColumn)
	depth, start := 0, -1
	column := func(end int) error {
		part := ts[start:end]
		for len(part) > 0 && part[0].Comment {
			part = part[1:]
		}
		if len(part) < 2 || !part[0].Quoted || !strings.HasPrefix(part[0].Text, `"`) {
			return nil // table-level constraints/storage declarations
		}
		name := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(part[0].Text, `"`), `"`), `""`, `"`)
		if _, duplicate := out[name]; duplicate {
			return errors.New("duplicate DM8 field declaration")
		}
		level, typeEnd := 0, len(part)
		for i, t := range part[1:] {
			if t.Comment || t.Quoted {
				continue
			}
			if t.Text == "(" {
				level++
			} else if t.Text == ")" {
				level--
			}
			if level == 0 {
				switch strings.ToUpper(t.Text) {
				case "NOT", "NULL", "DEFAULT", "GENERATED", "IDENTITY", "CONSTRAINT", "CHECK", "PRIMARY", "REFERENCES", "UNIQUE":
					typeEnd = i + 1
				}
			}
			if typeEnd != len(part) {
				break
			}
		}
		endByte := part[len(part)-1].End
		if typeEnd < len(part) {
			endByte = part[typeEnd].Start
		}
		c := DamengDDLColumn{Type: strings.TrimSpace(source[part[0].End:endByte])}
		if typeEnd < len(part) {
			c.Identity = Word(part[typeEnd], "IDENTITY")
			c.Generated = Word(part[typeEnd], "GENERATED") || c.Identity
		}
		if c.Type == "" {
			return errors.New("missing DM8 field type")
		}
		out[name] = c
		return nil
	}
	for i, t := range ts {
		if t.Quoted || t.Comment {
			continue
		}
		if t.Text == "(" {
			depth++
			if start < 0 {
				start = i + 1
			}
		} else if t.Text == ")" {
			if depth == 1 {
				if err = column(i); err != nil {
					return nil, err
				}
				return out, nil
			}
			depth--
		} else if t.Text == "," && depth == 1 {
			if err = column(i); err != nil {
				return nil, err
			}
			start = i + 1
		}
	}
	return nil, errors.New("incomplete DM8 table definition")
}
