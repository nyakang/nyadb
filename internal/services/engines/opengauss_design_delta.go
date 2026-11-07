package engines

import (
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// Native 6 ATExecAddColumn uses ATT_CMPR_NOCOMPRESS for a delta field and
// stores the added DEFAULT there. Direct CREATE instead copies a descriptor
// without defaults. Derive these ALTER effects from the independently parsed
// main declaration, never from the table being verified.
func gaussDesignAlterDeltaPrototype(probe *schemaddl.OpenGaussDesignGraph, original contract.SchemaDesignSnapshot, desired schemaddl.Table) error {
	if !original.Schema.Exists || probe.Records["relation:@table:reldeltarelid"] == nil {
		return nil
	}
	previous := map[string]schemaddl.Clause{}
	for _, col := range original.Schema.Table.Columns() {
		previous[col.ID] = col
	}
	for _, col := range desired.Columns() {
		old, retained := previous[col.ID]
		before, err := schemaddl.ParseOpenGaussField(old.SQL)
		if retained && err != nil {
			return err
		}
		next, err := schemaddl.ParseOpenGaussField(col.SQL)
		if err != nil {
			return err
		}
		attribute := "@column:" + col.ID
		delta := probe.Records["column:@table:reldeltarelid:"+attribute]
		main := probe.Records["column:@table:"+attribute]
		if delta == nil || main == nil {
			return schemaddl.ErrConflict
		}
		if !retained {
			delta["attcmprmode"] = 0
		}
		if retained && before.Type == next.Type && before.Default == next.Default && before.HasDefault == next.HasDefault {
			continue
		}
		delta["atthasdef"] = main["atthasdef"]
		mainID := "@default:" + attribute
		deltaID := "@default:@table:reldeltarelid:" + attribute
		if row := probe.Records["default:"+mainID]; row != nil {
			copy := gaussDesignCloneRecord(row)
			copy["adrelid"] = "@table:reldeltarelid"
			probe.Records["default:"+deltaID] = copy
			dependencies := probe.Records["dependencies:@table"]
			if dependencies == nil {
				return schemaddl.ErrConflict
			}
			rows, _ := dependencies["rows"].([]any)
			var added []any
			for _, value := range rows {
				x, ok := value.(map[string]any)
				if !ok || x["objid"] != mainID {
					continue
				}
				x = gaussDesignCloneRecord(x)
				x["objid"] = deltaID
				if x["refobjid"] == "@table" {
					x["refobjid"] = "@table:reldeltarelid"
				}
				added = append(added, x)
			}
			dependencies["rows"] = append(rows, added...)
		}
	}
	return nil
}
