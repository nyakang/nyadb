package solr

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (s *Session) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
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
	if err = s.validateNode(ctx, c); err != nil {
		return nil, err
	}
	return []contract.Namespace{{Ref: s.Namespace(""), Label: "Solr · " + s.identity.Mode}}, nil
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
	targets, err := c.ResolveTargets(ctx)
	if err != nil {
		return nil, err
	}
	again, err := c.ResolveTargets(ctx)
	if err != nil {
		return nil, err
	}
	if !sameTargets(targets, again) {
		return nil, ErrChanged
	}
	if err = s.validateNode(ctx, c); err != nil {
		return nil, err
	}
	out := make([]contract.DBObject, 0, len(targets))
	for _, t := range targets {
		out = append(out, contract.DBObject{Ref: n.Object(t.Kind, t.Name), Name: t.Name, Kind: t.Kind, Engine: "Solr", Status: strings.Join(t.Members, ", "), Definition: string(t.Definition)})
	}
	return out, nil
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
	if err = s.validateNode(ctx, c); err != nil {
		return nil, err
	}
	out := &contract.TableStructure{Object: o, Database: s.identity.Mode, Name: o.Name, Kind: o.Kind, Engine: "Solr", Definition: string(m.Target.Definition), Definitions: []contract.Definition{{Name: o.Name, Kind: "resolution", SQL: string(m.Target.Definition)}}}
	for _, v := range m.Members {
		columns, err := schemaColumns(v.Schema, v.Name, len(m.Members) > 1)
		if err != nil {
			return nil, err
		}
		for i := range columns {
			columns[i].Position = int64(len(out.Columns) + i + 1)
		}
		out.Columns = append(out.Columns, columns...)
		if len(out.Columns) > 8192 {
			return nil, ErrBounds
		}
		out.Definitions = append(out.Definitions, contract.Definition{Name: v.Name, Kind: "schema", SQL: string(v.Schema)}, contract.Definition{Name: v.Name, Kind: "config", SQL: string(v.Config)})
	}
	raw, _ := json.Marshal(struct {
		Node        Identity
		Fingerprint string
	}{s.identity, m.Fingerprint})
	out.Definitions = append(out.Definitions, contract.Definition{Name: o.Name, Kind: "observation", SQL: string(raw)})
	return out, nil
}

// Native uniqueKey is preserved in the complete schema; it alone does not prove
// stored source, optimistic versions or safe document editing. No SQL PK is inferred.
func schemaColumns(raw []byte, member string, qualify bool) ([]contract.StructureColumn, error) {
	type field struct {
		Name, Type                              string
		Required                                bool
		Stored, Indexed, DocValues, MultiValued *bool
		Default                                 json.RawMessage
	}
	var schema struct {
		Name          string
		Fields        []field
		DynamicFields []field           `json:"dynamicFields"`
		FieldTypes    []json.RawMessage `json:"fieldTypes"`
	}
	if decodeJSON(raw, &schema) != nil || schema.Name == "" || schema.Fields == nil || schema.FieldTypes == nil || len(schema.Fields)+len(schema.DynamicFields) > 8192 {
		return nil, ErrProtocol
	}
	out := []contract.StructureColumn{}
	seen := map[string]bool{}
	for _, group := range [][]field{schema.Fields, schema.DynamicFields} {
		for _, f := range group {
			if f.Name == "" || len(f.Name) > 1024 || strings.ContainsRune(f.Name, 0) || f.Type == "" || seen[f.Name] {
				return nil, ErrProtocol
			}
			seen[f.Name] = true
			name := f.Name
			if qualify {
				name = member + ":" + name
			}
			native, _ := json.Marshal(f)
			col := contract.StructureColumn{Name: name, Type: f.Type, DataType: f.Type, Position: int64(len(out) + 1), Nullable: !f.Required, Comment: member, Extra: string(native)}
			if len(f.Default) > 0 {
				value := string(f.Default)
				col.Default = &value
			}
			out = append(out, col)
		}
	}
	return out, nil
}
