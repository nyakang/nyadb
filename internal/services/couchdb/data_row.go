package couchdb

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type dataIdentity struct {
	Object                 contract.ObjectRef
	Creation, ID, Revision string
	Fingerprints           []string
	Signature              string
}

func documentValues(doc NativeDocument) []contract.Cell {
	return []contract.Cell{{Kind: contract.Text, Text: doc.ID}, {Kind: contract.Text, Text: doc.Revision}, {Kind: contract.JSON, Text: string(doc.JSON)}}
}
func fingerprint(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func documentFingerprints(doc NativeDocument) ([]string, error) {
	canonical, err := canonicalJSON(doc.JSON)
	if err != nil {
		return nil, err
	}
	return []string{fingerprint([]byte(doc.ID)), fingerprint([]byte(doc.Revision)), fingerprint(canonical)}, nil
}
func (l *dataLease) snapshot(doc NativeDocument) (*contract.RowSnapshot, error) {
	digests, err := documentFingerprints(doc)
	if err != nil {
		return nil, err
	}
	identity := dataIdentity{Object: l.object, Creation: l.binding(), ID: doc.ID, Revision: doc.Revision, Fingerprints: digests}
	identity.Signature = l.sign(identity)
	raw, _ := json.Marshal(identity)
	if len(raw) > 64<<10 {
		return nil, ErrBounds
	}
	snapshot := &contract.RowSnapshot{Mode: "fingerprints", Columns: []string{"_id", "_rev", "document"}, Metadata: identity.Creation,
		Ref: contract.RowRef{Object: l.object, Database: l.object.Catalog, Table: l.object.Name, Columns: []string{"_couchdb_document_identity"}, Values: [][]byte{raw},
			TypedValues: []contract.Cell{{Kind: contract.Text, Text: doc.ID}, {Kind: contract.Text, Text: doc.Revision}}}}
	for _, digest := range digests {
		v := digest
		snapshot.Fingerprints = append(snapshot.Fingerprints, &v)
	}
	return snapshot, nil
}
func (l *dataLease) preview(doc NativeDocument, budget *int) ([]contract.DataCell, *contract.RowSnapshot, error) {
	snapshot, err := l.snapshot(doc)
	if err != nil {
		return nil, nil, err
	}
	*budget -= len(snapshot.Ref.Values[0])*2 + 1024
	values := documentValues(doc)
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
		row[i] = contract.DataCell{Text: value.Text, Truncated: value.Truncated, Value: &value}
	}
	return row, snapshot, nil
}
func (l *dataLease) decodeRef(r contract.RowRef) (dataIdentity, error) {
	var identity dataIdentity
	if r.Object != l.object || r.Database != l.object.Catalog || r.Table != l.object.Name || !slices.Equal(r.Columns, []string{"_couchdb_document_identity"}) || len(r.Values) != 1 || len(r.Values[0]) > 64<<10 || json.Unmarshal(bytes.Clone(r.Values[0]), &identity) != nil {
		return identity, ErrInvalid
	}
	sig := identity.Signature
	identity.Signature = ""
	expected := l.sign(identity)
	identity.Signature = sig
	typed := []contract.Cell{{Kind: contract.Text, Text: identity.ID}, {Kind: contract.Text, Text: identity.Revision}}
	if !hmac.Equal([]byte(sig), []byte(expected)) || identity.Object != l.object || len(identity.Fingerprints) != 3 || !validRevision(identity.Revision) || !reflect.DeepEqual(r.TypedValues, typed) {
		return identity, ErrInvalid
	}
	if _, err := documentPath(l.object.Catalog, identity.ID); err != nil {
		return identity, ErrInvalid
	}
	return identity, nil
}
func (l *dataLease) originalDocument(ctx context.Context, c *Client, identity dataIdentity) (NativeDocument, error) {
	if _, err := l.guard(ctx, c); err != nil {
		return NativeDocument{}, err
	}
	if identity.Creation != l.binding() {
		return NativeDocument{}, ErrDataTargetChanged
	}
	doc, err := c.ReadDocument(ctx, l.object.Catalog, DocumentIdentity{identity.ID, identity.Revision})
	if err != nil {
		return NativeDocument{}, err
	}
	digests, err := documentFingerprints(doc)
	if err != nil {
		return NativeDocument{}, err
	}
	if !slices.Equal(digests, identity.Fingerprints) {
		return NativeDocument{}, contract.ErrDataConflict
	}
	if _, err = l.guard(ctx, c); err != nil {
		return NativeDocument{}, err
	}
	return doc, nil
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
	ref, err := l.decodeRef(r)
	if err != nil {
		return nil, err
	}
	c, closeClient, err := l.client(ctx)
	if err != nil {
		return nil, err
	}
	defer closeClient()
	doc, err := l.originalDocument(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	if len(doc.JSON) > 1<<20 || len(doc.ID) > 1<<20 || len(doc.Revision) > 1<<20 {
		return nil, ErrBounds
	}
	snapshot, err := l.snapshot(doc)
	if err != nil {
		return nil, err
	}
	snapshot.Mode = "values"
	snapshot.Fingerprints = nil
	snapshot.Original = documentValues(doc)
	out := &contract.DataRow{Columns: documentColumns(), Values: map[string]contract.DataValue{}, Snapshot: snapshot}
	for i, cell := range snapshot.Original {
		out.Values[snapshot.Columns[i]] = contract.DataValue{Kind: cell.Kind, Text: cell.Text}
	}
	return out, ctx.Err()
}

// The HTTP body is transport-bounded. Only this explicit native JSON preview
// is exposed to the inspector, with its own 1024-character cancellation scope.
func (l *dataLease) chunk(ctx context.Context, id int64, db, name, field string, r contract.RowRef, offset int64, units int) (*contract.FieldChunk, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	if offset < 0 || units < 1 || units > 1024 {
		return nil, ErrBounds
	}
	column := slices.Index([]string{"_id", "_rev", "document"}, field)
	if column < 0 {
		return nil, contract.ErrUnsupported
	}
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	ref, err := l.decodeRef(r)
	if err != nil {
		return nil, err
	}
	c, closeClient, err := l.client(ctx)
	if err != nil {
		return nil, err
	}
	defer closeClient()
	doc, err := l.originalDocument(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	runes := []rune(documentValues(doc)[column].Text)
	total := int64(len(runes))
	start := min(offset, total)
	end := min(start+int64(units), total)
	return &contract.FieldChunk{Text: string(runes[start:end]), Offset: offset, NextOffset: end, TotalUnits: total, HasMore: end < total}, ctx.Err()
}
