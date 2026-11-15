package questdb

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func validName(name string) bool {
	return name != "" && len(name) <= 1024 && utf8.ValidString(name) && strings.IndexFunc(name, unicode.IsControl) < 0
}
func quote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
func (s *Session) Namespace(database string) contract.NamespaceRef {
	if database != "qdb" {
		return contract.NamespaceRef{ProfileID: s.ProfileID()}
	}
	return contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: "qdb"}
}
func (s *Session) checkNamespace(n contract.NamespaceRef) error {
	if !s.Alive() {
		return contract.ErrRevoked
	}
	if n.ProfileID != s.ProfileID() || n.Catalog != "qdb" || n.Schema != "" {
		return connections.ErrInvalid
	}
	return nil
}
func (s *Session) checkObject(o contract.ObjectRef) error {
	if err := s.checkNamespace(o.Namespace()); err != nil {
		return err
	}
	if !validName(o.Name) || !strings.EqualFold(o.Kind, "TABLE") && !strings.EqualFold(o.Kind, "VIEW") && !strings.EqualFold(o.Kind, "MATERIALIZED VIEW") {
		return contract.ErrUnsupported
	}
	return nil
}
func (s *Session) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
	if err := s.checkNamespace(s.Namespace("qdb")); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, connections.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []contract.Namespace{{Ref: s.Namespace("qdb"), Label: "qdb"}}, nil
}
func tablesSQL(mode string) string {
	if mode == "basic" {
		return "SELECT table_name FROM tables()"
	}
	return "SELECT table_name," + mode + " FROM tables()"
}
func tableKind(row []contract.Cell, mode string) string {
	if len(row) > 1 {
		if mode == "matView" && row[1].Text == "true" || mode == "table_type" && row[1].Text == "M" {
			return "MATERIALIZED VIEW"
		}
		if mode == "table_type" && row[1].Text == "V" {
			return "VIEW"
		}
	}
	return "TABLE"
}
func (s *Session) Browse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	if err := s.checkNamespace(n); err != nil {
		return nil, err
	}
	var result []contract.DBObject
	err := s.withConnection(ctx, func(child context.Context, c *pgx.Conn) error {
		set, err := execute(child, c, tablesSQL(s.tableMode), nil, 0, nil, nil)
		if err != nil {
			return err
		}
		for _, row := range set.Rows {
			if len(row) == 0 || !validName(row[0].Text) {
				return contract.ErrUnsupported
			}
			kind := tableKind(row, s.tableMode)
			result = append(result, contract.DBObject{Ref: n.Object(kind, row[0].Text), Name: row[0].Text, Kind: kind, Engine: "QuestDB"})
		}
		return nil
	})
	return result, err
}
func (s *Session) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	if err := s.checkObject(o); err != nil {
		return nil, err
	}
	structure := &contract.TableStructure{Object: o, Database: "qdb", Name: o.Name, Kind: o.Kind, Engine: "QuestDB"}
	err := s.withConnection(ctx, func(child context.Context, c *pgx.Conn) error {
		objects, err := execute(child, c, tablesSQL(s.tableMode), nil, 0, nil, nil)
		if err != nil {
			return err
		}
		found := false
		for _, row := range objects.Rows {
			if len(row) > 0 && row[0].Text == o.Name {
				found = strings.EqualFold(tableKind(row, s.tableMode), o.Kind)
				break
			}
		}
		if !found {
			return connections.ErrNotFound
		}
		columns, err := execute(child, c, "SHOW COLUMNS FROM "+quote(o.Name), nil, 0, nil, nil)
		if err != nil {
			return err
		}
		ordinals := map[string]int{}
		for i, col := range columns.Columns {
			ordinals[strings.ToLower(col.Name)] = i
		}
		for _, required := range []string{"column", "type", "indexed"} {
			if _, ok := ordinals[required]; !ok {
				return contract.ErrUnsupported
			}
		}
		for position, row := range columns.Rows {
			get := func(name string) string {
				i, ok := ordinals[strings.ToLower(name)]
				if !ok || i >= len(row) {
					return ""
				}
				return row[i].Text
			}
			name, typ := get("column"), get("type")
			if !validName(name) || typ == "" {
				return contract.ErrUnsupported
			}
			col := contract.StructureColumn{Name: name, Type: typ, DataType: typ, Position: int64(position + 1), Nullable: typ != "BOOLEAN" && typ != "BYTE" && typ != "SHORT", ValueKind: valueKind(typ)}
			var extras []string
			if get("designated") == "true" {
				extras = append(extras, "designated timestamp")
			}
			if get("upsertKey") == "true" {
				extras = append(extras, "dedup upsert key")
			}
			if typ == "SYMBOL" {
				extras = append(extras, fmt.Sprintf("symbol capacity=%s cached=%s", get("symbolCapacity"), get("symbolCached")))
			}
			if get("indexed") == "true" {
				extras = append(extras, "index block capacity="+get("indexBlockCapacity"))
				structure.Indexes = append(structure.Indexes, contract.StructureIndex{Name: name, Type: "SYMBOL", Columns: []contract.StructureIndexColumn{{Name: name, Position: 1}}})
			}
			col.Extra = strings.Join(extras, "; ")
			structure.Columns = append(structure.Columns, col)
		}
		ddl := "SHOW CREATE TABLE "
		if strings.EqualFold(o.Kind, "VIEW") {
			ddl = "SHOW CREATE VIEW "
		} else if strings.EqualFold(o.Kind, "MATERIALIZED VIEW") {
			ddl = "SHOW CREATE MATERIALIZED VIEW "
		}
		definition, err := execute(child, c, ddl+quote(o.Name), nil, 0, nil, nil)
		if err != nil {
			return err
		}
		if len(definition.Rows) != 1 || len(definition.Rows[0]) != 1 || definition.Rows[0][0].Kind != contract.Text {
			return contract.ErrUnsupported
		}
		structure.Definition = definition.Rows[0][0].Text
		return nil
	})
	if err != nil {
		return nil, err
	}
	return structure, nil
}
