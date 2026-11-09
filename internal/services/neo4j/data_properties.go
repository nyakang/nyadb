package neo4j

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const graphPropertyPrefix = "property."
const graphProjectionBytes = 64 << 10

// A projection is a live, qualified inventory, never a relational schema or
// a snapshot of the entities. Row identities carry their original inventory
// so an independent field read cannot adopt a later page's column meanings.
type graphPropertyColumn struct {
	Key   string
	Types []string
}

func cloneGraphProjection(p []graphPropertyColumn) []graphPropertyColumn {
	p = slices.Clone(p)
	for i := range p {
		p[i].Types = slices.Clone(p[i].Types)
	}
	return p
}

func graphProjectionValid(p []graphPropertyColumn, base int) bool {
	if len(p)+base > 1024 {
		return false
	}
	for i, c := range p {
		if !utf8.ValidString(c.Key) || len(c.Key) > 4096 || i > 0 && c.Key <= p[i-1].Key || len(c.Types) == 0 || len(c.Types) > 16 {
			return false
		}
		for j, typ := range c.Types {
			if !utf8.ValidString(typ) || typ == "" || len(typ) > 256 || j > 0 && typ <= c.Types[j-1] {
				return false
			}
		}
	}
	raw, err := json.Marshal(p)
	return err == nil && len(raw) <= graphProjectionBytes
}

func (l *dataLease) discoverProperties(ctx context.Context, c *Client) ([]graphPropertyColumn, error) {
	limit := 1024 - len(graphColumns(l.object.Kind, nil)) + 1
	query := l.match() + " UNWIND keys(entity) AS property WITH property, collect(DISTINCT valueType(entity[property])) AS types RETURN property, types ORDER BY property LIMIT $property_limit"
	r, err := l.read(ctx, c, query, map[string]any{"property_limit": int64(limit)}, limit)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(r.Columns, []string{"property", "types"}) {
		return nil, ErrProtocol
	}
	if len(r.Rows) >= limit {
		return nil, ErrBounds
	}
	properties := make([]graphPropertyColumn, 0, len(r.Rows))
	for _, row := range r.Rows {
		if len(row) != 2 || row[0].Kind != "string" || row[1].Kind != "list" {
			return nil, ErrProtocol
		}
		column := graphPropertyColumn{Key: row[0].Text}
		for _, typ := range row[1].Values {
			if typ.Kind != "string" {
				return nil, ErrProtocol
			}
			column.Types = append(column.Types, typ.Text)
		}
		slices.Sort(column.Types)
		column.Types = slices.Compact(column.Types)
		properties = append(properties, column)
	}
	if !graphProjectionValid(properties, len(graphColumns(l.object.Kind, nil))) {
		return nil, ErrBounds
	}
	return properties, nil
}

func graphColumns(kind string, properties []graphPropertyColumn) []contract.StructureColumn {
	names := []string{"element_id", "labels", "properties", "node"}
	if kind == "relationship" {
		names = []string{"element_id", "type", "start_element_id", "end_element_id", "properties", "relationship"}
	}
	out := make([]contract.StructureColumn, 0, len(names)+len(properties))
	for i, name := range names {
		valueKind, typ := contract.Text, "string"
		if name == "properties" || name == "labels" || name == "node" || name == "relationship" {
			valueKind, typ = contract.JSON, "Neo4j native "+name
		}
		out = append(out, contract.StructureColumn{Name: name, Position: int64(i + 1), Type: typ, DataType: typ, ValueKind: valueKind})
	}
	for _, property := range properties {
		typ := strings.Join(property.Types, " | ")
		kind := contract.JSON
		if len(property.Types) == 1 {
			switch strings.TrimSuffix(property.Types[0], " NOT NULL") {
			case "STRING":
				kind = contract.Text
			case "INTEGER":
				kind = contract.Integer
			case "FLOAT":
				kind = contract.Floating
			case "BOOLEAN":
				kind = contract.Boolean
			}
		}
		out = append(out, contract.StructureColumn{Name: graphPropertyPrefix + property.Key, Position: int64(len(out) + 1), Type: typ, DataType: typ, ValueKind: kind, Nullable: true})
	}
	return out
}

func (l *dataLease) propertyProjection() []graphPropertyColumn {
	l.mu.Lock()
	defer l.mu.Unlock()
	return cloneGraphProjection(l.properties)
}

func (l *dataLease) columns() []contract.StructureColumn {
	return graphColumns(l.object.Kind, l.propertyProjection())
}

func nativePropertyCell(v Value) (contract.Cell, error) {
	switch v.Kind {
	case "null":
		return contract.Cell{Kind: contract.Null}, nil
	case "string":
		return contract.Cell{Kind: contract.Text, Text: v.Text}, nil
	case "integer":
		return contract.Cell{Kind: contract.Integer, Text: v.Text}, nil
	case "float":
		return contract.Cell{Kind: contract.Floating, Text: v.Text}, nil
	case "boolean":
		return contract.Cell{Kind: contract.Boolean, Text: v.Text}, nil
	case "bytes":
		raw, err := base64.StdEncoding.DecodeString(v.Text)
		if err != nil {
			return contract.Cell{}, ErrProtocol
		}
		return contract.Cell{Kind: contract.Binary, Bytes: raw}, nil
	default:
		// Temporal, spatial, duration and list values retain all native kind,
		// zone, component and exact-element metadata, without narrowing.
		return graphJSON(v)
	}
}
