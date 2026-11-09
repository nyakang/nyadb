package neo4j

import (
	"encoding/base64"
	"math"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"

	neo4jdriver "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type Property = contract.GraphProperty
type Value = contract.GraphValue
type Node = contract.GraphNode
type Relationship = contract.GraphRelationship
type Path = contract.GraphPath

type valueBudget struct{ bytes, items, limit int }

func (b *valueBudget) add(n int) error {
	b.bytes += n
	b.items++
	if b.bytes > b.limit || b.items > 100000 {
		return ErrBounds
	}
	return nil
}
func (b *valueBudget) properties(m map[string]any, graph bool, depth int) ([]Property, map[string]any, error) {
	if len(m) > 100000 {
		return nil, nil, ErrBounds
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	props := make([]Property, 0, len(keys))
	copy := make(map[string]any, len(keys))
	for _, k := range keys {
		if !utf8.ValidString(k) {
			return nil, nil, ErrProtocol
		}
		if e := b.add(len(k) + 16); e != nil {
			return nil, nil, e
		}
		v, raw, e := b.capture(m[k], graph, depth+1)
		if e != nil {
			return nil, nil, e
		}
		props = append(props, Property{Name: k, Value: v})
		copy[k] = raw
	}
	return props, copy, nil
}
func (b *valueBudget) node(n neo4jdriver.Node, depth int) (Node, error) {
	if e := b.add(len(n.ElementId) + 16); e != nil {
		return Node{}, e
	}
	labels := slices.Clone(n.Labels)
	for _, label := range labels {
		if !utf8.ValidString(label) {
			return Node{}, ErrProtocol
		}
		if e := b.add(len(label) + 8); e != nil {
			return Node{}, e
		}
	}
	p, _, e := b.properties(n.Props, true, depth+1)
	return Node{ElementID: n.ElementId, LegacyID: n.Id, Labels: labels, Properties: p}, e
}
func (b *valueBudget) relationship(r neo4jdriver.Relationship, depth int) (Relationship, error) {
	if e := b.add(len(r.ElementId) + len(r.StartElementId) + len(r.EndElementId) + len(r.Type) + 32); e != nil {
		return Relationship{}, e
	}
	p, _, e := b.properties(r.Props, true, depth+1)
	return Relationship{ElementID: r.ElementId, StartElementID: r.StartElementId, EndElementID: r.EndElementId, Type: r.Type, LegacyID: r.Id, StartLegacyID: r.StartId, EndLegacyID: r.EndId, Properties: p}, e
}
func (b *valueBudget) capture(raw any, graph bool, depth int) (Value, any, error) {
	if depth > 32 {
		return Value{}, nil, ErrBounds
	}
	if e := b.add(16); e != nil {
		return Value{}, nil, e
	}
	v := Value{}
	switch x := raw.(type) {
	case nil:
		v.Kind = "null"
	case bool:
		v.Kind = "boolean"
		v.Text = strconv.FormatBool(x)
	case int:
		return b.capture(int64(x), graph, depth)
	case int8:
		return b.capture(int64(x), graph, depth)
	case int16:
		return b.capture(int64(x), graph, depth)
	case int32:
		return b.capture(int64(x), graph, depth)
	case int64:
		v.Kind = "integer"
		v.Text = strconv.FormatInt(x, 10)
	case uint:
		if uint64(x) > math.MaxInt64 {
			return v, nil, ErrBounds
		}
		return b.capture(int64(x), graph, depth)
	case uint64:
		if x > math.MaxInt64 {
			return v, nil, ErrBounds
		}
		return b.capture(int64(x), graph, depth)
	case uint8:
		return b.capture(int64(x), graph, depth)
	case uint16:
		return b.capture(int64(x), graph, depth)
	case uint32:
		return b.capture(int64(x), graph, depth)
	case float32:
		return b.capture(float64(x), graph, depth)
	case float64:
		v.Kind = "float"
		v.Text = strconv.FormatFloat(x, 'g', -1, 64)
	case string:
		if !utf8.ValidString(x) {
			return v, nil, ErrProtocol
		}
		if e := b.add(len(x)); e != nil {
			return v, nil, e
		}
		v.Kind = "string"
		v.Text = x
	case []byte:
		if e := b.add(len(x) * 2); e != nil {
			return v, nil, e
		}
		v.Kind = "bytes"
		v.Text = base64.StdEncoding.EncodeToString(x)
		raw = slices.Clone(x)
	case []any:
		if len(x) > 100000 {
			return v, nil, ErrBounds
		}
		v.Kind = "list"
		v.Values = make([]Value, len(x))
		copy := make([]any, len(x))
		for i, item := range x {
			child, native, e := b.capture(item, graph, depth+1)
			if e != nil {
				return v, nil, e
			}
			v.Values[i], copy[i] = child, native
		}
		raw = copy
	case map[string]any:
		v.Kind = "map"
		props, copy, e := b.properties(x, graph, depth)
		if e != nil {
			return v, nil, e
		}
		v.Properties = props
		raw = copy
	case time.Time:
		v.Kind = "datetime"
		v.Text = x.Format(time.RFC3339Nano)
		_, offset := x.Zone()
		props, _, e := b.properties(map[string]any{"zone": x.Location().String(), "offset_seconds": int64(offset)}, false, depth)
		if e != nil {
			return v, nil, e
		}
		v.Properties = props
	case neo4jdriver.Date:
		v.Kind = "date"
		v.Text = x.String()
	case neo4jdriver.LocalDateTime:
		v.Kind = "local_datetime"
		v.Text = x.String()
	case neo4jdriver.Time:
		v.Kind = "time"
		v.Text = x.String()
		_, offset := time.Time(x).Zone()
		props, _, e := b.properties(map[string]any{"offset_seconds": int64(offset)}, false, depth)
		if e != nil {
			return v, nil, e
		}
		v.Properties = props
	case neo4jdriver.LocalTime:
		v.Kind = "local_time"
		v.Text = x.String()
	case neo4jdriver.Duration:
		v.Kind = "duration"
		v.Text = x.String()
		p, _, e := b.properties(map[string]any{"months": x.Months, "days": x.Days, "seconds": x.Seconds, "nanoseconds": int64(x.Nanos)}, false, depth)
		if e != nil {
			return v, nil, e
		}
		v.Properties = p
	case neo4jdriver.Point2D:
		v.Kind = "point2d"
		p, _, e := b.properties(map[string]any{"srid": int64(x.SpatialRefId), "x": x.X, "y": x.Y}, false, depth)
		if e != nil {
			return v, nil, e
		}
		v.Properties = p
	case neo4jdriver.Point3D:
		v.Kind = "point3d"
		p, _, e := b.properties(map[string]any{"srid": int64(x.SpatialRefId), "x": x.X, "y": x.Y, "z": x.Z}, false, depth)
		if e != nil {
			return v, nil, e
		}
		v.Properties = p
	case neo4jdriver.Node:
		if !graph {
			return v, nil, ErrInvalid
		}
		n, e := b.node(x, depth)
		if e != nil {
			return v, nil, e
		}
		v.Kind = "node"
		v.Node = &n
	case neo4jdriver.Relationship:
		if !graph {
			return v, nil, ErrInvalid
		}
		r, e := b.relationship(x, depth)
		if e != nil {
			return v, nil, e
		}
		v.Kind = "relationship"
		v.Relationship = &r
	case neo4jdriver.Path:
		if !graph || len(x.Nodes)+len(x.Relationships) > 100000 {
			return v, nil, ErrInvalid
		}
		p := &Path{Nodes: make([]Node, len(x.Nodes)), Relationships: make([]Relationship, len(x.Relationships))}
		for i, n := range x.Nodes {
			item, e := b.node(n, depth+1)
			if e != nil {
				return v, nil, e
			}
			p.Nodes[i] = item
		}
		for i, r := range x.Relationships {
			item, e := b.relationship(r, depth+1)
			if e != nil {
				return v, nil, e
			}
			p.Relationships[i] = item
		}
		v.Kind = "path"
		v.Path = p
	default:
		array := reflect.ValueOf(raw)
		if array.Kind() != reflect.Slice && array.Kind() != reflect.Array {
			return v, nil, ErrInvalid
		}
		if array.Len() > 100000 {
			return v, nil, ErrBounds
		}
		items := make([]any, array.Len())
		for i := range items {
			items[i] = array.Index(i).Interface()
		}
		return b.capture(items, graph, depth+1)
	}
	return v, raw, nil
}

func cloneProperties(p []Property) []Property {
	out := slices.Clone(p)
	for i := range out {
		out[i].Value = cloneValue(out[i].Value)
	}
	return out
}
func cloneNode(n Node) Node                         { return n.Clone() }
func cloneRelationship(r Relationship) Relationship { return r.Clone() }
func cloneValue(v Value) Value                      { return v.Clone() }
