package h2

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) check(o contract.ObjectRef) error {
	if o.ProfileID != s.ProfileID() || o.Catalog != s.catalog || !sqltext.ValidName(o.Schema) || !sqltext.ValidName(o.Name) {
		return connections.ErrInvalid
	}
	if !s.Alive() {
		return contract.ErrRevoked
	}
	return nil
}

func (s *Session) Namespaces(ctx context.Context) (out []contract.Namespace, err error) {
	err = s.withMetadata(ctx, func(child context.Context, session *agentbridge.Session) error {
		var names []string
		if err := nativeJSON(child, session, "list_schemas", map[string]any{}, &names); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, name := range names {
			if !sqltext.ValidName(name) || seen[name] {
				return contract.ErrUnsupported
			}
			seen[name] = true
			if name != "INFORMATION_SCHEMA" {
				out = append(out, contract.Namespace{Ref: contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: s.catalog, Schema: name}, Label: name})
			}
		}
		return nil
	})
	return
}

func nativeJSON(ctx context.Context, session *agentbridge.Session, method string, params any, target any) error {
	r, err := session.Call(ctx, method, params)
	if err != nil {
		return safeError(err)
	}
	if !r.ExecutorAcknowledged || len(r.Result) > 8<<20 || string(r.Result) == "null" || json.Unmarshal(r.Result, target) != nil {
		return contract.ErrUnsupported
	}
	return nil
}

type nativeObject struct {
	Name    string `json:"name"`
	Type    string `json:"object_type"`
	Schema  string `json:"schema"`
	Comment string `json:"comment"`
}

func (s *Session) Browse(ctx context.Context, n contract.NamespaceRef) (out []contract.DBObject, err error) {
	if err = s.check(n.Object("table", "_")); err != nil {
		return
	}
	err = s.withMetadata(ctx, func(child context.Context, session *agentbridge.Session) error {
		var objects []nativeObject
		if err := nativeJSON(child, session, "list_objects", map[string]string{"schema": n.Schema}, &objects); err != nil {
			return err
		}
		if len(objects) > 100000 {
			return contract.ErrUnsupported
		}
		counts := map[string]int{}
		for _, o := range objects {
			counts[o.Type+"\x00"+o.Name]++
		}
		seen := map[string]bool{}
		for _, o := range objects {
			if !sqltext.ValidName(o.Name) || o.Schema != "" && o.Schema != n.Schema {
				return contract.ErrUnsupported
			}
			kind, label := "", ""
			switch o.Type {
			case "TABLE", "BASE TABLE":
				kind, label = "table", "表"
			case "VIEW":
				kind, label = "view", "视图"
			case "FUNCTION":
				kind, label = "function", "函数"
			case "PROCEDURE":
				kind, label = "procedure", "存储过程"
			default:
				return contract.ErrUnsupported
			}
			key := kind + "\x00" + o.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			obj := contract.DBObject{Ref: n.Object(kind, o.Name), Name: o.Name, Kind: label, Engine: "H2", Comment: o.Comment}
			if kind == "function" || kind == "procedure" {
				if counts[o.Type+"\x00"+o.Name] > 1 {
					obj.Status = "unsupported_overload"
				} else {
					var source struct {
						Name, Schema, Source string
						Type                 string `json:"object_type"`
					}
					if err := nativeJSON(child, session, "get_object_source", map[string]string{"schema": n.Schema, "name": o.Name, "object_type": o.Type}, &source); err != nil {
						return err
					}
					if source.Name != o.Name || source.Schema != n.Schema || source.Type != o.Type {
						return contract.ErrUnsupported
					}
					obj.Definition = source.Source
				}
			}
			out = append(out, obj)
		}
		return nil
	})
	return
}

type nativeColumn struct {
	Name           string  `json:"name"`
	Type           string  `json:"data_type"`
	Nullable       *bool   `json:"is_nullable"`
	Default        *string `json:"column_default"`
	Primary        *bool   `json:"is_primary_key"`
	Extra, Comment string
	Precision      *int64 `json:"numeric_precision"`
	Scale          *int64 `json:"numeric_scale"`
	Length         *int64 `json:"character_maximum_length"`
}
type nativeIndex struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  *bool    `json:"is_unique"`
	Primary *bool    `json:"is_primary"`
	Type    string   `json:"index_type"`
}

func typeKind(typ string) contract.CellKind {
	name := strings.ToUpper(typ)
	switch name {
	case "TINYINT", "SMALLINT", "INTEGER", "INT", "BIGINT":
		return contract.Integer
	case "NUMERIC", "DECIMAL", "DECFLOAT":
		return contract.Decimal
	case "REAL", "DOUBLE", "DOUBLE PRECISION", "FLOAT":
		return contract.Floating
	case "BOOLEAN", "BOOL":
		return contract.Boolean
	case "BINARY", "BINARY VARYING", "VARBINARY", "BINARY LARGE OBJECT", "BLOB":
		return contract.Binary
	case "DATE", "TIME", "TIME WITH TIME ZONE", "TIMESTAMP", "TIMESTAMP WITH TIME ZONE":
		return contract.DateTime
	case "JSON":
		return contract.JSON
	default:
		return contract.Text
	}
}

