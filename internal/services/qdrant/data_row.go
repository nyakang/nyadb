package qdrant

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type dataIdentity struct {
	Object       contract.ObjectRef
	Observation  string
	ID           json.RawMessage
	IDValue      contract.Cell
	Fingerprints []string
	Signature    string
}

func fingerprint(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func pointValues(p Point) []contract.Cell {
	id := contract.Cell{Kind: contract.Integer, Text: string(p.ID)}
	if len(p.ID) > 0 && p.ID[0] == '"' {
		id.Kind = contract.Text
		_ = decodeJSON(p.ID, &id.Text)
	}
	jsonCell := func(raw []byte) contract.Cell {
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			return contract.Cell{Kind: contract.Null}
		}
		return contract.Cell{Kind: contract.JSON, Text: string(raw)}
	}
	return []contract.Cell{id, jsonCell(p.Vector), jsonCell(p.Payload), {Kind: contract.JSON, Text: string(p.JSON)}}
}
func pointFingerprints(p Point) ([]string, error) {
	values := pointValues(p)
	hashes := make([]string, len(values))
	for i, v := range values {
		if v.Kind == contract.JSON {
			raw, err := canonicalJSON([]byte(v.Text))
			if err != nil {
				return nil, err
			}
			v.Text = string(raw)
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		hashes[i] = fingerprint(raw)
	}
	return hashes, nil
}
func (l *dataLease) snapshot(p Point) (*contract.RowSnapshot, error) {
	hashes, err := pointFingerprints(p)
	if err != nil {
		return nil, err
	}
	values := pointValues(p)
	identity := dataIdentity{Object: l.object, Observation: l.binding(), ID: bytes.Clone(p.ID), IDValue: values[0], Fingerprints: hashes}
	identity.Signature = l.sign(identity)
	raw, _ := json.Marshal(identity)
	if len(raw) > 64<<10 {
		return nil, ErrBounds
	}
	snapshot := &contract.RowSnapshot{Mode: "fingerprints", Columns: []string{"id", "vector", "payload", "point"}, Metadata: identity.Observation, Ref: contract.RowRef{Object: l.object, Database: l.object.Catalog, Table: l.object.Name, Columns: []string{"_qdrant_point_identity"}, Values: [][]byte{raw}, TypedValues: []contract.Cell{values[0]}}}
	for i, hash := range hashes {
		if values[i].Kind == contract.Null {
			snapshot.Fingerprints = append(snapshot.Fingerprints, nil)
		} else {
			v := hash
			snapshot.Fingerprints = append(snapshot.Fingerprints, &v)
		}
	}
	return snapshot, nil
}
func (l *dataLease) preview(p Point, budget *int) ([]contract.DataCell, *contract.RowSnapshot, error) {
	snapshot, err := l.snapshot(p)
	if err != nil {
		return nil, nil, err
	}
	*budget -= len(snapshot.Ref.Values[0])*2 + 1024
	values := pointValues(p)
	row := make([]contract.DataCell, len(values))
	for i, value := range values {
		runes := []rune(value.Text)
		if len(runes) > 1024 {
			value.Text = string(runes[:1024])
			value.Truncated = true
		}
		*budget -= len(value.Text)*2 + 256
		if *budget < 0 {
			return nil, nil, ErrBounds
		}
		row[i] = contract.DataCell{Text: value.Text, Null: value.Kind == contract.Null, Truncated: value.Truncated, Value: &value}
	}
	return row, snapshot, nil
}
func (l *dataLease) decodeRef(r contract.RowRef) (dataIdentity, error) {
	var identity dataIdentity
	if r.Object != l.object || r.Database != l.object.Catalog || r.Table != l.object.Name || !slices.Equal(r.Columns, []string{"_qdrant_point_identity"}) || len(r.Values) != 1 || len(r.Values[0]) > 64<<10 || decodeJSON(bytes.Clone(r.Values[0]), &identity) != nil {
		return identity, ErrInvalid
	}
	sig := identity.Signature
	identity.Signature = ""
	expected := l.sign(identity)
	identity.Signature = sig
	if !hmac.Equal([]byte(sig), []byte(expected)) || identity.Object != l.object || len(identity.Fingerprints) != 4 || !validPointID(identity.ID) || !reflect.DeepEqual(r.TypedValues, []contract.Cell{identity.IDValue}) {
		return identity, ErrInvalid
	}
	return identity, nil
}

// ReadPoint returns the complete original native point, retaining exact JSON.
func (c *Client) ReadPoint(ctx context.Context, collection string, id json.RawMessage) (Point, Response, error) {
	if len(id) > 40 {
		return Point{}, Response{}, ErrInvalid
	}
	id = bytes.Clone(id)
	path, err := collectionPath(collection)
	if err != nil || !validPointID(id) {
		return Point{}, Response{}, ErrInvalid
	}
	idText := string(id)
	if id[0] == '"' {
		if decodeJSON(id, &idText) != nil {
			return Point{}, Response{}, ErrInvalid
		}
	}
	r, err := c.Do(ctx, Request{Method: "GET", Path: path + "/points/" + url.PathEscape(idText) + "?with_payload=true&with_vector=true"})
	if err != nil {
		return Point{}, r, err
	}
	result, err := NativeResult(r.Body)
	if err != nil {
		return Point{}, r, err
	}
	points, err := parsePoints(append(append([]byte{'['}, result...), ']'), 1)
	if err != nil || len(points) != 1 || !bytes.Equal(points[0].ID, id) {
		return Point{}, r, ErrProtocol
	}
	return points[0], r, nil
}
func (l *dataLease) originalPoint(ctx context.Context, c *Client, identity dataIdentity) (Point, error) {
	if _, err := l.guard(ctx, c); err != nil {
		return Point{}, err
	}
	if identity.Observation != l.binding() {
		return Point{}, ErrDataTargetChanged
	}
	p, _, err := c.ReadPoint(ctx, l.object.Catalog, identity.ID)
	if err != nil {
		var status *HTTPError
		if errors.As(err, &status) && status.Status == 404 {
			return Point{}, contract.ErrDataConflict
		}
		return Point{}, err
	}
	hashes, err := pointFingerprints(p)
	if err != nil {
		return Point{}, err
	}
	if !slices.Equal(hashes, identity.Fingerprints) || !reflect.DeepEqual(pointValues(p)[0], identity.IDValue) {
		return Point{}, contract.ErrDataConflict
	}
	if _, err = l.guard(ctx, c); err != nil {
		return Point{}, err
	}
	return p, nil
}
func (l *dataLease) row(ctx context.Context, id int64, db, name string, r contract.RowRef) (*contract.DataRow, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	identity, err := l.decodeRef(r)
	if err != nil {
		return nil, err
	}
	c, closeClient, err := l.client(ctx)
	if err != nil {
		return nil, err
	}
	defer closeClient()
	p, err := l.originalPoint(ctx, c, identity)
	if err != nil {
		return nil, err
	}
	if len(p.JSON) > 1<<20 {
		return nil, ErrBounds
	}
	snapshot, err := l.snapshot(p)
	if err != nil {
		return nil, err
	}
	snapshot.Mode = "values"
	snapshot.Fingerprints = nil
	snapshot.Original = pointValues(p)
	out := &contract.DataRow{Columns: pointColumns(), Snapshot: snapshot, Values: map[string]contract.DataValue{}}
	for i, v := range snapshot.Original {
		out.Values[snapshot.Columns[i]] = contract.DataValue{Kind: v.Kind, Text: v.Text, Null: v.Kind == contract.Null}
	}
	return out, ctx.Err()
}
func (l *dataLease) chunk(ctx context.Context, id int64, db, name, field string, r contract.RowRef, offset int64, units int) (*contract.FieldChunk, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	if offset < 0 || units < 1 || units > 1024 {
		return nil, ErrBounds
	}
	column := slices.Index([]string{"id", "vector", "payload", "point"}, field)
	if column < 0 {
		return nil, contract.ErrUnsupported
	}
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	identity, err := l.decodeRef(r)
	if err != nil {
		return nil, err
	}
	c, closeClient, err := l.client(ctx)
	if err != nil {
		return nil, err
	}
	defer closeClient()
	p, err := l.originalPoint(ctx, c, identity)
	if err != nil {
		return nil, err
	}
	v := pointValues(p)[column]
	runes := []rune(v.Text)
	total := int64(len(runes))
	start := min(offset, total)
	end := start + min(int64(units), total-start)
	return &contract.FieldChunk{Text: string(runes[start:end]), Null: v.Kind == contract.Null, Offset: offset, NextOffset: end, TotalUnits: total, HasMore: end < total}, ctx.Err()
}
