package cassandra

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"reflect"
	"slices"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (l *dataLease) count(ctx context.Context, id int64, db, name string) (int64, error) {
	if err := l.identity(id, db, name); err != nil {
		return 0, err
	}
	ctx, c, done, err := l.auxiliary(ctx)
	if err != nil {
		return 0, err
	}
	defer done()
	if _, err = l.guard(ctx, c); err != nil {
		return 0, err
	}
	r, err := c.execute(ctx, Request{Statement: "SELECT COUNT(*) FROM " + quoteName(db) + "." + quoteName(name), PageSize: 2}, true)
	if err != nil {
		return 0, err
	}
	if r.Kind != 2 || r.Next != nil || len(r.Rows) != 1 || len(r.Rows[0]) != 1 || r.Rows[0][0].Type == nil || r.Rows[0][0].Type.Code != 2 || r.Rows[0][0].Null {
		return 0, ErrProtocol
	}
	n, err := strconv.ParseInt(r.Rows[0][0].Text, 10, 64)
	if err != nil || n < 0 {
		return 0, ErrProtocol
	}
	if _, err = l.guard(ctx, c); err != nil {
		return 0, err
	}
	return n, ctx.Err()
}
func (l *dataLease) metadata(ctx context.Context, id int64, db, name string) (*contract.TableStructure, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	ctx, c, done, err := l.auxiliary(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	b, err := l.guard(ctx, c)
	if err != nil {
		return nil, err
	}
	return cloneStructure(b.Metadata), ctx.Err()
}
func (l *dataLease) info(ctx context.Context, id int64, db, name string) (*contract.TableInfo, error) {
	m, err := l.metadata(ctx, id, db, name)
	if err != nil {
		return nil, err
	}
	return &contract.TableInfo{Database: db, Name: name, Kind: m.Kind, Engine: m.Engine, Comment: m.Comment}, nil
}

// Stream every native page, without the query preview's 5000-row cap. It is a
// live scan with schema observations before publishing each bounded batch;
// cancellation does not affect the document's original paging connection.
func (l *dataLease) stream(ctx context.Context, id int64, db, name string, o contract.DataOptions, fields []string, sink func([]string, []Value) error) error {
	if err := l.identity(id, db, name); err != nil {
		return err
	}
	if o.Object != l.object || sink == nil {
		return ErrInvalid
	}
	if o.Offset != 0 || o.After != nil {
		return contract.ErrUnsupported
	}
	o.Limit = 100
	if err := dataOptions(o); err != nil {
		return err
	}
	fields = append([]string(nil), fields...)
	ctx, c, done, err := l.auxiliary(ctx)
	if err != nil {
		return err
	}
	defer done()
	b, err := l.guard(ctx, c)
	if err != nil {
		return err
	}
	if len(fields) == 0 {
		fields = append([]string(nil), b.Names...)
	}
	indices := make([]int, len(fields))
	seenFields := map[string]bool{}
	for i, f := range fields {
		indices[i] = slices.Index(b.Names, f)
		if indices[i] < 0 || seenFields[f] {
			return contract.ErrUnsupported
		}
		seenFields[f] = true
	}
	req := Request{Statement: dataSelect(l.object, b), PageSize: 100}
	// Keep a fixed window of cursor digests; streaming memory must not grow with
	// total rows/pages or retain up to 64 KiB for each native paging state.
	seen := map[[32]byte]bool{}
	var history [1024][32]byte
	pages := 0
	var columns []Column
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		r, e := c.execute(ctx, req, true)
		if e != nil {
			return e
		}
		if r.Kind != 2 {
			return ErrProtocol
		}
		if e = validateDataColumns(r.Columns, l.object, b); e != nil {
			return e
		}
		if columns == nil {
			columns = r.Columns
		} else if !reflect.DeepEqual(columns, r.Columns) {
			return ErrMetadataChanged
		}
		if _, e = l.guard(ctx, c); e != nil {
			return e
		}
		for _, row := range r.Rows {
			if err = ctx.Err(); err != nil {
				return err
			}
			if len(row) != len(b.Names) {
				return ErrProtocol
			}
			selected := make([]Value, len(fields))
			for i, j := range indices {
				selected[i] = row[j]
			}
			if e = sink(fields, selected); e != nil {
				return e
			}
		}
		if _, e = l.guard(ctx, c); e != nil {
			return e
		}
		if r.Next == nil {
			return ctx.Err()
		}
		token := sha256.Sum256(r.Next.state)
		if seen[token] {
			return ErrProtocol
		}
		seen[token] = true
		if pages >= len(history) {
			delete(seen, history[pages%len(history)])
		}
		history[pages%len(history)] = token
		pages++
		req.Cursor = r.Next.Clone()
	}
}
func (l *dataLease) exportRows(ctx context.Context, id int64, db, name string, o contract.DataOptions, fields []string, sink func([]contract.DataValue) error) error {
	if sink == nil {
		return ErrInvalid
	}
	return l.stream(ctx, id, db, name, o, fields, func(_ []string, values []Value) error {
		out := make([]contract.DataValue, len(values))
		budget := 1 << 20
		for i, v := range values {
			c, err := nativeCell(v)
			if err != nil {
				return err
			}
			budget -= len(c.Text) + len(c.Bytes) + 128
			if budget < 0 {
				return ErrBounds
			}
			out[i] = dataValue(c)
		}
		return sink(out)
	})
}
func (l *dataLease) jsonl(ctx context.Context, o contract.ObjectRef, w io.Writer) error {
	if o != l.object || w == nil {
		return ErrInvalid
	}
	return l.stream(ctx, o.ProfileID, o.Catalog, o.Name, contract.DataOptions{Object: o}, nil, func(fields []string, values []Value) error {
		out := make(map[string]encodedValue, len(fields))
		budget := 8 << 20
		for i, v := range values {
			budget -= len(fields[i]) * 6
			x, err := encodeValue(v, &budget)
			if err != nil {
				return err
			}
			out[fields[i]] = x
		}
		raw, err := json.Marshal(out)
		if err != nil || len(raw) > 1<<20 {
			return ErrBounds
		}
		raw = append(raw, '\n')
		n, err := w.Write(raw)
		if err == nil && n != len(raw) {
			err = io.ErrShortWrite
		}
		return err
	})
}
