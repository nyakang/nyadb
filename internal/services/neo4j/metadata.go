package neo4j

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func validDatabase(s string) bool {
	return s != "" && len(s) <= 4096 && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
func metadataQuery(ctx context.Context, c *Client, database, source string) (Response, error) {
	p, e := c.Prepare(Request{Database: database, Query: source, Mode: "read", MaxRows: 8192, MaxBytes: 16 << 20})
	if e != nil {
		return Response{}, e
	}
	r, e := c.Execute(ctx, p)
	if e != nil {
		return Response{}, e
	}
	if !r.Completed || r.Summary.Database != database {
		return Response{}, ErrProtocol
	}
	if r.Truncated {
		return Response{}, ErrBounds
	}
	return r, nil
}
func stringColumn(r Response, row int, name string) (string, error) {
	if row < 0 || row >= len(r.Rows) || len(r.Rows[row]) != len(r.Columns) {
		return "", ErrProtocol
	}
	index := -1
	for i, n := range r.Columns {
		if n == name {
			if index != -1 {
				return "", ErrProtocol
			}
			index = i
		}
	}
	if index < 0 || r.Rows[row][index].Kind != "string" {
		return "", ErrProtocol
	}
	return r.Rows[row][index].Text, nil
}
func databaseInfo(ctx context.Context, c *Client, database string) (Response, error) {
	r, e := metadataQuery(ctx, c, database, databaseProbe(database))
	if e != nil {
		return Response{}, e
	}
	if len(r.Rows) != 1 {
		return Response{}, ErrProtocol
	}
	name, e := stringColumn(r, 0, "name")
	if e != nil || name != database {
		return Response{}, ErrProtocol
	}
	return r, nil
}
func databaseProbe(database string) string {
	if database == "system" {
		return "SHOW DATABASES YIELD name WHERE name = 'system' RETURN name"
	}
	return "CALL db.info() YIELD name RETURN name"
}
func (s *Session) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
	var out []contract.Namespace
	e := s.withMetadata(ctx, func(ctx context.Context, c *Client) error {
		r, e := metadataQuery(ctx, c, "system", "SHOW DATABASES YIELD name RETURN name ORDER BY name")
		if e != nil {
			return e
		}
		seen := map[string]bool{}
		for i := range r.Rows {
			name, e := stringColumn(r, i, "name")
			if e != nil || !validDatabase(name) || seen[name] {
				return ErrProtocol
			}
			seen[name] = true
			out = append(out, contract.Namespace{Ref: s.Namespace(name), Label: name})
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	return out, nil
}
func (s *Session) GraphSchema(ctx context.Context, n contract.NamespaceRef) (snapshot contract.GraphSchemaSnapshot, e error) {
	if e = s.checkNamespace(n); e != nil {
		return snapshot, e
	}
	if n.Catalog == "system" {
		return snapshot, contract.ErrUnsupported
	}
	snapshot.Namespace = n
	e = s.withMetadata(ctx, func(ctx context.Context, c *Client) error {
		if _, e := databaseInfo(ctx, c, n.Catalog); e != nil {
			return e
		}
		reads := []struct {
			source string
			target *Response
		}{
			{"CALL db.labels() YIELD label RETURN label ORDER BY label", &snapshot.Labels},
			{"CALL db.relationshipTypes() YIELD relationshipType RETURN relationshipType ORDER BY relationshipType", &snapshot.RelationshipTypes},
			{"CALL db.schema.nodeTypeProperties()", &snapshot.NodeProperties},
			{"CALL db.schema.relTypeProperties()", &snapshot.RelationshipProperties},
			{"SHOW INDEXES", &snapshot.Indexes}, {"SHOW CONSTRAINTS", &snapshot.Constraints},
			{"SHOW PROCEDURES", &snapshot.Procedures}, {"SHOW FUNCTIONS", &snapshot.Functions},
		}
		budget := 16 << 20
		for _, read := range reads {
			r, e := metadataQuery(ctx, c, n.Catalog, read.source)
			if e != nil {
				return e
			}
			raw, e := json.Marshal(r)
			if e != nil {
				return ErrProtocol
			}
			budget -= len(raw)
			if budget < 0 {
				return ErrBounds
			}
			*read.target = r
		}
		_, e := databaseInfo(ctx, c, n.Catalog)
		return e
	})
	if e != nil {
		return contract.GraphSchemaSnapshot{}, e
	}
	return snapshot, nil
}
func (s *Session) Browse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	snapshot, e := s.GraphSchema(ctx, n)
	if e != nil {
		return nil, e
	}
	return graphObjects(snapshot)
}
func graphObjects(snapshot contract.GraphSchemaSnapshot) ([]contract.DBObject, error) {
	n := snapshot.Namespace
	var out []contract.DBObject
	sources := []struct {
		r            Response
		column, kind string
	}{
		{snapshot.Labels, "label", "label"}, {snapshot.RelationshipTypes, "relationshipType", "relationship"},
		{snapshot.Indexes, "name", "index"}, {snapshot.Constraints, "name", "constraint"},
		{snapshot.Procedures, "name", "procedure"}, {snapshot.Functions, "name", "function"},
	}
	groups := map[contract.ObjectRef]Response{}
	order := []contract.ObjectRef{}
	for _, source := range sources {
		for i := range source.r.Rows {
			name, e := stringColumn(source.r, i, source.column)
			if e != nil || name == "" || len(name) > 4096 || !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
				return nil, fmt.Errorf("%w: invalid %s metadata name", ErrProtocol, source.kind)
			}
			ref := n.Object(source.kind, name)
			r, exists := groups[ref]
			if exists && source.kind != "function" && source.kind != "procedure" {
				return nil, fmt.Errorf("%w: duplicate %s metadata name", ErrProtocol, source.kind)
			}
			if !exists {
				r = source.r
				r.Rows = nil
				order = append(order, ref)
			}
			// Overloads share a native symbol; retain every original signature row.
			r.Rows = append(r.Rows, source.r.Rows[i])
			groups[ref] = r
		}
	}
	budget := 16 << 20
	for _, ref := range order {
		r := groups[ref]
		raw, e := json.Marshal(r)
		if e != nil {
			return nil, ErrProtocol
		}
		budget -= len(raw) + len(ref.Name) + 128
		if budget < 0 || len(out) >= 8192 {
			return nil, ErrBounds
		}
		out = append(out, contract.DBObject{Ref: ref, Name: ref.Name, Kind: graphObjectLabel(ref.Kind), Engine: "Neo4j", Definition: string(raw)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
func (s *Session) TableMetadata(_ context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	if e := s.checkNamespace(o.Namespace()); e != nil {
		return nil, e
	}
	return nil, contract.ErrUnsupported
}

var _ contract.GraphMetadataProvider = (*Session)(nil)

func graphObjectLabel(kind string) string {
	switch kind {
	case "label":
		return "图标签"
	case "relationship":
		return "关系类型"
	case "index":
		return "图索引"
	case "constraint":
		return "图约束"
	case "procedure":
		return "图过程"
	case "function":
		return "图函数"
	}
	return kind
}
