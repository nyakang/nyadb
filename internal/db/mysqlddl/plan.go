package mysqlddl

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/nyakang/nyadb/internal/db/sqltext"
)

type Draft struct {
	Table    Table
	Triggers []Trigger
	New      bool
}

func (d Draft) Clone() Draft {
	d.Table = d.Table.Clone()
	d.Triggers = append([]Trigger(nil), d.Triggers...)
	return d
}

type Plan struct {
	Original    Snapshot
	Desired     Draft
	Fingerprint string
	Statements  []string
}

func Build(original Snapshot, draft Draft) (Plan, error) {
	p := Plan{Original: original, Desired: draft.Clone(), Fingerprint: original.Fingerprint()}
	if draft.Table.Database != original.Database || draft.Table.Name != original.Table {
		return p, errors.New("draft changed the original object identity")
	}
	if e := Validate(draft.Table); e != nil {
		return p, e
	}
	qualified := func(db, name string) string {
		a, _ := sqltext.Identifier(db, true)
		b, _ := sqltext.Identifier(name, true)
		return a + "." + b
	}
	target := qualified(original.Database, original.Table)
	if draft.New {
		p.Statements = append(p.Statements, draft.Table.DDL())
	} else {
		base, e := Parse(original.Database, original.Table, original.DDL)
		if e != nil {
			return p, e
		}
		old, now := map[string]Clause{}, map[string]Clause{}
		oldPositions, positions := map[string]int{}, map[string]int{}
		n := 0
		for _, c := range base.Clauses {
			old[c.ID] = c
			if c.Kind == Column {
				oldPositions[c.ID] = n
				n++
			}
		}
		n = 0
		for _, c := range draft.Table.Clauses {
			if _, exists := now[c.ID]; exists {
				return p, errors.New("duplicate draft identity")
			}
			now[c.ID] = c
			if c.Kind == Column {
				positions[c.ID] = n
				n++
			}
		}
		var drops, fields, adds []string
		for _, c := range base.Clauses {
			v, exists := now[c.ID]
			if c.Kind == Unknown {
				if !exists || v.SQL != c.SQL {
					return p, errors.New("unrecognized table clauses must remain verbatim")
				}
				continue
			}
			if c.Kind == Column {
				if !exists || v.Name != c.Name {
					unknown := original.TriggersUnknown || strings.Contains(strings.ToUpper(base.Options), "PARTITION")
					for _, clause := range base.Clauses {
						if clause.Kind == Unknown {
							unknown = true
						}
					}
					if unknown {
						return p, errors.New("field removal or rename is blocked by an unrecognized dependency or partition definition")
					}
				}
				f, _ := ParseColumn(c.SQL)
				if f.Unsupported && (!exists || v.SQL != c.SQL || positions[c.ID] != oldPositions[c.ID]) {
					return p, errors.New("unsupported field cannot be changed or reordered")
				}
				if !exists {
					name, _ := sqltext.Identifier(c.Name, true)
					fields = append(fields, "DROP COLUMN "+name)
				}
				continue
			}
			if !exists || Canonical(c.SQL) != Canonical(v.SQL) {
				name, _ := sqltext.Identifier(c.Name, true)
				switch c.Kind {
				case Primary:
					drops = append(drops, "DROP PRIMARY KEY")
				case Index:
					drops = append(drops, "DROP INDEX "+name)
				case Foreign:
					drops = append(drops, "DROP FOREIGN KEY "+name)
				case Check:
					drops = append(drops, "DROP CHECK "+name)
				}
			}
		}
		previous := ""
		for _, c := range draft.Table.Clauses {
			v, exists := old[c.ID]
			if c.Kind == Unknown {
				if !exists || c.SQL != v.SQL {
					return p, errors.New("new unrecognized clauses cannot be planned")
				}
				continue
			}
			if c.Kind != Column {
				if !exists || Canonical(v.SQL) != Canonical(c.SQL) {
					adds = append(adds, "ADD "+c.SQL)
				}
				continue
			}
			position := " FIRST"
			if previous != "" {
				name, _ := sqltext.Identifier(previous, true)
				position = " AFTER " + name
			}
			previous = c.Name
			if !exists {
				fields = append(fields, "ADD COLUMN "+c.SQL+position)
			} else if Canonical(v.SQL) != Canonical(c.SQL) || oldPositions[c.ID] != positions[c.ID] {
				name, _ := sqltext.Identifier(v.Name, true)
				fields = append(fields, "CHANGE COLUMN "+name+" "+c.SQL+position)
			}
		}
		// Raw partitioning and table options stay unchanged unless the limited
		// option editor made a surgical replacement of supported properties.
		optionSQL, e := optionDiff(base.Options, draft.Table.Options)
		if e != nil {
			return p, e
		}
		changes := append(drops, fields...)
		changes = append(changes, adds...)
		if optionSQL != "" {
			changes = append(changes, optionSQL)
		}
		if len(changes) > 0 {
			p.Statements = append(p.Statements, "ALTER TABLE "+target+" "+strings.Join(changes, ",\n  "))
		}
	}
	oldTriggers := map[string]Trigger{}
	if original.TriggersUnknown {
		if len(draft.Triggers) != len(original.Triggers) {
			return p, errors.New("trigger metadata is incomplete; trigger edits are disabled")
		}
		for i, t := range draft.Triggers {
			if t != original.Triggers[i] {
				return p, errors.New("trigger metadata is incomplete; trigger edits are disabled")
			}
		}
	}
	for _, t := range original.Triggers {
		oldTriggers[t.Name] = t
	}
	if !triggerOrderPreservable(original.Triggers, draft.Triggers) {
		return p, errors.New("trigger modification would change execution order; unsupported reordering is blocked before execution, draft retained")
	}
	seen := map[string]bool{}
	for _, t := range draft.Triggers {
		if seen[t.Name] {
			return p, errors.New("duplicate trigger name")
		}
		seen[t.Name] = true
		if e := ValidateTrigger(t, draft.Table.Database, draft.Table.Name); e != nil {
			return p, e
		}
		if old, ok := oldTriggers[t.Name]; ok && old == t {
			continue
		}
		if _, ok := oldTriggers[t.Name]; ok {
			p.Statements = append(p.Statements, "DROP TRIGGER "+qualified(original.Database, t.Name))
		}
		statement, e := qualifyTrigger(t, draft.Table.Database, draft.Table.Name)
		if e != nil {
			return p, e
		}
		if t.SQLMode != "" && t.SQLMode != original.SQLMode {
			p.Statements = append(p.Statements, "SET SESSION sql_mode="+Literal(t.SQLMode))
		}
		p.Statements = append(p.Statements, statement)
		if t.SQLMode != "" && t.SQLMode != original.SQLMode {
			p.Statements = append(p.Statements, "SET SESSION sql_mode="+Literal(original.SQLMode))
		}
	}
	for _, t := range original.Triggers {
		if !seen[t.Name] {
			p.Statements = append(p.Statements, "DROP TRIGGER "+qualified(original.Database, t.Name))
		}
	}
	return p, nil
}
func ValidateTrigger(t Trigger, database, table string) error {
	if e := sqltext.SingleDefinition(t.SQL); e != nil {
		return e
	}
	if !sqltext.ValidName(t.Name) {
		return errors.New("trigger name is required")
	}
	ts, e := significant(t.SQL)
	if e != nil {
		return e
	}
	if len(ts) < 8 || !sqltext.Word(ts[0], "CREATE") {
		return errors.New("complete CREATE TRIGGER definition is required")
	}
	for i := 1; i < len(ts)-1; i++ {
		if sqltext.Word(ts[i], "TRIGGER") {
			i++
			name := Name(ts[i])
			if i+2 < len(ts) && ts[i+1].Text == "." {
				if name != database {
					return errors.New("trigger schema does not match")
				}
				i += 2
				name = Name(ts[i])
			}
			if name != t.Name {
				return errors.New("trigger name does not match draft")
			}
			for i++; i < len(ts)-2; i++ {
				if sqltext.Word(ts[i], "ON") {
					i++
					name = Name(ts[i])
					if i+2 < len(ts) && ts[i+1].Text == "." {
						if name != database {
							return errors.New("trigger table schema does not match")
						}
						i += 2
						name = Name(ts[i])
					}
					if name != table {
						return errors.New("trigger table does not match draft")
					}
					return nil
				}
			}
			return errors.New("trigger target is missing")
		}
	}
	return errors.New("CREATE TRIGGER is missing")
}

