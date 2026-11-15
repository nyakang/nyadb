package rqlite

import (
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

type schemaClearProgram struct {
	sequence                  bool
	beforeGuards, afterGuards []string
}

func buildSchemaClearProgram(original contract.SchemaDesignSnapshot, reset bool) (*schemaClearProgram, schemaddl.Plan, error) {
	p := &schemaClearProgram{}
	for _, e := range original.Schema.SQLite.Entries {
		p.sequence = p.sequence || e.Type == "table" && e.Name == "sqlite_sequence"
	}
	if reset && !schemaddl.SQLiteAutoIncrement(original.Schema.Table) {
		return nil, schemaddl.Plan{}, contract.ErrUnsupported
	}
	plan := schemaddl.Plan{Mode: "clear", SQL: []string{"DELETE FROM main." + schemaddl.Quote(original.Target.Object.Name)}}
	p.afterGuards = append(p.afterGuards, fmt.Sprintf("(SELECT schema_version FROM pragma_schema_version)=%d", original.Schema.SQLite.Version))
	if p.sequence {
		source := schemaTextLiteral(original.Target.Object.Name)
		valid := []string{"(SELECT count(*) FROM main.sqlite_sequence WHERE name=" + source + ")<=1", "NOT EXISTS(SELECT 1 FROM main.sqlite_sequence WHERE name=" + source + " AND typeof(seq)<>'integer')"}
		p.beforeGuards = append(p.beforeGuards, valid...)
		if reset {
			plan.SQL = append(plan.SQL, "DELETE FROM main.sqlite_sequence WHERE name="+source)
			p.afterGuards = append(p.afterGuards, "NOT EXISTS(SELECT 1 FROM main.sqlite_sequence WHERE name="+source+")")
		} else {
			// DELETE programs can legitimately advance other or even their own
			// sequences. Preserve those declared effects without resetting them.
			p.afterGuards = append(p.afterGuards, valid...)
		}
	}
	return p, plan, nil
}

func (p *schemaClearProgram) capture(table, phase string) []string {
	count := "0"
	if p.sequence {
		count = "(SELECT count(*) FROM main.sqlite_sequence)"
	}
	sql := []string{"INSERT INTO " + table + "(ok,identity_phase,identity_count) SELECT 1," + schemaTextLiteral(phase) + "," + count}
	if p.sequence {
		sql = append(sql, "INSERT INTO "+table+"(ok,identity_phase,identity_rowid,identity_name_type,identity_name_hex,identity_value_type,identity_value_hex) SELECT 1,"+schemaTextLiteral(phase)+",rowid,typeof(name),hex("+dataBytes("name")+"),typeof(seq),hex("+dataBytes("seq")+") FROM main.sqlite_sequence ORDER BY rowid")
	}
	return sql
}

// The complete before/after sequence frames come from the same original native
// transaction. Count markers prove that no identity row disappeared from its
// acknowledgement; raw NativeJSON retains every byte and storage type.
func verifySchemaClearIdentities(native receipt, index int) error {
	columns := []string{"identity_phase", "identity_count", "identity_rowid", "identity_name_type", "identity_name_hex", "identity_value_type", "identity_value_hex"}
	if index < 0 || index >= len(native.Results) || native.Results[index].failed() || !slices.Equal(native.Results[index].Columns, columns) {
		return errIncomplete
	}
	counts := map[string]int64{}
	rows := map[string]int64{}
	ids := map[string]map[int64]bool{"before": {}, "after": {}}
	for _, raw := range native.Results[index].Values {
		row, err := native.Results[index].cells(raw)
		if err != nil || len(row) != 7 {
			return errIncomplete
		}
		phase, err := cellText(row[0])
		if err != nil || phase != "before" && phase != "after" {
			return errIncomplete
		}
		if row[1].Kind != contract.Null {
			if _, exists := counts[phase]; exists {
				return errIncomplete
			}
			n, err := cellInt(row[1])
			if err != nil || n < 0 {
				return errIncomplete
			}
			for _, c := range row[2:] {
				if c.Kind != contract.Null {
					return errIncomplete
				}
			}
			counts[phase] = n
			continue
		}
		id, err := cellInt(row[2])
		if err != nil || ids[phase][id] {
			return errIncomplete
		}
		ids[phase][id] = true
		for _, start := range []int{3, 5} {
			kind, err := cellText(row[start])
			if err != nil || kind != "null" && kind != "text" && kind != "blob" && kind != "integer" && kind != "real" {
				return errIncomplete
			}
			value, err := cellText(row[start+1])
			if err != nil {
				return errIncomplete
			}
			if _, err := hex.DecodeString(value); err != nil || kind == "null" && value != "" {
				return errIncomplete
			}
		}
		rows[phase]++
	}
	if len(counts) != 2 || counts["before"] != rows["before"] || counts["after"] != rows["after"] {
		return errIncomplete
	}
	return nil
}
