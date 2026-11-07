package trino

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func metadataRows(ctx context.Context, c *Client, q string) ([][]contract.Cell, error) {
	var rows [][]contract.Cell
	budget := 16 << 20
	_, err := c.Run(ctx, q, func(cols []Column, raw []json.RawMessage) error {
		if len(rows) >= 10000 {
			return ErrBounds
		}
		row := make([]contract.Cell, len(raw))
		for i, v := range raw {
			cell, err := nativeCell(v, cols[i].Type)
			if err != nil {
				return err
			}
			budget -= len(cell.Text) + len(cell.Bytes) + 64
			if budget < 0 {
				return ErrBounds
			}
			row[i] = cell
		}
		rows = append(rows, row)
		return nil
	})
	return rows, err
}
func (s *Session) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
	ctx, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	c, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	catalogs, err := metadataRows(ctx, c, "SHOW CATALOGS")
	if err != nil {
		return nil, err
	}
	var result []contract.Namespace
	known := map[string]contract.NamespaceRef{}
	for _, row := range catalogs {
		if len(row) != 1 || !validName(row[0].Text) {
			return nil, ErrProtocol
		}
		catalog := row[0].Text
		schemas, err := metadataRows(ctx, c, "SHOW SCHEMAS FROM "+quoteName(catalog))
		if err != nil {
			return nil, err
		}
		for _, r := range schemas {
			if len(r) != 1 || !validName(r[0].Text) || len(result) >= 10000 {
				return nil, ErrBounds
			}
			label := namespaceLabel(catalog, r[0].Text)
			ref := contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: catalog, Schema: r[0].Text}
			if _, exists := known[label]; exists {
				return nil, ErrProtocol
			}
			known[label] = ref
			result = append(result, contract.Namespace{Ref: ref, Label: label})
		}
	}
	preferred := namespaceLabel(s.profile.Database, s.profile.TrinoOptions.DefaultSchema)
	for i, n := range result {
		if n.Label == preferred {
			result = append([]contract.Namespace{n}, append(result[:i], result[i+1:]...)...)
			break
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, contract.ErrRevoked
	}
	s.namespaces = known
	return result, nil
}
func (s *Session) Browse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	if err := s.checkNamespace(n); err != nil {
		return nil, err
	}
	ctx, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	c, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	rows, err := metadataRows(ctx, c, "SELECT table_name,table_type FROM "+quoteName(n.Catalog)+".information_schema.tables WHERE table_schema="+quoteText(n.Schema)+" ORDER BY table_name")
	if err != nil {
		return nil, err
	}
	var out []contract.DBObject
	for _, r := range rows {
		if len(r) != 2 || !validName(r[0].Text) {
			return nil, ErrProtocol
		}
		kind := "TABLE"
		if r[1].Text == "VIEW" {
			kind = "VIEW"
		} else if r[1].Text != "BASE TABLE" {
			return nil, contract.ErrUnsupported
		}
		out = append(out, contract.DBObject{Ref: n.Object(kind, r[0].Text), Name: r[0].Text, Kind: kind, Engine: n.Catalog})
	}
	return out, nil
}
func tableMetadata(ctx context.Context, c *Client, o contract.ObjectRef) (*contract.TableStructure, error) {
	rows, err := metadataRows(ctx, c, "SELECT table_type FROM "+quoteName(o.Catalog)+".information_schema.tables WHERE table_schema="+quoteText(o.Schema)+" AND table_name="+quoteText(o.Name))
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 || len(rows[0]) != 1 {
		return nil, connections.ErrNotFound
	}
	kind := "TABLE"
	if rows[0][0].Text == "VIEW" {
		kind = "VIEW"
	} else if rows[0][0].Text != "BASE TABLE" {
		return nil, contract.ErrUnsupported
	}
	if !strings.EqualFold(o.Kind, kind) {
		return nil, connections.ErrInvalid
	}
	t := &contract.TableStructure{Object: o, Database: namespaceLabel(o.Catalog, o.Schema), Name: o.Name, Kind: kind, Engine: o.Catalog, Editable: false}
	fields, err := metadataRows(ctx, c, "SELECT column_name,data_type,is_nullable,column_default,ordinal_position FROM "+quoteName(o.Catalog)+".information_schema.columns WHERE table_schema="+quoteText(o.Schema)+" AND table_name="+quoteText(o.Name)+" ORDER BY ordinal_position")
	if err != nil {
		return nil, err
	}
	if len(fields) == 0 || len(fields) > 512 {
		return nil, ErrBounds
	}
	for i, r := range fields {
		if len(r) != 5 || !validName(r[0].Text) {
			return nil, ErrProtocol
		}
		pos, e := strconv.ParseInt(r[4].Text, 10, 64)
		if e != nil || pos != int64(i+1) {
			return nil, ErrProtocol
		}
		col := contract.StructureColumn{Name: r[0].Text, Type: r[1].Text, DataType: r[1].Text, Nullable: r[2].Text == "YES", Position: pos, ValueKind: valueKind(r[1].Text)}
		if r[3].Kind != contract.Null {
			v := r[3].Text
			col.Default = &v
		}
		t.Columns = append(t.Columns, col)
	}
	defs, err := metadataRows(ctx, c, "SHOW CREATE "+kind+" "+qualified(o))
	var queryErr *QueryError
	if err != nil {
		if errors.As(err, &queryErr) && queryErr.Name == "NOT_SUPPORTED" {
			return t, nil
		}
		return nil, err
	}
	if len(defs) != 1 || len(defs[0]) != 1 || defs[0][0].Kind != contract.Text {
		return nil, ErrProtocol
	}
	t.Definition = defs[0][0].Text
	t.Definitions = []contract.Definition{{Name: o.Name, Kind: kind, SQL: t.Definition}}
	return t, nil
}
func (s *Session) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	if err := s.checkObject(o); err != nil {
		return nil, err
	}
	ctx, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	c, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return tableMetadata(ctx, c, o)
}
func (s *Session) resolve(ctx context.Context, id int64, db, table string, o contract.ObjectRef) (*contract.TableStructure, error) {
	if !s.Alive() {
		return nil, contract.ErrRevoked
	}
	n := s.Namespace(db)
	if n.ProfileID != id || s.checkNamespace(n) != nil || !validName(table) {
		return nil, connections.ErrInvalid
	}
	if o.ProfileID != 0 {
		if o.Namespace() != n || o.Name != table {
			return nil, connections.ErrInvalid
		}
		return s.TableMetadata(ctx, o)
	}
	ctx, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	c, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	r, err := metadataRows(ctx, c, "SELECT table_type FROM "+quoteName(n.Catalog)+".information_schema.tables WHERE table_schema="+quoteText(n.Schema)+" AND table_name="+quoteText(table))
	if err != nil {
		return nil, err
	}
	if len(r) != 1 || len(r[0]) != 1 {
		return nil, connections.ErrNotFound
	}
	kind := "TABLE"
	if r[0][0].Text == "VIEW" {
		kind = "VIEW"
	}
	return tableMetadata(ctx, c, n.Object(kind, table))
}
