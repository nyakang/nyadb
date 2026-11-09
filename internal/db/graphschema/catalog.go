// Package graphschema parses full native catalogs and prepares bounded Neo4j
// DDL without I/O. It never claims an atomic catalog or schema incarnation.
package graphschema

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

var (
	ErrInvalid    = errors.New("invalid graph schema definition or draft")
	ErrIncomplete = errors.New("incomplete original graph schema catalog")
	ErrBounds     = errors.New("graph schema catalog or draft exceeds bounds")
)

const MaxBytes = 16 << 20
const MaxDefinitions = 8192

func nativeString(v contract.GraphValue) (string, error) {
	if v.Kind != "string" {
		return "", ErrIncomplete
	}
	s, e := contract.GraphPropertyScalarValue(v)
	if e != nil {
		return "", ErrIncomplete
	}
	return s.(string), nil
}
func optionalString(v contract.GraphValue) error {
	if v.Kind == "null" {
		if _, e := contract.GraphPropertyScalarValue(v); e == nil {
			return nil
		}
	}
	_, e := nativeString(v)
	return e
}
func stringList(v contract.GraphValue) ([]string, error) {
	if v.Kind != "list" || v.Text != "" || len(v.Properties) != 0 || v.Node != nil || v.Relationship != nil || v.Path != nil || len(v.Values) > 1000 {
		return nil, ErrIncomplete
	}
	out := make([]string, len(v.Values))
	for i, item := range v.Values {
		s, e := nativeString(item)
		if e != nil || s == "" {
			return nil, ErrIncomplete
		}
		out[i] = s
	}
	return out, nil
}

// Definitions requires the full YIELD * catalogs. Added native columns remain
// in the original rows; omitted, duplicate or malformed required fields fail.
func Definitions(c contract.GraphSchemaCatalog) ([]contract.GraphSchemaDefinition, error) {
	if c.Object.ProfileID <= 0 || c.Object.Catalog == "" || c.Object.Schema != "" {
		return nil, ErrInvalid
	}
	if len(c.Indexes.Rows)+len(c.Constraints.Rows) > MaxDefinitions {
		return nil, ErrBounds
	}
	if e := catalogBounds(c); e != nil {
		return nil, e
	}
	raw, e := json.Marshal(c)
	if e != nil {
		return nil, ErrInvalid
	}
	if len(raw) > MaxBytes {
		return nil, ErrBounds
	}
	out := []contract.GraphSchemaDefinition{}
	for _, source := range []struct {
		kind     string
		r        contract.GraphResult
		required []string
	}{
		{"index", c.Indexes, strings.Fields("id name state populationPercent type entityType labelsOrTypes properties indexProvider owningConstraint lastRead readCount trackedSince options failureMessage createStatement")},
		{"constraint", c.Constraints, strings.Fields("id name type entityType labelsOrTypes properties ownedIndex propertyType options createStatement")},
	} {
		r := source.r
		if !r.Attempted || !r.Completed || r.Truncated || r.Uncertain || r.Summary.Database != c.Object.Catalog || r.FailureCode != "" {
			return nil, ErrIncomplete
		}
		columns := map[string]int{}
		for i, name := range r.Columns {
			if _, exists := columns[name]; exists || name == "" {
				return nil, ErrIncomplete
			}
			columns[name] = i
		}
		for _, name := range source.required {
			if _, exists := columns[name]; !exists {
				return nil, fmt.Errorf("%w: missing %s", ErrIncomplete, name)
			}
		}
		seen, ids := map[string]bool{}, map[string]bool{}
		for _, row := range r.Rows {
			if len(row) != len(r.Columns) {
				return nil, ErrIncomplete
			}
			get := func(key string) contract.GraphValue { return row[columns[key]] }
			d := contract.GraphSchemaDefinition{Kind: source.kind, ID: get("id")}
			id, e := contract.GraphPropertyScalarValue(d.ID)
			if e != nil || d.ID.Kind != "integer" || id.(int64) < 0 {
				return nil, ErrIncomplete
			}
			if ids[d.ID.Text] {
				return nil, ErrIncomplete
			}
			ids[d.ID.Text] = true
			for key, dest := range map[string]*string{"name": &d.Name, "type": &d.Type, "entityType": &d.EntityType, "createStatement": &d.CreateStatement} {
				*dest, e = nativeString(get(key))
				if e != nil || *dest == "" {
					return nil, ErrIncomplete
				}
			}
			if seen[d.Name] || (d.EntityType != "NODE" && d.EntityType != "RELATIONSHIP") {
				return nil, ErrIncomplete
			}
			seen[d.Name] = true
			for key, target := range map[string]*[]string{"labelsOrTypes": &d.LabelsOrTypes, "properties": &d.Properties} {
				v := get(key)
				if source.kind == "index" && d.Type == "LOOKUP" && v.Kind == "null" {
					if _, e := contract.GraphPropertyScalarValue(v); e != nil {
						return nil, ErrIncomplete
					}
					if key == "labelsOrTypes" {
						d.LabelsOrTypesNull = true
					} else {
						d.PropertiesNull = true
					}
				} else {
					*target, e = stringList(v)
					if e != nil {
						return nil, e
					}
				}
			}
			if _, e = parameter(get("options"), 0); e != nil || get("options").Kind != "map" {
				return nil, ErrIncomplete
			}
			d.Options = get("options").Clone()
			if source.kind == "index" {
				d.Provider, e = nativeString(get("indexProvider"))
				if e != nil || d.Provider == "" {
					return nil, ErrIncomplete
				}
				d.OwningConstraint = get("owningConstraint").Clone()
				if e = optionalString(d.OwningConstraint); e != nil {
					return nil, e
				}
			} else {
				d.OwnedIndex, d.PropertyType = get("ownedIndex").Clone(), get("propertyType").Clone()
				if e = optionalString(d.OwnedIndex); e != nil {
					return nil, e
				}
				if e = optionalString(d.PropertyType); e != nil {
					return nil, e
				}
			}
			out = append(out, d)
		}
	}
	return out, nil
}

