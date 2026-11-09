package graphschema

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// PreflightMatches compares only the affected complete native observations.
// A changed observed ID rejects dispatch, but an equal ID is not incarnation/CAS.
func PreflightMatches(before, actual contract.GraphSchemaPreview) bool {
	if before.Request.Database != actual.Request.Database || len(before.Before) != len(actual.Before) {
		return false
	}
	for i, d := range before.Before {
		if !DefinitionEqual(d, actual.Before[i]) || d.ID.Kind != actual.Before[i].ID.Kind || d.ID.Text != actual.Before[i].ID.Text {
			return false
		}
	}
	return true
}

// DesiredMatches proves only this explicit read's requested catalog state. It
// cannot prove completion or incarnation and never turns a missing receipt valid.
func DesiredMatches(p contract.GraphSchemaPreview, c contract.GraphSchemaCatalog) (bool, error) {
	ds, e := Definitions(c)
	if e != nil {
		return false, e
	}
	if c.Object.Catalog != p.Request.Database {
		return false, ErrInvalid
	}
	draft := p.Draft
	if draft.Action == "drop_index" || draft.Action == "drop_constraint" {
		for _, before := range p.Before {
			for _, d := range ds {
				if d.Kind == before.Kind && d.Name == before.Name {
					return false, nil
				}
			}
		}
		return true, nil
	}
	kind, nativeType := "index", draft.Type
	if draft.Action == "create_constraint" {
		kind, nativeType = "constraint", "UNIQUENESS"
		if draft.EntityType == "RELATIONSHIP" {
			nativeType = "RELATIONSHIP_UNIQUENESS"
		}
	} else if draft.Action != "create_index" {
		return false, contract.ErrUnsupported
	}
	for _, d := range ds {
		if d.Name != draft.Name || d.Kind != kind {
			continue
		}
		if d.Type != nativeType || !sameTarget(d, draft) {
			return false, nil
		}
		if kind == "index" {
			if d.OwningConstraint.Kind != "null" || d.Provider != defaultProvider(draft.Type) {
				return false, nil
			}
		} else {
			if d.OwnedIndex.Kind != "string" || d.OwnedIndex.Text != draft.Name {
				return false, nil
			}
			backing := false
			for _, index := range ds {
				if index.Kind == "index" && index.Name == d.OwnedIndex.Text && index.Type == "RANGE" && index.Provider == defaultProvider("RANGE") && index.OwningConstraint.Kind == "string" && index.OwningConstraint.Text == draft.Name && sameTarget(index, draft) {
					backing = true
				}
			}
			if !backing {
				return false, nil
			}
		}
		if draft.Options.Kind != "" {
			expected, e := parameter(draft.Options, 0)
			if e != nil {
				return false, e
			}
			actual, e := parameter(d.Options, 0)
			if e != nil {
				return false, e
			}
			// Neo4j 5.26 emits the accepted VECTOR enum in uppercase.
			if draft.Type == "VECTOR" {
				if opts, ok := expected.(map[string]any); ok {
					if cfg, ok := opts["indexConfig"].(map[string]any); ok {
						if value, ok := cfg["vector.similarity_function"].(string); ok {
							cfg["vector.similarity_function"] = strings.ToUpper(value)
						}
					}
				}
			}
			if !subset(expected, actual) {
				return false, nil
			}
		}
		return true, nil
	}
	return false, nil
}
func sameTarget(d contract.GraphSchemaDefinition, draft contract.GraphSchemaDraft) bool {
	return !d.LabelsOrTypesNull && !d.PropertiesNull && d.EntityType == draft.EntityType && slices.Equal(d.LabelsOrTypes, draft.LabelsOrTypes) && slices.Equal(d.Properties, draft.Properties)
}
func defaultProvider(kind string) string {
	switch kind {
	case "RANGE":
		return "range-1.0"
	case "TEXT":
		return "text-2.0"
	case "POINT":
		return "point-1.0"
	case "FULLTEXT":
		return "fulltext-1.0"
	case "VECTOR":
		return "vector-2.0"
	}
	return ""
}
func subset(expected, actual any) bool {
	if m, ok := expected.(map[string]any); ok {
		n, ok := actual.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range m {
			got, exists := n[key]
			if !exists || !subset(value, got) {
				return false
			}
		}
		return true
	}
	a, e := json.Marshal(expected)
	if e != nil {
		return false
	}
	b, e := json.Marshal(actual)
	return e == nil && string(a) == string(b)
}
