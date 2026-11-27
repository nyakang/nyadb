package prestosql

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func exportProjection(m *contract.TableStructure, fields []string) (*dataProjection, error) {
	selected := *m
	if len(fields) > 0 {
		selected.Columns = nil
		seen := map[string]bool{}
		for _, name := range fields {
			if seen[name] {
				return nil, connections.ErrInvalid
			}
			seen[name] = true
			i := slices.IndexFunc(m.Columns, func(c contract.StructureColumn) bool { return c.Name == name })
			if i < 0 {
				return nil, connections.ErrInvalid
			}
			selected.Columns = append(selected.Columns, m.Columns[i])
		}
	}
	return newDataProjection(&selected, 0)
}
func (l *dataLease) exportRows(ctx context.Context, o contract.DataOptions, fields []string, sink func([]contract.StructureColumn, []contract.Cell) error) error {
	fields = slices.Clone(fields)
	o.Filters = slices.Clone(o.Filters)
	o.Sorts = slices.Clone(o.Sorts)
	if sink == nil || o.Object != (contract.ObjectRef{}) && o.Object != l.object {
		return connections.ErrInvalid
	}
	child, done, err := l.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	metadata, err := l.session.TableMetadata(child, l.object)
	if err != nil {
		return err
	}
	projection, err := exportProjection(metadata, fields)
	if err != nil {
		return err
	}
	source, err := dataSelect(metadata, projection, o, false)
	if err != nil {
		return err
	}
	l.session.mu.Lock()
	owner, err := l.session.dataOwnerLocked(l.ctx, "data-export", l.session.auxSlots)
	l.session.mu.Unlock()
	if err != nil {
		return err
	}
	defer owner.Release()
	return owner.streamNative(child, source, math.MaxInt32, func(result queryResult) error {
		rows, err := projection.decode(result)
		if err != nil {
			return err
		}
		for _, row := range rows {
			cells := make([]contract.Cell, len(row))
			for i, c := range row {
				if c.Value == nil || c.Truncated {
					return ErrResponse
				}
				cells[i] = c.Value.Clone()
			}
			columns := slices.Clone(projection.columns)
			if err = sink(columns, cells); err != nil {
				return err
			}
		}
		return nil
	})
}
func (l *dataLease) export(ctx context.Context, id int64, db, table string, o contract.DataOptions, fields []string, sink func([]contract.DataValue) error) error {
	if sink == nil {
		return connections.ErrInvalid
	}
	if err := l.identity(id, db, table); err != nil {
		return err
	}
	return l.exportRows(ctx, o, fields, func(_ []contract.StructureColumn, cells []contract.Cell) error {
		values := make([]contract.DataValue, len(cells))
		for i, c := range cells {
			values[i] = contract.DataValue{Kind: c.Kind, Text: c.Text, Bytes: c.Bytes, Null: c.Kind == contract.Null, Binary: c.Kind == contract.Binary}
		}
		return sink(values)
	})
}
func (l *dataLease) jsonl(ctx context.Context, object contract.ObjectRef, w io.Writer) error {
	if w == nil || object != l.object {
		return connections.ErrInvalid
	}
	encoder := json.NewEncoder(w)
	return l.exportRows(ctx, contract.DataOptions{Object: object}, nil, func(columns []contract.StructureColumn, cells []contract.Cell) error {
		return encoder.Encode(struct {
			Object  contract.ObjectRef         `json:"object"`
			Columns []contract.StructureColumn `json:"columns"`
			Values  []contract.Cell            `json:"values"`
		}{object, columns, cells})
	})
}
