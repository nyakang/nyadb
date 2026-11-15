package schemaddl

// SQLiteRowIDColumn identifies an INTEGER PRIMARY KEY alias from the complete
// declaration. Inline PRIMARY KEY DESC is SQLite's historical exception;
// table-level PRIMARY KEY(column DESC) still aliases the physical rowid.
func SQLiteRowIDColumn(t Table) string {
	if SQLiteWithoutRowID(t) {
		return ""
	}
	integer := map[string]string{}
	var keys []string
	for _, c := range t.Clauses {
		ts, err := lex(c.SQL)
		if err != nil {
			return ""
		}
		if c.Kind == "column" && len(ts) > 0 && ts[0].word("INTEGER") && (len(ts) == 1 || fieldAttribute(ts[1])) {
			integer[asciiLower(c.Name)] = c.Name
		}
		depth := 0
		for i, x := range ts {
			if x.text == "(" && x.kind == 'p' {
				depth++
			}
			if x.text == ")" && x.kind == 'p' {
				depth--
			}
			if depth != 0 || !x.word("PRIMARY") || i+1 >= len(ts) || !ts[i+1].word("KEY") {
				continue
			}
			if c.Kind == "column" {
				if i+2 < len(ts) && ts[i+2].word("DESC") {
					return ""
				}
				keys = append(keys, c.Name)
			} else {
				if i+3 >= len(ts) || ts[i+2].text != "(" || !identifierToken(ts[i+3]) {
					return ""
				}
				for _, term := range ts[i+4:] {
					if term.text == "," {
						return ""
					}
					if term.text == ")" {
						break
					}
				}
				keys = append(keys, ts[i+3].text)
			}
		}
	}
	if len(keys) != 1 {
		return ""
	}
	return integer[asciiLower(keys[0])]
}
