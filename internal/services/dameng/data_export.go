package dameng

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) DataExports() contract.DataExports {
	return contract.DataExports{Exporter: s.exportData, JSONL: s.exportJSONL}
}

func exportColumns(t *contract.TableStructure, names []string) ([]contract.StructureColumn, error) {
	if len(names) == 0 {
		for _, c := range t.Columns {
			names = append(names, c.Name)
		}
	}
	if len(names) == 0 || len(names) > 128 {
		return nil, connections.ErrInvalid
	}
	columns := dataColumns(t)
	seen := make(map[string]bool, len(names))
	selected := make([]contract.StructureColumn, 0, len(names))
	for _, name := range names {
		c, ok := columns[name]
		if !ok || seen[name] {
			return nil, connections.ErrInvalid
		}
		seen[name] = true
		selected = append(selected, c)
	}
	// Reuse the verified type whitelist, without using a preview as output.
	_, err := dataProjection(&contract.TableStructure{Columns: selected}, 1024)
	return selected, err
}

func exportTableAt(ctx context.Context, q metadataQueryer, t *contract.TableStructure) error {
	if _, err := tableIdentityAt(ctx, q, t.Object); err != nil {
		return err
	}
	var status, partitioned, temporary, locking, longRow string
	err := q.QueryRowContext(ctx, `SELECT STATUS,PARTITIONED,TEMPORARY,TABLE_LOCK,LONG_ROW FROM SYS.ALL_TABLES WHERE OWNER=? AND TABLE_NAME=?`, t.Object.Schema, t.Object.Name).Scan(&status, &partitioned, &temporary, &locking, &longRow)
	if err != nil {
		return err
	}
	if status != "VALID" || partitioned != "NO" || temporary != "N" || locking != "ENABLED" || longRow != "DISABLED" {
		return contract.ErrUnsupported
	}
	return nil
}

func (s *Session) exportData(ctx context.Context, id int64, schema, name string, options contract.DataOptions, names []string, sink func([]contract.DataValue) error) error {
	if sink == nil || options.After != nil || options.UseKeyset || options.Offset < 0 || options.Offset > 1<<53 || options.Limit < 0 || options.Limit > 1000 {
		return connections.ErrInvalid
	}
	o, err := s.dataObject(id, schema, name, options.Object)
	if err != nil {
		return err
	}
	return s.completeDataRead(ctx, o, func(ctx context.Context, q *sql.Conn, t *contract.TableStructure) error {
		selected, err := exportColumns(t, names)
		if err != nil {
			return err
		}
		if err = exportTableAt(ctx, q, t); err != nil {
			return err
		}
		where, args, err := dataWhere(t, options)
		if err != nil {
			return err
		}
		key, err := primaryKeyAt(ctx, q, t)
		if err != nil {
			return err
		}
		order, _, err := dataOrder(t, options, key)
		if err != nil {
			return err
		}
		terms := make([]string, 0, len(order)+1)
		for _, term := range order {
			direction := " ASC"
			if term.Descending {
				direction = " DESC"
			}
			terms = append(terms, quote(term.Column)+direction)
		}
		// Physical identity preserves duplicates without requiring an editable
		// primary key. It is private to this locked original read, never a RowRef.
		terms = append(terms, "ROWID ASC")
		query := "SELECT ROWID FROM " + qualified(o) + " WHERE " + where + " ORDER BY " + strings.Join(terms, ",")
		if options.Limit > 0 {
			query += fmt.Sprintf(" LIMIT %d", options.Limit)
		}
		if options.Offset > 0 && options.Limit > 0 {
			query += fmt.Sprintf(" OFFSET %d", options.Offset)
		}
		skip := int64(0)
		if options.Limit == 0 {
			// Native OFFSET without LIMIT rejects -2007. Skip only physical
			// identities while streaming, without an invented result-size cap.
			skip = options.Offset
		}
		return exportRowsAt(ctx, q, o, selected, query, args, skip, sink)
	})
}

