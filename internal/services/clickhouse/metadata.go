package clickhouse

import (
	"context"
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
	return "`" + strings.ReplaceAll(strings.ReplaceAll(v, "\\", "\\\\"), "`", "\\`") + "`"
}
func qualified(o contract.ObjectRef) string { return quote(o.Catalog) + "." + quote(o.Name) }
func (s *Session) Namespace(database string) contract.NamespaceRef {
	if !validName(database) {
		return contract.NamespaceRef{ProfileID: s.ProfileID()}
	}
	return contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: database}
}
func (s *Session) checkNamespace(n contract.NamespaceRef) error {
	if !s.Alive() {
		return contract.ErrRevoked
	}
	if n.ProfileID != s.ProfileID() || n.Schema != "" || !validName(n.Catalog) {
		return connections.ErrInvalid
	}
	return nil
}
func (s *Session) checkObject(o contract.ObjectRef) error {
	if err := s.checkNamespace(o.Namespace()); err != nil {
		return err
	}
	if !validName(o.Name) || (!strings.EqualFold(o.Kind, "TABLE") && !strings.EqualFold(o.Kind, "VIEW")) {
		return contract.ErrUnsupported
	}
	return nil
}
func (s *Session) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
	child, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := s.conn.Query(child, "SELECT name FROM system.databases ORDER BY name")
	if err != nil {
		return nil, safeError(err)
	}
	defer rows.Close()
	var result []contract.Namespace
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, safeError(err)
		}
		result = append(result, contract.Namespace{Ref: s.Namespace(name), Label: name})
	}
	return result, safeError(rows.Err())
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
	rows, err := s.conn.Query(child, "SELECT name, engine, comment FROM system.tables WHERE database = ? ORDER BY name", n.Catalog)
	if err != nil {
		return nil, safeError(err)
	}
	defer rows.Close()
	var result []contract.DBObject
	for rows.Next() {
		var name, engine, comment string
		if err = rows.Scan(&name, &engine, &comment); err != nil {
			return nil, safeError(err)
		}
		kind := "TABLE"
		if strings.Contains(engine, "View") {
			kind = "VIEW"
		}
		result = append(result, contract.DBObject{Ref: n.Object(kind, name), Name: name, Kind: kind, Engine: engine, Comment: comment})
	}
	return result, safeError(rows.Err())
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
	structure := &contract.TableStructure{Object: o, Database: o.Catalog, Name: o.Name, Kind: o.Kind}
	err = s.conn.QueryRow(child, "SELECT engine,comment,create_table_query FROM system.tables WHERE database=? AND name=?", o.Catalog, o.Name).Scan(&structure.Engine, &structure.Comment, &structure.Definition)
	if err != nil {
		return nil, safeError(err)
	}
	kind := "TABLE"
	if strings.Contains(structure.Engine, "View") {
		kind = "VIEW"
	}
	if !strings.EqualFold(kind, o.Kind) {
		return nil, connections.ErrInvalid
	}
	rows, err := s.conn.Query(child, "SELECT name,type,position,default_kind,default_expression,comment,is_in_primary_key FROM system.columns WHERE database=? AND table=? ORDER BY position", o.Catalog, o.Name)
	if err != nil {
		return nil, safeError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, typ, defaultKind, expr, comment string
		var position uint64
		var primary uint8
		if err = rows.Scan(&name, &typ, &position, &defaultKind, &expr, &comment, &primary); err != nil {
			return nil, safeError(err)
		}
		col := contract.StructureColumn{Name: name, Type: typ, DataType: typ, Position: int64(position), Nullable: strings.Contains(typ, "Nullable("), Generated: defaultKind == "MATERIALIZED" || defaultKind == "ALIAS", Extra: defaultKind, Comment: comment, ValueKind: valueKind(typ)}
		if defaultKind != "" {
			col.Default = &expr
		}
		if primary != 0 {
			col.Key = "PRI"
		}
		structure.Columns = append(structure.Columns, col)
	}
	return structure, safeError(rows.Err())
}
