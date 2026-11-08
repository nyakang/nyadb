package cassandra

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type schemaRow map[string]Value

func validObjectName(name string) bool {
	return name != "" && len(name) <= 1024 && utf8.ValidString(name) && !strings.ContainsRune(name, 0)
}
func (s *Session) checkObject(o contract.ObjectRef) error {
	if err := s.checkNamespace(o.Namespace()); err != nil {
		return err
	}
	if !validObjectName(o.Name) {
		return ErrInvalid
	}
	if o.Kind != "table" && o.Kind != "view" && o.Kind != "type" {
		return contract.ErrUnsupported
	}
	return nil
}

// Only package-owned fixed system_schema SELECTs reach this helper. Native
// parameter bytes preserve exact names; pagination stays on the original client.
func schemaRows(ctx context.Context, c *Client, statement string, names ...string) ([]schemaRow, error) {
	req := Request{Statement: statement, PageSize: 128}
	for _, name := range names {
		req.Parameters = append(req.Parameters, Parameter{Bytes: []byte(name)})
	}
	out := []schemaRow{}
	budget := 16 << 20
	var columns []Column
	for pages := 0; ; pages++ {
		if pages >= 1024 {
			return nil, ErrBounds
		}
		r, err := c.execute(ctx, req, true)
		if err != nil {
			return nil, err
		}
		if r.Kind != 2 {
			return nil, ErrProtocol
		}
		if pages > 0 && !reflect.DeepEqual(columns, r.Columns) {
			return nil, ErrMetadataChanged
		}
		columns = r.Columns
		for _, row := range r.Rows {
			if len(out) >= 8192 || len(row) != len(columns) {
				return nil, ErrBounds
			}
			item := schemaRow{}
			for i, col := range columns {
				if col.Keyspace != "system_schema" || col.Name == "" {
					return nil, ErrProtocol
				}
				if _, exists := item[col.Name]; exists {
					return nil, ErrProtocol
				}
				budget -= len(row[i].Raw) + len(col.Name) + 128
				if budget < 0 {
					return nil, ErrBounds
				}
				item[col.Name] = row[i]
			}
			out = append(out, item)
		}
		if r.Next == nil {
			return out, nil
		}
		if req.Cursor != nil && string(req.Cursor.state) == string(r.Next.state) {
			return nil, ErrProtocol
		}
		req.Cursor = r.Next.Clone()
	}
}
func schemaText(row schemaRow, name string) (string, error) {
	v, ok := row[name]
	if !ok || v.Null || v.Missing || v.Type == nil {
		return "", ErrProtocol
	}
	switch v.Type.Code {
	case 1, 10, 13, 12:
		return v.Text, nil
	}
	return "", ErrProtocol
}
func schemaName(row schemaRow, name string) (string, error) {
	s, err := schemaText(row, name)
	if err != nil || !validObjectName(s) {
		return "", ErrProtocol
	}
	return s, nil
}
func rowDefinition(row schemaRow) (string, error) {
	budget := 16 << 20
	out := map[string]encodedValue{}
	for name, value := range row {
		budget -= len(name) * 6
		v, err := encodeValue(value, &budget)
		if err != nil {
			return "", err
		}
		out[name] = v
	}
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > 16<<20 {
		return "", ErrBounds
	}
	return string(raw), nil
}
func readKeyspace(ctx context.Context, c *Client, name string) (schemaRow, error) {
	rows, err := schemaRows(ctx, c, "SELECT * FROM system_schema.keyspaces WHERE keyspace_name = ?", name)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, ErrMetadataChanged
	}
	actual, err := schemaText(rows[0], "keyspace_name")
	if err != nil || actual != name {
		return nil, ErrProtocol
	}
	return rows[0], nil
}