// DefinitionEqual ignores only observed ID and native map ordering. Names,
// definition order, providers, options and backing ownership remain significant.
// Equality never proves original incarnation or execution/commit completion.
func DefinitionEqual(a, b contract.GraphSchemaDefinition) bool {
	a.ID, b.ID = contract.GraphValue{}, contract.GraphValue{}
	aa, e := definitionJSON(a)
	if e != nil {
		return false
	}
	bb, e := definitionJSON(b)
	return e == nil && string(aa) == string(bb)
}
func definitionJSON(d contract.GraphSchemaDefinition) ([]byte, error) {
	options, e := parameter(d.Options, 0)
	if e != nil {
		return nil, e
	}
	d.Options = contract.GraphValue{}
	return json.Marshal(struct {
		Definition contract.GraphSchemaDefinition
		Options    any
	}{d, options})
}

func identifier(s string) (string, error) {
	if s == "" || len(s) > 4096 || !utf8.ValidString(s) {
		return "", ErrInvalid
	}
	// Neo4j processes Unicode escapes even inside quoted identifiers. Reject
	// backslashes rather than turn a literal \u0060 into syntax after quoting.
	for _, r := range s {
		if r == '\\' || r < 32 || r == 127 {
			return "", ErrInvalid
		}
	}
	return "`" + strings.ReplaceAll(s, "`", "``") + "`", nil
}
func parameter(v contract.GraphValue, depth int) (any, error) {
	if depth > 8 {
		return nil, ErrBounds
	}
	if v.Kind != "map" && v.Kind != "list" {
		return contract.GraphPropertyScalarValue(v)
	}
	if v.Text != "" || v.Node != nil || v.Relationship != nil || v.Path != nil {
		return nil, ErrInvalid
	}
	if v.Kind == "list" {
		if len(v.Properties) != 0 || len(v.Values) > 1000 {
			return nil, ErrBounds
		}
		out := make([]any, len(v.Values))
		for i, item := range v.Values {
			a, e := parameter(item, depth+1)
			if e != nil {
				return nil, e
			}
			out[i] = a
		}
		return out, nil
	}
	if len(v.Values) != 0 || len(v.Properties) > 1000 {
		return nil, ErrBounds
	}
	out := map[string]any{}
	for _, p := range v.Properties {
		if p.Name == "" || len(p.Name) > 4096 || !utf8.ValidString(p.Name) {
			return nil, ErrInvalid
		}
		if _, exists := out[p.Name]; exists {
			return nil, ErrInvalid
		}
		a, e := parameter(p.Value, depth+1)
		if e != nil {
			return nil, e
		}
		out[p.Name] = a
	}
	return out, nil
}
func Parameters(r contract.GraphSchemaRequest) (map[string]any, error) {
	if len(r.Parameters) > 1 {
		return nil, ErrInvalid
	}
	out := map[string]any{}
	for _, p := range r.Parameters {
		if p.Name != "options" {
			return nil, ErrInvalid
		}
		a, e := parameter(p.Value, 0)
		if e != nil {
			return nil, e
		}
		out[p.Name] = a
	}
	return out, nil
}

func positiveDimension(v contract.GraphValue) bool {
	if v.Kind != "integer" {
		return false
	}
	n, e := strconv.ParseInt(v.Text, 10, 64)
	return e == nil && n >= 1 && n <= 4096 && strconv.FormatInt(n, 10) == v.Text
}
