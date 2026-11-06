package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func validName(v string) bool {
	return v != "" && utf8.ValidString(v) && len([]byte(v)) <= 128 && strings.IndexFunc(v, unicode.IsControl) < 0
}
func quote(v string) string                 { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
func qualified(o contract.ObjectRef) string { return quote(o.Schema) + "." + quote(o.Name) }
func (s *Session) Namespace(schema string) contract.NamespaceRef {
	return contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: s.Catalog(), Schema: schema}
}
func (s *Session) check(o contract.ObjectRef) error {
	if !s.Alive() {
		return contract.ErrRevoked
	}
	if o.ProfileID != s.ProfileID() || o.Catalog != s.Catalog() || !validName(o.Schema) || !validName(o.Name) {
		return connections.ErrInvalid
	}
	return nil
}
func (s *Session) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
	ctx, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := s.db.QueryContext(ctx, `SELECT username FROM all_users WHERE username=:1 OR username IN (SELECT owner FROM all_objects WHERE object_type IN ('TABLE','VIEW') AND owner NOT IN ('SYS','SYSTEM','XDB','MDSYS','CTXSYS','ORDSYS','WMSYS','DBSNMP')) ORDER BY CASE WHEN username=:2 THEN 0 ELSE 1 END,username`, s.profile.OracleOptions.DefaultSchema, s.profile.OracleOptions.DefaultSchema)
	if err != nil {
		return nil, safeError(err)
	}
	defer rows.Close()
	var out []contract.Namespace
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, safeError(err)
		}
		out = append(out, contract.Namespace{Ref: s.Namespace(name), Label: name})
	}
	return out, safeError(rows.Err())
}
func (s *Session) Browse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	if err := s.check(n.Object("table", "_")); err != nil {
		return nil, err
	}
	ctx, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := s.db.QueryContext(ctx, `SELECT o.object_name,o.object_type,NVL(c.comments,' '),TO_CHAR(o.created,'YYYY-MM-DD"T"HH24:MI:SS'),TO_CHAR(o.last_ddl_time,'YYYY-MM-DD"T"HH24:MI:SS') FROM all_objects o LEFT JOIN all_tab_comments c ON c.owner=o.owner AND c.table_name=o.object_name WHERE o.owner=:1 AND o.subobject_name IS NULL AND o.object_type IN ('TABLE','VIEW','PROCEDURE','FUNCTION','PACKAGE','TRIGGER','SEQUENCE','TYPE') ORDER BY o.object_name,o.object_type`, n.Schema)
	if err != nil {
		return nil, safeError(err)
	}
	var out []contract.DBObject
	for rows.Next() {
		var o contract.DBObject
		var typ string
		if err = rows.Scan(&o.Name, &typ, &o.Comment, &o.CreatedAt, &o.ModifiedAt); err != nil {
			rows.Close()
			return nil, safeError(err)
		}
		kind := strings.ToLower(typ)
		label := map[string]string{"TABLE": "表", "VIEW": "视图", "PROCEDURE": "存储过程", "FUNCTION": "函数", "PACKAGE": "包", "TRIGGER": "触发器", "SEQUENCE": "序列", "TYPE": "类型"}[typ]
		o.Kind = label
		o.Ref = n.Object(kind, o.Name)
		o.Engine = "Oracle"
		o.Comment = strings.TrimSpace(o.Comment)
		out = append(out, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, safeError(err)
	}
	// Source is fetched in one ordered query, after the object cursor is closed,
	// so a one-connection background budget cannot deadlock.
	rows, err = s.db.QueryContext(ctx, `SELECT name,type,text FROM all_source WHERE owner=:1 ORDER BY name,type,line`, n.Schema)
	if err != nil {
		return nil, safeError(err)
	}
	defer rows.Close()
	source := map[string]*strings.Builder{}
	for rows.Next() {
		var name, typ, text string
		if err = rows.Scan(&name, &typ, &text); err != nil {
			return nil, safeError(err)
		}
		key := name + "\x00" + strings.ToLower(typ)
		if source[key] == nil {
			source[key] = new(strings.Builder)
		}
		source[key].WriteString(text)
	}
	for i := range out {
		key := out[i].Name + "\x00" + out[i].Ref.Kind
		if b := source[key]; b != nil {
			out[i].Definition = "CREATE OR REPLACE " + b.String()
			if body := source[out[i].Name+"\x00"+out[i].Ref.Kind+" body"]; body != nil {
				out[i].Definition += "\n/\nCREATE OR REPLACE " + body.String()
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, safeError(err)
	}
	for i := range out {
		if out[i].Ref.Kind == "sequence" {
			ddl, err := s.db.QueryContext(ctx, "SELECT DBMS_METADATA.GET_DDL('SEQUENCE',:1,:2) FROM dual", out[i].Name, n.Schema)
			if err == nil {
				if ddl.Next() {
					_ = ddl.Scan(&out[i].Definition)
				}
				ddl.Close()
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
	}
	return out, nil
}
func (s *Session) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	ctx, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.structure(ctx, s.db, o)
}
func (s *Session) structure(ctx context.Context, c queryer, o contract.ObjectRef) (*contract.TableStructure, error) {
	if err := s.check(o); err != nil {
		return nil, err
	}
	if o.Kind != "table" && o.Kind != "view" {
		return nil, contract.ErrUnsupported
	}
	rows, err := c.QueryContext(ctx, `SELECT object_type FROM all_objects WHERE owner=:1 AND object_name=:2 AND subobject_name IS NULL AND object_type IN ('TABLE','VIEW')`, o.Schema, o.Name)
	if err != nil {
		return nil, fmt.Errorf("Oracle object lookup: %w", safeError(err))
	}
	var typ string
	if !rows.Next() {
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("Oracle object lookup: %w", safeError(err))
		}
		return nil, connections.ErrNotFound
	}
	err = rows.Scan(&typ)
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("Oracle object lookup: %w", safeError(err))
	}
	if strings.ToLower(typ) != o.Kind {
		return nil, connections.ErrInvalid
	}
	label := "表"
	if o.Kind == "view" {
		label = "视图"
	}
	t := &contract.TableStructure{Object: o, Database: o.Schema, Name: o.Name, Kind: label, Engine: "Oracle"}
	rows, err = c.QueryContext(ctx, `SELECT c.column_name,c.data_type,c.column_id,c.nullable,c.identity_column,c.virtual_column,c.data_default,c.char_length,c.char_used,c.data_precision,c.data_scale,NVL(m.comments,' ') FROM all_tab_cols c LEFT JOIN all_col_comments m ON m.owner=c.owner AND m.table_name=c.table_name AND m.column_name=c.column_name WHERE c.owner=:1 AND c.table_name=:2 AND c.hidden_column='NO' ORDER BY c.column_id`, o.Schema, o.Name)
	if err != nil {
		return nil, fmt.Errorf("Oracle columns: %w", safeError(err))
	}
	for rows.Next() {
		var col contract.StructureColumn
		var nullable, identity, virtual string
		var def sql.NullString
		var length, precision, scale sql.NullInt64
		var charUsed sql.NullString
		if err = rows.Scan(&col.Name, &col.DataType, &col.Position, &nullable, &identity, &virtual, &def, &length, &charUsed, &precision, &scale, &col.Comment); err != nil {
			rows.Close()
			return nil, fmt.Errorf("Oracle columns: %w", safeError(err))
		}
		col.Nullable = nullable == "Y"
		col.Generated = identity == "YES" || virtual == "YES"
		if identity == "YES" {
			col.Extra = "IDENTITY"
		}
		if virtual == "YES" {
			col.Extra = "VIRTUAL"
		}
		col.ValueKind = typeKind(col.DataType)
		col.Type = col.DataType
		if strings.HasPrefix(col.DataType, "TIMESTAMP(") {
			end := strings.IndexByte(col.DataType, ')')
			if end > 10 {
				if precision, err := strconv.ParseInt(col.DataType[10:end], 10, 64); err == nil {
					col.DatetimePrecision = &precision
				}
			}
		}
		col.Comment = strings.TrimSpace(col.Comment)
		if def.Valid {
			v := strings.TrimSpace(def.String)
			col.Default = &v
		}
		if precision.Valid {
			v := precision.Int64
			col.NumericPrecision = &v
		}
		if scale.Valid {
			v := scale.Int64
			col.NumericScale = &v
		}
		if col.DataType == "NUMBER" && precision.Valid {
			col.Type = fmt.Sprintf("NUMBER(%d,%d)", precision.Int64, scale.Int64)
		}
		if length.Valid && (col.DataType == "VARCHAR2" || col.DataType == "NVARCHAR2" || col.DataType == "CHAR" || col.DataType == "NCHAR") {
			v := length.Int64
			col.CharacterMaxLength = &v
			unit := " BYTE"
			if charUsed.String == "C" {
				unit = " CHAR"
			}
			if strings.HasPrefix(col.DataType, "N") {
				unit = ""
			}
			col.Type += fmt.Sprintf("(%d%s)", length.Int64, unit)
		}
		t.Columns = append(t.Columns, col)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("Oracle columns: %w", safeError(err))
	}
	rows, err = c.QueryContext(ctx, `SELECT i.index_name,i.index_type,i.uniqueness,ic.column_name,ic.column_position,ic.descend,CASE WHEN pc.constraint_type='P' AND pc.status='ENABLED' AND pc.validated='VALIDATED' AND pc.deferrable='NOT DEFERRABLE' THEN 1 ELSE 0 END,i.status FROM all_indexes i JOIN all_ind_columns ic ON ic.index_owner=i.owner AND ic.index_name=i.index_name LEFT JOIN all_constraints pc ON pc.index_owner=i.owner AND pc.index_name=i.index_name AND pc.constraint_type='P' WHERE i.table_owner=:1 AND i.table_name=:2 ORDER BY i.index_name,ic.column_position`, o.Schema, o.Name)
	if err != nil {
		return nil, fmt.Errorf("Oracle indexes: %w", safeError(err))
	}
	indexes := map[string]int{}
	for rows.Next() {
		var name, kind, unique, col, desc, status string
		var pos, primary int64
		if err = rows.Scan(&name, &kind, &unique, &col, &pos, &desc, &primary, &status); err != nil {
			rows.Close()
			return nil, fmt.Errorf("Oracle indexes: %w", safeError(err))
		}
		i, ok := indexes[name]
		if !ok {
			i = len(t.Indexes)
			indexes[name] = i
			t.Indexes = append(t.Indexes, contract.StructureIndex{Name: name, Type: kind, Unique: unique == "UNIQUE" && status == "VALID" && kind == "NORMAL"})
		}
		t.Indexes[i].Columns = append(t.Indexes[i].Columns, contract.StructureIndexColumn{Name: col, Position: pos, Direction: desc})
		if primary == 1 {
			for j := range t.Columns {
				if t.Columns[j].Name == col {
					t.Columns[j].Key = "PRI"
				}
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("Oracle indexes: %w", safeError(err))
	}
	rows, err = c.QueryContext(ctx, `SELECT f.constraint_name,r.owner,r.table_name,f.delete_rule,fc.column_name,rc.column_name,fc.position FROM all_constraints f JOIN all_constraints r ON r.owner=f.r_owner AND r.constraint_name=f.r_constraint_name JOIN all_cons_columns fc ON fc.owner=f.owner AND fc.constraint_name=f.constraint_name JOIN all_cons_columns rc ON rc.owner=r.owner AND rc.constraint_name=r.constraint_name AND rc.position=fc.position WHERE f.owner=:1 AND f.table_name=:2 AND f.constraint_type='R' ORDER BY f.constraint_name,fc.position`, o.Schema, o.Name)
	if err != nil {
		return nil, fmt.Errorf("Oracle foreign keys: %w", safeError(err))
	}
	fks := map[string]int{}
	for rows.Next() {
		var f contract.StructureForeignKey
		var v contract.StructureForeignKeyColumn
		if err = rows.Scan(&f.Name, &f.ReferencedDatabase, &f.ReferencedTable, &f.OnDelete, &v.Name, &v.ReferencedName, &v.Position); err != nil {
			rows.Close()
			return nil, fmt.Errorf("Oracle foreign keys: %w", safeError(err))
		}
		f.OnUpdate = "NO ACTION"
		i, ok := fks[f.Name]
		if !ok {
			i = len(t.ForeignKeys)
			fks[f.Name] = i
			t.ForeignKeys = append(t.ForeignKeys, f)
		}
		t.ForeignKeys[i].Columns = append(t.ForeignKeys[i].Columns, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("Oracle foreign keys: %w", safeError(err))
	}
	// DBMS_METADATA is permission limited to the current user's objects. Metadata
	// browsing for accessible foreign schemas still works without this privilege.
	rows, err = c.QueryContext(ctx, `SELECT DBMS_METADATA.GET_DDL(:1,:2,:3) FROM dual`, typ, o.Name, o.Schema)
	if err == nil {
		if rows.Next() {
			_ = rows.Scan(&t.Definition)
		}
		rows.Close()
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	t.Editable = o.Kind == "table" && len(keyColumns(t)) > 0
	for _, col := range t.Columns {
		if !supported(col) {
			t.Editable = false
		}
		if lob(col) && !s.crypto {
			t.Editable = false
		}
	}
	return t, nil
}
func keyColumns(t *contract.TableStructure) []string {
	known := map[string]contract.StructureColumn{}
	for _, c := range t.Columns {
		known[c.Name] = c
	}
	valid := func(names []string) bool {
		if len(names) == 0 {
			return false
		}
		for _, n := range names {
			c, ok := known[n]
			if !ok || c.Nullable || !comparable(c) || c.Generated && c.Extra != "IDENTITY" {
				return false
			}
		}
		return true
	}
	for _, idx := range t.Indexes {
		if !idx.Unique {
			continue
		}
		var names []string
		for _, c := range idx.Columns {
			names = append(names, c.Name)
		}
		if valid(names) {
			return names
		}
	}
	return nil
}
func lob(c contract.StructureColumn) bool {
	return c.DataType == "CLOB" || c.DataType == "NCLOB" || c.DataType == "BLOB"
}
func comparable(c contract.StructureColumn) bool {
	switch c.DataType {
	case "NUMBER", "FLOAT", "BINARY_FLOAT", "BINARY_DOUBLE", "CHAR", "NCHAR", "VARCHAR2", "NVARCHAR2", "RAW", "DATE":
		return true
	}
	return strings.HasPrefix(c.DataType, "TIMESTAMP")
}
func supported(c contract.StructureColumn) bool { return comparable(c) || lob(c) }
