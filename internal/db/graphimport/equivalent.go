package graphimport

import (
	"math"
	"reflect"
	"slices"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// EquivalentRequest admits native canonical spelling and ordering while
// preserving every source reference, direction, kind and exact component.
func EquivalentRequest(a, b contract.GraphImportRequest) bool {
	if ValidateRequest(a) != nil || ValidateRequest(b) != nil || a.Bundle.Source != b.Bundle.Source || a.Bundle.Format != b.Bundle.Format || !reflect.DeepEqual(a.Bindings, b.Bindings) || len(a.Bundle.Nodes) != len(b.Bundle.Nodes) || len(a.Bundle.Relationships) != len(b.Bundle.Relationships) {
		return false
	}
	for i, n := range a.Bundle.Nodes {
		other := b.Bundle.Nodes[i]
		labels, otherLabels := slices.Clone(n.Labels), slices.Clone(other.Labels)
		slices.Sort(labels)
		slices.Sort(otherLabels)
		if n.ElementID != other.ElementID || n.LegacyID != other.LegacyID || !slices.Equal(labels, otherLabels) || !equivalentProperties(n.Properties, other.Properties) {
			return false
		}
	}
	for i, r := range a.Bundle.Relationships {
		other := b.Bundle.Relationships[i]
		if r.ElementID != other.ElementID || r.Type != other.Type || r.StartElementID != other.StartElementID || r.EndElementID != other.EndElementID || r.LegacyID != other.LegacyID || r.StartLegacyID != other.StartLegacyID || r.EndLegacyID != other.EndLegacyID || !equivalentProperties(r.Properties, other.Properties) {
			return false
		}
	}
	return true
}

func equivalentProperties(a, b []contract.GraphProperty) bool {
	if len(a) != len(b) {
		return false
	}
	byName := map[string]contract.GraphValue{}
	for _, p := range b {
		byName[p.Name] = p.Value
	}
	for _, p := range a {
		other, ok := byName[p.Name]
		if !ok || !equivalentValue(p.Value, other) {
			return false
		}
	}
	return true
}

func equivalentValue(a, b contract.GraphValue) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Kind == "bytes" {
		return a.Text == b.Text
	}
	if a.Kind == "list" {
		if len(a.Values) != len(b.Values) {
			return false
		}
		for i, v := range a.Values {
			if !equivalentValue(v, b.Values[i]) {
				return false
			}
		}
		return true
	}
	x, err := contract.GraphPropertyScalarValue(a)
	y, otherErr := contract.GraphPropertyScalarValue(b)
	if err != nil || otherErr != nil {
		x, err = contract.GraphPropertyExtendedValue(a)
		y, otherErr = contract.GraphPropertyExtendedValue(b)
	}
	if err != nil || otherErr != nil {
		return false
	}
	if f, ok := x.(float64); ok {
		g, ok := y.(float64)
		return ok && math.Float64bits(f) == math.Float64bits(g)
	}
	if t, ok := x.(time.Time); ok {
		u, ok := y.(time.Time)
		_, offset := t.Zone()
		_, otherOffset := u.Zone()
		return ok && t.Equal(u) && t.Location().String() == u.Location().String() && offset == otherOffset
	}
	return reflect.DeepEqual(x, y)
}
