package cassandra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type dataBinding struct {
	ID, Signature                      string
	Metadata                           *contract.TableStructure
	Names, Keys, Partition, Clustering []string
	Static                             map[string]bool
}

// Metadata is a before/after observation of the original node, not a distributed
// schema lock. The signature includes native table/view UUID and its dependencies.
func (s *Session) readDataBinding(ctx context.Context, c *Client, o contract.ObjectRef) (*dataBinding, error) {
	before, err := s.validateNode(ctx, c)
	if err != nil {
		return nil, err
	}
	ks, err := readKeyspace(ctx, c, o.Catalog)
	if err != nil {
		return nil, err
	}
	row, err := readObject(ctx, c, o)
	if err != nil {
		return nil, err
	}
	columns, err := schemaRows(ctx, c, "SELECT * FROM system_schema.columns WHERE keyspace_name = ? AND table_name = ?", o.Catalog, o.Name)
	if err != nil {
		return nil, err
	}
	if len(columns) < 1 || len(columns) > MaxColumns {
		return nil, ErrBounds
	}
	indexes, err := schemaRows(ctx, c, "SELECT * FROM system_schema.indexes WHERE keyspace_name = ? AND table_name = ?", o.Catalog, o.Name)
	if err != nil {
		return nil, err
	}
	types, err := schemaRows(ctx, c, "SELECT * FROM system_schema.types WHERE keyspace_name = ?", o.Catalog)
	if err != nil {
		return nil, err
	}
	deps, err := referencedTypes(columns, types, o.Catalog)
	if err != nil {
		return nil, err
	}
	var base schemaRow
	if o.Kind == "view" {
		name, e := schemaName(row, "base_table_name")
		if e != nil {
			return nil, e
		}
		base, err = readObject(ctx, c, o.Namespace().Object("table", name))
		if err != nil {
			return nil, err
		}
		baseID, e := schemaText(base, "id")
		viewBaseID, e2 := schemaText(row, "base_table_id")
		if e != nil || e2 != nil || baseID != viewBaseID {
			return nil, ErrDataTargetChanged
		}
	}
	again, err := readObject(ctx, c, o)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(row, again) {
		return nil, ErrMetadataChanged
	}
	after, err := s.validateNode(ctx, c)
	if err != nil {
		return nil, err
	}
	if before.SchemaVersion != after.SchemaVersion {
		return nil, ErrMetadataChanged
	}
	m, err := schemaStructure(o, row, columns, indexes, s.identity)
	if err != nil {
		return nil, err
	}
	b := &dataBinding{Metadata: m, Static: map[string]bool{}}
	b.ID, err = schemaText(row, "id")
	if err != nil {
		return nil, err
	}
	for _, col := range m.Columns {
		b.Names = append(b.Names, col.Name)
		switch col.Key {
		case "partition_key":
			b.Partition = append(b.Partition, col.Name)
			b.Keys = append(b.Keys, col.Name)
		case "clustering":
			b.Clustering = append(b.Clustering, col.Name)
			b.Keys = append(b.Keys, col.Name)
		}
	}
	for _, col := range columns {
		kind, e := schemaText(col, "kind")
		name, e2 := schemaName(col, "column_name")
		if e != nil || e2 != nil {
			return nil, ErrProtocol
		}
		if kind == "static" {
			b.Static[name] = true
		}
	}
	// Canonicalize row lists without modifying the server-owned observations.
	canonical := func(rows []schemaRow, field string) ([]string, error) {
		out := make([]string, 0, len(rows))
		ordered := append([]schemaRow(nil), rows...)
		sort.Slice(ordered, func(i, j int) bool { return ordered[i][field].Text < ordered[j][field].Text })
		for _, r := range ordered {
			raw, e := rowDefinition(r)
			if e != nil {
				return nil, e
			}
			out = append(out, raw)
		}
		return out, nil
	}
	cols, err := canonical(columns, "column_name")
	if err != nil {
		return nil, err
	}
	idx, err := canonical(indexes, "index_name")
	if err != nil {
		return nil, err
	}
	udts, err := canonical(deps, "type_name")
	if err != nil {
		return nil, err
	}
	ksDef, err := rowDefinition(ks)
	if err != nil {
		return nil, err
	}
	baseDef := ""
	if base != nil {
		baseDef, err = rowDefinition(base)
		if err != nil {
			return nil, err
		}
	}
	raw, _ := json.Marshal(struct {
		Object                     contract.ObjectRef
		Keyspace, Definition, Base string
		Columns, Indexes, Types    []string
	}{o, ksDef, m.Definition, baseDef, cols, idx, udts})
	if len(raw) > 16<<20 {
		return nil, ErrBounds
	}
	h := sha256.Sum256(raw)
	b.Signature = hex.EncodeToString(h[:])
	return b, ctx.Err()
}
func (l *dataLease) guard(ctx context.Context, c *Client) (*dataBinding, error) {
	l.bindingMu.Lock()
	defer l.bindingMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := l.session.readDataBinding(ctx, c, l.object)
	if err != nil {
		if errors.Is(err, ErrDataTargetChanged) {
			l.mu.Lock()
			l.changed = true
			l.cancel()
			l.mu.Unlock()
		}
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.lost {
		return nil, contract.ErrRevoked
	}
	if l.changed {
		return nil, ErrDataTargetChanged
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if l.binding == nil {
		l.binding = b
	} else if l.binding.Signature != b.Signature {
		l.changed = true
		l.cancel()
		return nil, ErrDataTargetChanged
	}
	return l.binding, nil
}

// system_schema expresses UDT dependencies in CQL type text. Lex identifiers
// rather than substring-match names, preserving quoted names and frozen nesting.
func referencedTypes(columns, types []schemaRow, keyspace string) ([]schemaRow, error) {
	byName := map[string]schemaRow{}
	for _, row := range types {
		ks, e := schemaText(row, "keyspace_name")
		name, e2 := schemaName(row, "type_name")
		if e != nil || e2 != nil || ks != keyspace {
			return nil, ErrProtocol
		}
		if _, ok := byName[name]; ok {
			return nil, ErrProtocol
		}
		byName[name] = row
	}
	builtins := map[string]bool{}
	for _, name := range strings.Fields("ascii bigint blob boolean counter decimal double float int text timestamp uuid varchar varint timeuuid inet date time smallint tinyint duration list map set tuple frozen vector") {
		builtins[name] = true
	}
	seen := map[string]bool{}
	var out []schemaRow
	var visit func(string, int) error
	visit = func(source string, depth int) error {
		if depth > maxDepth || len(source) > 65535 {
			return ErrBounds
		}
		for i := 0; i < len(source); {
			quoted := source[i] == '"'
			var name string
			if quoted {
				i++
				var b strings.Builder
				closed := false
				for i < len(source) {
					if source[i] == '"' {
						i++
						if i < len(source) && source[i] == '"' {
							b.WriteByte('"')
							i++
							continue
						}
						closed = true
						break
					}
					b.WriteByte(source[i])
					i++
				}
				if !closed {
					return ErrProtocol
				}
				name = b.String()
			} else if source[i] >= 'a' && source[i] <= 'z' || source[i] >= 'A' && source[i] <= 'Z' || source[i] == '_' {
				start := i
				i++
				for i < len(source) && (source[i] >= 'a' && source[i] <= 'z' || source[i] >= 'A' && source[i] <= 'Z' || source[i] >= '0' && source[i] <= '9' || source[i] == '_') {
					i++
				}
				name = strings.ToLower(source[start:i])
			} else {
				i++
				continue
			}
			j := i
			for j < len(source) && source[j] == ' ' {
				j++
			}
			if j < len(source) && source[j] == '.' {
				if name != keyspace {
					return contract.ErrUnsupported
				}
				continue
			}
			if !quoted && builtins[name] {
				continue
			}
			row, ok := byName[name]
			if !ok || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, row)
			fieldTypes := row["field_types"]
			if fieldTypes.Type == nil || fieldTypes.Type.Code != 0x20 || fieldTypes.Null {
				return ErrProtocol
			}
			for _, field := range fieldTypes.Items {
				typ, e := schemaText(schemaRow{"v": field}, "v")
				if e != nil {
					return e
				}
				if e = visit(typ, depth+1); e != nil {
					return e
				}
			}
		}
		return nil
	}
	for _, col := range columns {
		typ, err := schemaText(col, "type")
		if err != nil {
			return nil, err
		}
		if err = visit(typ, 0); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func cloneStructure(v *contract.TableStructure) *contract.TableStructure {
	raw, _ := json.Marshal(v)
	var out contract.TableStructure
	_ = json.Unmarshal(raw, &out)
	return &out
}
