package sqltext

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CompletionCatalog is a read-only snapshot of known metadata for one profile.
// Unknown column lists stay empty; completion never performs a database read.
type CompletionCatalog struct {
	Dialect, Namespace string
	Namespaces         []string
	Objects            []CompletionObject
}
type CompletionObject struct {
	Namespace, Name string
	Columns         []string
}
type SQLCompletion struct {
	Name, Insert, Detail, Kind string
}
type CompletionResult struct {
	Start int
	Items []SQLCompletion
}
type completionToken struct {
	text        string
	start, end  int
	ident, word bool
	protected   bool
}
type completionScope struct {
	start, end int
	parent     *completionScope
	children   map[int]*completionScope
	tokens     []completionToken
	relations  []completionRelation
	ctes       map[string][]string
}
type completionRelation struct {
	alias, name, namespace string
	columns                []string
	start, end             int
}

// CompleteSQL handles incomplete SQL conservatively. Relations are resolved
// within the current statement and lexical query scope, including correlated
// parents, CTE columns and named derived projections. Ambiguous identities do
// not acquire columns from an unrelated table. Offsets are UTF-8 byte offsets.
func CompleteSQL(source string, offset int, catalog CompletionCatalog) CompletionResult {
	offset = max(0, min(offset, len(source)))
	result := CompletionResult{Start: offset}
	if len(source) > 256<<10 || offset < len(source) && !utf8.RuneStart(source[offset]) {
		return result
	}
	ts := completionTokens(source, catalog.Dialect)
	if len(ts) > 65536 {
		return result
	}
	prefix, quoted := "", false
	before := []completionToken{}
	for _, t := range ts {
		if t.protected && offset > t.start && offset <= t.end {
			return result
		}
		if t.ident && offset > t.start && offset <= t.end {
			result.Start = t.start
			prefix = source[t.start:offset]
			quoted = !t.word
			if quoted {
				prefix = completionName(prefix)
			}
		}
		if t.end <= result.Start && !t.protected {
			before = append(before, t)
		}
	}
	// Determine the statement containing the caret before constructing scopes.
	start, end := 0, len(source)
	for _, t := range ts {
		if t.text == ";" && !t.protected {
			if t.end <= offset {
				start = t.end
			} else {
				end = t.start
				break
			}
		}
	}
	root := &completionScope{start: start, end: end, children: map[int]*completionScope{}, ctes: map[string][]string{}}
	stack := []*completionScope{root}
	for _, t := range ts {
		if t.start < start || t.end > end || t.protected {
			continue
		}
		scope := stack[len(stack)-1]
		if t.text == ")" && len(stack) > 1 {
			scope.end = t.start
			stack = stack[:len(stack)-1]
			continue
		}
		scope.tokens = append(scope.tokens, t)
		if t.text == "(" {
			if len(stack) >= 128 {
				return result
			}
			child := &completionScope{start: t.end, end: end, parent: scope, children: map[int]*completionScope{}, ctes: map[string][]string{}}
			scope.children[t.start] = child
			stack = append(stack, child)
		}
	}
	buildCompletionRelations(root, catalog)
	scope := root
	for {
		var next *completionScope
		for _, child := range scope.children {
			if offset >= child.start && offset <= child.end {
				next = child
				break
			}
		}
		if next == nil {
			break
		}
		scope = next
	}
	// Parentheses in expressions inherit their containing query's relations.
	for scope.parent != nil && !scope.hasQuery() {
		scope = scope.parent
	}
	var qualifiers []string
	for i := len(before) - 1; i > 0 && before[i].text == "." && before[i-1].ident; i -= 2 {
		qualifiers = append([]string{completionName(before[i-1].text)}, qualifiers...)
	}
	seen := map[string]bool{}
	add := func(name, detail, kind string) {
		if name == "" || !strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) {
			return
		}
		key := strings.ToLower(name)
		if seen[key] {
			return
		}
		seen[key] = true
		insert := name
		if kind != "keyword" && (quoted || !completionBareName(name) || completionReserved(name) || catalog.Dialect == "postgres" && name != strings.ToLower(name)) {
			insert, _ = Identifier(name, catalog.Dialect == "mysql" || catalog.Dialect == "clickhouse" || catalog.Dialect == "")
			if catalog.Dialect == "tsql" {
				insert = "[" + strings.ReplaceAll(name, "]", "]]") + "]"
			}
			if quoted && offset < len(source) && source[offset] == insert[len(insert)-1] {
				insert = insert[:len(insert)-1]
			}
		}
		result.Items = append(result.Items, SQLCompletion{name, insert, detail, kind})
	}
	if len(qualifiers) > 0 {
		// An incomplete FROM schema. is a namespace prefix, even if the
		// relation parser temporarily treats schema as a table/alias.
		if len(qualifiers) == 1 && len(before) >= 3 {
			context := strings.ToUpper(before[len(before)-3].text)
			if context == "FROM" || context == "JOIN" || context == "UPDATE" || context == "INTO" || context == "TABLE" {
				for _, o := range catalog.Objects {
					if strings.EqualFold(o.Namespace, qualifiers[0]) {
						add(o.Name, o.Namespace, "table")
					}
				}
				if len(result.Items) > 0 {
					return finishCompletion(result)
				}
			}
		}
		var relations []completionRelation
		if len(qualifiers) == 1 {
			for s := scope; s != nil; s = s.parent {
				for _, r := range s.relations {
					if offset >= r.start && offset <= r.end && strings.EqualFold(r.alias, qualifiers[0]) {
						relations = append(relations, r)
					}
				}
				if len(relations) > 0 {
					break // Inner aliases shadow correlated parent names.
				}
			}
		}
		if len(relations) == 0 {
			name := qualifiers[len(qualifiers)-1]
			namespace := catalog.Namespace
			if len(qualifiers) == 2 {
				namespace = qualifiers[0]
			}
			if len(qualifiers) <= 2 {
				for _, o := range catalog.Objects {
					if strings.EqualFold(o.Name, name) && strings.EqualFold(o.Namespace, namespace) {
						relations = append(relations, completionRelation{columns: o.Columns, namespace: namespace, name: name})
					}
				}
			}
			if len(qualifiers) == 1 && len(relations) == 0 {
				for _, o := range catalog.Objects {
					if strings.EqualFold(o.Namespace, name) {
						add(o.Name, o.Namespace, "table")
					}
				}
			}
		}
		if len(relations) == 1 {
			for _, name := range relations[0].columns {
				add(name, strings.Trim(relations[0].namespace+"."+relations[0].name, "."), "column")
			}
		}
	} else {
		context := ""
		if len(before) > 0 && before[len(before)-1].start >= start {
			context = strings.ToUpper(before[len(before)-1].text)
		}
		tablesOnly := context == "FROM" || context == "JOIN" || context == "UPDATE" || context == "INTO" || context == "TABLE"
		if !tablesOnly {
			shadowed := map[string]bool{}
			for s := scope; s != nil; s = s.parent {
				for _, r := range s.relations {
					if offset < r.start || offset > r.end {
						continue
					}
					if shadowed[strings.ToLower(r.alias)] {
						continue
					}
					for _, name := range r.columns {
						add(name, r.alias, "column")
					}
					add(r.alias, r.name, "alias")
					shadowed[strings.ToLower(r.alias)] = true
				}
			}
		}
		for _, o := range catalog.Objects {
			if strings.EqualFold(o.Namespace, catalog.Namespace) {
				add(o.Name, o.Namespace, "table")
			}
		}
		for s := scope; s != nil; s = s.parent {
			for name := range s.ctes {
				add(name, "CTE", "table")
			}
		}
		for _, name := range catalog.Namespaces {
			add(name, "", "namespace")
		}
		if !tablesOnly && !quoted {
			for _, word := range completionKeywords(catalog.Dialect) {
				add(word, "", "keyword")
			}
		}
	}
	return finishCompletion(result)
}

