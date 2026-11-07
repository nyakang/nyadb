package starrocks

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func validName(v string) bool {
	return v != "" && len(v) <= 1024 && utf8.ValidString(v) && strings.IndexFunc(v, unicode.IsControl) < 0
}
func quote(v string) string {
	return "`" + strings.ReplaceAll(strings.ReplaceAll(v, "\\", "\\\\"), "`", "``") + "`"
}
func qualified(o contract.ObjectRef) string {
	return quote(o.Catalog) + "." + quote(o.Schema) + "." + quote(o.Name)
}
func (s *Session) Namespace(database string) contract.NamespaceRef {
	if !validName(database) {
		return contract.NamespaceRef{ProfileID: s.ProfileID()}
	}
	return contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: "default_catalog", Schema: database}
}
func (s *Session) checkNamespace(n contract.NamespaceRef) error {
	if !s.Alive() {
		return contract.ErrRevoked
	}
	if n.ProfileID != s.ProfileID() || n.Catalog != "default_catalog" || !validName(n.Schema) {
		return connections.ErrInvalid
	}
	return nil
}
func (s *Session) checkObject(o contract.ObjectRef) error {
	if err := s.checkNamespace(o.Namespace()); err != nil {
		return err
	}
	if !validName(o.Name) {
		return connections.ErrInvalid
	}
	if !strings.EqualFold(o.Kind, "TABLE") && !strings.EqualFold(o.Kind, "VIEW") {
		return contract.ErrUnsupported
	}
	return nil
}

