package engines

import (
	"encoding/json"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func openGaussDesignNativeColumn(catalog *schemaddl.OpenGaussCatalog, name string) (map[string]any, error) {
	var found map[string]any
	if catalog == nil {
		return nil, schemaddl.ErrConflict
	}
	for _, entry := range catalog.Entries {
		if entry.Kind != "column" || entry.OID != catalog.RelationOID {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(string(entry.Data)))
		decoder.UseNumber()
		var row map[string]any
		if err := decoder.Decode(&row); err != nil {
			return nil, err
		}
		if row["attname"] == name && row["attisdropped"] == false {
			if found != nil {
				return nil, schemaddl.ErrConflict
			}
			found = row
		}
	}
	if found == nil {
		return nil, schemaddl.ErrConflict
	}
	return found, nil
}

// Native ALTER TYPE rebuilds FK identities even for an alias-only spelling
// change. Once independent CREATE proves equal physical descriptors, omit
// only that redundant type/collation change. Every other requested edit stays
// in the plan, and declarations/unknown records are still verified afterward.
func openGaussEffectiveDesignPlan(original contract.SchemaDesignSnapshot, desired schemaddl.Table, baseline openGaussDesignBaseline, allowDrop bool) (schemaddl.Plan, error) {
	effective := desired.Clone()
	if original.Schema.Exists {
		for i, next := range effective.Clauses {
			if next.Kind != "column" {
				continue
			}
			for _, prior := range original.Schema.Table.Columns() {
				if prior.ID != next.ID {
					continue
				}
				a, err := schemaddl.ParseOpenGaussField(prior.SQL)
				if err != nil {
					return schemaddl.Plan{}, err
				}
				b, err := schemaddl.ParseOpenGaussField(next.SQL)
				if err != nil {
					return schemaddl.Plan{}, err
				}
				if a.Type == b.Type && a.Collation == b.Collation {
					continue
				}
				before, err := openGaussDesignNativeColumn(original.Schema.OpenGauss, prior.Name)
				if err != nil {
					return schemaddl.Plan{}, err
				}
				prototype, err := openGaussDesignNativeColumn(baseline.Catalog, next.Name)
				if err != nil {
					return schemaddl.Plan{}, err
				}
				sameType, sameCollation := true, true
				for _, field := range []string{"atttypid", "atttypmod", "attndims", "attcollation"} {
					if before[field] == nil || prototype[field] == nil {
						return schemaddl.Plan{}, schemaddl.ErrConflict
					}
					if schemaddl.Signature(before[field]) != schemaddl.Signature(prototype[field]) {
						if field == "attcollation" {
							sameCollation = false
						} else {
							sameType = false
						}
					}
				}
				if sameType {
					b.Type = a.Type
				}
				if sameCollation {
					b.Collation = a.Collation
				}
				effective.Clauses[i].SQL, err = b.SQL()
				if err != nil {
					return schemaddl.Plan{}, err
				}
			}
		}
	}
	return schemaddl.PlanOpenGauss(original.Schema, effective, original.Target.Object.Schema, allowDrop)
}
