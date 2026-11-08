package cassandra

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type dataIdentity struct {
	Object       contract.ObjectRef
	ID, Binding  string
	Keys         []string
	Parameters   []Parameter
	Typed        []contract.Cell
	StaticOnly   bool
	Fingerprints []string
	Signature    string
}

// These are value observations, never native versions, transactions, or write
// predicates. Null, missing, and empty native bytes have distinct fingerprints.
func nativeFingerprint(v Value) string {
	h := sha256.New()
	flags := []byte{0, 0}
	if v.Null {
		flags[0] = 1
	}
	if v.Missing {
		flags[1] = 1
	}
	_, _ = h.Write(flags)
	_, _ = h.Write(v.Raw)
	return hex.EncodeToString(h.Sum(nil))
}
func (l *dataLease) snapshot(values []Value, b *dataBinding) (*contract.RowSnapshot, error) {
	identity := dataIdentity{Object: l.object, ID: b.ID, Binding: b.Signature, Keys: append([]string(nil), b.Keys...)}
	nullClustering := 0
	for _, key := range b.Keys {
		i := slices.Index(b.Names, key)
		if i < 0 || i >= len(values) {
			return nil, ErrProtocol
		}
		v := values[i]
		if v.Missing {
			return nil, ErrProtocol
		}
		if v.Null {
			if !slices.Contains(b.Clustering, key) {
				return nil, ErrProtocol
			}
			nullClustering++
		}
		cell, err := nativeCell(v)
		if err != nil {
			return nil, err
		}
		identity.Typed = append(identity.Typed, cell)
		raw := make([]byte, len(v.Raw))
		copy(raw, v.Raw)
		identity.Parameters = append(identity.Parameters, Parameter{Bytes: raw, Null: v.Null})
	}
	if nullClustering > 0 {
		if nullClustering != len(b.Clustering) {
			return nil, ErrProtocol
		}
		identity.StaticOnly = true
	}
	snapshot := &contract.RowSnapshot{Mode: "fingerprints", Columns: append([]string(nil), b.Names...)}
	for _, v := range values {
		f := nativeFingerprint(v)
		identity.Fingerprints = append(identity.Fingerprints, f)
		snapshot.Fingerprints = append(snapshot.Fingerprints, &f)
	}
	identity.Signature = l.sign(identity)
	raw, err := json.Marshal(identity)
	if err != nil || len(raw) > 1<<20 {
		return nil, ErrBounds
	}
	snapshot.Metadata = b.Signature
	snapshot.Ref = contract.RowRef{Object: l.object, Database: l.object.Catalog, Table: l.object.Name, Columns: []string{"_cassandra_row_identity"}, Values: [][]byte{raw}, TypedValues: identity.Typed}
	return snapshot, nil
}
func gridPreview(v contract.Cell) contract.Cell {
	if v.Kind == contract.Binary {
		if len(v.Bytes) > 1024 {
			v.Bytes = bytes.Clone(v.Bytes[:1024])
			v.Truncated = true
		}
	} else {
		runes := []rune(v.Text)
		if len(runes) > 1024 {
			v.Text = string(runes[:1024])
			v.Truncated = true
		}
	}
	return v
}
func (l *dataLease) preview(values []Value, b *dataBinding, budget *int) ([]contract.DataCell, *contract.RowSnapshot, error) {
	snapshot, err := l.snapshot(values, b)
	if err != nil {
		return nil, nil, err
	}
	*budget -= len(snapshot.Ref.Values[0])*2 + len(snapshot.Fingerprints)*128 + 256
	out := make([]contract.DataCell, len(values))
	for i, v := range values {
		cell, err := nativeCell(v)
		if err != nil {
			return nil, nil, err
		}
		cell = gridPreview(cell)
		text := cell.Text
		if cell.Kind == contract.Binary {
			text = hex.EncodeToString(cell.Bytes)
		}
		// Include the legacy hex display as well as the authoritative typed bytes.
		*budget -= len(text) + len(cell.Text) + len(cell.Bytes) + 256
		if *budget < 0 {
			return nil, nil, ErrBounds
		}
		out[i] = contract.DataCell{Text: text, Null: cell.Kind == contract.Null, Truncated: cell.Truncated, Value: &cell}
	}
	return out, snapshot, nil
}
func (l *dataLease) decodeRef(r contract.RowRef) (dataIdentity, error) {
	var v dataIdentity
	if r.Object != l.object || r.Database != l.object.Catalog || r.Table != l.object.Name || !slices.Equal(r.Columns, []string{"_cassandra_row_identity"}) || len(r.Values) != 1 || len(r.Values[0]) > 1<<20 {
		return v, ErrInvalid
	}
	if json.Unmarshal(r.Values[0], &v) != nil {
		return v, ErrInvalid
	}
	sig := v.Signature
	v.Signature = ""
	expected := l.sign(v)
	v.Signature = sig
	if !hmac.Equal([]byte(sig), []byte(expected)) || v.Object != l.object || !sameIdentityCells(r.TypedValues, v.Typed) || len(v.Keys) != len(v.Parameters) || len(v.Keys) != len(v.Typed) {
		return v, ErrInvalid
	}
	return v, nil
}

