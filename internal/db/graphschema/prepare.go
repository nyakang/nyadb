package graphschema

import (
	"encoding/json"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func Prepare(c contract.GraphSchemaCatalog, draft contract.GraphSchemaDraft) (contract.GraphSchemaPreview, error) {
	definitions, e := Definitions(c)
	if e != nil {
		return contract.GraphSchemaPreview{}, e
	}
	if len(draft.LabelsOrTypes) > 16 || len(draft.Properties) > 16 {
		return contract.GraphSchemaPreview{}, ErrBounds
	}
	b := budget{remaining: 1 << 20}
	if e := b.value(draft.Options, 0); e != nil {
		return contract.GraphSchemaPreview{}, e
	}
	if draft.Options.Kind == "" && (draft.Options.Text != "" || len(draft.Options.Values) != 0 || len(draft.Options.Properties) != 0 || draft.Options.Node != nil || draft.Options.Relationship != nil || draft.Options.Path != nil) {
		return contract.GraphSchemaPreview{}, ErrInvalid
	}
	if _, e := parameter(draft.Options, 0); draft.Options.Kind != "" && e != nil {
		return contract.GraphSchemaPreview{}, e
	}
	raw, e := json.Marshal(draft)
	if e != nil {
		return contract.GraphSchemaPreview{}, ErrInvalid
	}
	if len(raw) > 1<<20 {
		return contract.GraphSchemaPreview{}, ErrBounds
	}
	name, e := identifier(draft.Name)
	if e != nil {
		return contract.GraphSchemaPreview{}, e
	}
	p := contract.GraphSchemaPreview{Draft: draft.Clone(), Request: contract.GraphSchemaRequest{Database: c.Object.Catalog}}
	switch draft.Action {
	case "drop_index", "drop_constraint":
		if draft.Type != "" || draft.EntityType != "" || len(draft.LabelsOrTypes) != 0 || len(draft.Properties) != 0 || draft.Options.Kind != "" {
			return p, ErrInvalid
		}
		kind := strings.TrimPrefix(draft.Action, "drop_")
		var found *contract.GraphSchemaDefinition
		for i := range definitions {
			d := &definitions[i]
			if d.Kind == kind && d.Name == draft.Name {
				found = d
				break
			}
		}
		if found == nil {
			return p, contract.ErrGraphSchemaChanged
		}
		if kind == "index" && found.OwningConstraint.Kind != "null" {
			return p, contract.ErrGraphSchemaBackingIndex
		}
		p.Before = append(p.Before, found.Clone())
		if kind == "constraint" && found.OwnedIndex.Kind != "null" {
			matched := false
			for _, d := range definitions {
				if d.Kind == "index" && d.Name == found.OwnedIndex.Text && d.OwningConstraint.Kind == "string" && d.OwningConstraint.Text == found.Name {
					p.Before = append(p.Before, d.Clone())
					matched = true
					break
				}
			}
			if !matched {
				return p, contract.ErrGraphSchemaChanged
			}
		}
		p.NonAtomicDrop = true
		p.Request.Query = "DROP " + strings.ToUpper(kind) + " " + name
		return p, nil
	case "create_index", "create_constraint":
		for _, d := range definitions {
			if d.Name == draft.Name {
				return p, contract.ErrGraphSchemaExists
			}
		}
	default:
		return p, contract.ErrUnsupported
	}
	if draft.EntityType != "NODE" && draft.EntityType != "RELATIONSHIP" || len(draft.LabelsOrTypes) == 0 || len(draft.Properties) == 0 {
		return p, ErrInvalid
	}
	variable := "n"
	if draft.EntityType == "RELATIONSHIP" {
		variable = "r"
	}
	labels, e := identifiers(draft.LabelsOrTypes, "")
	if e != nil {
		return p, e
	}
	properties, e := identifiers(draft.Properties, variable+".")
	if e != nil {
		return p, e
	}
	pattern := "(" + variable + ":" + strings.Join(labels, "|") + ")"
	if draft.EntityType == "RELATIONSHIP" {
		pattern = "()-[" + variable + ":" + strings.Join(labels, "|") + "]-()"
	}
	if draft.Action == "create_constraint" {
		if draft.Type != "UNIQUE" {
			return p, contract.ErrUnsupported
		}
		if len(labels) != 1 || draft.Options.Kind != "" {
			return p, ErrInvalid
		}
		p.Request.Query = "CREATE CONSTRAINT " + name + " FOR " + pattern + " REQUIRE (" + strings.Join(properties, ", ") + ") IS UNIQUE"
		return p, nil
	}
	switch draft.Type {
	case "RANGE":
		if len(labels) != 1 {
			return p, ErrInvalid
		}
	case "TEXT", "POINT", "VECTOR":
		if len(labels) != 1 || len(properties) != 1 {
			return p, ErrInvalid
		}
	case "FULLTEXT":
	default:
		return p, contract.ErrUnsupported
	}
	p.Request.Query = "CREATE " + draft.Type + " INDEX " + name + " FOR " + pattern
	if draft.Type == "FULLTEXT" {
		p.Request.Query += " ON EACH [" + strings.Join(properties, ", ") + "]"
	} else {
		p.Request.Query += " ON (" + strings.Join(properties, ", ") + ")"
	}
	if draft.Options.Kind != "" {
		if e := validateOptions(draft.Type, draft.Options); e != nil {
			return p, e
		}
		p.Request.Query += " OPTIONS $options"
		p.Request.Parameters = []contract.GraphProperty{{Name: "options", Value: draft.Options.Clone()}}
	}
	return p, nil
}

func identifiers(source []string, prefix string) ([]string, error) {
	out := make([]string, len(source))
	seen := map[string]bool{}
	for i, s := range source {
		q, e := identifier(s)
		if e != nil {
			return nil, e
		}
		if seen[s] {
			return nil, ErrInvalid
		}
		seen[s] = true
		out[i] = prefix + q
	}
	return out, nil
}

// Only settings proven on the pinned native fixture are offered. Unknown
// providers, enterprise constraint semantics and arbitrary Cypher are rejected.
func validateOptions(kind string, v contract.GraphValue) error {
	if v.Kind != "map" || len(v.Properties) != 1 || v.Properties[0].Name != "indexConfig" {
		return ErrInvalid
	}
	cfg := v.Properties[0].Value
	if cfg.Kind != "map" || len(cfg.Properties) == 0 {
		return ErrInvalid
	}
	for _, p := range cfg.Properties {
		switch kind {
		case "VECTOR":
			switch p.Name {
			case "vector.dimensions":
				if !positiveDimension(p.Value) {
					return ErrInvalid
				}
			case "vector.similarity_function":
				if p.Value.Kind != "string" || p.Value.Text != "cosine" && p.Value.Text != "euclidean" {
					return ErrInvalid
				}
			default:
				return contract.ErrUnsupported
			}
		case "FULLTEXT":
			switch p.Name {
			case "fulltext.analyzer":
				if p.Value.Kind != "string" || p.Value.Text == "" || len(p.Value.Text) > 256 {
					return ErrInvalid
				}
			case "fulltext.eventually_consistent":
				if p.Value.Kind != "boolean" {
					return ErrInvalid
				}
			default:
				return contract.ErrUnsupported
			}
		default:
			return contract.ErrUnsupported
		}
	}
	return nil
}
