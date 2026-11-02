package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// Presentation-only catalog reads do not affect the grid's edit-key choice.
// Keep expression/index order and every FK member; no user rows are read.
func (s *Session) sqlitePresentationMetadata(ctx context.Context, q queryer, out *contract.TableStructure) error {
	o := out.Object
	rows, err := q.QueryContext(ctx, "PRAGMA "+quote(o.Schema)+".index_list("+quote(o.Name)+")")
	if err != nil {
		return err
	}
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err = rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			return err
		}
		if len(out.Indexes) >= 1000 {
			rows.Close()
			return errors.New("structure index limit exceeded")
		}
		kind := "INDEX"
		if partial != 0 {
			kind = "PARTIAL INDEX"
		}
		out.Indexes = append(out.Indexes, contract.StructureIndex{Name: name, Type: kind, Unique: unique != 0})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for i := range out.Indexes {
		index := &out.Indexes[i]
		rows, err = q.QueryContext(ctx, "PRAGMA "+quote(o.Schema)+".index_xinfo("+quote(index.Name)+")")
		if err != nil {
			return err
		}
		for rows.Next() {
			var seq, cid, desc, key int
			var name, collation sql.NullString
			if err = rows.Scan(&seq, &cid, &name, &desc, &collation, &key); err != nil {
				rows.Close()
				return err
			}
			if key == 0 {
				continue
			}
			if len(index.Columns) >= 10000 {
				rows.Close()
				return errors.New("structure index field limit exceeded")
			}
			value := name.String
			if !name.Valid {
				value = "<expression>"
				if cid == -1 {
					value = "<rowid>"
				}
			}
			direction := "ASC"
			if desc != 0 {
				direction = "DESC"
			}
			index.Columns = append(index.Columns, contract.StructureIndexColumn{Name: value, Position: int64(seq + 1), Direction: direction})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	rows, err = q.QueryContext(ctx, "PRAGMA "+quote(o.Schema)+".foreign_key_list("+quote(o.Name)+")")
	if err != nil {
		return err
	}
	positions := map[int]int{}
	for rows.Next() {
		var id, seq int
		var table, from, onUpdate, onDelete, match string
		var to sql.NullString
		if err = rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			rows.Close()
			return err
		}
		i, found := positions[id]
		if !found {
			if len(out.ForeignKeys) >= 1000 {
				rows.Close()
				return errors.New("structure foreign key limit exceeded")
			}
			i = len(out.ForeignKeys)
			positions[id] = i
			out.ForeignKeys = append(out.ForeignKeys, contract.StructureForeignKey{Name: fmt.Sprintf("foreign_key_%d", id), ReferencedDatabase: o.Schema, ReferencedTable: table, OnUpdate: onUpdate, OnDelete: onDelete})
		}
		if len(out.ForeignKeys[i].Columns) >= 10000 {
			rows.Close()
			return errors.New("structure foreign key field limit exceeded")
		}
		target := to.String
		if !to.Valid {
			target = "<implicit primary key>"
		}
		out.ForeignKeys[i].Columns = append(out.ForeignKeys[i].Columns, contract.StructureForeignKeyColumn{Name: from, ReferencedName: target, Position: int64(seq + 1)})
	}
	err = rows.Err()
	rows.Close()
	return err
}

