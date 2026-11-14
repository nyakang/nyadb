package doris

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func verifySelectDBIdentity(comment, catalog string) error {
	// SelectDB is a product identity, not merely a compatible VERSION().
	// An ordinary Doris server is rejected even if it shares the catalog.
	if !strings.HasPrefix(comment, "selectdb-doris-2.1.6-") || catalog != "internal" {
		return errors.New("unsupported SelectDB product or kernel; requires native SelectDB 2.1.6")
	}
	return nil
}

func (s *Session) catalog() string {
	if s.profile.Kind == connections.SelectDB {
		return s.profile.SelectDBOptions.Catalog
	}
	return "internal"
}

// queryRows preserves the product's selected wire transport. It never falls
// back to another command after a failed request, including a failed prepare.
func queryRows(ctx context.Context, c *physical, q string, args ...any) (*sql.Rows, func(), error) {
	if c.textProtocol {
		rows, err := c.conn.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, nil, err
		}
		return rows, func() { _ = rows.Close() }, nil
	}
	statement, err := c.conn.PrepareContext(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	rows, err := statement.QueryContext(ctx, args...)
	if err != nil {
		_ = statement.Close()
		return nil, nil, err
	}
	return rows, func() { _ = rows.Close(); _ = statement.Close() }, nil
}

func selectDBBrowse(ctx context.Context, c *physical, n contract.NamespaceRef) ([]contract.DBObject, error) {
	rows, err := metadataRows(ctx, c, "SHOW FULL TABLES FROM "+quote(n.Catalog)+"."+quote(n.Schema))
	if err != nil {
		return nil, err
	}
	objects := make([]contract.DBObject, 0, len(rows))
	for _, row := range rows {
		// SelectDB 2.1.6 appends storage and inverted-index formats. They are
		// native table properties, not an engine name or a relational key.
		if len(row) != 4 || !row[0].Valid || !validName(row[0].String) || !row[1].Valid {
			return nil, contract.ErrUnsupported
		}
		var kind string
		switch strings.ToUpper(row[1].String) {
		case "BASE TABLE":
			kind = "TABLE"
		case "VIEW":
			kind = "VIEW"
		default:
			continue
		}
		objects = append(objects, contract.DBObject{Ref: n.Object(kind, row[0].String), Name: row[0].String, Kind: kind})
	}
	return objects, nil
}

func selectDBTableMetadata(ctx context.Context, c *physical, o contract.ObjectRef) (*contract.TableStructure, error) {
	objects, err := selectDBBrowse(ctx, c, o.Namespace())
	if err != nil {
		return nil, err
	}
	found := false
	for _, object := range objects {
		if object.Name == o.Name {
			if !strings.EqualFold(object.Kind, o.Kind) {
				return nil, connections.ErrInvalid
			}
			found = true
			break
		}
	}
	if !found {
		return nil, connections.ErrNotFound
	}
	defs, err := metadataRows(ctx, c, "SHOW CREATE TABLE "+qualified(o))
	if err != nil {
		return nil, err
	}
	if len(defs) != 1 || len(defs[0]) < 2 || !defs[0][1].Valid || defs[0][1].String == "" {
		return nil, contract.ErrUnsupported
	}
	t := &contract.TableStructure{Object: o, Database: o.Schema, Name: o.Name, Kind: o.Kind, Editable: false, Definition: defs[0][1].String}
	t.Definitions = []contract.Definition{{Name: o.Name, Kind: o.Kind, SQL: t.Definition}}
	cols, err := metadataRows(ctx, c, "DESCRIBE "+qualified(o))
	if err != nil {
		return nil, err
	}
	for i, row := range cols {
		if len(row) != 6 || !row[0].Valid || !validName(row[0].String) || !row[1].Valid {
			return nil, contract.ErrUnsupported
		}
		column := contract.StructureColumn{Name: row[0].String, Type: row[1].String, DataType: row[1].String, Nullable: strings.EqualFold(row[2].String, "Yes"), Position: int64(i + 1), Extra: row[5].String, ValueKind: valueKind(row[1].String)}
		if strings.EqualFold(row[3].String, "true") {
			column.Key = "SORT"
		}
		if row[4].Valid {
			v := row[4].String
			column.Default = &v
		}
		t.Columns = append(t.Columns, column)
	}
	if len(t.Columns) == 0 || len(t.Columns) > 512 {
		return nil, contract.ErrUnsupported
	}
	return t, nil
}