func (s *Session) TableMetadata(ctx context.Context, o contract.ObjectRef) (out *contract.TableStructure, err error) {
	if err = s.check(o); err != nil {
		return
	}
	err = s.withMetadata(ctx, func(child context.Context, session *agentbridge.Session) error {
		var e error
		out, e = s.structure(child, session, o)
		return e
	})
	return
}

func (s *Session) structure(ctx context.Context, session *agentbridge.Session, o contract.ObjectRef) (*contract.TableStructure, error) {
	if err := s.check(o); err != nil {
		return nil, err
	}
	if o.Kind != "table" && o.Kind != "view" {
		return nil, contract.ErrUnsupported
	}
	params := map[string]string{"schema": o.Schema, "table": o.Name}
	var columns []nativeColumn
	if err := nativeJSON(ctx, session, "get_columns", params, &columns); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, connections.ErrNotFound
	}
	if len(columns) > 512 {
		return nil, contract.ErrUnsupported
	}
	t := &contract.TableStructure{Object: o, Database: o.Schema, Name: o.Name, Kind: "表", Engine: "H2"}
	if o.Kind == "view" {
		t.Kind = "视图"
	}
	seen := map[string]bool{}
	for i, c := range columns {
		if !sqltext.ValidName(c.Name) || c.Type == "" || c.Nullable == nil || c.Primary == nil || seen[c.Name] {
			return nil, contract.ErrUnsupported
		}
		seen[c.Name] = true
		col := contract.StructureColumn{Name: c.Name, Type: c.Type, DataType: c.Type, ValueKind: typeKind(c.Type), Position: int64(i + 1), Nullable: *c.Nullable, Default: c.Default, Extra: c.Extra, Comment: c.Comment, NumericPrecision: c.Precision, NumericScale: c.Scale, CharacterMaxLength: c.Length}
		if *c.Primary {
			col.Key = "PRI"
		}
		col.Generated = c.Extra != ""
		if c.Precision != nil && c.Scale != nil && (c.Type == "NUMERIC" || c.Type == "DECIMAL") {
			col.Type = fmt.Sprintf("%s(%d,%d)", c.Type, *c.Precision, *c.Scale)
		} else if c.Length != nil && *c.Length > 0 {
			col.Type = fmt.Sprintf("%s(%d)", c.Type, *c.Length)
		}
		t.Columns = append(t.Columns, col)
	}
	if o.Kind == "table" {
		if err := nativeJSON(ctx, session, "get_table_ddl", params, &t.Definition); err != nil {
			return nil, err
		}
		var indexes []nativeIndex
		if err := nativeJSON(ctx, session, "list_indexes", params, &indexes); err != nil {
			return nil, err
		}
		for _, index := range indexes {
			if !sqltext.ValidName(index.Name) || len(index.Columns) == 0 || len(index.Columns) > 512 || index.Unique == nil || index.Primary == nil {
				return nil, contract.ErrUnsupported
			}
			v := contract.StructureIndex{Name: index.Name, Type: index.Type, Unique: *index.Unique}
			if *index.Primary {
				v.Type = "PRIMARY KEY"
			}
			keys := map[string]bool{}
			for i, name := range index.Columns {
				if !seen[name] || keys[name] {
					return nil, contract.ErrUnsupported
				}
				keys[name] = true
				v.Columns = append(v.Columns, contract.StructureIndexColumn{Name: name, Position: int64(i + 1)})
			}
			t.Indexes = append(t.Indexes, v)
		}
	} else {
		var source struct {
			Source, Name, Schema string
			Type                 string `json:"object_type"`
		}
		if err := nativeJSON(ctx, session, "get_object_source", map[string]string{"schema": o.Schema, "name": o.Name, "object_type": "VIEW"}, &source); err != nil {
			return nil, err
		}
		if source.Source == "" || source.Name != o.Name || source.Schema != o.Schema || source.Type != "VIEW" {
			return nil, contract.ErrUnsupported
		}
		t.Definition = "CREATE VIEW " + qualified(o) + " AS " + source.Source + ";"
	}
	if t.Definition == "" {
		return nil, contract.ErrUnsupported
	}
	t.Editable = o.Kind == "table" && !s.profile.H2Options.ReadOnly && len(keyColumns(t)) > 0
	for _, c := range t.Columns {
		if !readable(c) {
			t.Editable = false
		}
	}
	return t, nil
}
