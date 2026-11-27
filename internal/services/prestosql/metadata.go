package prestosql

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (p *process) metadata(ctx context.Context, method string, params any) ([]json.RawMessage, error) {
	r, err := p.runtime.Client.Call(ctx, method, params)
	p.observed(method, r)
	if err != nil {
		if r.Dispatched || !p.runtime.Client.Alive() {
			_ = p.runtime.Close()
			return nil, errors.Join(contract.ErrSQLSessionLost, err)
		}
		return nil, err
	}
	if !r.ExecutorAcknowledged || r.RemoteFailure || len(r.Result) > 8<<20 {
		return nil, p.invalidMetadata()
	}
	items, err := array(r.Result)
	if err != nil || len(items) > 10000 {
		return nil, p.invalidMetadata()
	}
	return items, nil
}

func (p *process) invalidMetadata() error {
	_ = p.runtime.Close()
	return errors.Join(contract.ErrSQLSessionLost, ErrResponse)
}

func metadataString(raw json.RawMessage, nullable bool) (string, error) {
	if nullable && string(raw) == "null" {
		return "", nil
	}
	var value string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &value) != nil {
		return "", ErrResponse
	}
	return value, nil
}

func (s *Session) Namespaces(ctx context.Context) (out []contract.Namespace, err error) {
	known := map[string]contract.NamespaceRef{}
	err = s.withMetadata(ctx, func(child context.Context, original *process) error {
		catalogs, err := original.metadata(child, "listDatabases", nil)
		if err != nil {
			return err
		}
		seenCatalogs := map[string]bool{}
		for _, raw := range catalogs {
			fields, e := resultFields(raw)
			catalog, nameErr := metadataString(fields["name"], false)
			if e != nil || len(fields) != 1 || nameErr != nil || !validName(catalog) || seenCatalogs[catalog] {
				return original.invalidMetadata()
			}
			seenCatalogs[catalog] = true
			schemas, e := original.metadata(child, "listSchemas", map[string]any{"database": catalog, "exactMetadataIdentity": true})
			if e != nil {
				return e
			}
			for _, raw := range schemas {
				schema, e := metadataString(raw, false)
				if e != nil || !validName(schema) || len(out) >= 10000 {
					return original.invalidMetadata()
				}
				n := contract.NamespaceRef{ProfileID: s.profileID, Catalog: catalog, Schema: schema}
				label := namespaceLabel(n)
				if _, duplicate := known[label]; duplicate {
					return original.invalidMetadata()
				}
				known[label] = n
				out = append(out, contract.Namespace{Ref: n, Label: label})
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed || s.lost || child.Err() != nil {
			return contract.ErrRevoked
		}
		// Publish while holding the original metadata operation lock so an
		// older concurrent enumeration cannot replace a newer namespace map.
		s.namespaces = known
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func browse(ctx context.Context, original *process, n contract.NamespaceRef) ([]contract.DBObject, error) {
	items, err := original.metadata(ctx, "listTables", map[string]any{"database": n.Catalog, "schema": n.Schema, "exactMetadataIdentity": true})
	if err != nil {
		return nil, err
	}
	var out []contract.DBObject
	seen := map[string]bool{}
	for _, raw := range items {
		fields, err := resultFields(raw)
		name, nameErr := metadataString(fields["name"], false)
		typeName, typeErr := metadataString(fields["table_type"], false)
		comment, commentErr := metadataString(fields["comment"], true)
		if err != nil || len(fields) != 3 || nameErr != nil || typeErr != nil || commentErr != nil || !validName(name) || seen[name] {
			return nil, original.invalidMetadata()
		}
		seen[name] = true
		kind, label := "", ""
		switch typeName {
		case "TABLE", "BASE TABLE", "SYSTEM TABLE":
			kind, label = "table", "表"
		case "VIEW", "SYSTEM VIEW", "MATERIALIZED VIEW":
			kind, label = "view", "视图"
		default:
			return nil, contract.ErrUnsupported
		}
		out = append(out, contract.DBObject{Ref: n.Object(kind, name), Name: name, Kind: label, Comment: comment, Engine: n.Catalog})
	}
	return out, nil
}

func (s *Session) Browse(ctx context.Context, n contract.NamespaceRef) (out []contract.DBObject, err error) {
	if err = s.checkNamespace(n); err != nil {
		return nil, err
	}
	err = s.withMetadata(ctx, func(child context.Context, original *process) error {
		out, err = browse(child, original, n)
		return err
	})
	return out, err
}

func metadataInteger(raw json.RawMessage) (*int64, error) {
	if string(raw) == "null" {
		return nil, nil
	}
	var value int64
	if json.Unmarshal(raw, &value) != nil || value < 0 {
		return nil, ErrResponse
	}
	return &value, nil
}

func fieldKind(dataType string) contract.CellKind {
	t := strings.ToLower(strings.TrimSpace(dataType))
	switch {
	case t == "tinyint" || t == "smallint" || t == "integer" || t == "bigint":
		return contract.Integer
	case strings.HasPrefix(t, "decimal("):
		return contract.Decimal
	case t == "real" || t == "double":
		return contract.Floating
	case t == "boolean":
		return contract.Boolean
	case t == "varbinary":
		return contract.Binary
	case t == "date" || strings.HasPrefix(t, "time"):
		return contract.DateTime
	case t == "json":
		return contract.JSON
	default:
		return contract.Text
	}
}

func decodeColumn(raw json.RawMessage, position int64) (contract.StructureColumn, error) {
	var column contract.StructureColumn
	fields, err := resultFields(raw)
	if err != nil || len(fields) != 10 {
		return column, ErrResponse
	}
	for _, pair := range []struct {
		key      string
		target   *string
		nullable bool
	}{
		{"name", &column.Name, false}, {"data_type", &column.Type, false},
		{"extra", &column.Extra, true}, {"comment", &column.Comment, true},
	} {
		*pair.target, err = metadataString(fields[pair.key], pair.nullable)
		if err != nil {
			return column, err
		}
	}
	if !validName(column.Name) || column.Type == "" {
		return column, ErrResponse
	}
	for _, key := range []string{"is_nullable", "is_primary_key"} {
		if string(fields[key]) != "true" && string(fields[key]) != "false" {
			return column, ErrResponse
		}
	}
	column.Nullable = string(fields["is_nullable"]) == "true"
	if string(fields["is_primary_key"]) == "true" {
		column.Key = "PRI"
	}
	if string(fields["column_default"]) != "null" {
		value, err := metadataString(fields["column_default"], false)
		if err != nil {
			return column, err
		}
		column.Default = &value
	}
	for _, pair := range []struct {
		key    string
		target **int64
	}{
		{"numeric_precision", &column.NumericPrecision}, {"numeric_scale", &column.NumericScale},
		{"character_maximum_length", &column.CharacterMaxLength},
	} {
		*pair.target, err = metadataInteger(fields[pair.key])
		if err != nil {
			return column, err
		}
	}
	column.Position, column.DataType, column.ValueKind = position, column.Type, fieldKind(column.Type)
	return column, nil
}

func (s *Session) TableMetadata(ctx context.Context, object contract.ObjectRef) (out *contract.TableStructure, err error) {
	if err = s.checkNamespace(object.Namespace()); err != nil {
		return nil, err
	}
	if !validName(object.Name) || object.Kind != "table" && object.Kind != "view" {
		return nil, connections.ErrInvalid
	}
	err = s.withMetadata(ctx, func(child context.Context, original *process) error {
		objects, err := browse(child, original, object.Namespace())
		if err != nil {
			return err
		}
		var found *contract.DBObject
		for i := range objects {
			if objects[i].Ref == object {
				found = &objects[i]
				break
			}
		}
		if found == nil {
			return connections.ErrNotFound
		}
		items, err := original.metadata(child, "getColumns", map[string]any{"database": object.Catalog, "schema": object.Schema, "table": object.Name, "exactMetadataIdentity": true})
		if err != nil {
			return err
		}
		if len(items) == 0 || len(items) > 4096 {
			return original.invalidMetadata()
		}
		out = &contract.TableStructure{Object: object, Database: namespaceLabel(object.Namespace()), Name: object.Name,
			Kind: object.Kind, Engine: object.Catalog, Comment: found.Comment, Editable: false}
		seen := map[string]bool{}
		for i, raw := range items {
			column, err := decodeColumn(raw, int64(i+1))
			if err != nil || seen[column.Name] {
				return original.invalidMetadata()
			}
			seen[column.Name] = true
			out.Columns = append(out.Columns, column)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

var _ contract.MetadataProvider = (*Session)(nil)
