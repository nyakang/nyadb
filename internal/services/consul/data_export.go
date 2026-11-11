package consul

import (
	"bytes"
	"context"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// Export validates a complete bounded original HTTP observation before emitting
// any rows. It never joins several live reads into a claimed fixed snapshot.
func (l *dataLease) export(parent context.Context, id int64, catalog, name string, options contract.DataOptions, columns []string, emit func([]contract.DataValue) error) error {
	if err := l.check(id, catalog, name); err != nil {
		return err
	}
	if !validDataOptions(l.object, options) || options.Offset != 0 || options.After != nil || emit == nil || len(columns) == 0 {
		return contract.ErrUnsupported
	}
	names := []string{"key", "value", "flags", "create_index", "modify_index", "lock_index", "session"}
	indices := make([]int, len(columns))
	seen := map[string]bool{}
	for i, name := range columns {
		indices[i] = slices.Index(names, name)
		if indices[i] < 0 || seen[name] {
			return ErrInvalid
		}
		seen[name] = true
	}
	ctx, done, err := l.begin(parent)
	if err != nil {
		return err
	}
	defer done()
	_, entries, err := l.fullObservation(ctx, dataExportScope)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := l.check(id, catalog, name); err != nil {
			return err
		}
		cells := dataCells(entry, false)
		values := make([]contract.DataValue, len(indices))
		for i, index := range indices {
			cell := cells[index].Value
			values[i] = contract.DataValue{Kind: cell.Kind, Text: cell.Text, Bytes: bytes.Clone(cell.Bytes), Null: cell.Kind == contract.Null, Binary: cell.Kind == contract.Binary}
		}
		if err := emit(values); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return l.check(id, catalog, name)
}
