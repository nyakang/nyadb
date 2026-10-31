package sqltext

import (
	"errors"
	"strings"
)

// SingleDefinition permits semicolons inside SQL compound bodies while
// rejecting a second top-level statement after a complete public definition.
func SingleDefinition(raw string) error {
	ts, e := Tokens(raw)
	if e != nil {
		return e
	}
	var tokens []Token
	for _, t := range ts {
		if t.Comment {
			if strings.HasPrefix(t.Text, "/*!") && strings.Contains(t.Text, ";") {
				return errors.New("statement delimiter in executable definition comment")
			}
			continue
		}
		tokens = append(tokens, t)
	}
	depth := 0
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if t.Quoted {
			continue
		}
		if Word(t, "END") {
			if depth == 0 {
				return errors.New("unexpected END in definition")
			}
			depth--
			if i+1 < len(tokens) {
				for _, kind := range []string{"IF", "CASE", "LOOP", "WHILE", "REPEAT"} {
					if Word(tokens[i+1], kind) {
						i++
						break
					}
				}
			}
			continue
		}
		if Word(t, "BEGIN") || Word(t, "CASE") {
			depth++
		}
		if Word(t, "LOOP") || Word(t, "WHILE") || Word(t, "REPEAT") {
			depth++
		}
		if Word(t, "IF") {
			// IF(expr,a,b) is an expression. Procedural IF has a THEN before
			// its first statement delimiter; quoted THEN text cannot count.
			start := i + 1
			if start < len(tokens) && tokens[start].Text == "(" {
				parens := 0
				j := start
				for ; j < len(tokens); j++ {
					if tokens[j].Quoted {
						continue
					}
					if tokens[j].Text == "(" {
						parens++
					}
					if tokens[j].Text == ")" {
						parens--
						if parens == 0 {
							j++
							break
						}
					}
				}
				if j < len(tokens) && Word(tokens[j], "THEN") {
					depth++
				}
			} else {
				for j := start; j < len(tokens) && tokens[j].Text != ";"; j++ {
					if Word(tokens[j], "THEN") {
						depth++
						break
					}
				}
			}
		}
		if t.Text == ";" && depth == 0 && i != len(tokens)-1 {
			return errors.New("multiple top-level statements in public definition")
		}
	}
	if depth != 0 {
		return errors.New("unclosed SQL compound definition")
	}
	return nil
}
