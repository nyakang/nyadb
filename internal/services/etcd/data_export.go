package etcd

import (
	"bytes"
	"context"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// Export has its own executor and preserves complete byte values and decimal
// int64 text at the original revision. Consumers control file encoding.
func (l *dataLease) export(ctx context.Context, id int64, catalog, name string, options contract.DataOptions, columns []string, emit func([]contract.DataValue) error) error {
	if err := l.check(id, catalog, name); err != nil {
		return err
	}
	if !l.exportMu.TryLock() {
		return contract.ErrKVBusy
	}
	defer l.exportMu.Unlock()
	if options.Object != (contract.ObjectRef{}) && options.Object != l.object || options.Offset != 0 || options.After != nil || options.Condition != nil || len(options.Filters) > 0 || len(options.Sorts) > 0 || options.FilterColumn != "" || options.FilterMode != "" || options.FilterValue != "" || options.SortColumn != "" || options.SortDescending || emit == nil {
		return contract.ErrUnsupported
	}
	names := []string{"key", "value", "create_revision", "mod_revision", "version", "lease"}
	indices := make([]int, len(columns))
	if len(columns) == 0 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for i, column := range columns {
		indices[i] = slices.Index(names, column)
		if indices[i] < 0 || seen[column] {
			return ErrInvalid
		}
		seen[column] = true
	}
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	revision, err := l.originalRevision(ctx, dataExportScope)
	if err != nil {
		return err
	}
	// Range has no server-side value slicing or byte limit. One key per RPC
	// prevents a batch of individually valid large values exceeding transport.
	request := PrefixRange(l.session.prefix, 1, revision)
	var previous []byte
	for {
		result, err := l.native(ctx, dataExportScope, request)
		if err != nil {
			return err
		}
		if !result.Completed || result.Reply == nil || result.ProjectionError != "" || result.Reply.Kind != "range" {
			return ErrProtocol
		}
		for _, entry := range result.Reply.Entries {
			if !validDataEntry(entry) || !bytes.HasPrefix(entry.Key, l.session.prefix) || bytes.Compare(entry.Key, request.Key) < 0 || previous != nil && bytes.Compare(entry.Key, previous) <= 0 || entry.ModRevision > revision {
				return ErrProtocol
			}
			previous = bytes.Clone(entry.Key)
			values := make([]contract.DataValue, len(columns))
			cells := entryCells(entry, false)
			for i, index := range indices {
				cell := cells[index].Value
				values[i] = contract.DataValue{Kind: cell.Kind, Text: cell.Text, Bytes: bytes.Clone(cell.Bytes), Binary: cell.Kind == contract.Binary}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := emit(values); err != nil {
				return err
			}
		}
		if !result.Reply.More {
			return ctx.Err()
		}
		if len(result.Reply.Entries) == 0 {
			return ErrProtocol
		}
		request.Key = dataAfterKey(previous)
		if request.Key == nil || !bytes.HasPrefix(request.Key, l.session.prefix) {
			return ErrProtocol
		}
	}
}