func (s *Session) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
	var out []contract.Namespace
	err := s.withMetadata(ctx, func(ctx context.Context, c *Client) error {
		rows, err := schemaRows(ctx, c, "SELECT * FROM system_schema.keyspaces")
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, row := range rows {
			name, err := schemaText(row, "keyspace_name")
			if err != nil || !validKeyspace(name) || seen[name] {
				return ErrProtocol
			}
			seen[name] = true
			out = append(out, contract.Namespace{Ref: s.Namespace(name), Label: name})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, nil
}

var browseSources = [...]struct{ table, column, kind, caption string }{
	{"tables", "table_name", "table", "表"},
	{"views", "view_name", "view", "视图"},
	{"types", "type_name", "type", "类型"},
}

func (s *Session) Browse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	if err := s.checkNamespace(n); err != nil {
		return nil, err
	}
	out := []contract.DBObject{}
	err := s.withMetadata(ctx, func(ctx context.Context, c *Client) error {
		if _, err := readKeyspace(ctx, c, n.Catalog); err != nil {
			return err
		}
		budget := 16 << 20
		for _, source := range browseSources {
			rows, err := schemaRows(ctx, c, "SELECT * FROM system_schema."+source.table+" WHERE keyspace_name = ?", n.Catalog)
			if err != nil {
				return err
			}
			seen := map[string]bool{}
			for _, row := range rows {
				ks, err := schemaText(row, "keyspace_name")
				if err != nil || ks != n.Catalog {
					return ErrProtocol
				}
				name, err := schemaName(row, source.column)
				if err != nil || seen[name] {
					return ErrProtocol
				}
				seen[name] = true
				definition, err := rowDefinition(row)
				if err != nil {
					return err
				}
				budget -= len(definition) + 256
				if budget < 0 || len(out) >= 8192 {
					return ErrBounds
				}
				v := contract.DBObject{Ref: n.Object(source.kind, name), Name: name, Kind: source.caption, Engine: "Cassandra", Definition: definition}
				if comment, ok := row["comment"]; ok && !comment.Null {
					v.Comment = comment.Text
				}
				out = append(out, v)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ref.Kind != out[j].Ref.Kind {
			return out[i].Ref.Kind < out[j].Ref.Kind
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func readObject(ctx context.Context, c *Client, o contract.ObjectRef) (schemaRow, error) {
	var table, field string
	switch o.Kind {
	case "table":
		table, field = "tables", "table_name"
	case "view":
		table, field = "views", "view_name"
	case "type":
		table, field = "types", "type_name"
	default:
		return nil, contract.ErrUnsupported
	}
	rows, err := schemaRows(ctx, c, "SELECT * FROM system_schema."+table+" WHERE keyspace_name = ? AND "+field+" = ?", o.Catalog, o.Name)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, ErrMetadataChanged
	}
	ks, err := schemaText(rows[0], "keyspace_name")
	if err != nil || ks != o.Catalog {
		return nil, ErrProtocol
	}
	name, err := schemaName(rows[0], field)
	if err != nil || name != o.Name {
		return nil, ErrProtocol
	}
	if o.Kind != "type" {
		id := rows[0]["id"]
		if id.Type == nil || id.Type.Code != 12 || id.Null || len(id.Text) != 36 {
			return nil, ErrProtocol
		}
	}
	return rows[0], nil
}

func (s *Session) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	if err := s.checkObject(o); err != nil {
		return nil, err
	}
	out := &contract.TableStructure{Object: o, Database: o.Catalog, Name: o.Name, Kind: o.Kind, Engine: "Cassandra"}
	err := s.withMetadata(ctx, func(ctx context.Context, c *Client) error {
		row, err := readObject(ctx, c, o)
		if err != nil {
			return err
		}
		out.Definition, err = rowDefinition(row)
		if err != nil {
			return err
		}
		budget := (16 << 20) - len(out.Definition)
		add := func(name, kind string, row schemaRow) error {
			raw, err := rowDefinition(row)
			if err != nil {
				return err
			}
			budget -= len(raw) + 128
			if budget < 0 {
				return ErrBounds
			}
			out.Definitions = append(out.Definitions, contract.Definition{Name: name, Kind: kind, SQL: raw})
			return nil
		}
		if comment, ok := row["comment"]; ok && !comment.Null {
			out.Comment = comment.Text
		}
		if o.Kind == "type" {
			out.Columns, err = typeColumns(row)
			if err != nil {
				return err
			}
		} else {
			columns, err := schemaRows(ctx, c, "SELECT * FROM system_schema.columns WHERE keyspace_name = ? AND table_name = ?", o.Catalog, o.Name)
			if err != nil {
				return err
			}
			out.Columns, out.Indexes, err = tableColumns(columns, o)
			if err != nil {
				return err
			}
			for _, col := range columns {
				name, _ := schemaText(col, "column_name")
				if err = add(name, "column", col); err != nil {
					return err
				}
			}
			indexes, err := schemaRows(ctx, c, "SELECT * FROM system_schema.indexes WHERE keyspace_name = ? AND table_name = ?", o.Catalog, o.Name)
			if err != nil {
				return err
			}
			seen := map[string]bool{}
			for _, index := range indexes {
				ks, e := schemaText(index, "keyspace_name")
				table, e2 := schemaText(index, "table_name")
				name, e3 := schemaName(index, "index_name")
				kind, e4 := schemaText(index, "kind")
				if e != nil || e2 != nil || e3 != nil || e4 != nil || ks != o.Catalog || table != o.Name || seen[name] {
					return ErrProtocol
				}
				seen[name] = true
				out.Indexes = append(out.Indexes, contract.StructureIndex{Name: name, Type: kind})
				if err = add(name, "index", index); err != nil {
					return err
				}
			}
		}
		again, err := readObject(ctx, c, o)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(row, again) {
			return ErrMetadataChanged
		}
		raw, _ := json.Marshal(s.identity)
		out.Definitions = append(out.Definitions, contract.Definition{Name: o.Name, Kind: "original-node-observation", SQL: string(raw)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func schemaValueKind(typ string) contract.CellKind {
	switch typ {
	case "bigint", "counter", "int", "varint", "smallint", "tinyint":
		return contract.Integer
	case "blob":
		return contract.Binary
	case "boolean":
		return contract.Boolean
	case "decimal":
		return contract.Decimal
	case "float", "double":
		return contract.Floating
	case "timestamp", "date", "time":
		return contract.DateTime
	}
	if strings.HasPrefix(typ, "list<") || strings.HasPrefix(typ, "set<") || strings.HasPrefix(typ, "map<") || strings.HasPrefix(typ, "tuple<") || strings.HasPrefix(typ, "frozen<") {
		return contract.JSON
	}
	return contract.Text
}
func tableColumns(rows []schemaRow, o contract.ObjectRef) ([]contract.StructureColumn, []contract.StructureIndex, error) {
	type field struct {
		col            contract.StructureColumn
		nativePosition int
		direction      string
		rank           int
	}
	var fields []field
	seen := map[string]bool{}
	ranks := map[string]int{"partition_key": 0, "clustering": 1, "static": 2, "regular": 3}
	positions := [2]map[int]bool{{}, {}}
	for _, row := range rows {
		ks, e := schemaText(row, "keyspace_name")
		table, e2 := schemaText(row, "table_name")
		name, e3 := schemaName(row, "column_name")
		typ, e4 := schemaText(row, "type")
		kind, e5 := schemaText(row, "kind")
		direction, e6 := schemaText(row, "clustering_order")
		pos := row["position"]
		if e != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil || ks != o.Catalog || table != o.Name || seen[name] || typ == "" || pos.Type == nil || pos.Type.Code != 9 || pos.Null {
			return nil, nil, ErrProtocol
		}
		rank, ok := ranks[kind]
		n, err := strconv.Atoi(pos.Text)
		if !ok || err != nil || n < -1 || n > 8192 {
			return nil, nil, ErrProtocol
		}
		if rank < 2 {
			if n < 0 || positions[rank][n] {
				return nil, nil, ErrProtocol
			}
			positions[rank][n] = true
		} else if n != -1 {
			return nil, nil, ErrProtocol
		}
		if kind == "clustering" {
			if direction != "asc" && direction != "desc" {
				return nil, nil, ErrProtocol
			}
		} else if direction != "none" {
			return nil, nil, ErrProtocol
		}
		seen[name] = true
		raw, err := rowDefinition(row)
		if err != nil {
			return nil, nil, err
		}
		key := ""
		if rank < 2 {
			key = kind
		}
		fields = append(fields, field{contract.StructureColumn{Name: name, Type: typ, DataType: typ, ValueKind: schemaValueKind(typ), Key: key, Nullable: rank >= 2, Extra: raw}, n, direction, rank})
	}
	if len(positions[0]) == 0 {
		return nil, nil, ErrProtocol
	}
	for _, group := range positions {
		for n := 0; n < len(group); n++ {
			if !group[n] {
				return nil, nil, ErrProtocol
			}
		}
	}
	sort.Slice(fields, func(i, j int) bool {
		if fields[i].rank != fields[j].rank {
			return fields[i].rank < fields[j].rank
		}
		if fields[i].nativePosition != fields[j].nativePosition {
			return fields[i].nativePosition < fields[j].nativePosition
		}
		return fields[i].col.Name < fields[j].col.Name
	})
	columns := []contract.StructureColumn{}
	pk := contract.StructureIndex{Name: "PRIMARY KEY", Type: "CQL partition/clustering key", Unique: true}
	for i, f := range fields {
		f.col.Position = int64(i + 1)
		columns = append(columns, f.col)
		if f.rank < 2 {
			pk.Columns = append(pk.Columns, contract.StructureIndexColumn{Name: f.col.Name, Direction: f.direction, Position: int64(len(pk.Columns) + 1)})
		}
	}
	return columns, []contract.StructureIndex{pk}, nil
}
func typeColumns(row schemaRow) ([]contract.StructureColumn, error) {
	names, types := row["field_names"], row["field_types"]
	if names.Type == nil || types.Type == nil || names.Type.Code != 0x20 || types.Type.Code != 0x20 || names.Null || types.Null || len(names.Items) != len(types.Items) || len(names.Items) > 8192 {
		return nil, ErrProtocol
	}
	out := []contract.StructureColumn{}
	seen := map[string]bool{}
	for i, name := range names.Items {
		n, err := schemaName(schemaRow{"v": name}, "v")
		typ, e2 := schemaText(schemaRow{"v": types.Items[i]}, "v")
		if err != nil || e2 != nil || typ == "" || seen[n] {
			return nil, ErrProtocol
		}
		seen[n] = true
		out = append(out, contract.StructureColumn{Name: n, Type: typ, DataType: typ, ValueKind: schemaValueKind(typ), Nullable: true, Position: int64(i + 1)})
	}
	return out, nil
}