func exportRowsAt(ctx context.Context, q metadataQueryer, o contract.ObjectRef, columns []contract.StructureColumn, query string, args []any, skip int64, sink func([]contract.DataValue) error) (err error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rows.Err(), rows.Close()) }()
	types, err := rows.ColumnTypes()
	if err != nil {
		return err
	}
	if len(types) != 1 {
		return contract.ErrUnsupported
	}
	// This kernel exposes its ROWID pseudocolumn as BIGINT through DPI.
	if types[0].DatabaseTypeName() != "BIGINT" {
		return fmt.Errorf("DM8 physical export identity type %s: %w", types[0].DatabaseTypeName(), contract.ErrUnsupported)
	}
	for rows.Next() {
		if err = ctx.Err(); err != nil {
			return err
		}
		var rowID string
		if err = rows.Scan(&rowID); err != nil {
			return err
		}
		if rowID == "" || len(rowID) > 128 {
			return contract.ErrUnsupported
		}
		if skip > 0 {
			skip--
			continue
		}
		values, e := exportRowAt(ctx, q, o, columns, rowID)
		if e != nil {
			return e
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = sink(values); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func exportFieldLength(c contract.StructureColumn) string {
	length := fieldLength(c)
	if c.ValueKind == contract.Text {
		length = "LENGTHB(" + quote(c.Name) + ")"
	}
	// Native LENGTHB(empty text) returns NULL although the field is not NULL.
	return "CASE WHEN " + quote(c.Name) + " IS NULL THEN NULL ELSE COALESCE(" + length + ",0) END"
}

func exportRowAt(ctx context.Context, q metadataQueryer, o contract.ObjectRef, columns []contract.StructureColumn, rowID string) ([]contract.DataValue, error) {
	var lengths []string
	var positions []int
	for i, c := range columns {
		if variableField(c) {
			lengths = append(lengths, exportFieldLength(c))
			positions = append(positions, i)
		}
	}
	totals := make([]sql.NullInt64, len(columns))
	if len(lengths) > 0 {
		dest := make([]any, len(lengths))
		for i, pos := range positions {
			dest[i] = &totals[pos]
		}
		err := q.QueryRowContext(ctx, "SELECT "+strings.Join(lengths, ",")+" FROM "+qualified(o)+" WHERE ROWID=?", rowID).Scan(dest...)
		if err != nil {
			return nil, err
		}
		var size int64
		for _, pos := range positions {
			n := totals[pos].Int64
			if n < 0 || n > contract.DataExportFieldMaxBytes {
				return nil, contract.ErrDataEditLimit
			}
			size += n
		}
		if size > contract.DataExportRowMaxBytes {
			return nil, contract.ErrDataEditLimit
		}
	}
	values := make([]contract.DataValue, len(columns))
	size := 0
	for i, c := range columns {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// One field per response avoids the private worker's 16 MiB frame
		// boundary even for binary/base64 or JSON-escaped complete rows.
		var raw any
		if err := q.QueryRowContext(ctx, "SELECT "+quote(c.Name)+" FROM "+qualified(o)+" WHERE ROWID=?", rowID).Scan(&raw); err != nil {
			return nil, err
		}
		v, err := cell(raw, c.DataType)
		if err != nil {
			return nil, err
		}
		bytes := len(v.Text) + len(v.Bytes)
		if variableField(c) && (totals[i].Valid != (v.Kind != contract.Null) || totals[i].Valid && totals[i].Int64 != int64(bytes)) {
			return nil, fmt.Errorf("DM8 export field %s length valid=%t expected=%d actual=%d null=%t: %w", c.Name, totals[i].Valid, totals[i].Int64, bytes, v.Kind == contract.Null, contract.ErrDataConflict)
		}
		size += bytes
		if v.Truncated || bytes > contract.DataExportFieldMaxBytes || size > contract.DataExportRowMaxBytes {
			return nil, contract.ErrDataEditLimit
		}
		values[i] = contract.DataValue{Kind: v.Kind, Text: v.Text, Bytes: v.Bytes, Null: v.Kind == contract.Null, Binary: v.Kind == contract.Binary}
	}
	return values, nil
}

func (s *Session) exportJSONL(ctx context.Context, o contract.ObjectRef, w io.Writer) error {
	if w == nil {
		return connections.ErrInvalid
	}
	encoder := json.NewEncoder(w)
	return s.exportData(ctx, o.ProfileID, o.Schema, o.Name, contract.DataOptions{Object: o}, nil, func(row []contract.DataValue) error { return encoder.Encode(row) })
}

var _ contract.DataExportsProvider = (*Session)(nil)
