package schemaddl

import (
	"encoding/json"
	"errors"
)

// Native ALTER TYPE rejects a field while a retained stored generated field
// depends on it. Use captured native dependencies rather than guessing from
// identifier text in the expression. Explicitly removed generated fields do
// not block the independently verified input conversion.
func gaussDesignCheckGeneratedTypeChanges(original Snapshot, desired Table) error {
	retained, changed := map[int]bool{}, map[int]bool{}
	for _, next := range desired.Columns() {
		for _, old := range original.Table.Columns() {
			if old.ID != next.ID || old.OpenGauss == nil {
				continue
			}
			retained[old.OpenGauss.Number] = true
			a, err := ParseOpenGaussField(old.SQL)
			if err != nil {
				return err
			}
			b, err := ParseOpenGaussField(next.SQL)
			if err != nil {
				return err
			}
			changed[old.OpenGauss.Number] = a.Type != b.Type || a.Collation != b.Collation
		}
	}
	generated, generatedColumns := map[uint64]bool{}, map[int]bool{}
	for _, entry := range original.OpenGauss.Entries {
		if entry.Kind != "default" {
			continue
		}
		var row struct {
			Relation   uint64 `json:"adrelid"`
			Number     int    `json:"adnum"`
			Generation string `json:"adgencol"`
		}
		if json.Unmarshal(entry.Data, &row) != nil {
			return ErrConflict
		}
		if row.Relation == original.OpenGauss.RelationOID && row.Generation == "s" && retained[row.Number] {
			generated[entry.OID] = true
			generatedColumns[row.Number] = true
		}
	}
	for _, entry := range original.OpenGauss.Entries {
		if entry.Kind != "dependencies" || entry.OID != original.OpenGauss.RelationOID {
			continue
		}
		var aggregate struct {
			Rows []struct {
				Class          uint64 `json:"classid"`
				Object         uint64 `json:"objid"`
				ObjectColumn   int    `json:"objsubid"`
				ReferenceClass uint64 `json:"refclassid"`
				Reference      uint64 `json:"refobjid"`
				Column         int    `json:"refobjsubid"`
			}
		}
		if json.Unmarshal(entry.Data, &aggregate) != nil {
			return ErrConflict
		}
		for _, row := range aggregate.Rows {
			// Native generated dependencies connect pg_class attribute slots;
			// the pg_attrdef record separately depends on its owning slot.
			dependsOnInput := row.Class == original.OpenGauss.Classes["pg_attrdef"] && generated[row.Object] || row.Class == original.OpenGauss.Classes["pg_class"] && row.Object == original.OpenGauss.RelationOID && generatedColumns[row.ObjectColumn]
			if dependsOnInput && row.ReferenceClass == original.OpenGauss.Classes["pg_class"] && row.Reference == original.OpenGauss.RelationOID && changed[row.Column] {
				return errors.New("native type change requires separately removing its dependent stored generated field")
			}
		}
	}
	return nil
}
