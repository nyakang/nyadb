package manticore

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const exportBatch = 1000

func (d *dataLease) exportRows(ctx context.Context, o contract.DataOptions, fields []string, sink func([]contract.StructureColumn, []contract.Cell) error) error {
	if sink == nil {
		return connections.ErrInvalid
	}
	var e error
	o, e = captureOptions(o, d.object)
	if e != nil {
		return e
	}
	fields = slices.Clone(fields)
	child, done, e := d.request(ctx)
	if e != nil {
		return e
	}
	defer done()
	m, e := d.table(child)
	if e != nil {
		return e
	}
	// Resolve the immutable requested projection before dispatch, retaining exact
	// source order and rejecting duplicate or foreign column names.
	q, e := selectData(m, o, fields, 0, exportBatch)
	if e != nil {
		return e
	}
	columns := slices.Clone(m.Columns)
	if len(fields) > 0 {
		columns = nil
		for _, field := range fields {
			for _, col := range m.Columns {
				if col.Name == field {
					columns = append(columns, col)
					break
				}
			}
		}
	}
	owner, e := d.auxiliary()
	if e != nil {
		return e
	}
	defer owner.Release()
	// Each native query explicitly raises max_matches to cover its requested
	// range. The native default would otherwise silently stop a large export.
	// These are live ordered reads, not a database transaction or frozen cursor.
	for offset := int64(0); ; offset += exportBatch {
		if e = child.Err(); e != nil {
			return e
		}
		if offset > math.MaxInt32-exportBatch {
			return contract.ErrUnsupported
		}
		if offset > 0 {
			q, e = selectData(m, o, fields, offset, exportBatch)
			if e != nil {
				return e
			}
		}
		seen := 0
		var sinkError error
		_, e = owner.ExecuteStream(child, d.object.Catalog, q.sql, func(_ int, wireColumns []contract.Column, row []contract.Cell) error {
			seen++
			if seen > exportBatch {
				return contract.ErrDataPage
			}
			cells, e := dataCells(columns, wireColumns, row, false)
			if e != nil {
				return e
			}
			sinkError = sink(slices.Clone(columns), cells)
			return sinkError
		})
		if e != nil {
			return errors.Join(e, sinkError)
		}
		if seen < exportBatch {
			return nil
		}
	}
}
func (d *dataLease) export(ctx context.Context, id int64, database, table string, o contract.DataOptions, fields []string, sink func([]contract.DataValue) error) error {
	if e := d.identity(id, database, table); e != nil {
		return e
	}
	if sink == nil {
		return connections.ErrInvalid
	}
	return d.exportRows(ctx, o, fields, func(_ []contract.StructureColumn, cells []contract.Cell) error {
		values := make([]contract.DataValue, len(cells))
		for i, c := range cells {
			values[i] = contract.DataValue{Kind: c.Kind, Text: c.Text, Bytes: c.Bytes, Null: c.Kind == contract.Null, Binary: c.Kind == contract.Binary}
		}
		return sink(values)
	})
}
func (d *dataLease) jsonl(ctx context.Context, object contract.ObjectRef, w io.Writer) error {
	if object != d.object || w == nil {
		return connections.ErrInvalid
	}
	encoder := json.NewEncoder(w)
	return d.exportRows(ctx, contract.DataOptions{Object: object}, nil, func(columns []contract.StructureColumn, cells []contract.Cell) error {
		return encoder.Encode(struct {
			Object  contract.ObjectRef         `json:"object"`
			Columns []contract.StructureColumn `json:"columns"`
			Values  []contract.Cell            `json:"values"`
		}{object, columns, cells})
	})
}
