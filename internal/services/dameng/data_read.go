package dameng

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) dataObject(id int64, schema, name string, requested contract.ObjectRef) (contract.ObjectRef, error) {
	o := s.Namespace(schema).Object("table", name)
	if id != s.ProfileID() || requested != (contract.ObjectRef{}) && requested != o {
		return o, connections.ErrInvalid
	}
	return o, s.checkObject(o)
}

// A bounded read holds one original physical connection, a serializable
// snapshot and a row-share table lock. Native GET_DDL rejects READ ONLY;
// only adapter-owned reads are issued here. SELECT LIMIT 0 alone does not
// prevent native DDL. No database/sql transaction retry path is involved.
func (s *Session) dataRead(ctx context.Context, o contract.ObjectRef, read func(context.Context, *sql.Conn, *contract.TableStructure) error) (err error) {
	return s.dataReadLocked(ctx, o, false, read)
}

func (s *Session) dataReadLocked(ctx context.Context, o contract.ObjectRef, complete bool, read func(context.Context, *sql.Conn, *contract.TableStructure) error) (err error) {
	if err = s.checkObject(o); err != nil {
		return err
	}
	if o.Kind != "table" {
		return contract.ErrUnsupported
	}
	ctx, done, err := s.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	c, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	var tx driver.Tx
	defer func() {
		if tx != nil {
			rollbackErr := tx.Rollback()
			err = errors.Join(err, rollbackErr)
			if rollbackErr != nil {
				_ = c.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
		if connectionLost(err) {
			_ = c.Raw(func(any) error { return driver.ErrBadConn })
		}
		err = errors.Join(err, c.Close())
	}()
	err = c.Raw(func(raw any) error {
		begin, ok := raw.(driver.ConnBeginTx)
		if !ok {
			return contract.ErrUnsupported
		}
		var e error
		tx, e = begin.BeginTx(ctx, driver.TxOptions{})
		return e
	})
	if err != nil {
		_ = c.Raw(func(any) error { return driver.ErrBadConn })
		return err
	}
	mode := "ROW SHARE"
	if complete {
		mode = "SHARE"
	}
	for _, q := range []string{"SET TRANSACTION ISOLATION LEVEL SERIALIZABLE", "LOCK TABLE " + qualified(o) + " IN " + mode + " MODE"} {
		if _, err = c.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	t, err := structureAt(ctx, c, o)
	if err != nil {
		return err
	}
	if len(t.Columns) > 512 {
		return contract.ErrUnsupported
	}
	return read(ctx, c, t)
}

func dataProjection(t *contract.TableStructure, units int) (string, error) {
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		name := quote(c.Name)
		var expr, length string
		switch c.DataType {
		case "CLOB", "TEXT", "BLOB", "IMAGE":
			length = "SYS.DBMS_LOB.GETLENGTH(" + name + ")"
			expr = fmt.Sprintf("SYS.DBMS_LOB.SUBSTR(%s,%d,1)", name, units+1)
		case "BINARY", "VARBINARY":
			length = "SYS.UTL_RAW.LENGTH(" + name + ")"
			expr = fmt.Sprintf("SYS.UTL_RAW.SUBSTR(%s,1,LEAST(%d,%s))", name, units+1, length)
		case "CHAR", "VARCHAR":
			length = "LENGTH(" + name + ")"
			expr = fmt.Sprintf("SUBSTR(%s,1,%d)", name, units+1)
		case "DECIMAL", "NUMERIC", "NUMBER", "DEC", "INT", "INTEGER", "BIGINT", "SMALLINT", "TINYINT", "BIT", "BOOLEAN", "REAL", "DOUBLE", "FLOAT", "DATE", "TIME", "TIMESTAMP", "TIME WITH TIME ZONE", "TIMESTAMP WITH TIME ZONE":
			expr = name
		default:
			return "", contract.ErrUnsupported
		}
		if length != "" {
			empty := name
			// Returning the original LOB in CASE forces native conversion of a
			// large value even on the bounded branch (-6108). Fixed constructors
			// preserve empty versus NULL without sending the original large LOB.
			switch c.DataType {
			case "CLOB", "TEXT":
				empty = "EMPTY_CLOB()"
			case "BLOB", "IMAGE":
				empty = "EMPTY_BLOB()"
			}
			expr = "CASE WHEN " + length + "=0 THEN " + empty + " ELSE " + expr + " END"
		}
		cols[i] = expr + " AS " + name
	}
	return strings.Join(cols, ","), nil
}

func boundedCell(c contract.Cell, units int) contract.Cell {
	if c.Kind == contract.Binary && len(c.Bytes) > units {
		c.Bytes, c.Truncated = bytes.Clone(c.Bytes[:units]), true
	}
	if utf8.RuneCountInString(c.Text) > units {
		at := 0
		for i := 0; i < units; i++ {
			_, n := utf8.DecodeRuneInString(c.Text[at:])
			at += n
		}
		c.Text, c.Truncated = strings.Clone(c.Text[:at]), true
	}
	return c
}

func dataCell(c contract.Cell) contract.DataCell {
	clone := c.Clone()
	text := c.Text
	if c.Kind == contract.Binary {
		text = strings.ToUpper(hex.EncodeToString(c.Bytes))
	}
	return contract.DataCell{Value: &clone, Text: text, Null: c.Kind == contract.Null, Truncated: c.Truncated}
}

func scanData(rows *sql.Rows, t *contract.TableStructure, limit, units int) ([][]contract.Cell, error) {
	var result [][]contract.Cell
	budget := 16 << 20
	for rows.Next() {
		if len(result) >= limit {
			return nil, contract.ErrUnsupported
		}
		values, dest := make([]any, len(t.Columns)), make([]any, len(t.Columns))
		for i := range dest {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := make([]contract.Cell, len(values))
		size := 24 + len(row)*192
		for i, v := range values {
			c, err := cell(v, t.Columns[i].DataType)
			if err != nil {
				return nil, err
			}
			row[i] = boundedCell(c, units)
			// Binary bytes are retained as typed cells and as hexadecimal display
			// text, with an independent preview copy. Include that retained cost.
			size += len(row[i].Text) + len(row[i].Bytes)*4
		}
		if size > budget {
			return nil, errors.New("DM8 page memory limit exceeded; reduce page size")
		}
		budget -= size
		result = append(result, row)
	}
	return result, rows.Err()
}

func tableIdentityAt(ctx context.Context, q metadataQueryer, o contract.ObjectRef) (string, error) {
	var id int64
	var created, changed, temporary string
	err := q.QueryRowContext(ctx, `SELECT OBJECT_ID,TO_CHAR(CREATED,'YYYY-MM-DD HH24:MI:SS.FF6'),TO_CHAR(LAST_DDL_TIME,'YYYY-MM-DD HH24:MI:SS.FF6'),TEMPORARY FROM SYS.ALL_OBJECTS WHERE OWNER=? AND OBJECT_NAME=? AND OBJECT_TYPE='TABLE' AND SUBOBJECT_NAME IS NULL`, o.Schema, o.Name).Scan(&id, &created, &changed, &temporary)
	if err != nil {
		return "", err
	}
	if id <= 0 || temporary != "N" {
		return "", contract.ErrUnsupported
	}
	return fmt.Sprintf("%d/%s/%s", id, created, changed), nil
}

func (s *Session) loadData(ctx context.Context, id int64, schema, name string, o contract.DataOptions) (out *contract.DataPage, err error) {
	object, err := s.dataObject(id, schema, name, o.Object)
	if err != nil {
		return nil, err
	}
	if o.Limit <= 0 {
		o.Limit = 100
	}
	if o.Limit > 1000 || o.Offset < 0 || o.Offset > 1<<53 {
		return nil, connections.ErrInvalid
	}
	started := time.Now()
	err = s.dataRead(ctx, object, func(ctx context.Context, q *sql.Conn, t *contract.TableStructure) error {
		identity, e := tableIdentityAt(ctx, q, object)
		if e != nil {
			return e
		}
		key, e := primaryKeyAt(ctx, q, t)
		if e != nil {
			return e
		}
		where, args, e := dataWhere(t, o)
		if e != nil {
			return e
		}
		order, stable, e := dataOrder(t, o, key)
		if e != nil {
			return e
		}
		signature := s.dataSignature(t, o, order, identity)
		if o.After != nil {
			if !o.UseKeyset || !stable {
				return contract.ErrUnsupported
			}
			seek, e := seekWhere(t, order, signature, o.After, &args)
			if e != nil {
				return e
			}
			where = "(" + where + ") AND " + seek
		}
		projection, e := dataProjection(t, 1024)
		if e != nil {
			return e
		}
		text := "SELECT " + projection + " FROM " + qualified(object) + " WHERE " + where
		if len(order) > 0 {
			var terms []string
			for _, term := range order {
				direction := " ASC"
				if term.Descending {
					direction = " DESC"
				}
				terms = append(terms, quote(term.Column)+direction)
			}
			text += " ORDER BY " + strings.Join(terms, ",")
		}
		text += fmt.Sprintf(" LIMIT %d", o.Limit+1)
		if o.After == nil {
			text += fmt.Sprintf(" OFFSET %d", o.Offset)
		}
		rows, e := q.QueryContext(ctx, text, args...)
		if e != nil {
			return e
		}
		values, e := scanData(rows, t, o.Limit+1, 1024)
		if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
			return e
		}
		editable := false
		out = &contract.DataPage{Object: object, Editable: &editable, Database: schema, Table: name, Offset: o.Offset, Limit: o.Limit, HasMore: len(values) > o.Limit, StableOrder: stable, KeysetAvailable: stable && o.UseKeyset, QuerySQL: text, ColumnMetadata: t.Columns}
		for _, c := range t.Columns {
			out.Columns = append(out.Columns, c.Name)
			out.ColumnKinds = append(out.ColumnKinds, c.DataType)
		}
		if out.HasMore {
			values = values[:o.Limit]
		}
		positions := map[string]int{}
		for i, c := range t.Columns {
			positions[c.Name] = i
		}
		for _, row := range values {
			var display []contract.DataCell
			for _, c := range row {
				display = append(display, dataCell(c))
			}
			out.Rows = append(out.Rows, display)
			var ref *contract.RowRef
			if len(key) > 0 {
				ref = &contract.RowRef{Object: object, Database: schema, Table: name, Columns: append([]string(nil), key...)}
				for _, k := range key {
					c := row[positions[k]].Clone()
					if c.Kind == contract.Null || c.Truncated {
						ref = nil
						break
					}
					b, _ := json.Marshal(c)
					ref.TypedValues = append(ref.TypedValues, c)
					ref.Values = append(ref.Values, b)
				}
			}
			out.RowRefs = append(out.RowRefs, ref)
		}
		if out.KeysetAvailable && len(values) > 0 {
			cursor := &contract.DataCursor{RuleSignature: signature, Database: schema, Table: name}
			for _, term := range order {
				c := values[len(values)-1][positions[term.Column]]
				if c.Truncated || c.Kind == contract.Null {
					out.KeysetAvailable = false
					break
				}
				b, _ := json.Marshal(c)
				cursor.Columns = append(cursor.Columns, term.Column)
				cursor.Descending = append(cursor.Descending, term.Descending)
				cursor.Values = append(cursor.Values, b)
			}
			if out.KeysetAvailable {
				out.NextCursor = cursor
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	out.Duration, out.CompletedAt = time.Since(started), time.Now()
	return out, nil
}

func (s *Session) countData(ctx context.Context, id int64, schema, name string) (count int64, err error) {
	o, err := s.dataObject(id, schema, name, contract.ObjectRef{})
	if err != nil {
		return 0, err
	}
	err = s.dataRead(ctx, o, func(ctx context.Context, q *sql.Conn, _ *contract.TableStructure) error {
		return q.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(o)).Scan(&count)
	})
	return count, err
}

func (s *Session) chunkData(ctx context.Context, id int64, schema, name, field string, ref contract.RowRef, offset int64, units int) (out *contract.FieldChunk, err error) {
	o, err := s.dataObject(id, schema, name, ref.Object)
	if err != nil {
		return nil, err
	}
	if offset < 0 || offset > 1<<53 || units <= 0 || units > 1024 {
		return nil, connections.ErrInvalid
	}
	err = s.dataRead(ctx, o, func(ctx context.Context, q *sql.Conn, t *contract.TableStructure) error {
		key, e := primaryKeyAt(ctx, q, t)
		if e != nil {
			return e
		}
		where, args, e := rowWhere(t, key, ref)
		if e != nil {
			return e
		}
		c, ok := dataColumns(t)[field]
		if !ok {
			return connections.ErrInvalid
		}
		col := quote(c.Name)
		binary := c.ValueKind == contract.Binary
		var length, part string
		switch c.DataType {
		case "CLOB", "TEXT", "BLOB", "IMAGE":
			length = "SYS.DBMS_LOB.GETLENGTH(" + col + ")"
			part = fmt.Sprintf("SYS.DBMS_LOB.SUBSTR(%s,%d,%d)", col, units, offset+1)
		case "BINARY", "VARBINARY":
			length = "SYS.UTL_RAW.LENGTH(" + col + ")"
			part = fmt.Sprintf("SYS.UTL_RAW.SUBSTR(%s,%d,LEAST(%d,%s-%d))", col, offset+1, units, length, offset)
		case "CHAR", "VARCHAR":
			length = "LENGTH(" + col + ")"
			part = fmt.Sprintf("SUBSTR(%s,%d,%d)", col, offset+1, units)
		default:
			return contract.ErrUnsupported
		}
		// UTL_RAW.SUBSTR rejects amounts extending beyond the remaining bytes;
		// it also rejects an offset beyond EOF instead of returning SQL NULL.
		part = fmt.Sprintf("CASE WHEN %s>%d THEN %s ELSE NULL END", length, offset, part)
		var total sql.NullInt64
		var raw any
		e = q.QueryRowContext(ctx, "SELECT "+length+","+part+" FROM "+qualified(o)+" WHERE "+where, args...).Scan(&total, &raw)
		if errors.Is(e, sql.ErrNoRows) {
			return connections.ErrNotFound
		}
		if e != nil {
			return e
		}
		out = &contract.FieldChunk{Offset: offset, NextOffset: offset, Null: !total.Valid, Binary: binary, TotalUnits: total.Int64}
		if !total.Valid || offset >= total.Int64 {
			return nil
		}
		v, e := cell(raw, c.DataType)
		if e != nil {
			return e
		}
		if v.Kind == contract.Null {
			return errors.New("DM8 field slice unexpectedly returned NULL")
		}
		read := int64(utf8.RuneCountInString(v.Text))
		out.Text = v.Text
		if binary {
			read = int64(len(v.Bytes))
			out.Text = strings.ToUpper(hex.EncodeToString(v.Bytes))
		}
		if read <= 0 || read > int64(units) || read > total.Int64-offset {
			return contract.ErrUnsupported
		}
		out.NextOffset = offset + read
		out.HasMore = out.NextOffset < total.Int64
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Session) DataReads() contract.DataReads {
	return contract.DataReads{AdvancedLoader: s.loadData, Loader: func(ctx context.Context, id int64, schema, name string, offset int64, limit int) (*contract.DataPage, error) {
		return s.loadData(ctx, id, schema, name, contract.DataOptions{Offset: offset, Limit: limit})
	}, Count: s.countData, Chunk: s.chunkData, RowReader: s.readDataRow, TableMetadata: func(ctx context.Context, id int64, schema, name string) (*contract.TableStructure, error) {
		o, err := s.dataObject(id, schema, name, contract.ObjectRef{})
		if err != nil {
			return nil, err
		}
		return s.TableMetadata(ctx, o)
	}, TableInfo: func(ctx context.Context, id int64, schema, name string) (out *contract.TableInfo, err error) {
		o, err := s.dataObject(id, schema, name, contract.ObjectRef{})
		if err != nil {
			return nil, err
		}
		err = s.dataRead(ctx, o, func(_ context.Context, _ *sql.Conn, t *contract.TableStructure) error {
			out = &contract.TableInfo{Database: schema, Name: name, Kind: "BASE TABLE", Engine: "Dameng", Comment: t.Comment}
			return nil
		})
		return out, err
	}}
}

func (s *Session) DataFilterModes() []string {
	return []string{"equal", "not_equal", "less", "greater", "less_equal", "greater_equal", "like", "not_like", "is_null", "is_not_null"}
}

var _ contract.DataReadsProvider = (*Session)(nil)
