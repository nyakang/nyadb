package rqlite

import (
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// Keep every original violation, including repeated NULL rowids from WITHOUT
// ROWID tables. The native transaction checks the same multiset relation as
// DBX's SQLite executor: final violations may only consume original ones.
func schemaRebuildForeignKeyCapture(table, phase string) []string {
	p := schemaTextLiteral(phase)
	return []string{
		"INSERT INTO " + table + "(ok,fk_phase,fk_table_hex,fk_rowid,fk_parent_hex,fk_id) SELECT 1," + p + ",hex(\"table\"),rowid,hex(parent),fkid FROM pragma_foreign_key_check",
		"INSERT INTO " + table + "(ok,fk_phase,fk_count) SELECT 1," + p + ",count(*) FROM " + table + " WHERE fk_phase=" + p + " AND fk_count IS NULL",
	}
}

func schemaRebuildForeignKeyGuard(table string) string {
	return "NOT EXISTS(SELECT 1 FROM " + table + " f WHERE f.fk_phase='after' AND f.fk_count IS NULL GROUP BY f.fk_table_hex,f.fk_rowid,f.fk_parent_hex,f.fk_id HAVING count(*)>(SELECT count(*) FROM " + table + " b WHERE b.fk_phase='before' AND b.fk_count IS NULL AND b.fk_table_hex=f.fk_table_hex COLLATE BINARY AND b.fk_rowid IS f.fk_rowid AND b.fk_parent_hex=f.fk_parent_hex COLLATE BINARY AND b.fk_id=f.fk_id))"
}

func verifySchemaRebuildForeignKeys(native receipt, batch schemaBatch) error {
	columns := []string{"fk_phase", "fk_count", "fk_table_hex", "fk_rowid", "fk_parent_hex", "fk_id"}
	i := batch.rebuildFKFrames
	if i < 0 || i >= len(native.Results) || native.Results[i].failed() || !slices.Equal(native.Results[i].Columns, columns) {
		return errIncomplete
	}
	type violation struct {
		table, parent string
		rowid, id     int64
		nullRowID     bool
	}
	counts, physical := map[string]int64{}, map[string]int64{}
	frames := map[string]map[violation]int64{"before": {}, "after": {}}
	// Deliberately do not use decodedRows' generic metadata row cap. The complete
	// original body remains bounded by the original write transport receipt limit.
	for _, raw := range native.Results[i].Values {
		row, err := native.Results[i].cells(raw)
		if err != nil || len(row) != len(columns) {
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
			count, err := cellInt(row[1])
			if err != nil || count < 0 {
				return errIncomplete
			}
			for _, c := range row[2:] {
				if c.Kind != contract.Null {
					return errIncomplete
				}
			}
			counts[phase] = count
			continue
		}
		name, err := schemaCatalogText(row[2])
		if err != nil || !validName(name) {
			return errIncomplete
		}
		parent, err := schemaCatalogText(row[4])
		if err != nil || !validName(parent) {
			return errIncomplete
		}
		v := violation{table: name, parent: parent, nullRowID: row[3].Kind == contract.Null}
		if !v.nullRowID {
			v.rowid, err = cellInt(row[3])
			if err != nil {
				return errIncomplete
			}
		}
		v.id, err = cellInt(row[5])
		if err != nil || v.id < 0 {
			return errIncomplete
		}
		frames[phase][v]++
		physical[phase]++
	}
	if len(counts) != 2 {
		return errIncomplete
	}
	for phase, capture := range map[string]int{"before": batch.rebuildFKBefore, "after": batch.rebuildFKAfter} {
		if counts[phase] != physical[phase] || capture < 0 || capture >= len(native.Results) || native.Results[capture].failed() {
			return errIncomplete
		}
		affected := native.Results[capture].Affected
		if affected == "" && counts[phase] == 0 {
			continue // Native execute responses omit rows_affected for an empty INSERT.
		}
		n, err := affected.Int64()
		if err != nil || n != counts[phase] {
			return errIncomplete
		}
	}
	for v, count := range frames["after"] {
		if count > frames["before"][v] {
			return errIncomplete
		}
	}
	return nil
}