func finishCompletion(result CompletionResult) CompletionResult {
	sort.SliceStable(result.Items, func(i, j int) bool {
		if result.Items[i].Kind != result.Items[j].Kind {
			return completionRank(result.Items[i].Kind) < completionRank(result.Items[j].Kind)
		}
		return strings.ToLower(result.Items[i].Name) < strings.ToLower(result.Items[j].Name)
	})
	if len(result.Items) > 256 {
		result.Items = result.Items[:256]
	}
	return result
}

func (s *completionScope) hasQuery() bool {
	for _, t := range s.tokens {
		if t.word && (strings.EqualFold(t.text, "SELECT") || strings.EqualFold(t.text, "UPDATE") || strings.EqualFold(t.text, "DELETE") || strings.EqualFold(t.text, "INSERT") || strings.EqualFold(t.text, "WITH")) {
			return true
		}
	}
	return false
}
func buildCompletionRelations(s *completionScope, catalog CompletionCatalog) {
	// Explicit CTE column lists are available before inspecting their bodies.
	ts := s.tokens
	if len(ts) > 0 && strings.EqualFold(ts[0].text, "WITH") {
		for i := 1; i < len(ts); {
			if strings.EqualFold(ts[i].text, "RECURSIVE") {
				i++
				continue
			}
			if !ts[i].ident {
				break
			}
			name := completionName(ts[i].text)
			i++
			var columns []string
			if i < len(ts) && ts[i].text == "(" {
				if child := s.children[ts[i].start]; child != nil {
					for _, t := range child.tokens {
						if t.ident {
							columns = append(columns, completionName(t.text))
						}
					}
				}
				i++
			}
			if i >= len(ts) || !strings.EqualFold(ts[i].text, "AS") {
				break
			}
			i++
			for i < len(ts) && (strings.EqualFold(ts[i].text, "NOT") || strings.EqualFold(ts[i].text, "MATERIALIZED")) {
				i++
			}
			if i >= len(ts) || ts[i].text != "(" {
				break
			}
			body := s.children[ts[i].start]
			s.ctes[strings.ToLower(name)] = columns
			if body != nil && len(columns) == 0 {
				s.ctes[strings.ToLower(name)] = completionProjection(body)
			}
			i++
			if i >= len(ts) || ts[i].text != "," {
				break
			}
			i++
		}
	}
	from := false
	for i := 0; i < len(ts); i++ {
		word := strings.ToUpper(ts[i].text)
		if word == "WHERE" || word == "GROUP" || word == "ORDER" || word == "HAVING" || word == "LIMIT" || word == "SET" || word == "VALUES" || word == "RETURNING" || word == "UNION" {
			from = false
		}
		introducer := ts[i].word && (word == "FROM" || word == "JOIN" || word == "UPDATE" || word == "INTO")
		if !introducer && !(from && ts[i].text == ",") {
			continue
		}
		from = true
		i++
		for i < len(ts) && ts[i].word && (strings.EqualFold(ts[i].text, "ONLY") || strings.EqualFold(ts[i].text, "LATERAL")) {
			i++
		}
		if i >= len(ts) {
			break
		}
		r := completionRelation{namespace: catalog.Namespace, start: s.start, end: s.end}
		for _, t := range ts {
			if t.word && (strings.EqualFold(t.text, "UNION") || strings.EqualFold(t.text, "EXCEPT") || strings.EqualFold(t.text, "INTERSECT")) {
				if t.end < ts[i].start {
					r.start = t.end
				} else if t.start > ts[i].end {
					r.end = t.start
					break
				}
			}
		}
		if ts[i].text == "(" {
			if child := s.children[ts[i].start]; child != nil {
				r.columns = completionProjection(child)
			}
		} else if ts[i].ident && (!ts[i].word || !completionReserved(ts[i].text)) {
			r.name = completionName(ts[i].text)
			qualified := i+2 < len(ts) && ts[i+1].text == "." && ts[i+2].ident
			if qualified {
				r.namespace, r.name = r.name, completionName(ts[i+2].text)
				i += 2
			}
			// A table-valued function has unknown output; retain its alias only.
			function := i+1 < len(ts) && ts[i+1].text == "("
			if function {
				i++
			} else {
				cte, found := s.cte(r.name)
				if found && !qualified {
					r.columns = cte
				} else {
					var matches []CompletionObject
					for _, o := range catalog.Objects {
						if strings.EqualFold(o.Name, r.name) && strings.EqualFold(o.Namespace, r.namespace) {
							matches = append(matches, o)
						}
					}
					if len(matches) == 1 {
						r.columns = matches[0].Columns
					}
				}
			}
		} else {
			continue
		}
		r.alias = r.name
		if i+1 < len(ts) && strings.EqualFold(ts[i+1].text, "AS") {
			i++
		}
		if i+1 < len(ts) && ts[i+1].ident && (!ts[i+1].word || !completionReserved(ts[i+1].text)) {
			i++
			r.alias = completionName(ts[i].text)
		}
		if r.alias != "" {
			s.relations = append(s.relations, r)
		}
	}
	for _, child := range s.children {
		buildCompletionRelations(child, catalog)
	}
}
func (s *completionScope) cte(name string) ([]string, bool) {
	for scope := s; scope != nil; scope = scope.parent {
		if columns, ok := scope.ctes[strings.ToLower(name)]; ok {
			return columns, true
		}
	}
	return nil, false
}
func completionProjection(s *completionScope) []string {
	var columns []string
	selecting := false
	var field []completionToken
	finish := func() {
		if len(field) == 0 {
			return
		}
		last := field[len(field)-1]
		// Simple references and explicit/trailing aliases have known names.
		simple := len(field)%2 == 1
		for i, t := range field {
			if i%2 == 0 && !t.ident || i%2 == 1 && t.text != "." {
				simple = false
			}
		}
		alias := len(field) > 1 && (strings.EqualFold(field[len(field)-2].text, "AS") || field[len(field)-2].end < last.start && field[len(field)-2].text != "+" && field[len(field)-2].text != "-" && field[len(field)-2].text != "*" && field[len(field)-2].text != "/" && field[len(field)-2].text != "(" && !completionReserved(last.text))
		if last.ident && (simple || alias) {
			columns = append(columns, completionName(last.text))
		}
		field = nil
	}
	for _, t := range s.tokens {
		if t.word && strings.EqualFold(t.text, "SELECT") {
			selecting = true
			continue
		}
		if !selecting {
			continue
		}
		if t.word && (strings.EqualFold(t.text, "FROM") || strings.EqualFold(t.text, "UNION") || strings.EqualFold(t.text, "EXCEPT") || strings.EqualFold(t.text, "INTERSECT")) {
			break
		}
		if t.text == "," {
			finish()
		} else {
			field = append(field, t)
		}
	}
	finish()
	return columns
}
func completionName(raw string) string {
	if raw == "" {
		return ""
	}
	q, end := raw[0], byte(0)
	if q == '`' || q == '"' {
		end = q
	} else if q == '[' {
		end = ']'
	}
	if end == 0 {
		return raw
	}
	raw = raw[1:]
	if len(raw) > 0 && raw[len(raw)-1] == end {
		raw = raw[:len(raw)-1]
	}
	return strings.ReplaceAll(raw, string([]byte{end, end}), string(end))
}
func completionBareName(name string) bool {
	for i, r := range name {
		if !unicode.IsLetter(r) && r != '_' && !(i > 0 && unicode.IsDigit(r)) {
			return false
		}
	}
	return name != ""
}
func completionRank(kind string) int {
	switch kind {
	case "column":
		return 0
	case "alias":
		return 1
	case "table":
		return 2
	case "namespace":
		return 3
	default:
		return 4
	}
}
func completionKeywords(dialect string) []string {
	base := "SELECT FROM WHERE ORDER BY GROUP HAVING LIMIT OFFSET INSERT INTO VALUES UPDATE SET DELETE JOIN LEFT RIGHT INNER OUTER CROSS ON USING NULL IS AND OR NOT AS DISTINCT EXPLAIN CREATE ALTER DROP TABLE VIEW INDEX PRIMARY KEY FOREIGN REFERENCES UNIQUE DEFAULT CHECK CONSTRAINT BEGIN END WHEN THEN ELSE CASE UNION ALL ASC DESC EXISTS WITH RECURSIVE OVER PARTITION ROWS RANGE BETWEEN EXCEPT INTERSECT"
	switch dialect {
	case "duckdb":
		base += " QUALIFY UNNEST PIVOT UNPIVOT SAMPLE EXCLUDE REPLACE ATTACH DETACH COPY EXPORT IMPORT DATABASE SCHEMA MACRO STRUCT MAP LIST HUGEINT UHUGEINT UUID BLOB JSON READ_CSV READ_PARQUET READ_JSON"
	case "tsql":
		base = "SELECT FROM WHERE ORDER BY GROUP HAVING OFFSET FETCH NEXT ROWS ONLY TOP INSERT INTO VALUES UPDATE SET DELETE JOIN LEFT RIGHT INNER OUTER CROSS ON NULL IS AND OR NOT AS DISTINCT CREATE ALTER DROP TABLE VIEW INDEX PRIMARY KEY FOREIGN REFERENCES UNIQUE DEFAULT CHECK CONSTRAINT BEGIN END WHEN THEN ELSE CASE UNION ALL ASC DESC EXISTS WITH OVER PARTITION ROWS RANGE BETWEEN EXCEPT INTERSECT DECLARE EXEC EXECUTE PROCEDURE FUNCTION OUTPUT IDENTITY TRY CATCH THROW TRANSACTION COMMIT ROLLBACK USE GO NVARCHAR VARBINARY DATETIME2 DATETIMEOFFSET UNIQUEIDENTIFIER"
	case "postgres":
		base += " RETURNING ILIKE LATERAL FILTER CONFLICT MATERIALIZED JSONB SERIAL BIGSERIAL"
	case "sqlite":
		base += " RETURNING PRAGMA ATTACH DETACH REPLACE CONFLICT VACUUM WITHOUT ROWID"
	default:
		base += " SHOW DESCRIBE DATABASES SCHEMAS DUPLICATE AUTO_INCREMENT ENGINE CHARACTER COLLATE STRAIGHT_JOIN REGEXP MATCH AGAINST"
	}
	return strings.Fields(base)
}
func completionReserved(name string) bool {
	return completionReservedWords[strings.ToUpper(name)]
}

