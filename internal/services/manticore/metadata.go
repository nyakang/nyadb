package manticore

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func validName(name string) bool {
	return name != "" && len(name) <= 1024 && utf8.ValidString(name) && strings.IndexFunc(name, unicode.IsControl) < 0
}
func quote(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }
func (s *Session) Namespace(database string) contract.NamespaceRef {
	if database != "Manticore" {
		return contract.NamespaceRef{ProfileID: s.ProfileID()}
	}
	return contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: database}
}
func (s *Session) checkNamespace(n contract.NamespaceRef) error {
	if !s.Alive() {
		return contract.ErrRevoked
	}
	if n.ProfileID != s.ProfileID() || n.Catalog != "Manticore" || n.Schema != "" {
		return connections.ErrInvalid
	}
	return nil
}
func (s *Session) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
	var result []contract.Namespace
	err := s.withConnection(ctx, func(child context.Context, c driver.Conn) error {
		set, e := read(child, c, "SHOW DATABASES", 0, nil, nil)
		if e != nil {
			return e
		}
		if !hasDataCatalog(set) {
			return contract.ErrUnsupported
		}
		result = append(result, contract.Namespace{Ref: s.Namespace("Manticore"), Label: "Manticore"})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// The native data catalog is distinct from the information_schema compatibility
// catalog reported by newer servers. That catalog is not yet published by this
// private adapter, and its presence must not reject an otherwise valid server.
func hasDataCatalog(set contract.ResultSet) bool {
	if len(set.Truncation) > 0 || len(set.Columns) != 1 || (set.Columns[0].Name != "Database" && set.Columns[0].Name != "Databases") {
		return false
	}
	found := false
	for _, row := range set.Rows {
		if len(row) != 1 || !validName(row[0].Text) {
			return false
		}
		if row[0].Text == "Manticore" {
			if found {
				return false
			}
			found = true
		}
	}
	return found
}

func tableObjects(n contract.NamespaceRef, set contract.ResultSet) ([]contract.DBObject, error) {
	if len(set.Truncation) > 0 || len(set.Columns) != 2 || (set.Columns[0].Name != "Index" && set.Columns[0].Name != "Table") || set.Columns[1].Name != "Type" {
		return nil, contract.ErrUnsupported
	}
	var objects []contract.DBObject
	for _, row := range set.Rows {
		if len(row) != 2 || !validName(row[0].Text) || row[1].Text == "" {
			return nil, contract.ErrUnsupported
		}
		// Preserve the native rt/plain/distributed/percolate kind as engine data;
		// listing alone never grants editing or administrative capabilities.
		objects = append(objects, contract.DBObject{Ref: n.Object("TABLE", row[0].Text), Name: row[0].Text, Kind: "TABLE", Engine: row[1].Text})
	}
	return objects, nil
}
func (s *Session) Browse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	if e := s.checkNamespace(n); e != nil {
		return nil, e
	}
	var objects []contract.DBObject
	err := s.withConnection(ctx, func(child context.Context, c driver.Conn) error {
		set, e := read(child, c, "SHOW TABLES", 0, nil, nil)
		if e != nil {
			return e
		}
		objects, e = tableObjects(n, set)
		if e != nil {
			return e
		}
		plugins, e := read(child, c, "SHOW PLUGINS", 0, nil, nil)
		if e != nil {
			return e
		}
		functions, e := pluginObjects(n, plugins)
		if e != nil {
			return e
		}
		objects = append(objects, functions...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return objects, nil
}
func (s *Session) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	native := o
	if native.Kind == "table" {
		native.Kind = "TABLE"
	}
	m, err := s.tableMetadata(ctx, native, true)
	if m != nil {
		m.Object, m.Kind = o, o.Kind
	}
	return m, err
}

// Data grants compare stable column/DDL observations, never changing index
// coverage or status. Extra native metadata belongs to explicit structure reads.
func (s *Session) tableMetadata(ctx context.Context, o contract.ObjectRef, indexes bool) (*contract.TableStructure, error) {
	if e := s.checkNamespace(o.Namespace()); e != nil {
		return nil, e
	}
	if !validName(o.Name) || o.Kind != "TABLE" {
		return nil, contract.ErrUnsupported
	}
	structure := &contract.TableStructure{Object: o, Database: o.Catalog, Name: o.Name, Kind: o.Kind}
	err := s.withConnection(ctx, func(child context.Context, c driver.Conn) error {
		tables, e := read(child, c, "SHOW TABLES", 0, nil, nil)
		if e != nil {
			return e
		}
		objects, e := tableObjects(o.Namespace(), tables)
		if e != nil {
			return e
		}
		found := false
		for _, obj := range objects {
			if obj.Name == o.Name {
				structure.Engine = obj.Engine
				found = true
				break
			}
		}
		if !found {
			return connections.ErrNotFound
		}
		// DESCRIBE is accepted by both 6.3.8 and 32.1.3. SHOW FULL COLUMNS is
		// unsupported by both; SHOW COLUMNS is unsupported by 6.3.8.
		fields, e := read(child, c, "DESCRIBE "+quote(o.Catalog)+"."+quote(o.Name), 0, nil, nil)
		if e != nil {
			return e
		}
		if len(fields.Truncation) > 0 || len(fields.Columns) != 3 || fields.Columns[0].Name != "Field" || fields.Columns[1].Name != "Type" || fields.Columns[2].Name != "Properties" {
			return contract.ErrUnsupported
		}
		for i, row := range fields.Rows {
			if len(row) != 3 || !validName(row[0].Text) || row[1].Text == "" {
				return contract.ErrUnsupported
			}
			// DESCRIBE does not establish a primary key, nullability or uniqueness.
			// In particular, the implicit id column must not become a fabricated PK.
			structure.Columns = append(structure.Columns, contract.StructureColumn{Name: row[0].Text, Type: row[1].Text, DataType: row[1].Text, Extra: row[2].Text, Position: int64(i + 1), ValueKind: valueKind(row[1].Text)})
		}
		command := "SHOW CREATE TABLE " + quote(o.Name)
		ddl, e := read(child, c, command, 0, nil, nil)
		if e != nil {
			// A plain/local index has readable fields and attributes but no
			// CREATE TABLE definition. Admit only this original native response;
			// a missing table, transport failure or cancellation still fails.
			var server *mysqldriver.MySQLError
			if structure.Engine != "local" || child.Err() != nil || !errors.As(e, &server) || server.Number != 1146 || string(server.SQLState[:]) != "42S02" || server.Message != fmt.Sprintf("table '%s' is not real-time or percolate", o.Name) {
				return e
			}
			message := server.Message
			if secret := s.profileSecret(); secret != "" {
				message = strings.ReplaceAll(message, secret, "[redacted]")
			}
			raw, err := json.Marshal(contract.NativeMetadataObservation{Command: command, ErrorCode: server.Number, SQLState: string(server.SQLState[:]), Error: message})
			if err != nil {
				return err
			}
			structure.Definitions = append(structure.Definitions, contract.Definition{Name: o.Name, Kind: "manticore_definition", SQL: string(raw)})
		} else {
			if len(ddl.Truncation) > 0 || len(ddl.Rows) != 1 || len(ddl.Rows[0]) != 2 || ddl.Rows[0][0].Text != o.Name {
				return contract.ErrUnsupported
			}
			structure.Definition = ddl.Rows[0][1].Text
		}
		if indexes {
			observation, e := s.indexObservation(child, c, o)
			if e != nil {
				return e
			}
			structure.Definitions = append(structure.Definitions, observation)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return structure, nil
}

var _ contract.Session = (*Session)(nil)
var _ contract.MetadataProvider = (*Session)(nil)
var _ contract.QueryProvider = (*Session)(nil)
var _ contract.QueryLeaseStatus = (*queryLease)(nil)
