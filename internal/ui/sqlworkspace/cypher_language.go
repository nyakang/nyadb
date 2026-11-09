package sqlworkspace

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui/editor"
)

var cypherKeywords = strings.Fields("MATCH OPTIONAL WHERE RETURN WITH UNWIND AS CALL YIELD CREATE MERGE SET REMOVE DELETE DETACH FOREACH UNION ALL DISTINCT ORDER BY SKIP LIMIT ASC DESC CASE WHEN THEN ELSE END AND OR XOR NOT IN IS NULL TRUE FALSE EXISTS STARTS ENDS CONTAINS EXPLAIN PROFILE SHOW DATABASES INDEXES CONSTRAINTS PROCEDURES FUNCTIONS USE LOAD CSV FROM HEADERS TRANSACTIONS ON ERROR CONTINUE BREAK FAIL")

var cypherLanguage = &editor.Language{
	Name: "Cypher", Extensions: []string{".cypher", ".cyp"}, TabSize: 2, WordChars: "$",
	LineComment: "//", BlockComment: [2]string{"/*", "*/"},
	Brackets:  []editor.BracketPair{{Open: "(", Close: ")"}, {Open: "[", Close: "]"}, {Open: "{", Close: "}"}},
	AutoClose: []editor.BracketPair{{Open: "(", Close: ")"}, {Open: "[", Close: "]"}, {Open: "{", Close: "}"}, {Open: "'", Close: "'", NotIn: []editor.TokenKind{editor.TokenString, editor.TokenComment}}, {Open: "\"", Close: "\"", NotIn: []editor.TokenKind{editor.TokenString, editor.TokenComment}}, {Open: "`", Close: "`", NotIn: []editor.TokenKind{editor.TokenString, editor.TokenComment}}},
	Tokenizer: editor.TokenizerFunc(cypherTokens),
}

func cypherWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }
func cypherTokens(line string, state uint64, tokens []editor.Token) ([]editor.Token, uint64) {
	for i := 0; i < len(line); {
		kind := editor.TokenText
		if state == 'B' {
			kind = editor.TokenComment
			if n := strings.Index(line[i:], "*/"); n >= 0 {
				i += n + 2
				state = 0
			} else {
				i = len(line)
			}
		} else if state == '\'' || state == '"' || state == '`' {
			kind = editor.TokenString
			quote := byte(state)
			for i < len(line) {
				ch := line[i]
				i++
				if ch == '\\' && quote != '`' && i < len(line) {
					i++
					continue
				}
				if ch == quote {
					if quote == '`' && i < len(line) && line[i] == '`' {
						i++
						continue
					}
					state = 0
					break
				}
			}
		} else {
			r, size := utf8.DecodeRuneInString(line[i:])
			switch {
			case strings.HasPrefix(line[i:], "//"):
				kind = editor.TokenComment
				i = len(line)
			case strings.HasPrefix(line[i:], "/*"):
				state = 'B'
				kind = editor.TokenComment
				i += 2
			case r == '\'' || r == '"' || r == '`':
				state = uint64(r)
				kind = editor.TokenString
				i += size
			case r == '$':
				kind = editor.TokenVariable
				i++
				for i < len(line) {
					r, n := utf8.DecodeRuneInString(line[i:])
					if !cypherWord(r) {
						break
					}
					i += n
				}
			case unicode.IsDigit(r):
				kind = editor.TokenNumber
				i += size
				for i < len(line) && strings.ContainsRune("0123456789abcdefABCDEFxX.eE", rune(line[i])) {
					i++
				}
			case cypherWord(r):
				start := i
				i += size
				for i < len(line) {
					r, n := utf8.DecodeRuneInString(line[i:])
					if !cypherWord(r) {
						break
					}
					i += n
				}
				word := strings.ToUpper(line[start:i])
				for _, k := range cypherKeywords {
					if word == k {
						kind = editor.TokenKeyword
						break
					}
				}
			case strings.ContainsRune("=<>+-*/%^|!", r):
				kind = editor.TokenOperator
				i += size
			case strings.ContainsRune("(){}[],:;.", r):
				kind = editor.TokenPunctuation
				i += size
			default:
				i += size
			}
		}
		tokens = append(tokens, editor.Token{End: i, Kind: kind})
	}
	return tokens, state
}

type GraphCompletion struct{ Name, Kind string }

func (s *State) cypherCompletions(d *Document, r editor.CompletionRequest) {
	if d.closed || d.Editor == nil || d.Editor.Buffer.Version() != r.Version {
		return
	}
	source := d.Editor.Buffer.String()
	offset := min(max(0, r.Offset), len(source))
	start := offset
	for start > 0 {
		ch, n := utf8.DecodeLastRuneInString(source[:start])
		if !cypherWord(ch) && ch != '$' && ch != '.' {
			break
		}
		start -= n
	}
	prefix := source[start:offset]
	before := strings.TrimSpace(source[:start])
	param := strings.HasPrefix(prefix, "$")
	label := strings.HasSuffix(before, ":")
	items := []editor.Completion{}
	seen := map[string]bool{}
	add := func(name, insert string, kind editor.CompletionKind) {
		if seen[name] || !strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) {
			return
		}
		seen[name] = true
		items = append(items, editor.Completion{Label: name, Insert: insert, Kind: kind})
	}
	if param {
		if params, e := graphParameters(d.graphParameters); e == nil {
			names := make([]string, 0, len(params))
			for name := range params {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				add("$"+name, "$"+name, editor.CompletionVariable)
			}
		}
	} else {
		if !label {
			for _, name := range cypherKeywords {
				add(name, name, editor.CompletionKeyword)
			}
		}
		if s.deps.GraphCompletions != nil {
			profile, db := d.sourceScope()
			for _, v := range s.deps.GraphCompletions(profile, db) {
				if label {
					if v.Kind != "label" && v.Kind != "relationship" {
						continue
					}
				} else if v.Kind != "procedure" && v.Kind != "function" {
					continue
				}
				insert := v.Name
				if label {
					first, _ := utf8.DecodeRuneInString(insert)
					quoted := !(unicode.IsLetter(first) || first == '_')
					for _, ch := range insert {
						if !cypherWord(ch) {
							quoted = true
							break
						}
					}
					if quoted {
						insert = "`" + strings.ReplaceAll(v.Name, "`", "``") + "`"
					}
				}
				add(v.Name, insert, editor.CompletionFunction)
			}
		}
	}
	d.Editor.ShowCompletions(start, items)
}