var completionReservedWords = func() map[string]bool {
	words := map[string]bool{}
	for _, dialect := range []string{"mysql", "postgres", "sqlite", "tsql"} {
		for _, word := range completionKeywords(dialect) {
			words[word] = true
		}
	}
	return words
}()

// Unlike the strict DDL lexer, this lexer retains unfinished tokens. Protected
// spans prevent suggestions in comments, strings and PostgreSQL dollar bodies.
func completionTokens(source, dialect string) []completionToken {
	var out []completionToken
	mysql := dialect == "mysql" || dialect == "clickhouse" || dialect == ""
	for i := 0; i < len(source); {
		if source[i] <= ' ' {
			i++
			continue
		}
		t := completionToken{start: i}
		switch {
		case strings.HasPrefix(source[i:], "/*"):
			t.protected = true
			i += 2
			depth := 1
			for i < len(source) && depth > 0 {
				if strings.HasPrefix(source[i:], "/*") && !mysql {
					depth++
					i += 2
				} else if strings.HasPrefix(source[i:], "*/") {
					depth--
					i += 2
				} else {
					i++
				}
			}
		case strings.HasPrefix(source[i:], "--") && (!mysql || dialect == "clickhouse" || i+2 == len(source) || source[i+2] <= ' ') || mysql && source[i] == '#':
			t.protected = true
			for i < len(source) && source[i] != '\n' {
				i++
			}
		case source[i] == '\'' || source[i] == '"' || source[i] == '`' || dialect == "sqlite" && source[i] == '[':
			quote := source[i]
			end := quote
			if quote == '[' {
				end = ']'
			}
			t.ident = quote == '`' || quote == '[' || quote == '"' && (!mysql || dialect == "clickhouse")
			t.protected = !t.ident
			escape := mysql || quote == '\'' && i > 0 && (source[i-1] == 'E' || source[i-1] == 'e')
			i++
			for i < len(source) {
				if escape && source[i] == '\\' && i+1 < len(source) {
					i += 2
				} else if source[i] == end {
					i++
					if i >= len(source) || source[i] != end {
						break
					}
					i++
				} else {
					i++
				}
			}
		default:
			if source[i] == '$' && (dialect == "postgres" || dialect == "clickhouse") {
				j := i + 1
				for j < len(source) && (source[j] == '_' || source[j] >= 'a' && source[j] <= 'z' || source[j] >= 'A' && source[j] <= 'Z' || j > i+1 && source[j] >= '0' && source[j] <= '9') {
					j++
				}
				if j < len(source) && source[j] == '$' {
					tag := source[i : j+1]
					t.protected = true
					if n := strings.Index(source[j+1:], tag); n >= 0 {
						i = j + 1 + n + len(tag)
					} else {
						i = len(source)
					}
				}
			}
			if i == t.start {
				r, n := utf8.DecodeRuneInString(source[i:])
				if unicode.IsLetter(r) || r == '_' {
					t.ident, t.word = true, true
					i += n
					for i < len(source) {
						r, n = utf8.DecodeRuneInString(source[i:])
						if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && !(r == '$' && dialect == "postgres") {
							break
						}
						i += n
					}
				} else {
					i += n
				}
			}
		}
		t.end, t.text = i, source[t.start:i]
		out = append(out, t)
	}
	return out
}
