package tdengine

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func validName(name string) bool {
	return name != "" && len(name) <= 192 && utf8.ValidString(name) && strings.IndexFunc(name, unicode.IsControl) < 0 && !strings.ContainsAny(name, "`/\\")
}
func quote(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }
func literal(text string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(text, "\\", "\\\\"), "'", "\\'") + "'"
}
func qualified(db, table string) string { return quote(db) + "." + quote(table) }
func (s *Session) Namespace(db string) contract.NamespaceRef {
	return contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: db}
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
	if !validName(o.Name) || !strings.EqualFold(o.Kind, "TABLE") && !strings.EqualFold(o.Kind, "STABLE") {
		return contract.ErrUnsupported
	}
	return nil
}
func metadataRead(ctx context.Context, w *wire, sql string) (contract.ResultSet, error) {
	// Native definitions and metadata must retain complete text independently
	// of the SQL preview's 4096-byte cells. Bound the complete publication.
	var rows [][]contract.Cell
	budget, size := 0, 0
	set, _, err := w.query(ctx, sql, func(_ []contract.Column, cells []contract.Cell) error {
		size += 64 * len(cells)
		for _, c := range cells {
			if c.Truncated {
				return contract.ErrUnsupported
			}
			size += len(c.Text) + len(c.Bytes)
		}
		if len(rows) >= 10000 || size > 32<<20 {
			return contract.ErrUnsupported
		}
		rows = append(rows, cells)
		return nil
	}, &budget)
	set.Rows, set.Truncation = rows, nil
	return set, err
}
func (s *Session) Namespaces(ctx context.Context) (result []contract.Namespace, err error) {
	err = s.withConnection(ctx, func(child context.Context, w *wire) error {
		rows, e := metadataRead(child, w, "SHOW DATABASES")
		if e != nil {
			return e
		}
		for _, row := range rows.Rows {
			if len(row) == 0 || !validName(row[0].Text) {
				return contract.ErrUnsupported
			}
			name := row[0].Text
			if name == "information_schema" || name == "performance_schema" {
				continue
			}
			result = append(result, contract.Namespace{Ref: s.Namespace(name), Label: name})
		}
		return nil
	})
	return
}
func (s *Session) browse(ctx context.Context, w *wire, n contract.NamespaceRef) ([]contract.DBObject, error) {
	stables, err := metadataRead(ctx, w, "SHOW "+quote(n.Catalog)+".STABLES")
	if err != nil {
		return nil, err
	}
	tables, err := metadataRead(ctx, w, "SELECT table_name, stable_name FROM information_schema.ins_tables WHERE db_name="+literal(n.Catalog))
	if err != nil {
		return nil, err
	}
	result := []contract.DBObject{}
	for _, row := range stables.Rows {
		if len(row) == 0 || !validName(row[0].Text) {
			return nil, contract.ErrUnsupported
		}
		name := row[0].Text
		result = append(result, contract.DBObject{Ref: n.Object("stable", name), Name: name, Kind: "表", Engine: "TDengine", Comment: "STABLE"})
	}
	for _, row := range tables.Rows {
		if len(row) != 2 || !validName(row[0].Text) {
			return nil, contract.ErrUnsupported
		}
		name := row[0].Text
		o := contract.DBObject{Ref: n.Object("table", name), Name: name, Kind: "表", Engine: "TDengine"}
		if row[1].Kind != contract.Null && row[1].Text != "" {
			if !validName(row[1].Text) {
				return nil, contract.ErrUnsupported
			}
			o.ParentObject = n.Object("stable", row[1].Text)
			o.Comment = "STABLE: " + row[1].Text
		}
		result = append(result, o)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Ref.Kind != result[j].Ref.Kind {
			return result[i].Ref.Kind == "stable"
		}
		if result[i].ParentObject.Name != result[j].ParentObject.Name {
			return result[i].ParentObject.Name < result[j].ParentObject.Name
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}
func (s *Session) Browse(ctx context.Context, n contract.NamespaceRef) (result []contract.DBObject, err error) {
	if err = s.checkNamespace(n); err != nil {
		return nil, err
	}
	err = s.withConnection(ctx, func(child context.Context, w *wire) error { var e error; result, e = s.browse(child, w, n); return e })
	return
}
func (s *Session) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	if err := s.checkObject(o); err != nil {
		return nil, err
	}
	var result *contract.TableStructure
	err := s.withConnection(ctx, func(child context.Context, w *wire) error {
		var e error
		result, e = s.tableMetadataOnWire(child, w, o)
		return e
	})
	return result, err
}

func (s *Session) tableMetadataOnWire(ctx context.Context, w *wire, o contract.ObjectRef) (*contract.TableStructure, error) {
	objects, e := s.browse(ctx, w, o.Namespace())
	if e != nil {
		return nil, e
	}
	found := false
	for _, candidate := range objects {
		if candidate.Ref == o {
			found = true
			break
		}
	}
	if !found {
		return nil, contract.ErrUnsupported
	}
	rows, e := metadataRead(ctx, w, "DESCRIBE "+qualified(o.Catalog, o.Name))
	if e != nil {
		return nil, e
	}
	source, e := metadataRead(ctx, w, "SHOW CREATE "+strings.ToUpper(o.Kind)+" "+qualified(o.Catalog, o.Name))
	if e != nil {
		return nil, e
	}
	if len(source.Rows) != 1 || len(source.Rows[0]) < 2 || source.Rows[0][1].Kind != contract.Text {
		return nil, contract.ErrUnsupported
	}
	t := &contract.TableStructure{Object: o, Database: o.Catalog, Name: o.Name, Kind: o.Kind, Engine: "TDengine", Definition: source.Rows[0][1].Text, Definitions: []contract.Definition{{Name: o.Name, Kind: o.Kind, SQL: source.Rows[0][1].Text}}, Editable: false}
	for i, row := range rows.Rows {
		if len(row) < 4 || !validName(row[0].Text) {
			return nil, contract.ErrUnsupported
		}
		typ, note := row[1].Text, row[3].Text
		column := contract.StructureColumn{Name: row[0].Text, Type: typ, DataType: typ, Position: int64(i + 1), Nullable: true, ValueKind: valueKind(typ), Extra: note}
		if strings.Contains(note, "TAG") {
			column.Comment = "TAG"
		} else if i == 0 || strings.Contains(note, "COMPOSITE KEY") {
			column.Nullable = false
			if !strings.EqualFold(o.Kind, "stable") {
				column.Key = "PRI"
			}
		}
		if typ == "VARCHAR" || typ == "NCHAR" || typ == "VARBINARY" {
			n, e := strconv.ParseInt(row[2].Text, 10, 64)
			if e != nil {
				return nil, contract.ErrUnsupported
			}
			column.CharacterMaxLength = &n
			column.Type = typ + "(" + row[2].Text + ")"
		}
		t.Columns = append(t.Columns, column)
	}
	return t, nil
}

func (s *Session) resolve(ctx context.Context, id int64, db, table string, o contract.ObjectRef) (*contract.TableStructure, error) {
	if o.ProfileID != 0 {
		if o.ProfileID != id || o.Catalog != db || o.Name != table {
			return nil, connections.ErrInvalid
		}
		return s.TableMetadata(ctx, o)
	}
	objects, err := s.Browse(ctx, contract.NamespaceRef{ProfileID: id, Catalog: db})
	if err != nil {
		return nil, err
	}
	for _, candidate := range objects {
		if candidate.Name == table {
			return s.TableMetadata(ctx, candidate.Ref)
		}
	}
	return nil, contract.ErrUnsupported
}
func (s *Session) tableMetadata(ctx context.Context, id int64, db, table string) (*contract.TableStructure, error) {
	return s.resolve(ctx, id, db, table, contract.ObjectRef{})
}
