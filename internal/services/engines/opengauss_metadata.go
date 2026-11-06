package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// Catalog vectors are decoded in the adapter, avoiding LATERAL/ORDINALITY and
// driver-specific array values in neutral metadata.
func openGaussNumbers(raw string) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "{") && strings.HasSuffix(raw, "}") {
		raw = raw[1 : len(raw)-1]
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || r == ',' })
	if len(parts) > 10000 {
		return nil, errors.New("catalog vector limit exceeded")
	}
	out := make([]int, len(parts))
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 32767 {
			return nil, errors.New("invalid catalog attribute vector")
		}
		out[i] = n
	}
	return out, nil
}

type openGaussIndex struct {
	id                      int64
	name, kind, definition  string
	unique, primary, unsafe bool
	keyCount                int
	numbers                 []int
}

func openGaussIndexes(ctx context.Context, q queryer, o contract.ObjectRef) ([]openGaussIndex, error) {
	rows, err := q.QueryContext(ctx, `SELECT i.indexrelid,ic.relname,am.amname,i.indisunique,i.indisprimary,i.indnkeyatts,i.indkey::text,
 NOT i.indisvalid OR NOT i.indisready OR NOT i.indisusable OR NOT i.indimmediate OR i.indpred IS NOT NULL OR i.indexprs IS NOT NULL OR am.amname<>'btree'
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_opclass oc JOIN pg_catalog.pg_namespace ns ON ns.oid=oc.opcnamespace WHERE oc.oid=ANY(i.indclass) AND (NOT oc.opcdefault OR ns.nspname<>'pg_catalog')),
 pg_get_indexdef(i.indexrelid)
 FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid=i.indrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 JOIN pg_catalog.pg_class ic ON ic.oid=i.indexrelid JOIN pg_catalog.pg_am am ON am.oid=ic.relam
 WHERE n.nspname=$1 AND c.relname=$2 ORDER BY i.indisprimary DESC,i.indexrelid`, o.Schema, o.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var indexes []openGaussIndex
	for rows.Next() {
		var index openGaussIndex
		var raw string
		if err = rows.Scan(&index.id, &index.name, &index.kind, &index.unique, &index.primary, &index.keyCount, &raw, &index.unsafe, &index.definition); err != nil {
			return nil, err
		}
		index.numbers, err = openGaussNumbers(raw)
		if err != nil {
			return nil, err
		}
		if index.keyCount < 1 || index.keyCount > len(index.numbers) || len(indexes) >= 1000 || len(index.definition) > 1<<20 {
			return nil, errors.New("invalid or excessive index metadata")
		}
		indexes = append(indexes, index)
	}
	return indexes, rows.Err()
}

func openGaussAttributes(ctx context.Context, q queryer, relation int64) (map[int]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT attnum,attname FROM pg_catalog.pg_attribute WHERE attrelid=$1 AND attnum>0 AND NOT attisdropped ORDER BY attnum`, relation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var n int
		var name string
		if err = rows.Scan(&n, &name); err != nil {
			return nil, err
		}
		if len(out) >= 10000 {
			return nil, errors.New("attribute metadata limit exceeded")
		}
		out[n] = name
	}
	return out, rows.Err()
}

func openGaussKey(index openGaussIndex, numbers map[int]string, fields map[string]contract.Field) []string {
	if !index.unique || index.unsafe || index.keyCount > 16 {
		return nil
	}
	var key []string
	for _, number := range index.numbers[:index.keyCount] {
		name, ok := numbers[number]
		field, found := fields[name]
		if !ok || !found || field.Nullable || field.Generated {
			return nil
		}
		key = append(key, name)
	}
	return key
}

func (openGaussDialect) structure(ctx context.Context, s *Session, q queryer, o contract.ObjectRef) (contract.Table, error) {
	t := contract.Table{Object: o}
	if err := s.check(o); err != nil {
		return t, err
	}
	var kind string
	var relation int64
	if err := queryOne(ctx, q, `SELECT c.oid,c.relkind FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2`, []any{o.Schema, o.Name}, &relation, &kind); err != nil {
		return t, err
	}
	if kind == "v" || kind == "m" {
		t.Object.Kind = "view"
	}
	if kind != "r" && kind != "v" && kind != "m" && kind != "f" {
		return t, contract.ErrUnsupported
	}
	rows, err := q.QueryContext(ctx, `SELECT a.attnum,a.attname,format_type(a.atttypid,a.atttypmod),NOT a.attnotnull,COALESCE(d.adgencol<>'',false),CASE WHEN d.adgencol<>'' THEN NULL ELSE pg_get_expr(d.adbin,d.adrelid) END
 FROM pg_catalog.pg_attribute a LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
 WHERE a.attrelid=$1 AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, relation)
	if err != nil {
		return t, err
	}
	numbers := map[int]string{}
	for rows.Next() {
		var n int
		var field contract.Field
		var def sql.NullString
		if err = rows.Scan(&n, &field.Name, &field.DatabaseType, &field.Nullable, &field.Generated, &def); err != nil {
			break
		}
		if len(t.Columns) >= 10000 {
			err = errors.New("column metadata limit exceeded")
			break
		}
		if def.Valid {
			value := def.String
			field.Default = &value
		}
		numbers[n] = field.Name
		t.Columns = append(t.Columns, field)
	}
	err = errors.Join(err, rows.Err())
	rows.Close()
	if err != nil {
		return t, err
	}
	if len(t.Columns) == 0 {
		return t, errors.New("object absent or has no visible fields")
	}
	indexes, err := openGaussIndexes(ctx, q, o)
	if err != nil {
		return t, err
	}
	for _, index := range indexes {
		if len(t.Key) == 0 {
			t.Key = openGaussKey(index, numbers, validColumns(t))
			if len(t.Key) > 0 && index.primary {
				for i := range t.Columns {
					for _, k := range t.Key {
						if t.Columns[i].Name == k {
							t.Columns[i].Primary = true
						}
					}
				}
			}
		}
		t.Definitions = append(t.Definitions, contract.Definition{Name: index.name, Kind: "index", SQL: index.definition})
	}
	t.Editable = o.Kind == "table" && kind == "r" && len(t.Key) > 0
	rows, err = q.QueryContext(ctx, `SELECT conname,pg_get_constraintdef(oid,true) FROM pg_catalog.pg_constraint WHERE conrelid=$1 ORDER BY conname`, relation)
	if err != nil {
		return t, err
	}
	for rows.Next() {
		var d contract.Definition
		d.Kind = "constraint"
		if err = rows.Scan(&d.Name, &d.SQL); err != nil {
			break
		}
		if len(t.Definitions) >= 2000 || len(d.SQL) > 1<<20 {
			err = errors.New("definition limit exceeded")
			break
		}
		t.Definitions = append(t.Definitions, d)
	}
	err = errors.Join(err, rows.Err())
	rows.Close()
	if err != nil {
		return t, err
	}
	if kind == "r" {
		err = queryOne(ctx, q, `SELECT pg_get_tabledef($1::oid::regclass)`, []any{relation}, &t.Definition)
	} else if kind == "v" || kind == "m" {
		err = queryOne(ctx, q, `SELECT pg_get_viewdef($1::oid,true)`, []any{relation}, &t.Definition)
	}
	if len(t.Definition) > 1<<20 {
		return t, errors.New("definition exceeds 1 MiB")
	}
	return t, err
}

