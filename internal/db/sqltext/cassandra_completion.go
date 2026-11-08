package sqltext

import (
	"strings"
	"unicode/utf8"
)

// CQL completion uses native keyspaces and tables. It never infers SQL aliases,
// joins, correlated scopes or transaction syntax.
func completeCassandra(source string, offset int, catalog CompletionCatalog) CompletionResult {
	r := CompletionResult{Start: offset}
	if len(source) > 256<<10 || offset < len(source) && !utf8.RuneStart(source[offset]) {
		return r
	}
	ts := completionTokens(source, "cassandra")
	if len(ts) > 65536 {
		return r
	}
	prefix, quoted := "", false
	start, end := 0, len(source)
	for _, t := range ts {
		if t.protected && offset > t.start && offset <= t.end {
			return r
		}
		if t.text == ";" && !t.protected && t.end <= offset {
			start = t.end
		}
		if t.text == ";" && !t.protected && t.end > offset && end == len(source) {
			end = t.start
		}
		if t.ident && offset > t.start && offset <= t.end {
			r.Start = t.start
			prefix = completionName(source[t.start:offset])
			quoted = !t.word
		}
	}
	seen := map[string]bool{}
	add := func(name, detail, kind string) {
		if name == "" || !strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) || seen[kind+"\x00"+name] {
			return
		}
		seen[kind+"\x00"+name] = true
		insert := name
		if kind != "keyword" && (quoted || !completionBareName(name) || completionReserved(name) || name != strings.ToLower(name)) {
			insert, _ = Identifier(name, false)
			if quoted && offset < len(source) && source[offset] == '"' {
				insert = strings.TrimSuffix(insert, "\"")
			}
		}
		r.Items = append(r.Items, SQLCompletion{name, insert, detail, kind})
	}
	var before []completionToken
	for _, t := range ts {
		if t.start >= start && t.end <= r.Start && !t.protected {
			before = append(before, t)
		}
	}
	nameOf := func(t completionToken) string {
		v := completionName(t.text)
		if t.word {
			v = strings.ToLower(v)
		}
		return v
	}
	tableContext := func(v string) bool {
		switch strings.ToUpper(v) {
		case "FROM", "INTO", "UPDATE", "TABLE":
			return true
		}
		return false
	}
	if len(before) >= 3 && before[len(before)-1].text == "." && before[len(before)-2].ident && tableContext(before[len(before)-3].text) {
		ks := nameOf(before[len(before)-2])
		for _, o := range catalog.Objects {
			if o.Namespace == ks {
				add(o.Name, ks, "table")
			}
		}
		return finishCompletion(r)
	}
	if len(before) > 0 && tableContext(before[len(before)-1].text) {
		for _, o := range catalog.Objects {
			if o.Namespace == catalog.Namespace {
				add(o.Name, o.Namespace, "table")
			}
		}
		for _, ns := range catalog.Namespaces {
			add(ns, "", "namespace")
		}
		return finishCompletion(r)
	}
	// Resolve a single native relation in this statement, including SELECT
	// projections before FROM. Exact quoted identity prevents cross-keyspace leaks.
	var relation *CompletionObject
	for i, t := range ts {
		if t.start < start || t.end > end || t.protected || !tableContext(t.text) || i+1 >= len(ts) || !ts[i+1].ident {
			continue
		}
		ks, table := catalog.Namespace, nameOf(ts[i+1])
		if i+3 < len(ts) && ts[i+2].text == "." && ts[i+3].ident {
			ks, table = table, nameOf(ts[i+3])
		}
		for j := range catalog.Objects {
			o := &catalog.Objects[j]
			if o.Namespace == ks && o.Name == table {
				relation = o
				break
			}
		}
		break
	}
	if relation != nil {
		for _, col := range relation.Columns {
			add(col, relation.Namespace+"."+relation.Name, "column")
		}
	}
	if !quoted {
		for _, word := range completionKeywords("cassandra") {
			add(word, "", "keyword")
		}
	}
	return finishCompletion(r)
}
