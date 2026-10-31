package mysqlddl

import (
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"strings"
)

func qualifyTrigger(t Trigger, database, table string) (string, error) {
	if e := ValidateTrigger(t, database, table); e != nil {
		return "", e
	}
	ts, _ := significant(t.SQL)
	db, _ := sqltext.Identifier(database, true)
	var insert []int
	for i := 1; i < len(ts)-1; i++ {
		if sqltext.Word(ts[i], "TRIGGER") {
			i++
			if i+1 < len(ts) && ts[i+1].Text != "." {
				insert = append(insert, ts[i].Start)
			}
			for i++; i < len(ts)-1; i++ {
				if sqltext.Word(ts[i], "ON") {
					i++
					if i+1 < len(ts) && ts[i+1].Text != "." {
						insert = append(insert, ts[i].Start)
					}
					break
				}
			}
			break
		}
	}
	raw := t.SQL
	for i := len(insert) - 1; i >= 0; i-- {
		raw = raw[:insert[i]] + db + "." + raw[insert[i]:]
	}
	return raw, nil
}
func triggerEquivalent(want, got Trigger, database, table string) bool {
	if want.SQLMode != "" && want.SQLMode != got.SQLMode {
		return false
	}
	if ValidateTrigger(want, database, table) != nil || ValidateTrigger(got, database, table) != nil {
		return false
	}
	normalize := func(raw string, includeDefiner bool) string {
		ts, _ := significant(raw)
		start := 0
		var remove [][2]int
		for i := 1; i < len(ts)-1; i++ {
			if sqltext.Word(ts[i], "TRIGGER") {
				if !includeDefiner {
					start = ts[i].Start
				}
				i++
				if i+2 < len(ts) && ts[i+1].Text == "." {
					remove = append(remove, [2]int{ts[i].Start, ts[i+1].End})
					i += 2
				}
				for i++; i < len(ts)-1; i++ {
					if sqltext.Word(ts[i], "ON") {
						i++
						if i+2 < len(ts) && ts[i+1].Text == "." {
							remove = append(remove, [2]int{ts[i].Start, ts[i+1].End})
						}
						break
					}
				}
				break
			}
		}
		for i := len(remove) - 1; i >= 0; i-- {
			raw = raw[:remove[i][0]] + raw[remove[i][1]:]
		}
		return Canonical(raw[start:])
	}
	// The server inserts CURRENT_USER's definer for a newly authored trigger.
	// Existing explicit definers stay part of the comparison.
	ts, _ := significant(want.SQL)
	definer := false
	for _, t := range ts {
		if sqltext.Word(t, "TRIGGER") {
			break
		}
		if sqltext.Word(t, "DEFINER") {
			definer = true
		}
	}
	return normalize(want.SQL, definer) == normalize(got.SQL, definer)
}

func triggerGroup(t Trigger) string {
	ts, _ := significant(t.SQL)
	seen := false
	for i, tok := range ts {
		if sqltext.Word(tok, "TRIGGER") {
			seen = true
		}
		if seen && (sqltext.Word(tok, "BEFORE") || sqltext.Word(tok, "AFTER")) && i+1 < len(ts) {
			return strings.ToUpper(tok.Text) + "/" + strings.ToUpper(ts[i+1].Text)
		}
	}
	return ""
}
func triggerOrderEquivalent(want, got []Trigger) bool {
	group := func(ts []Trigger) map[string][]string {
		m := map[string][]string{}
		for _, t := range ts {
			k := triggerGroup(t)
			m[k] = append(m[k], t.Name)
		}
		return m
	}
	a, b := group(want), group(got)
	if len(a) != len(b) {
		return false
	}
	for k, names := range a {
		if strings.Join(names, "\x00") != strings.Join(b[k], "\x00") {
			return false
		}
	}
	return true
}

// triggerOrderPreservable simulates the server's default append-on-CREATE
// behavior. Unsupported reordering is rejected before a preview can execute.
func triggerOrderPreservable(original, desired []Trigger) bool {
	actual := append([]Trigger(nil), original...)
	byname := map[string]Trigger{}
	wanted := map[string]bool{}
	for _, t := range original {
		byname[t.Name] = t
	}
	for _, t := range desired {
		wanted[t.Name] = true
		if old, ok := byname[t.Name]; ok && old == t {
			continue
		}
		kept := actual[:0]
		for _, old := range actual {
			if old.Name != t.Name {
				kept = append(kept, old)
			}
		}
		actual = append(kept, t)
	}
	kept := actual[:0]
	for _, t := range actual {
		if wanted[t.Name] {
			kept = append(kept, t)
		}
	}
	return triggerOrderEquivalent(desired, kept)
}