func sameIdentityCells(a, b []contract.Cell) bool {
	if len(a) != len(b) {
		return false
	}
	for i, v := range a {
		if v.Kind != b[i].Kind || v.Text != b[i].Text || v.Truncated || b[i].Truncated || !bytes.Equal(v.Bytes, b[i].Bytes) {
			return false
		}
	}
	return true
}
func dataValue(c contract.Cell) contract.DataValue {
	return contract.DataValue{Kind: c.Kind, Text: c.Text, Bytes: bytes.Clone(c.Bytes), Null: c.Kind == contract.Null, Binary: c.Kind == contract.Binary}
}
func (l *dataLease) row(ctx context.Context, id int64, db, name string, r contract.RowRef) (*contract.DataRow, error) {
	return l.readRow(ctx, id, db, name, r, false)
}
func (l *dataLease) readRow(ctx context.Context, id int64, db, name string, r contract.RowRef, allowLarge bool) (*contract.DataRow, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	ctx, c, done, err := l.auxiliary(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	ref, err := l.decodeRef(r)
	if err != nil {
		return nil, err
	}
	b, err := l.guard(ctx, c)
	if err != nil {
		return nil, err
	}
	if ref.ID != b.ID || ref.Binding != b.Signature || !slices.Equal(ref.Keys, b.Keys) || len(ref.Fingerprints) != len(b.Names) {
		return nil, ErrDataTargetChanged
	}
	where := ""
	params := []Parameter{}
	for i, k := range ref.Keys {
		if ref.StaticOnly && slices.Contains(b.Clustering, k) {
			continue
		}
		if ref.Parameters[i].Null || ref.Parameters[i].Unset {
			return nil, ErrInvalid
		}
		if where != "" {
			where += " AND "
		}
		where += quoteName(k) + " = ?"
		params = append(params, ref.Parameters[i])
	}
	if where == "" {
		return nil, ErrProtocol
	}
	result, err := c.execute(ctx, Request{Statement: dataSelect(l.object, b) + " WHERE " + where + " LIMIT 2", Parameters: params, PageSize: 2}, true)
	if err != nil {
		return nil, err
	}
	if result.Kind != 2 {
		return nil, ErrProtocol
	}
	if err = validateDataColumns(result.Columns, l.object, b); err != nil {
		return nil, err
	}
	if len(result.Rows) != 1 || result.Next != nil || len(result.Rows[0]) != len(b.Names) {
		return nil, contract.ErrDataConflict
	}
	values := result.Rows[0]
	for i, v := range values {
		if nativeFingerprint(v) != ref.Fingerprints[i] {
			return nil, contract.ErrDataConflict
		}
	}
	if _, err = l.guard(ctx, c); err != nil {
		return nil, err
	}
	snapshot, err := l.snapshot(values, b)
	if err != nil {
		return nil, err
	}
	snapshot.Mode = "values"
	snapshot.Fingerprints = nil
	out := &contract.DataRow{Columns: cloneStructure(b.Metadata).Columns, Values: map[string]contract.DataValue{}, Snapshot: snapshot}
	budget := 1 << 20
	if allowLarge {
		budget = maxDecoded
	}
	for i, v := range values {
		cell, err := nativeCell(v)
		if err != nil {
			return nil, err
		}
		budget -= len(cell.Text) + len(cell.Bytes) + 128
		if budget < 0 {
			return nil, ErrBounds
		}
		out.Values[b.Names[i]] = dataValue(cell)
		snapshot.Original = append(snapshot.Original, cell)
	}
	return out, ctx.Err()
}

// Native CQL has no portable bounded substring/blob projection. The native
// response remains frame bounded; only this explicit presentation chunk is 1024
// characters/bytes. Complete-row reads enforce their separate 1 MiB bound.
func (l *dataLease) chunk(ctx context.Context, id int64, db, name, field string, r contract.RowRef, offset int64, units int) (*contract.FieldChunk, error) {
	if offset < 0 || units < 1 || units > 1024 {
		return nil, ErrBounds
	}
	row, err := l.readRow(ctx, id, db, name, r, true)
	if err != nil {
		return nil, err
	}
	v, ok := row.Values[field]
	if !ok {
		return nil, contract.ErrUnsupported
	}
	out := &contract.FieldChunk{Offset: offset, Null: v.Null, Binary: v.Binary}
	if v.Null {
		return out, nil
	}
	if v.Binary {
		out.TotalUnits = int64(len(v.Bytes))
		start := min(offset, out.TotalUnits)
		end := min(start+int64(units), out.TotalUnits)
		out.Text = hex.EncodeToString(v.Bytes[start:end])
		out.NextOffset = end
	} else {
		runes := []rune(v.Text)
		out.TotalUnits = int64(len(runes))
		start := min(offset, out.TotalUnits)
		end := min(start+int64(units), out.TotalUnits)
		out.Text = string(runes[start:end])
		out.NextOffset = end
	}
	out.HasMore = out.NextOffset < out.TotalUnits
	return out, nil
}
