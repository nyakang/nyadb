package neo4j

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

type graphDataIdentity struct {
	Object                            contract.ObjectRef
	ElementID, Fingerprint, Signature string
}

func cloneRowRef(r contract.RowRef) contract.RowRef {
	r.Columns = slices.Clone(r.Columns)
	r.Values = slices.Clone(r.Values)
	for i := range r.Values {
		r.Values[i] = bytes.Clone(r.Values[i])
	}
	r.TypedValues = slices.Clone(r.TypedValues)
	for i := range r.TypedValues {
		r.TypedValues[i] = r.TypedValues[i].Clone()
	}
	return r
}
func fingerprint(v any) string {
	raw, _ := json.Marshal(v)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func (l *dataLease) project(v Value) ([]contract.Cell, error) {
	id, e := l.entityID(v)
	if e != nil {
		return nil, e
	}
	out := []contract.Cell{{Kind: contract.Text, Text: id}}
	if v.Node != nil {
		x, e := graphJSON(v.Node.Labels)
		if e != nil {
			return nil, e
		}
		out = append(out, x)
	} else {
		r := v.Relationship
		out = append(out, contract.Cell{Kind: contract.Text, Text: r.Type}, contract.Cell{Kind: contract.Text, Text: r.StartElementID}, contract.Cell{Kind: contract.Text, Text: r.EndElementID})
	}
	props := v.Node
	var properties []Property
	if props != nil {
		properties = props.Properties
	} else {
		properties = v.Relationship.Properties
	}
	x, e := graphJSON(Value{Kind: "map", Properties: properties})
	if e != nil {
		return nil, e
	}
	out = append(out, x)
	x, e = graphJSON(v)
	if e != nil {
		return nil, e
	}
	return append(out, x), nil
}
func (l *dataLease) snapshot(v Value, cells []contract.Cell) (*contract.RowSnapshot, error) {
	id, e := l.entityID(v)
	if e != nil {
		return nil, e
	}
	i := graphDataIdentity{Object: l.object, ElementID: id, Fingerprint: fingerprint(v)}
	i.Signature = l.sign(i)
	raw, e := json.Marshal(i)
	if e != nil {
		return nil, e
	}
	s := &contract.RowSnapshot{Mode: "fingerprints", Metadata: i.Signature, Ref: contract.RowRef{Object: l.object, Database: l.object.Catalog, Table: l.object.Name, Columns: []string{"_neo4j_entity_identity"}, Values: [][]byte{raw}, TypedValues: []contract.Cell{{Kind: contract.Text, Text: id}}}}
	for n, c := range l.columns() {
		s.Columns = append(s.Columns, c.Name)
		f := fingerprint(cells[n])
		s.Fingerprints = append(s.Fingerprints, &f)
	}
	return s, nil
}
func (l *dataLease) preview(v Value, budget *int) ([]contract.DataCell, *contract.RowSnapshot, error) {
	cells, e := l.project(v)
	if e != nil {
		return nil, nil, e
	}
	s, e := l.snapshot(v, cells)
	if e != nil {
		return nil, nil, e
	}
	*budget -= len(s.Ref.Values[0])*2 + len(s.Fingerprints)*128 + 256
	out := make([]contract.DataCell, len(cells))
	for i, c := range cells {
		runes := []rune(c.Text)
		if len(runes) > 1024 {
			c.Text = string(runes[:1024])
			c.Truncated = true
		}
		*budget -= len(c.Text)*2 + 256
		if *budget < 0 {
			return nil, nil, ErrBounds
		}
		out[i] = contract.DataCell{Text: c.Text, Value: &c, Truncated: c.Truncated}
	}
	return out, s, nil
}
func (l *dataLease) decodeRef(r contract.RowRef) (graphDataIdentity, error) {
	var i graphDataIdentity
	if r.Object != l.object || r.Database != l.object.Catalog || r.Table != l.object.Name || !slices.Equal(r.Columns, []string{"_neo4j_entity_identity"}) || len(r.Values) != 1 || len(r.Values[0]) > 16384 || len(r.TypedValues) != 1 {
		return i, ErrInvalid
	}
	if json.Unmarshal(r.Values[0], &i) != nil || i.Object != l.object || !validDatabase(i.ElementID) || len(i.Fingerprint) != 64 {
		return i, ErrInvalid
	}
	sig := i.Signature
	i.Signature = ""
	expected := l.sign(i)
	i.Signature = sig
	v := r.TypedValues[0]
	if !hmac.Equal([]byte(sig), []byte(expected)) || v.Kind != contract.Text || v.Text != i.ElementID || v.Truncated || len(v.Bytes) > 0 {
		return i, ErrInvalid
	}
	return i, nil
}
func (l *dataLease) readEntity(ctx context.Context, c *Client, r contract.RowRef) (Value, error) {
	i, e := l.decodeRef(r)
	if e != nil {
		return Value{}, e
	}
	q := l.match() + " AND elementId(entity) = $element_id RETURN entity LIMIT 2"
	x, e := l.read(ctx, c, q, map[string]any{"element_id": i.ElementID}, 2)
	if e != nil {
		return Value{}, e
	}
	if !slices.Equal(x.Columns, []string{"entity"}) {
		return Value{}, ErrProtocol
	}
	if len(x.Rows) != 1 || len(x.Rows[0]) != 1 {
		return Value{}, contract.ErrDataConflict
	}
	v := x.Rows[0][0]
	id, e := l.entityID(v)
	if e != nil {
		return Value{}, e
	}
	if id != i.ElementID || fingerprint(v) != i.Fingerprint {
		return Value{}, contract.ErrDataConflict
	}
	return v, nil
}
func (l *dataLease) readRow(ctx context.Context, id int64, db, name string, r contract.RowRef, large bool) (*contract.DataRow, error) {
	r = cloneRowRef(r)
	if e := l.identity(id, db, name); e != nil {
		return nil, e
	}
	ctx, done, e := l.request(ctx)
	if e != nil {
		return nil, e
	}
	defer done()
	// Reject forged/foreign identities before opening any original client.
	if _, e = l.decodeRef(r); e != nil {
		return nil, e
	}
	c, closeClient, e := l.client(ctx)
	if e != nil {
		return nil, e
	}
	defer closeClient()
	v, e := l.readEntity(ctx, c, r)
	if e != nil {
		return nil, e
	}
	cells, e := l.project(v)
	if e != nil {
		return nil, e
	}
	s, e := l.snapshot(v, cells)
	if e != nil {
		return nil, e
	}
	s.Mode = "values"
	s.Fingerprints = nil
	out := &contract.DataRow{Columns: l.columns(), Values: map[string]contract.DataValue{}, Snapshot: s}
	budget := 16 << 20
	for i, c := range cells {
		if !large && len(c.Text)+len(c.Bytes) > 1<<20 {
			return nil, ErrBounds
		}
		budget -= len(c.Text) + len(c.Bytes) + 128
		if budget < 0 {
			return nil, ErrBounds
		}
		out.Values[out.Columns[i].Name] = contract.DataValue{Kind: c.Kind, Text: c.Text, Bytes: bytes.Clone(c.Bytes)}
		s.Original = append(s.Original, c.Clone())
	}
	return out, ctx.Err()
}
func (l *dataLease) row(ctx context.Context, id int64, db, name string, r contract.RowRef) (*contract.DataRow, error) {
	return l.readRow(ctx, id, db, name, r, false)
}

// Native values are read in one bounded result and checked against the complete
// original entity fingerprint. The explicit presentation chunk is at most 1024
// characters; this does not promise a bounded driver allocation or atomic CAS.
func (l *dataLease) chunk(ctx context.Context, id int64, db, name, field string, r contract.RowRef, offset int64, units int) (*contract.FieldChunk, error) {
	if offset < 0 || units < 1 || units > 1024 {
		return nil, ErrBounds
	}
	if !slices.ContainsFunc(l.columns(), func(c contract.StructureColumn) bool { return c.Name == field }) {
		return nil, contract.ErrUnsupported
	}
	row, e := l.readRow(ctx, id, db, name, r, true)
	if e != nil {
		return nil, e
	}
	v := row.Values[field]
	runes := []rune(v.Text)
	total := int64(len(runes))
	start := min(offset, total)
	end := min(start+int64(units), total)
	return &contract.FieldChunk{Text: string(runes[start:end]), Offset: offset, NextOffset: end, TotalUnits: total, HasMore: end < total}, nil
}