// Verify compares parsed field properties and full constraint definitions.
// A canonicalization mismatch is reported for inspection, never replayed.
func Verify(desired Draft, actual Snapshot) error {
	t, e := Parse(actual.Database, actual.Table, actual.DDL)
	if e != nil {
		return e
	}
	clauses := map[string]Clause{}
	for _, c := range t.Clauses {
		clauses[string(c.Kind)+":"+c.Name] = c
	}
	if len(t.Clauses) != len(desired.Table.Clauses) {
		return errors.New("reloaded clause count differs from requested design")
	}
	var wantOrder, gotOrder []string
	for _, c := range desired.Table.Clauses {
		if c.Kind == Column {
			wantOrder = append(wantOrder, c.Name)
		}
	}
	for _, c := range t.Clauses {
		if c.Kind == Column {
			gotOrder = append(gotOrder, c.Name)
		}
	}
	if strings.Join(wantOrder, "\x00") != strings.Join(gotOrder, "\x00") {
		return errors.New("reloaded field order differs from requested design")
	}
	for _, want := range desired.Table.Clauses {
		got, ok := clauses[string(want.Kind)+":"+want.Name]
		if !ok {
			return fmt.Errorf("reloaded clause missing: %s", want.Name)
		}
		if Canonical(got.SQL) == Canonical(want.SQL) {
			continue
		}
		if want.Kind == Column {
			a, e1 := ParseColumn(want.SQL)
			b, e2 := ParseColumn(got.SQL)
			if e1 == nil && e2 == nil && !a.Unsupported && !b.Unsupported {
				if a.Nullable && a.DefaultKind == NoDefault && b.DefaultKind == NullDefault {
					a.DefaultKind = NullDefault
				}
				if Canonical(a.Type) == Canonical(b.Type) && a.Nullable == b.Nullable && a.DefaultKind == b.DefaultKind && defaultsEqual(a, b) && Canonical(a.Generation) == Canonical(b.Generation) && a.Storage == b.Storage && strings.EqualFold(a.Charset, b.Charset) && strings.EqualFold(a.Collation, b.Collation) && a.Comment == b.Comment && a.AutoIncrement == b.AutoIncrement && Canonical(a.Extras) == Canonical(b.Extras) {
					continue
				}
			}
		}
		return fmt.Errorf("reloaded definition differs for %s; inspect server normalization before further changes", want.Name)
	}
	if e = verifyOptions(desired.Table.Options, t.Options, desired.New); e != nil {
		return e
	}
	if !triggerOrderEquivalent(desired.Triggers, actual.Triggers) {
		return errors.New("reloaded trigger execution order differs; inspect before further changes")
	}
	if len(desired.Triggers) != len(actual.Triggers) {
		return errors.New("reloaded trigger count differs")
	}
	for _, want := range desired.Triggers {
		found := false
		for _, got := range actual.Triggers {
			if want.Name == got.Name && triggerEquivalent(want, got, actual.Database, actual.Table) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("reloaded trigger differs: %s", want.Name)
		}
	}
	return nil
}

func defaultsEqual(a, b Field) bool {
	if a.DefaultKind == LiteralDefault || b.DefaultKind == LiteralDefault {
		typ := strings.ToUpper(a.Type)
		if strings.Contains(typ, "INT") || strings.HasPrefix(typ, "DECIMAL") || strings.HasPrefix(typ, "NUMERIC") || strings.HasPrefix(typ, "FLOAT") || strings.HasPrefix(typ, "DOUBLE") {
			x, ok1 := new(big.Rat).SetString(a.Default)
			y, ok2 := new(big.Rat).SetString(b.Default)
			if ok1 && ok2 {
				return x.Cmp(y) == 0
			}
		}
		return a.Default == b.Default
	}
	return Canonical(a.Default) == Canonical(b.Default)
}
