package consul

import (
	"bytes"
	"context"
	"encoding/hex"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (l *dataLease) readEntry(ctx context.Context, ref contract.RowRef) (contract.KVResult, contract.KVHTTPEntry, error) {
	key, err := l.validateRef(ref)
	if err != nil {
		return contract.KVResult{}, contract.KVHTTPEntry{}, err
	}
	request, err := KeyReadRequest(key)
	if err != nil {
		return contract.KVResult{}, contract.KVHTTPEntry{}, err
	}
	result, err := l.read(ctx, dataFieldScope, request)
	if err != nil {
		return result, contract.KVHTTPEntry{}, err
	}
	entries, err := l.entries(result)
	if err != nil {
		return result, contract.KVHTTPEntry{}, err
	}
	if len(entries) != 1 {
		return result, contract.KVHTTPEntry{}, contract.ErrDataConflict
	}
	if entries[0].Key != key {
		return result, contract.KVHTTPEntry{}, ErrProtocol
	}
	return result, entries[0], nil
}

func (l *dataLease) row(parent context.Context, id int64, catalog, name string, ref contract.RowRef) (*contract.DataRow, error) {
	ref = cloneDataRef(ref)
	if err := l.check(id, catalog, name); err != nil {
		return nil, err
	}
	if _, err := l.validateRef(ref); err != nil {
		return nil, err
	}
	ctx, done, err := l.begin(parent)
	if err != nil {
		return nil, err
	}
	defer done()
	result, entry, err := l.readEntry(ctx, ref)
	if err != nil {
		return nil, err
	}
	row := &contract.DataRow{Columns: dataColumns(), Snapshot: l.snapshot(entry, result.HTTP.Metadata), Values: map[string]contract.DataValue{}}
	for i, cell := range dataCells(entry, false) {
		row.Values[row.Columns[i].Name] = contract.DataValue{Kind: cell.Value.Kind, Text: cell.Value.Text, Bytes: bytes.Clone(cell.Value.Bytes), Null: cell.Null, Binary: cell.Value.Kind == contract.Binary}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := l.check(id, catalog, name); err != nil {
		return nil, err
	}
	return row, nil
}

func (l *dataLease) chunk(parent context.Context, id int64, catalog, name, field string, ref contract.RowRef, offset int64, limit int) (*contract.FieldChunk, error) {
	ref = cloneDataRef(ref)
	if err := l.check(id, catalog, name); err != nil {
		return nil, err
	}
	if offset < 0 || limit < 1 || limit > 1024 || field != "key" && field != "value" {
		return nil, ErrInvalid
	}
	if _, err := l.validateRef(ref); err != nil {
		return nil, err
	}
	ctx, done, err := l.begin(parent)
	if err != nil {
		return nil, err
	}
	defer done()
	_, entry, err := l.readEntry(ctx, ref)
	if err != nil {
		return nil, err
	}
	chunk := &contract.FieldChunk{Offset: offset}
	if field == "value" {
		total := int64(len(entry.Value))
		start := min(offset, total)
		end := min(start+int64(limit), total)
		chunk.Text, chunk.Binary, chunk.Null = "0x"+hex.EncodeToString(entry.Value[start:end]), true, entry.ValueNull
		chunk.NextOffset, chunk.TotalUnits, chunk.HasMore = end, total, end < total
	} else {
		runes := []rune(entry.Key)
		total := int64(len(runes))
		start := min(offset, total)
		end := min(start+int64(limit), total)
		// Keep the UTF-8 preview at most 1024 bytes as well as 1024 characters.
		used := 0
		bounded := start
		for bounded < end && used+utf8.RuneLen(runes[bounded]) <= 1024 {
			used += utf8.RuneLen(runes[bounded])
			bounded++
		}
		end = bounded
		chunk.Text = string(runes[start:end])
		chunk.NextOffset, chunk.TotalUnits, chunk.HasMore = end, total, end < total
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := l.check(id, catalog, name); err != nil {
		return nil, err
	}
	return chunk, nil
}