// Metadata has separate limits and retains full native definitions/defaults.
func metadataRows(ctx context.Context, c *sql.Conn, q string, args ...any) ([][]sql.NullString, error) {
	var rows *sql.Rows
	var err error
	if firstWord(q) == "SELECT" || firstWord(q) == "WITH" {
		st, e := c.PrepareContext(ctx, q)
		if e != nil {
			return nil, safeError(e)
		}
		defer st.Close()
		rows, err = st.QueryContext(ctx, args...)
	} else {
		if len(args) != 0 {
			return nil, contract.ErrUnsupported
		}
		rows, err = c.QueryContext(ctx, q)
	}
	if err != nil {
		return nil, safeError(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, safeError(err)
	}
	if len(cols) > 512 {
		return nil, contract.ErrUnsupported
	}
	var result [][]sql.NullString
	budget := 16 << 20
	for rows.Next() {
		v := make([]sql.NullString, len(cols))
		d := make([]any, len(cols))
		for i := range d {
			d[i] = &v[i]
		}
		if err = rows.Scan(d...); err != nil {
			return nil, safeError(err)
		}
		for _, x := range v {
			budget -= len(x.String)
			if len(x.String) > 1<<20 || budget < 0 {
				return nil, contract.ErrUnsupported
			}
		}
		if len(result) >= 10000 {
			return nil, contract.ErrUnsupported
		}
		result = append(result, v)
	}
	return result, safeError(rows.Err())
}
func (s *Session) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
	child, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	f, err := s.open(child)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows, err := metadataRows(child, f.conn, "SHOW DATABASES FROM default_catalog")
	if err != nil {
		return nil, err
	}
	var result []contract.Namespace
	for _, row := range rows {
		if len(row) != 1 || !validName(row[0].String) {
			return nil, contract.ErrUnsupported
		}
		name := row[0].String
		result = append(result, contract.Namespace{Ref: s.Namespace(name), Label: name})
	}
	return result, nil
}
func (s *Session) Browse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	if err := s.checkNamespace(n); err != nil {
		return nil, err
	}
	child, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	f, err := s.open(child)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows, err := metadataRows(child, f.conn, "SELECT TABLE_NAME,TABLE_TYPE,ENGINE,TABLE_COMMENT FROM default_catalog.information_schema.tables WHERE TABLE_SCHEMA=? ORDER BY TABLE_NAME", n.Schema)
	if err != nil {
		return nil, err
	}
	var result []contract.DBObject
	for _, row := range rows {
		if len(row) != 4 || !validName(row[0].String) {
			return nil, contract.ErrUnsupported
		}
		kind := "TABLE"
		if row[1].String == "VIEW" {
			kind = "VIEW"
		} else if row[1].String != "BASE TABLE" {
			continue
		}
		result = append(result, contract.DBObject{Ref: n.Object(kind, row[0].String), Name: row[0].String, Kind: kind, Engine: row[2].String, Comment: row[3].String})
	}
	return result, nil
}
func (s *Session) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	if err := s.checkObject(o); err != nil {
		return nil, err
	}
	child, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	f, err := s.open(child)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return tableMetadata(child, f.conn, o)
}
func tableMetadata(ctx context.Context, c *sql.Conn, o contract.ObjectRef) (*contract.TableStructure, error) {
	rows, err := metadataRows(ctx, c, "SELECT TABLE_TYPE,ENGINE,TABLE_COMMENT FROM default_catalog.information_schema.tables WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", o.Schema, o.Name)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 || len(rows[0]) != 3 {
		return nil, connections.ErrNotFound
	}
	kind := "TABLE"
	if rows[0][0].String == "VIEW" {
		kind = "VIEW"
	} else if rows[0][0].String != "BASE TABLE" {
		return nil, contract.ErrUnsupported
	}
	if !strings.EqualFold(o.Kind, kind) {
		return nil, connections.ErrInvalid
	}
	t := &contract.TableStructure{Object: o, Database: o.Schema, Name: o.Name, Kind: kind, Engine: rows[0][1].String, Comment: rows[0][2].String, Editable: false}
	defs, err := metadataRows(ctx, c, "SHOW CREATE TABLE "+qualified(o))
	if err != nil {
		return nil, err
	}
	if len(defs) != 1 || len(defs[0]) < 2 || !defs[0][1].Valid {
		return nil, contract.ErrUnsupported
	}
	t.Definition = defs[0][1].String
	t.Definitions = []contract.Definition{{Name: o.Name, Kind: kind, SQL: t.Definition}}
	cols, err := metadataRows(ctx, c, "DESCRIBE "+qualified(o))
	if err != nil {
		return nil, err
	}
	for i, r := range cols {
		if len(r) != 6 || !validName(r[0].String) {
			return nil, contract.ErrUnsupported
		}
		col := contract.StructureColumn{Name: r[0].String, Type: r[1].String, DataType: r[1].String, Nullable: strings.EqualFold(r[2].String, "Yes"), Position: int64(i + 1), Extra: r[5].String, ValueKind: valueKind(r[1].String)}
		// Native model keys describe ordering and aggregation/upsert semantics.
		// Even PRIMARY KEY tables remain read-only in this adapter.
		if strings.EqualFold(r[3].String, "true") {
			col.Key = "SORT"
		}
		if r[4].Valid {
			v := r[4].String
			col.Default = &v
		}
		t.Columns = append(t.Columns, col)
	}
	if len(t.Columns) == 0 || len(t.Columns) > 512 {
		return nil, contract.ErrUnsupported
	}
	attributes, err := metadataRows(ctx, c, "SELECT COLUMN_NAME,COLUMN_COMMENT,NUMERIC_PRECISION,NUMERIC_SCALE,CHARACTER_MAXIMUM_LENGTH FROM default_catalog.information_schema.columns WHERE TABLE_SCHEMA=? AND TABLE_NAME=? ORDER BY ORDINAL_POSITION", o.Schema, o.Name)
	if err != nil {
		return nil, err
	}
	if len(attributes) != len(t.Columns) {
		return nil, contract.ErrUnsupported
	}
	optionalNumber := func(v sql.NullString) (*int64, error) {
		if !v.Valid {
			return nil, nil
		}
		n, e := strconv.ParseInt(v.String, 10, 64)
		if e != nil || n < 0 {
			return nil, contract.ErrUnsupported
		}
		return &n, nil
	}
	for i, r := range attributes {
		if len(r) != 5 || r[0].String != t.Columns[i].Name {
			return nil, contract.ErrUnsupported
		}
		col := &t.Columns[i]
		col.Comment = r[1].String
		if col.NumericPrecision, err = optionalNumber(r[2]); err != nil {
			return nil, err
		}
		if col.NumericScale, err = optionalNumber(r[3]); err != nil {
			return nil, err
		}
		if col.CharacterMaxLength, err = optionalNumber(r[4]); err != nil {
			return nil, err
		}
	}
	return t, nil
}
