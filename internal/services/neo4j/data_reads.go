package neo4j

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (l *dataLease) count(ctx context.Context, id int64, db, name string) (int64, error) {
	if e := l.identity(id, db, name); e != nil {
		return 0, e
	}
	ctx, done, e := l.request(ctx)
	if e != nil {
		return 0, e
	}
	defer done()
	c, closeClient, e := l.client(ctx)
	if e != nil {
		return 0, e
	}
	defer closeClient()
	r, e := l.read(ctx, c, l.match()+" RETURN count(entity) AS count", nil, 1)
	if e != nil {
		return 0, e
	}
	if !slices.Equal(r.Columns, []string{"count"}) || len(r.Rows) != 1 || len(r.Rows[0]) != 1 || r.Rows[0][0].Kind != "integer" {
		return 0, ErrProtocol
	}
	n, e := strconv.ParseInt(r.Rows[0][0].Text, 10, 64)
	if e != nil || n < 0 {
		return 0, ErrProtocol
	}
	return n, nil
}
func (l *dataLease) metadata(ctx context.Context, id int64, db, name string) (*contract.TableStructure, error) {
	if e := l.identity(id, db, name); e != nil {
		return nil, e
	}
	ctx, done, e := l.request(ctx)
	if e != nil {
		return nil, e
	}
	defer done()
	return &contract.TableStructure{Object: l.object, Database: db, Name: name, Kind: l.object.Kind, Engine: "Neo4j", Columns: l.columns(), Editable: false, Definition: "Native graph projection; label/type and properties are live observations, not relational schema"}, ctx.Err()
}
func (l *dataLease) info(ctx context.Context, id int64, db, name string) (*contract.TableInfo, error) {
	if _, e := l.metadata(ctx, id, db, name); e != nil {
		return nil, e
	}
	return &contract.TableInfo{Database: db, Name: name, Kind: l.object.Kind, Engine: "Neo4j"}, nil
}

// Streaming uses bounded native reads on one original driver. It does not
// create a consistent snapshot across pages and never restarts after failure.
func (l *dataLease) stream(ctx context.Context, id int64, db, name string, o contract.DataOptions, fields []string, sink func(Value, []contract.Cell) error) error {
	if e := l.identity(id, db, name); e != nil {
		return e
	}
	o = cloneDataOptions(o)
	fields = slices.Clone(fields)
	if e := l.options(o); e != nil {
		return e
	}
	if o.After != nil || o.Offset != 0 {
		return contract.ErrUnsupported
	}
	ctx, done, e := l.request(ctx)
	if e != nil {
		return e
	}
	defer done()
	columns := l.columns()
	if len(fields) == 0 {
		for _, c := range columns {
			fields = append(fields, c.Name)
		}
	}
	indices := make([]int, len(fields))
	seen := map[string]bool{}
	for i, f := range fields {
		j := slices.IndexFunc(columns, func(c contract.StructureColumn) bool { return c.Name == f })
		if j < 0 || seen[f] {
			return contract.ErrUnsupported
		}
		seen[f] = true
		indices[i] = j
	}
	c, closeClient, e := l.client(ctx)
	if e != nil {
		return e
	}
	defer closeClient()
	after := ""
	for {
		values, _, e := l.entities(ctx, c, after, 100)
		if e != nil {
			return e
		}
		for _, v := range values {
			if e = ctx.Err(); e != nil {
				return e
			}
			cells, e := l.project(v)
			if e != nil {
				return e
			}
			selected := make([]contract.Cell, len(indices))
			for i, j := range indices {
				if len(cells[j].Text)+len(cells[j].Bytes) > 1<<20 {
					return ErrBounds
				}
				selected[i] = cells[j].Clone()
			}
			if e = sink(v, selected); e != nil {
				return e
			}
			after, _ = l.entityID(v)
		}
		if len(values) < 100 {
			return ctx.Err()
		}
	}
}
func (l *dataLease) exportRows(ctx context.Context, id int64, db, name string, o contract.DataOptions, fields []string, sink func([]contract.DataValue) error) error {
	if sink == nil {
		return ErrInvalid
	}
	return l.stream(ctx, id, db, name, o, fields, func(_ Value, cells []contract.Cell) error {
		out := make([]contract.DataValue, len(cells))
		for i, c := range cells {
			out[i] = contract.DataValue{Kind: c.Kind, Text: c.Text, Bytes: slices.Clone(c.Bytes)}
		}
		return sink(out)
	})
}
func (l *dataLease) jsonl(ctx context.Context, o contract.ObjectRef, w io.Writer) error {
	if o != l.object || w == nil {
		return ErrInvalid
	}
	entity := "node"
	if o.Kind == "relationship" {
		entity = "relationship"
	}
	return l.stream(ctx, o.ProfileID, o.Catalog, o.Name, contract.DataOptions{Object: o}, []string{entity}, func(v Value, _ []contract.Cell) error {
		raw, e := json.Marshal(v)
		if e != nil {
			return e
		}
		if len(raw) > 1<<20 {
			return ErrBounds
		}
		raw = append(raw, '\n')
		n, e := w.Write(raw)
		if e == nil && n != len(raw) {
			e = io.ErrShortWrite
		}
		return e
	})
}
