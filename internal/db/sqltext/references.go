package sqltext

import "strings"

// ReferencedObjects reads relation names from incomplete SQL using the same
// lexer as completion. Aliases, comments, literals and local CTE names are not
// treated as server objects. The caller supplies the original namespace.
func ReferencedObjects(source, dialect, namespace string) []CompletionObject {
	if len(source) > 256<<10 {
		return nil
	}
	tokens := completionTokens(source, dialect)
	ctes := map[string]bool{}
	for i, t := range tokens {
		if t.ident && i+2 < len(tokens) && strings.EqualFold(tokens[i+1].text, "AS") && tokens[i+2].text == "(" {
			ctes[strings.ToLower(completionName(t.text))] = true
		}
	}
	out := []CompletionObject{}
	seen := map[string]bool{}
	depth := 0
	from := map[int]bool{}
	for i, t := range tokens {
		if t.protected {
			continue
		}
		if t.text == "(" {
			depth++
			continue
		}
		if t.text == ")" {
			delete(from, depth)
			depth = max(0, depth-1)
			continue
		}
		if t.text == ";" {
			clear(from)
			continue
		}
		comma := t.text == "," && from[depth]
		if !t.word && !comma {
			continue
		}
		switch strings.ToUpper(t.text) {
		case "FROM", "JOIN":
			from[depth] = true
		case "UPDATE", "INTO":
		case "WHERE", "GROUP", "ORDER", "HAVING", "LIMIT", "OFFSET", "UNION", "RETURNING", "SET", "VALUES":
			delete(from, depth)
			continue
		default:
			if !comma {
				continue
			}
		}
		j := i + 1
		if j >= len(tokens) || !tokens[j].ident || tokens[j].protected {
			continue
		}
		parts := []string{completionName(tokens[j].text)}
		for j+2 < len(tokens) && tokens[j+1].text == "." && tokens[j+2].ident {
			j += 2
			parts = append(parts, completionName(tokens[j].text))
		}
		if j+1 < len(tokens) && tokens[j+1].text == "(" {
			continue
		}
		if len(parts) > 2 {
			continue
		}
		name := parts[len(parts)-1]
		ns := namespace
		if len(parts) == 2 {
			ns = parts[0]
		} else if ctes[strings.ToLower(name)] {
			continue
		}
		key := ns + "\x00" + name
		if name == "" || ns == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, CompletionObject{Namespace: ns, Name: name})
		if len(out) >= 16 {
			break
		}
	}
	return out
}
