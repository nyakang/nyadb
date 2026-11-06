package engines

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type cockroachIndex struct {
	name                    string
	unique, primary, unsafe bool
	columns                 []contract.StructureIndexColumn
}

func cockroachIndexes(ctx context.Context, q queryer, o contract.ObjectRef) ([]cockroachIndex, error) {
	flags := map[string]cockroachIndex{}
	rows, err := q.QueryContext(ctx, `SELECT ic.relname,i.indisprimary,NOT i.indisvalid OR i.indpred IS NOT NULL OR i.indexprs IS NOT NULL FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid=i.indrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace JOIN pg_catalog.pg_class ic ON ic.oid=i.indexrelid WHERE n.nspname=$1 AND c.relname=$2`, o.Schema, o.Name)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var f cockroachIndex
		if err = rows.Scan(&f.name, &f.primary, &f.unsafe); err != nil {
			break
		}
		if len(flags) >= 1000 {
			err = errors.New("index metadata limit exceeded")
			break
		}
		flags[f.name] = f
	}
	err = errors.Join(err, rows.Err())
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = q.QueryContext(ctx, "SELECT index_name,NOT non_unique,seq_in_index,column_name,direction FROM [SHOW INDEXES FROM "+qualified(o)+"] WHERE NOT storing AND NOT implicit ORDER BY index_name,seq_in_index")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var indexes []cockroachIndex
	for rows.Next() {
		var name, column, direction string
		var unique bool
		var position int64
		if err = rows.Scan(&name, &unique, &position, &column, &direction); err != nil {
			return nil, err
		}
		if len(indexes) == 0 || indexes[len(indexes)-1].name != name {
			if len(indexes) >= 1000 {
				return nil, errors.New("index metadata limit exceeded")
			}
			f, ok := flags[name]
			f.name, f.unique = name, unique
			if !ok {
				f.unsafe = true
			}
			indexes = append(indexes, f)
		}
		index := &indexes[len(indexes)-1]
		if len(index.columns) >= 10000 {
			return nil, errors.New("index field limit exceeded")
		}
		index.columns = append(index.columns, contract.StructureIndexColumn{Name: column, Position: position, Direction: direction})
	}
	return indexes, rows.Err()
}
func (s *Session) cockroachStructure(ctx context.Context, q queryer, o contract.ObjectRef) (contract.Table, error) {
	t := contract.Table{Object: o}
	if err := s.check(o); err != nil {
		return t, err
	}
	var kind string
	if err := queryOne(ctx, q, `SELECT table_type FROM information_schema.tables WHERE table_catalog=current_database() AND table_schema=$1 AND table_name=$2`, []any{o.Schema, o.Name}, &kind); err != nil {
		return t, err
	}
	if kind != "BASE TABLE" {
		t.Object.Kind = "view"
	}
	rows, err := q.QueryContext(ctx, "SELECT column_name,data_type,is_nullable,column_default,generation_expression FROM [SHOW COLUMNS FROM "+qualified(o)+"] WHERE NOT is_hidden")
	if err != nil {
		return t, err
	}
	for rows.Next() {
		var f contract.Field
		var def, generated sql.NullString
		if err = rows.Scan(&f.Name, &f.DatabaseType, &f.Nullable, &def, &generated); err != nil {
			break
		}
		f.DatabaseType = cockroachDataType(f.DatabaseType)
		f.Generated = generated.Valid && generated.String != ""
		if def.Valid {
			v := def.String
			f.Default = &v
		}
		if len(t.Columns) >= 10000 {
			err = errors.New("column metadata limit exceeded")
			break
		}
		t.Columns = append(t.Columns, f)
	}
	err = errors.Join(err, rows.Err())
	rows.Close()
	if err != nil {
		return t, err
	}
	if len(t.Columns) == 0 {
		return t, errors.New("object has no visible fields")
	}
	if kind == "BASE TABLE" {
		indexes, e := cockroachIndexes(ctx, q, o)
		if e != nil {
			return t, e
		}
		cols := validColumns(t)
		for _, i := range indexes {
			if !i.unique || i.unsafe || len(i.columns) == 0 {
				continue
			}
			valid := true
			var key []string
			for _, c := range i.columns {
				f, ok := cols[c.Name]
				valid = valid && ok && !f.Nullable && !strings.Contains(f.DatabaseType, "collate")
				key = append(key, c.Name)
			}
			if valid && (len(t.Key) == 0 || i.primary) {
				t.Key = key
				for j := range t.Columns {
					t.Columns[j].Primary = false
					for _, n := range key {
						if t.Columns[j].Name == n {
							t.Columns[j].Primary = i.primary
						}
					}
				}
				if i.primary {
					break
				}
			}
		}
	}
	t.Editable = o.Kind == "table" && kind == "BASE TABLE" && len(t.Key) > 0
	t.Definition, err = cockroachCreate(ctx, q, o)
	if err != nil {
		return t, err
	}
	t.Definitions = []contract.Definition{{Name: o.Name, Kind: t.Object.Kind, SQL: t.Definition}}
	return t, nil
}
func (s *Session) cockroachPresentationMetadata(ctx context.Context, q queryer, out *contract.TableStructure) error {
	if out.Object.Kind == "view" {
		return nil
	}
	indexes, err := cockroachIndexes(ctx, q, out.Object)
	if err != nil {
		return err
	}
	for _, i := range indexes {
		kind := "INDEX"
		if i.primary {
			kind = "PRIMARY KEY"
		}
		out.Indexes = append(out.Indexes, contract.StructureIndex{Name: i.name, Type: kind, Unique: i.unique, Columns: i.columns})
	}
	// PG-compatible constraint arrays are read-only. Native complete DDL above
	// additionally retains STORING, predicates, locality and family clauses.
	o := out.Object
	rows, err := q.QueryContext(ctx, `SELECT con.conname,rn.nspname,rc.relname,con.confupdtype::text,con.confdeltype::text,k.ord,a.attname,ra.attname FROM pg_catalog.pg_constraint con JOIN pg_catalog.pg_class c ON c.oid=con.conrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace JOIN pg_catalog.pg_class rc ON rc.oid=con.confrelid JOIN pg_catalog.pg_namespace rn ON rn.oid=rc.relnamespace CROSS JOIN LATERAL generate_subscripts(con.conkey,1) k(ord) JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum=con.conkey[k.ord] JOIN pg_catalog.pg_attribute ra ON ra.attrelid=rc.oid AND ra.attnum=con.confkey[k.ord] WHERE n.nspname=$1 AND c.relname=$2 AND con.contype='f' ORDER BY con.conname,k.ord`, o.Schema, o.Name)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name, schema, table, update, delete, from, to string
		var position int64
		if err = rows.Scan(&name, &schema, &table, &update, &delete, &position, &from, &to); err != nil {
			return err
		}
		if len(out.ForeignKeys) == 0 || out.ForeignKeys[len(out.ForeignKeys)-1].Name != name {
			if len(out.ForeignKeys) >= 1000 {
				return errors.New("foreign key metadata limit exceeded")
			}
			out.ForeignKeys = append(out.ForeignKeys, contract.StructureForeignKey{Name: name, ReferencedDatabase: o.Catalog + " / " + schema, ReferencedTable: table, OnUpdate: postgresForeignAction(update), OnDelete: postgresForeignAction(delete)})
		}
		fk := &out.ForeignKeys[len(out.ForeignKeys)-1]
		if len(fk.Columns) >= 10000 {
			return errors.New("foreign key field limit exceeded")
		}
		fk.Columns = append(fk.Columns, contract.StructureForeignKeyColumn{Name: from, ReferencedName: to, Position: position})
	}
	return rows.Err()
}