func (s *Session) postgresPresentationMetadata(ctx context.Context, q queryer, out *contract.TableStructure) error {
	o := out.Object
	rows, err := q.QueryContext(ctx, `SELECT COALESCE(obj_description(c.oid,'pg_class'),''),a.attname,COALESCE(col_description(c.oid,a.attnum),'') FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped WHERE n.nspname=$1 AND c.relname=$2 ORDER BY a.attnum`, o.Schema, o.Name)
	if err != nil {
		return err
	}
	columns := map[string]int{}
	for i, c := range out.Columns {
		columns[c.Name] = i
	}
	for rows.Next() {
		var name sql.NullString
		var comment string
		if err = rows.Scan(&out.Comment, &name, &comment); err != nil {
			rows.Close()
			return err
		}
		if i, found := columns[name.String]; name.Valid && found {
			out.Columns[i].Comment = comment
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = q.QueryContext(ctx, `SELECT ic.relname,am.amname,i.indisunique,k.ord,pg_get_indexdef(i.indexrelid,k.ord::int,true) FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid=i.indrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace JOIN pg_catalog.pg_class ic ON ic.oid=i.indexrelid JOIN pg_catalog.pg_am am ON am.oid=ic.relam CROSS JOIN LATERAL generate_series(1,i.indnatts) k(ord) WHERE n.nspname=$1 AND c.relname=$2 ORDER BY ic.relname,k.ord`, o.Schema, o.Name)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name, kind, field string
		var unique bool
		var position int64
		if err = rows.Scan(&name, &kind, &unique, &position, &field); err != nil {
			rows.Close()
			return err
		}
		if len(out.Indexes) == 0 || out.Indexes[len(out.Indexes)-1].Name != name {
			if len(out.Indexes) >= 1000 {
				rows.Close()
				return errors.New("structure index limit exceeded")
			}
			out.Indexes = append(out.Indexes, contract.StructureIndex{Name: name, Type: kind, Unique: unique})
		}
		index := &out.Indexes[len(out.Indexes)-1]
		if len(index.Columns) >= 10000 {
			rows.Close()
			return errors.New("structure index field limit exceeded")
		}
		index.Columns = append(index.Columns, contract.StructureIndexColumn{Name: field, Position: position})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = q.QueryContext(ctx, `SELECT con.conname,rn.nspname,rc.relname,con.confupdtype::text,con.confdeltype::text,k.ord,a.attname,ra.attname FROM pg_catalog.pg_constraint con JOIN pg_catalog.pg_class c ON c.oid=con.conrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace JOIN pg_catalog.pg_class rc ON rc.oid=con.confrelid JOIN pg_catalog.pg_namespace rn ON rn.oid=rc.relnamespace CROSS JOIN LATERAL generate_subscripts(con.conkey,1) k(ord) JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum=con.conkey[k.ord] JOIN pg_catalog.pg_attribute ra ON ra.attrelid=rc.oid AND ra.attnum=con.confkey[k.ord] WHERE n.nspname=$1 AND c.relname=$2 AND con.contype='f' ORDER BY con.conname,k.ord`, o.Schema, o.Name)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name, schema, table, update, delete, from, to string
		var position int64
		if err = rows.Scan(&name, &schema, &table, &update, &delete, &position, &from, &to); err != nil {
			rows.Close()
			return err
		}
		if len(out.ForeignKeys) == 0 || out.ForeignKeys[len(out.ForeignKeys)-1].Name != name {
			if len(out.ForeignKeys) >= 1000 {
				rows.Close()
				return errors.New("structure foreign key limit exceeded")
			}
			out.ForeignKeys = append(out.ForeignKeys, contract.StructureForeignKey{Name: name, ReferencedDatabase: o.Catalog + " / " + schema, ReferencedTable: table, OnUpdate: postgresForeignAction(update), OnDelete: postgresForeignAction(delete)})
		}
		fk := &out.ForeignKeys[len(out.ForeignKeys)-1]
		if len(fk.Columns) >= 10000 {
			rows.Close()
			return errors.New("structure foreign key field limit exceeded")
		}
		fk.Columns = append(fk.Columns, contract.StructureForeignKeyColumn{Name: from, ReferencedName: to, Position: position})
	}
	err = rows.Err()
	rows.Close()
	return err
}
func postgresForeignAction(value string) string {
	switch value {
	case "a":
		return "NO ACTION"
	case "r":
		return "RESTRICT"
	case "c":
		return "CASCADE"
	case "n":
		return "SET NULL"
	case "d":
		return "SET DEFAULT"
	}
	return value
}
