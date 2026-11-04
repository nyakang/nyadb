package sqltext

import "strings"

// IsRollback recognizes a standalone transaction recovery command. It never
// treats a batch containing application queries as namespace-independent.
func IsRollback(source string) bool {
	if _, err := SplitPortable(source); err != nil {
		return false
	}
	tokens, err := Tokens(source)
	if err != nil {
		return false
	}
	kept := tokens[:0]
	for _, token := range tokens {
		if !token.Comment {
			kept = append(kept, token)
		}
	}
	tokens = kept
	if len(tokens) > 0 && tokens[len(tokens)-1].Text == ";" {
		tokens = tokens[:len(tokens)-1]
	}
	word := func(i int, text string) bool {
		return i < len(tokens) && !tokens[i].Quoted && strings.EqualFold(tokens[i].Text, text)
	}
	if !word(0, "ROLLBACK") && !word(0, "ABORT") {
		return false
	}
	i := 1
	if word(i, "WORK") || word(i, "TRANSACTION") {
		i++
	}
	if word(i, "TO") {
		i++
		if word(i, "SAVEPOINT") {
			i++
		}
		if i != len(tokens)-1 {
			return false
		}
		name := tokens[i].Text
		if tokens[i].Quoted {
			return strings.HasPrefix(name, `"`)
		}
		return name != "" && (name[0] == '_' || name[0] >= 'a' && name[0] <= 'z' || name[0] >= 'A' && name[0] <= 'Z' || name[0] >= 128)
	}
	if word(i, "AND") {
		i++
		if word(i, "NO") {
			i++
		}
		if !word(i, "CHAIN") {
			return false
		}
		i++
	}
	return i == len(tokens)
}
