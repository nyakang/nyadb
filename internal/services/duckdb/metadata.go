package duckdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func quote(v string) string { return "\"" + strings.ReplaceAll(v, "\"", "\"\"") + "\"" }
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
	if o.ProfileID != s.ProfileID() || o.Catalog != s.Catalog() || !sqltext.ValidName(o.Schema) || !sqltext.ValidName(o.Name) {
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
	rows, err := s.db.QueryContext(child, "SELECT schema_name FROM duckdb_schemas() WHERE database_name=? AND (NOT internal OR schema_name='main') ORDER BY CASE WHEN schema_name=? THEN 0 ELSE 1 END,schema_name", s.Catalog(), s.profile.DuckDBOptions.DefaultSchema)
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
	child, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	query := `SELECT table_name,'table',COALESCE(comment,''),sql,estimated_size,index_count>0 FROM duckdb_tables() WHERE database_name=? AND schema_name=? AND NOT internal AND NOT temporary UNION ALL SELECT view_name,'view',COALESCE(comment,''),sql,NULL,false FROM duckdb_views() WHERE database_name=? AND schema_name=? AND NOT internal AND NOT temporary ORDER BY 2,1`
	rows, err := s.db.QueryContext(child, query, n.Catalog, n.Schema, n.Catalog, n.Schema)
	if err != nil {
		return nil, safeError(err)
	}
	var out []contract.DBObject
	for rows.Next() {
		var o contract.DBObject
		var kind string
		var estimate sql.NullInt64
		var indexes bool
		if err = rows.Scan(&o.Name, &kind, &o.Comment, &o.Definition, &estimate, &indexes); err != nil {
			rows.Close()
			return nil, safeError(err)
		}
		o.Ref = n.Object(kind, o.Name)
		o.Engine = "DuckDB"
		o.Kind = "表"
		if kind == "view" {
			o.Kind = "视图"
		} else {
			o.HasIndexes = &indexes
		}
		if estimate.Valid {
			v := estimate.Int64
			o.RowsEstimate = &v
		}
		out = append(out, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, safeError(err)
	}
	rows, err = s.db.QueryContext(child, "SELECT sequence_name,COALESCE(comment,''),sql,start_value,increment_by,min_value,max_value,cycle FROM duckdb_sequences() WHERE database_name=? AND schema_name=? AND NOT temporary ORDER BY sequence_name", n.Catalog, n.Schema)
	if err != nil {
		return nil, safeError(err)
	}
	for rows.Next() {
		o := contract.DBObject{Kind: "序列", Engine: "DuckDB", Sequence: &contract.SequenceInfo{DataType: "BIGINT"}}
		if err = rows.Scan(&o.Name, &o.Comment, &o.Definition, &o.Sequence.Start, &o.Sequence.Increment, &o.Sequence.Min, &o.Sequence.Max, &o.Sequence.Cycle); err != nil {
			rows.Close()
			return nil, safeError(err)
		}
		o.Ref = n.Object("sequence", o.Name)
		out = append(out, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, safeError(err)
	}
	rows, err = s.db.QueryContext(child, "SELECT function_name,function_type,COALESCE(comment,''),COALESCE(return_type,''),CAST(to_json(parameters) AS VARCHAR),macro_definition FROM duckdb_functions() WHERE database_name=? AND schema_name=? AND NOT internal AND function_type IN ('macro','table_macro') ORDER BY function_name", n.Catalog, n.Schema)
	if err != nil {
		return nil, safeError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var o contract.DBObject
		var typ, parameters, definition string
		if err = rows.Scan(&o.Name, &typ, &o.Comment, &o.ReturnType, &parameters, &definition); err != nil {
			return nil, safeError(err)
		}
		var names []string
		if json.Unmarshal([]byte(parameters), &names) != nil {
			return nil, contract.ErrUnsupported
		}
		var params []string
		for _, name := range names {
			params = append(params, quote(name))
		}
		o.Parameters = strings.Join(params, ", ")
		o.Ref = n.Object("function", o.Name)
		o.Kind = "函数"
		o.Engine = "DuckDB"
		o.Definition = "CREATE MACRO " + qualified(o.Ref) + "(" + o.Parameters + ") AS "
		if typ == "table_macro" {
			o.Definition += "TABLE "
		}
		o.Definition += definition + ";"
		out = append(out, o)
	}
	return out, safeError(rows.Err())
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
	if o.Kind != "table" && o.Kind != "view" {
		return nil, contract.ErrUnsupported
	}
	source, name := "duckdb_tables()", "table_name"
	kind := "表"
	if o.Kind == "view" {
		source, name = "duckdb_views()", "view_name"
		kind = "视图"
	}
	rows, err := c.QueryContext(ctx, "SELECT COALESCE(comment,''),sql FROM "+source+" WHERE database_name=? AND schema_name=? AND "+name+"=? AND NOT internal AND NOT temporary", o.Catalog, o.Schema, o.Name)
	if err != nil {
		return nil, safeError(err)
	}
	t := &contract.TableStructure{Object: o, Database: o.Schema, Name: o.Name, Kind: kind, Engine: "DuckDB"}
	if !rows.Next() {
		rows.Close()
		return nil, connections.ErrNotFound
	}
	err = rows.Scan(&t.Comment, &t.Definition)
	rows.Close()
	if err != nil {
		return nil, safeError(err)
	}
	generated, err := generatedColumns(t.Definition)
	if err != nil {
		return nil, err
	}
	rows, err = c.QueryContext(ctx, "SELECT column_name,data_type,column_index+1,is_nullable,column_default,COALESCE(comment,''),numeric_precision,numeric_scale,character_maximum_length FROM duckdb_columns() WHERE database_name=? AND schema_name=? AND table_name=? ORDER BY column_index", o.Catalog, o.Schema, o.Name)
	if err != nil {
		return nil, safeError(err)
	}
	for rows.Next() {
		var col contract.StructureColumn
		var def sql.NullString
		var precision, scale, length sql.NullInt64
		if err = rows.Scan(&col.Name, &col.Type, &col.Position, &col.Nullable, &def, &col.Comment, &precision, &scale, &length); err != nil {
			rows.Close()
			return nil, safeError(err)
		}
		col.DataType = col.Type
		col.ValueKind = typeKind(col.Type)
		col.Generated = generated[col.Name]
		if col.Generated {
			col.Extra = "GENERATED"
		}
		if def.Valid {
			v := def.String
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
		if length.Valid {
			v := length.Int64
			col.CharacterMaxLength = &v
		}
		t.Columns = append(t.Columns, col)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, safeError(err)
	}
	if len(t.Columns) == 0 || len(t.Columns) > 512 {
		return nil, contract.ErrUnsupported
	}
	if o.Kind == "table" {
		rows, err = c.QueryContext(ctx, "SELECT constraint_type,constraint_name,CAST(to_json(constraint_column_names) AS VARCHAR),COALESCE(referenced_table,''),CAST(to_json(referenced_column_names) AS VARCHAR) FROM duckdb_constraints() WHERE database_name=? AND schema_name=? AND table_name=? ORDER BY constraint_index", o.Catalog, o.Schema, o.Name)
		if err != nil {
			return nil, safeError(err)
		}
		for rows.Next() {
			var typ, name, columns, target, referenced string
			if err = rows.Scan(&typ, &name, &columns, &target, &referenced); err != nil {
				rows.Close()
				return nil, safeError(err)
			}
			var names, refs []string
			if json.Unmarshal([]byte(columns), &names) != nil || json.Unmarshal([]byte(referenced), &refs) != nil {
				rows.Close()
				return nil, contract.ErrUnsupported
			}
			if typ == "PRIMARY KEY" || typ == "UNIQUE" {
				idx := contract.StructureIndex{Name: name, Type: typ, Unique: true}
				for i, n := range names {
					idx.Columns = append(idx.Columns, contract.StructureIndexColumn{Name: n, Position: int64(i + 1), Direction: "ASC"})
					if typ == "PRIMARY KEY" {
						for j := range t.Columns {
							if t.Columns[j].Name == n {
								t.Columns[j].Key = "PRI"
								t.Columns[j].Nullable = false
							}
						}
					}
				}
				t.Indexes = append(t.Indexes, idx)
			}
			if typ == "FOREIGN KEY" {
				if len(names) != len(refs) {
					rows.Close()
					return nil, contract.ErrUnsupported
				}
				fk := contract.StructureForeignKey{Name: name, ReferencedDatabase: o.Schema, ReferencedTable: target, OnUpdate: "NO ACTION", OnDelete: "NO ACTION"}
				for i, n := range names {
					fk.Columns = append(fk.Columns, contract.StructureForeignKeyColumn{Name: n, ReferencedName: refs[i], Position: int64(i + 1)})
				}
				t.ForeignKeys = append(t.ForeignKeys, fk)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, safeError(err)
		}
		rows, err = c.QueryContext(ctx, "SELECT index_name,is_unique,CAST(to_json(CAST(expressions AS VARCHAR[])) AS VARCHAR) FROM duckdb_indexes() WHERE database_name=? AND schema_name=? AND table_name=? ORDER BY index_name", o.Catalog, o.Schema, o.Name)
		if err != nil {
			return nil, safeError(err)
		}
		for rows.Next() {
			idx := contract.StructureIndex{Type: "ART"}
			var expressions string
			if err = rows.Scan(&idx.Name, &idx.Unique, &expressions); err != nil {
				rows.Close()
				return nil, safeError(err)
			}
			var names []string
			if json.Unmarshal([]byte(expressions), &names) != nil {
				rows.Close()
				return nil, contract.ErrUnsupported
			}
			for i, name := range names {
				idx.Columns = append(idx.Columns, contract.StructureIndexColumn{Name: name, Position: int64(i + 1)})
			}
			t.Indexes = append(t.Indexes, idx)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, safeError(err)
		}
	}
	t.Editable = o.Kind == "table" && !s.profile.DuckDBOptions.ReadOnly && len(keyColumns(t)) > 0
	return t, nil
}
func keyColumns(t *contract.TableStructure) []string {
	for _, idx := range t.Indexes {
		if !idx.Unique {
			continue
		}
		var keys []string
		valid := true
		for _, key := range idx.Columns {
			found := false
			for _, c := range t.Columns {
				if c.Name == key.Name {
					found = true
					if c.Nullable || c.Generated || !comparable(c) {
						valid = false
					}
				}
			}
			if !found {
				valid = false
			}
			keys = append(keys, key.Name)
		}
		if valid && len(keys) > 0 {
			return keys
		}
	}
	return nil
}

var decimalType = regexp.MustCompile(`^DECIMAL\(([0-9]{1,2}),([0-9]{1,2})\)$`)

func comparable(c contract.StructureColumn) bool {
	switch c.Type {
	case "BOOLEAN", "TINYINT", "SMALLINT", "INTEGER", "BIGINT", "HUGEINT", "UTINYINT", "USMALLINT", "UINTEGER", "UBIGINT", "UHUGEINT", "FLOAT", "DOUBLE", "VARCHAR", "BLOB", "UUID", "DATE", "TIME", "TIME_NS", "TIMESTAMP", "TIMESTAMP_S", "TIMESTAMP_MS", "TIMESTAMP_NS", "TIMESTAMP WITH TIME ZONE", "TIME WITH TIME ZONE", "JSON":
		return true
	}
	return decimalType.MatchString(c.Type)
}
func generatedColumns(definition string) (map[string]bool, error) {
	tokens, err := sqltext.DuckDBTokens(definition)
	if err != nil {
		return nil, contract.ErrUnsupported
	}
	out := map[string]bool{}
	depth := 0
	start := -1
	for i, t := range tokens {
		if t.Comment {
			continue
		}
		if !t.Quoted && t.Text == "(" {
			depth++
			if depth == 1 {
				start = i + 1
			}
			continue
		}
		if !t.Quoted && (t.Text == "," && depth == 1 || t.Text == ")" && depth == 1) {
			if start >= 0 && start < i {
				first := tokens[start]
				name := first.Text
				if first.Quoted && strings.HasPrefix(name, "\"") {
					name = strings.ReplaceAll(name[1:len(name)-1], "\"\"", "\"")
				}
				for _, x := range tokens[start+1 : i] {
					if sqltext.Word(x, "GENERATED") {
						out[name] = true
					}
				}
			}
			start = i + 1
		}
		if !t.Quoted && t.Text == ")" {
			depth--
		}
	}
	return out, nil
}

var _ contract.MetadataProvider = (*Session)(nil)
