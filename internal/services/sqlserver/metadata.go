package sqlserver

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func validName(v string) bool {
	return v != "" && utf8.ValidString(v) && len([]rune(v)) <= 128 && strings.IndexFunc(v, unicode.IsControl) < 0
}
func quote(v string) string { return "[" + strings.ReplaceAll(v, "]", "]]") + "]" }
func qualified(o contract.ObjectRef) string {
	return quote(o.Catalog) + "." + quote(o.Schema) + "." + quote(o.Name)
}
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
	child, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := s.db.QueryContext(child, "SELECT name FROM sys.schemas WHERE schema_id < 16384 AND name NOT IN (N'sys',N'INFORMATION_SCHEMA') ORDER BY CASE WHEN name=@p1 THEN 0 ELSE 1 END,name", s.profile.SQLServerOptions.DefaultSchema)
	if err != nil {
		return nil, safeError(err)
	}
	defer rows.Close()
	var result []contract.Namespace
	for rows.Next() {
		var n string
		if err = rows.Scan(&n); err != nil {
			return nil, safeError(err)
		}
		result = append(result, contract.Namespace{Ref: s.Namespace(n), Label: n})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, safeError(err)
	}
	return result, nil
}
func (s *Session) Browse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	if err := s.check(n.Object("table", "_")); err != nil {
		return nil, err
	}
	child, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := s.db.QueryContext(child, `SELECT o.name,o.type,COALESCE(CONVERT(nvarchar(4000),ep.value),N''),CONVERT(nvarchar(33),o.create_date,126),CONVERT(nvarchar(33),o.modify_date,126),COALESCE(m.definition,N'') FROM sys.objects o JOIN sys.schemas s ON s.schema_id=o.schema_id LEFT JOIN sys.sql_modules m ON m.object_id=o.object_id LEFT JOIN sys.extended_properties ep ON ep.major_id=o.object_id AND ep.minor_id=0 AND ep.name=N'MS_Description' WHERE s.name=@p1 AND o.is_ms_shipped=0 AND o.type IN ('U','V','P','FN','IF','TF','SO') ORDER BY o.name`, n.Schema)
	if err != nil {
		return nil, safeError(err)
	}
	defer rows.Close()
	var result []contract.DBObject
	for rows.Next() {
		var o contract.DBObject
		var typ string
		if err = rows.Scan(&o.Name, &typ, &o.Comment, &o.CreatedAt, &o.ModifiedAt, &o.Definition); err != nil {
			return nil, safeError(err)
		}
		kind, label := "table", "表"
		switch strings.TrimSpace(typ) {
		case "V":
			kind, label = "view", "视图"
		case "P":
			kind, label = "procedure", "存储过程"
		case "FN", "IF", "TF":
			kind, label = "function", "函数"
		case "SO":
			kind, label = "sequence", "序列"
		}
		o.Kind = label
		o.Ref = n.Object(kind, o.Name)
		o.Engine = "SQL Server"
		result = append(result, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, safeError(err)
	}
	if err = s.sequenceMetadata(child, n, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Session) sequenceMetadata(ctx context.Context, n contract.NamespaceRef, objects []contract.DBObject) error {
	positions := map[string]int{}
	for i, o := range objects {
		if o.Ref.Kind == "sequence" {
			positions[o.Name] = i
		}
	}
	if len(positions) == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT q.name,t.name,q.precision,CONVERT(nvarchar(100),q.start_value),CONVERT(nvarchar(100),q.increment),CONVERT(nvarchar(100),q.minimum_value),CONVERT(nvarchar(100),q.maximum_value),q.is_cycling,q.is_cached,COALESCE(CONVERT(nvarchar(100),q.cache_size),N'0') FROM sys.sequences q JOIN sys.schemas s ON s.schema_id=q.schema_id JOIN sys.types t ON t.user_type_id=q.user_type_id WHERE s.name=@p1`, n.Schema)
	if err != nil {
		return safeError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, typ, start, increment, min, max, cache string
		var precision int
		var cycle, cached bool
		if err = rows.Scan(&name, &typ, &precision, &start, &increment, &min, &max, &cycle, &cached, &cache); err != nil {
			return safeError(err)
		}
		i, ok := positions[name]
		if !ok {
			continue
		}
		if typ == "decimal" || typ == "numeric" {
			typ += fmt.Sprintf("(%d,0)", precision)
		}
		definition := "CREATE SEQUENCE " + qualified(objects[i].Ref) + " AS " + typ + " START WITH " + start + " INCREMENT BY " + increment + " MINVALUE " + min + " MAXVALUE " + max
		if cycle {
			definition += " CYCLE"
		} else {
			definition += " NO CYCLE"
		}
		if cached {
			definition += " CACHE " + cache
		} else {
			definition += " NO CACHE"
		}
		objects[i].Definition = definition + ";"
		values := []string{start, increment, min, max, cache}
		parsed := make([]int64, 5)
		fits := true
		for j, value := range values {
			v, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				fits = false
				break
			}
			parsed[j] = v
		}
		if fits {
			objects[i].Sequence = &contract.SequenceInfo{DataType: typ, Start: parsed[0], Increment: parsed[1], Min: parsed[2], Max: parsed[3], Cache: parsed[4], Cycle: cycle}
		}
	}
	return safeError(rows.Err())
}
func (s *Session) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	child, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.structure(child, s.db, o)
}
func (s *Session) structure(ctx context.Context, c queryer, o contract.ObjectRef) (*contract.TableStructure, error) {
	if err := s.check(o); err != nil {
		return nil, err
	}
	if !strings.EqualFold(o.Kind, "table") && !strings.EqualFold(o.Kind, "view") {
		return nil, contract.ErrUnsupported
	}
	objects, err := c.QueryContext(ctx, `SELECT o.object_id,o.type,COALESCE(m.definition,N'') FROM sys.objects o JOIN sys.schemas s ON s.schema_id=o.schema_id LEFT JOIN sys.sql_modules m ON m.object_id=o.object_id WHERE s.name=@p1 AND o.name=@p2 AND o.type IN ('U','V')`, o.Schema, o.Name)
	if err != nil {
		return nil, safeError(err)
	}
	var objectID int64
	var typ, definition string
	if !objects.Next() {
		objects.Close()
		return nil, connections.ErrNotFound
	}
	err = objects.Scan(&objectID, &typ, &definition)
	objects.Close()
	if err != nil {
		return nil, safeError(err)
	}
	actual := "table"
	label := "表"
	if strings.TrimSpace(typ) == "V" {
		actual, label = "view", "视图"
	}
	if !strings.EqualFold(actual, o.Kind) {
		return nil, connections.ErrInvalid
	}
	t := &contract.TableStructure{Object: o, Database: o.Schema, Name: o.Name, Kind: label, Engine: "SQL Server", Definition: definition}
	rows, err := c.QueryContext(ctx, `SELECT c.name,t.name,c.column_id,c.is_nullable,c.is_identity,c.is_computed,c.generated_always_type,COALESCE(d.definition,N''),c.max_length,c.precision,c.scale,c.collation_name,COALESCE(CONVERT(nvarchar(4000),ep.value),N'') FROM sys.columns c JOIN sys.types t ON t.user_type_id=c.user_type_id LEFT JOIN sys.default_constraints d ON d.object_id=c.default_object_id LEFT JOIN sys.extended_properties ep ON ep.major_id=c.object_id AND ep.minor_id=c.column_id AND ep.name=N'MS_Description' WHERE c.object_id=@p1 ORDER BY c.column_id`, objectID)
	if err != nil {
		return nil, safeError(err)
	}
	for rows.Next() {
		var col contract.StructureColumn
		var identity, computed bool
		var generated, length, precision, scale int64
		var defaultExpr string
		var collation sql.NullString
		if err = rows.Scan(&col.Name, &col.DataType, &col.Position, &col.Nullable, &identity, &computed, &generated, &defaultExpr, &length, &precision, &scale, &collation, &col.Comment); err != nil {
			rows.Close()
			return nil, safeError(err)
		}
		col.Type = col.DataType
		col.ValueKind = typeKind(col.DataType)
		col.Generated = identity || computed || generated != 0 || col.DataType == "timestamp" || col.DataType == "rowversion"
		if identity {
			col.Extra = "IDENTITY"
		}
		if computed {
			col.Extra = "COMPUTED"
		}
		if defaultExpr != "" {
			col.Default = &defaultExpr
		}
		if collation.Valid {
			v := collation.String
			col.Collation = &v
		}
		switch col.DataType {
		case "nvarchar", "nchar", "varchar", "char", "varbinary", "binary":
			if length == -1 {
				col.Type += "(max)"
			} else {
				if strings.HasPrefix(col.DataType, "n") {
					length /= 2
				}
				col.Type += fmt.Sprintf("(%d)", length)
			}
			col.CharacterMaxLength = &length
		case "decimal", "numeric":
			col.Type += fmt.Sprintf("(%d,%d)", precision, scale)
			col.NumericPrecision = &precision
			col.NumericScale = &scale
		case "datetime2", "datetimeoffset", "time":
			col.Type += fmt.Sprintf("(%d)", scale)
			col.DatetimePrecision = &scale
		}
		t.Columns = append(t.Columns, col)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, safeError(err)
	}
	rows, err = c.QueryContext(ctx, `SELECT i.name,i.type_desc,i.is_unique,i.is_primary_key,c.name,ic.key_ordinal,ic.is_descending_key,i.has_filter,i.is_disabled FROM sys.indexes i JOIN sys.index_columns ic ON ic.object_id=i.object_id AND ic.index_id=i.index_id JOIN sys.columns c ON c.object_id=ic.object_id AND c.column_id=ic.column_id WHERE i.object_id=@p1 AND ic.key_ordinal>0 ORDER BY i.index_id,ic.key_ordinal`, objectID)
	if err != nil {
		return nil, safeError(err)
	}
	indexes := map[string]int{}
	for rows.Next() {
		var name, kind, col string
		var unique, primary, desc, filter, disabled bool
		var ordinal int64
		if err = rows.Scan(&name, &kind, &unique, &primary, &col, &ordinal, &desc, &filter, &disabled); err != nil {
			rows.Close()
			return nil, safeError(err)
		}
		i, ok := indexes[name]
		if !ok {
			i = len(t.Indexes)
			indexes[name] = i
			t.Indexes = append(t.Indexes, contract.StructureIndex{Name: name, Type: kind, Unique: unique && !filter && !disabled})
		}
		direction := "ASC"
		if desc {
			direction = "DESC"
		}
		t.Indexes[i].Columns = append(t.Indexes[i].Columns, contract.StructureIndexColumn{Name: col, Position: ordinal, Direction: direction})
		if primary && !disabled {
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
		return nil, safeError(err)
	}
	rows, err = c.QueryContext(ctx, `SELECT fk.name,rs.name,rt.name,fk.update_referential_action_desc,fk.delete_referential_action_desc,pc.name,rc.name,fkc.constraint_column_id FROM sys.foreign_keys fk JOIN sys.foreign_key_columns fkc ON fkc.constraint_object_id=fk.object_id JOIN sys.columns pc ON pc.object_id=fkc.parent_object_id AND pc.column_id=fkc.parent_column_id JOIN sys.columns rc ON rc.object_id=fkc.referenced_object_id AND rc.column_id=fkc.referenced_column_id JOIN sys.tables rt ON rt.object_id=fk.referenced_object_id JOIN sys.schemas rs ON rs.schema_id=rt.schema_id WHERE fk.parent_object_id=@p1 ORDER BY fk.object_id,fkc.constraint_column_id`, objectID)
	if err != nil {
		return nil, safeError(err)
	}
	fks := map[string]int{}
	for rows.Next() {
		var f contract.StructureForeignKey
		var v contract.StructureForeignKeyColumn
		var schema string
		if err = rows.Scan(&f.Name, &schema, &f.ReferencedTable, &f.OnUpdate, &f.OnDelete, &v.Name, &v.ReferencedName, &v.Position); err != nil {
			rows.Close()
			return nil, safeError(err)
		}
		f.ReferencedDatabase = o.Catalog + "." + schema
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
		return nil, safeError(err)
	}
	t.Editable = actual == "table" && len(keyColumns(t)) > 0
	return t, nil
}
func keyColumns(t *contract.TableStructure) []string {
	known := map[string]contract.StructureColumn{}
	var primary []string
	for _, c := range t.Columns {
		known[c.Name] = c
		if c.Key == "PRI" {
			primary = append(primary, c.Name)
		}
	}
	valid := func(names []string) bool {
		for _, n := range names {
			c, ok := known[n]
			if !ok || c.Nullable || c.Generated && c.Extra != "IDENTITY" || !comparable(c) {
				return false
			}
		}
		return len(names) > 0
	}
	if valid(primary) {
		return primary
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
func comparable(c contract.StructureColumn) bool {
	switch c.DataType {
	case "bigint", "int", "smallint", "tinyint", "bit", "decimal", "numeric", "money", "smallmoney", "float", "real", "date", "time", "datetime", "datetime2", "datetimeoffset", "smalldatetime", "char", "varchar", "nchar", "nvarchar", "binary", "varbinary", "uniqueidentifier", "timestamp", "rowversion":
		return true
	}
	return false
}