func (s *Session) openGaussPresentationMetadata(ctx context.Context, q queryer, out *contract.TableStructure) error {
	o := out.Object
	var relation int64
	if err := queryOne(ctx, q, `SELECT c.oid,COALESCE(obj_description(c.oid,'pg_class'),'') FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2`, []any{o.Schema, o.Name}, &relation, &out.Comment); err != nil {
		return err
	}
	attributes, err := openGaussAttributes(ctx, q, relation)
	if err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, `SELECT attname,COALESCE(col_description(attrelid,attnum),'') FROM pg_catalog.pg_attribute WHERE attrelid=$1 AND attnum>0 AND NOT attisdropped`, relation)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name, comment string
		if err = rows.Scan(&name, &comment); err != nil {
			break
		}
		for i := range out.Columns {
			if out.Columns[i].Name == name {
				out.Columns[i].Comment = comment
			}
		}
	}
	err = errors.Join(err, rows.Err())
	rows.Close()
	if err != nil {
		return err
	}
	indexes, err := openGaussIndexes(ctx, q, o)
	if err != nil {
		return err
	}
	for _, index := range indexes {
		i := contract.StructureIndex{Name: index.name, Type: index.kind, Unique: index.unique}
		for position, number := range index.numbers {
			name := attributes[number]
			if name == "" {
				if err = queryOne(ctx, q, `SELECT pg_get_indexdef($1::oid,$2::int,true)`, []any{index.id, position + 1}, &name); err != nil {
					return err
				}
			}
			i.Columns = append(i.Columns, contract.StructureIndexColumn{Name: name, Position: int64(position + 1)})
		}
		out.Indexes = append(out.Indexes, i)
	}
	rows, err = q.QueryContext(ctx, `SELECT con.conname,rn.nspname,rc.relname,con.confrelid,con.confupdtype::text,con.confdeltype::text,con.conkey::text,con.confkey::text
 FROM pg_catalog.pg_constraint con JOIN pg_catalog.pg_class rc ON rc.oid=con.confrelid JOIN pg_catalog.pg_namespace rn ON rn.oid=rc.relnamespace
 WHERE con.conrelid=$1 AND con.contype='f' ORDER BY con.conname`, relation)
	if err != nil {
		return err
	}
	type foreign struct {
		key      contract.StructureForeignKey
		relation int64
		from, to []int
	}
	var pending []foreign
	for rows.Next() {
		var f foreign
		var schema, update, delete, from, to string
		if err = rows.Scan(&f.key.Name, &schema, &f.key.ReferencedTable, &f.relation, &update, &delete, &from, &to); err != nil {
			break
		}
		f.from, err = openGaussNumbers(from)
		if err != nil {
			break
		}
		f.to, err = openGaussNumbers(to)
		if err != nil {
			break
		}
		if len(f.from) == 0 || len(f.from) != len(f.to) || len(pending) >= 1000 {
			err = errors.New("invalid or excessive foreign key metadata")
			break
		}
		f.key.ReferencedDatabase = o.Catalog + " / " + schema
		f.key.OnUpdate = postgresForeignAction(update)
		f.key.OnDelete = postgresForeignAction(delete)
		pending = append(pending, f)
	}
	err = errors.Join(err, rows.Err())
	rows.Close()
	if err != nil {
		return err
	}
	cache := map[int64]map[int]string{relation: attributes}
	for _, f := range pending {
		names, ok := cache[f.relation]
		if !ok {
			names, err = openGaussAttributes(ctx, q, f.relation)
			if err != nil {
				return err
			}
			cache[f.relation] = names
		}
		for i, n := range f.from {
			from, to := attributes[n], names[f.to[i]]
			if from == "" || to == "" {
				return errors.New("foreign key attribute identity is unavailable")
			}
			f.key.Columns = append(f.key.Columns, contract.StructureForeignKeyColumn{Name: from, ReferencedName: to, Position: int64(i + 1)})
		}
		out.ForeignKeys = append(out.ForeignKeys, f.key)
	}
	return nil
}

func openGaussImportKeys(ctx context.Context, q queryer, t contract.Table) ([][]string, error) {
	var relation int64
	if err := queryOne(ctx, q, `SELECT c.oid FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2`, []any{t.Object.Schema, t.Object.Name}, &relation); err != nil {
		return nil, err
	}
	attributes, err := openGaussAttributes(ctx, q, relation)
	if err != nil {
		return nil, err
	}
	indexes, err := openGaussIndexes(ctx, q, t.Object)
	if err != nil {
		return nil, err
	}
	var keys [][]string
	for _, index := range indexes {
		if key := openGaussKey(index, attributes, validColumns(t)); len(key) > 0 {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func openGaussRelationIdentity(ctx context.Context, q queryer, t contract.Table) (string, error) {
	var relation int64
	if err := queryOne(ctx, q, `SELECT c.oid FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2`, []any{t.Object.Schema, t.Object.Name}, &relation); err != nil {
		return "", err
	}
	return fmt.Sprint(relation), nil
}
