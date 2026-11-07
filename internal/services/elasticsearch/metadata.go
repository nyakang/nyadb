package elasticsearch

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (s *Session) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !s.Alive() {
		return nil, contract.ErrRevoked
	}
	return []contract.Namespace{{Ref: s.Namespace(""), Label: s.identity.ClusterName}}, nil
}
func (s *Session) Browse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	if err := s.checkNamespace(n); err != nil {
		return nil, err
	}
	ctx, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	c, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	targets, err := c.ResolveTargets(ctx, "*")
	if err != nil {
		return nil, err
	}
	out := make([]contract.DBObject, 0, len(targets))
	for _, target := range targets {
		kind := targetCaption(target.Kind)
		out = append(out, contract.DBObject{Ref: n.Object(target.Kind, target.Name), Name: target.Name, Kind: kind, Engine: "Elasticsearch", Status: strings.Join(target.Attributes, ", "), Definition: string(target.Definition)})
	}
	return out, nil
}
func targetCaption(kind string) string {
	switch kind {
	case "index":
		return "索引"
	case "alias":
		return "别名"
	case "data_stream":
		return "数据流"
	}
	return kind
}
func (s *Session) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	if err := s.checkObject(o); err != nil {
		return nil, err
	}
	ctx, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	c, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	m, err := c.ReadTargetMetadata(ctx, o.Name, o.Kind)
	if err != nil {
		return nil, err
	}
	columns, err := mappingColumns(m.Mappings)
	if err != nil {
		return nil, err
	}
	out := &contract.TableStructure{Object: o, Database: s.identity.ClusterName, Name: o.Name, Kind: targetCaption(o.Kind), Engine: "Elasticsearch", Columns: columns, Definition: string(m.Mappings), Definitions: []contract.Definition{{Name: o.Name, Kind: "resolution", SQL: string(m.Target.Definition)}, {Name: o.Name, Kind: "mapping", SQL: string(m.Mappings)}}}
	if len(m.Aliases) > 0 {
		out.Definitions = append(out.Definitions, contract.Definition{Name: o.Name, Kind: "aliases", SQL: string(m.Aliases)})
	}
	if len(m.Stream) > 0 {
		out.Definitions = append(out.Definitions, contract.Definition{Name: o.Name, Kind: "data_stream", SQL: string(m.Stream)})
	}
	return out, nil
}

// Native mappings may disagree across alias/data-stream members. Preserve each
// concrete index's fields instead of inventing a merged SQL schema.
func mappingColumns(raw []byte) ([]contract.StructureColumn, error) {
	var indices map[string]struct {
		Mappings *struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"mappings"`
	}
	if decodeJSON(raw, &indices) != nil || len(indices) > 16384 {
		return nil, ErrProtocol
	}
	names := make([]string, 0, len(indices))
	for name := range indices {
		names = append(names, name)
	}
	sort.Strings(names)
	out := []contract.StructureColumn{}
	seen := map[string]bool{}
	var visit func(string, map[string]json.RawMessage, string, int) error
	visit = func(parent string, properties map[string]json.RawMessage, index string, depth int) error {
		if depth > 64 {
			return ErrProtocol
		}
		fields := make([]string, 0, len(properties))
		for field := range properties {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		for _, field := range fields {
			if field == "" || strings.ContainsRune(field, 0) || len(field) > 1024 {
				return ErrProtocol
			}
			path := field
			if parent != "" {
				path = parent + "." + field
			}
			if len(path) > 4096 {
				return ErrProtocol
			}
			var native struct {
				Type       string                     `json:"type"`
				Properties map[string]json.RawMessage `json:"properties"`
				Fields     map[string]json.RawMessage `json:"fields"`
			}
			if decodeJSON(properties[field], &native) != nil {
				return ErrProtocol
			}
			if native.Type == "" {
				native.Type = "object"
			}
			name := path
			if len(indices) > 1 {
				name = index + ":" + path
			}
			if seen[name] || len(out) >= 8192 {
				return ErrProtocol
			}
			seen[name] = true
			out = append(out, contract.StructureColumn{Name: name, Type: native.Type, DataType: native.Type, Position: int64(len(out) + 1), Nullable: true, Generated: true, Comment: index})
			if err := visit(path, native.Properties, index, depth+1); err != nil {
				return err
			}
			if err := visit(path, native.Fields, index, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	for _, name := range names {
		if !validIndex(name) || indices[name].Mappings == nil {
			return nil, ErrProtocol
		}
		if err := visit("", indices[name].Mappings.Properties, name, 0); err != nil {
			return nil, err
		}
	}
	return out, nil
}
