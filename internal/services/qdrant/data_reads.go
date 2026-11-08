package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (l *dataLease) count(ctx context.Context, id int64, db, name string) (int64, error) {
	if err := l.identity(id, db, name); err != nil {
		return 0, err
	}
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer done()
	c, closeClient, err := l.client(ctx)
	if err != nil {
		return 0, err
	}
	defer closeClient()
	if _, err = l.guard(ctx, c); err != nil {
		return 0, err
	}
	n, _, err := c.Count(ctx, db, nil)
	if err != nil {
		return 0, err
	}
	if n > math.MaxInt64 {
		return 0, ErrBounds
	}
	if _, err = l.guard(ctx, c); err != nil {
		return 0, err
	}
	return int64(n), ctx.Err()
}
func (l *dataLease) metadata(ctx context.Context, id int64, db, name string) (*contract.TableStructure, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	c, closeClient, err := l.client(ctx)
	if err != nil {
		return nil, err
	}
	defer closeClient()
	raw, err := l.guard(ctx, c)
	if err != nil {
		return nil, err
	}
	columns := pointColumns()
	if _, err := vectorWriteSchema(raw); err != nil {
		columns[3].Generated = true
	}
	return &contract.TableStructure{Object: l.object, Database: db, Name: name, Kind: "points", Engine: "Qdrant", Columns: columns, Definition: string(raw), Comment: "Native live points; count and full JSONL cover the entire physical collection; point version is not CAS"}, ctx.Err()
}
func (l *dataLease) info(ctx context.Context, id int64, db, name string) (*contract.TableInfo, error) {
	m, err := l.metadata(ctx, id, db, name)
	if err != nil {
		return nil, err
	}
	return &contract.TableInfo{Database: db, Name: name, Kind: "points", Engine: m.Engine, Comment: m.Comment}, nil
}
func (l *dataLease) stream(ctx context.Context, id int64, db, name string, o contract.DataOptions, sink func(Point) error) error {
	if err := l.identity(id, db, name); err != nil {
		return err
	}
	if o.Object != l.object || o.Offset != 0 || o.After != nil || sink == nil {
		return contract.ErrUnsupported
	}
	o, err := cloneDataOptions(o)
	if err != nil {
		return err
	}
	q, err := pointDataQuery(o)
	if err != nil {
		return err
	}
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	c, closeClient, err := l.client(ctx)
	if err != nil {
		return err
	}
	defer closeClient()
	if _, err = l.guard(ctx, c); err != nil {
		return err
	}
	var position json.RawMessage
	history := make([]string, 0, 1024)
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		page, _, err := c.Scroll(ctx, db, position, 100, q.Filter)
		if err != nil {
			return err
		}
		if _, err = l.guard(ctx, c); err != nil {
			return err
		}
		for _, point := range page.Points {
			if err = ctx.Err(); err != nil {
				return err
			}
			if err = sink(point); err != nil {
				return err
			}
		}
		if len(page.NextOffset) == 0 {
			return ctx.Err()
		}
		digest := fingerprint(page.NextOffset)
		if slices.Contains(history, digest) {
			return ErrProtocol
		}
		if len(history) == 1024 {
			copy(history, history[1:])
			history = history[:1023]
		}
		history = append(history, digest)
		position = bytes.Clone(page.NextOffset)
	}
}
func (l *dataLease) exportRows(ctx context.Context, id int64, db, name string, o contract.DataOptions, fields []string, sink func([]contract.DataValue) error) error {
	if sink == nil {
		return ErrInvalid
	}
	if len(fields) > 4 {
		return contract.ErrUnsupported
	}
	fields = slices.Clone(fields)
	if len(fields) == 0 {
		fields = []string{"id", "vector", "payload", "point"}
	}
	indices := make([]int, len(fields))
	seen := map[string]bool{}
	for i, f := range fields {
		indices[i] = slices.Index([]string{"id", "vector", "payload", "point"}, f)
		if indices[i] < 0 || seen[f] {
			return contract.ErrUnsupported
		}
		seen[f] = true
	}
	return l.stream(ctx, id, db, name, o, func(p Point) error {
		cells := pointValues(p)
		values := make([]contract.DataValue, len(fields))
		for i, index := range indices {
			v := cells[index]
			values[i] = contract.DataValue{Kind: v.Kind, Text: v.Text, Null: v.Kind == contract.Null}
		}
		return sink(values)
	})
}
func (l *dataLease) jsonl(ctx context.Context, o contract.ObjectRef, w io.Writer) error {
	if o != l.object || w == nil {
		return ErrInvalid
	}
	return l.stream(ctx, o.ProfileID, o.Catalog, o.Name, contract.DataOptions{Object: o}, func(p Point) error {
		var b bytes.Buffer
		if err := json.Compact(&b, p.JSON); err != nil {
			return ErrProtocol
		}
		b.WriteByte('\n')
		raw := b.Bytes()
		n, err := w.Write(raw)
		if err == nil && n != len(raw) {
			return io.ErrShortWrite
		}
		return err
	})
}
