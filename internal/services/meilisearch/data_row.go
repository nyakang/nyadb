package meilisearch

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type dataDocument struct {
	ID      string
	IDValue contract.Cell
	JSON    json.RawMessage
}
type dataIdentity struct {
	Object       contract.ObjectRef
	Binding, ID  string
	IDValue      contract.Cell
	Fingerprints []string
	Signature    string
}

func (l *dataLease) document(raw []byte) (dataDocument, error) {
	fields, err := nativeObject(raw)
	if err != nil {
		return dataDocument{}, err
	}
	primary, ok := l.primaryKey()
	if !ok {
		return dataDocument{}, ErrProtocol
	}
	value, present := fields[primary]
	if !present {
		return dataDocument{}, ErrProtocol
	}
	doc := dataDocument{JSON: bytes.Clone(raw)}
	if id, err := nativeString(value); err == nil {
		doc.ID = id
		doc.IDValue = contract.Cell{Kind: contract.Text, Text: id}
	} else {
		number := string(bytes.TrimSpace(value))
		digits := strings.TrimPrefix(number, "-")
		if digits == "" {
			return dataDocument{}, ErrProtocol
		}
		for i := range len(digits) {
			if digits[i] < '0' || digits[i] > '9' {
				return dataDocument{}, ErrProtocol
			}
		}
		doc.ID = number
		doc.IDValue = contract.Cell{Kind: contract.Integer, Text: number}
	}
	if !validDocumentID(doc.ID) {
		return dataDocument{}, ErrProtocol
	}
	return doc, nil
}
func documentValues(doc dataDocument) []contract.Cell {
	return []contract.Cell{doc.IDValue, {Kind: contract.JSON, Text: string(doc.JSON)}}
}
func fingerprint(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func documentFingerprints(doc dataDocument) ([]string, error) {
	raw, err := canonicalJSON(doc.JSON)
	if err != nil {
		return nil, err
	}
	id, _ := json.Marshal(doc.IDValue)
	return []string{fingerprint(id), fingerprint(raw)}, nil
}
func (l *dataLease) snapshot(doc dataDocument) (*contract.RowSnapshot, error) {
	hashes, err := documentFingerprints(doc)
	if err != nil {
		return nil, err
	}
	identity := dataIdentity{Object: l.object, Binding: l.binding(), ID: doc.ID, IDValue: doc.IDValue, Fingerprints: hashes}
	identity.Signature = l.sign(identity)
	raw, _ := json.Marshal(identity)
	snapshot := &contract.RowSnapshot{Mode: "fingerprints", Columns: []string{"id", "document"}, Metadata: identity.Binding, Ref: contract.RowRef{Object: l.object, Database: l.object.Catalog, Table: l.object.Name, Columns: []string{"_meilisearch_document_identity"}, Values: [][]byte{raw}, TypedValues: []contract.Cell{doc.IDValue}}}
	for _, hash := range hashes {
		v := hash
		snapshot.Fingerprints = append(snapshot.Fingerprints, &v)
	}
	return snapshot, nil
}
func (l *dataLease) preview(doc dataDocument, budget *int) ([]contract.DataCell, *contract.RowSnapshot, error) {
	snapshot, err := l.snapshot(doc)
	if err != nil {
		return nil, nil, err
	}
	*budget -= len(snapshot.Ref.Values[0])*2 + 512
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
	if r.Object != l.object || r.Database != l.object.Catalog || r.Table != l.object.Name || !slices.Equal(r.Columns, []string{"_meilisearch_document_identity"}) || len(r.Values) != 1 || len(r.Values[0]) > 64<<10 || json.Unmarshal(bytes.Clone(r.Values[0]), &identity) != nil {
		return identity, ErrInvalid
	}
	sig := identity.Signature
	identity.Signature = ""
	expected := l.sign(identity)
	identity.Signature = sig
	if !hmac.Equal([]byte(sig), []byte(expected)) || identity.Object != l.object || len(identity.Fingerprints) != 2 || !validDocumentID(identity.ID) || !reflect.DeepEqual(r.TypedValues, []contract.Cell{identity.IDValue}) {
		return identity, ErrInvalid
	}
	return identity, nil
}
func (l *dataLease) originalDocument(ctx context.Context, c *Client, identity dataIdentity) (dataDocument, error) {
	if _, err := l.guard(ctx, c); err != nil {
		return dataDocument{}, err
	}
	if identity.Binding != l.binding() {
		return dataDocument{}, ErrDataTargetChanged
	}
	raw, _, err := c.ReadDocument(ctx, l.object.Catalog, identity.ID)
	if err != nil {
		return dataDocument{}, err
	}
	doc, err := l.document(raw)
	if err != nil {
		return doc, err
	}
	if doc.ID != identity.ID || !reflect.DeepEqual(doc.IDValue, identity.IDValue) {
		return doc, contract.ErrDataConflict
	}
	hashes, err := documentFingerprints(doc)
	if err != nil {
		return doc, err
	}
	if !slices.Equal(hashes, identity.Fingerprints) {
		return doc, contract.ErrDataConflict
	}
	if _, err = l.guard(ctx, c); err != nil {
		return doc, err
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
	identity, err := l.decodeRef(r)
	if err != nil {
		return nil, err
	}
	c, closeClient, err := l.client(ctx)
	if err != nil {
		return nil, err
	}
	defer closeClient()
	doc, err := l.originalDocument(ctx, c, identity)
	if err != nil {
		return nil, err
	}
	if len(doc.JSON) > 1<<20 {
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
func (l *dataLease) chunk(ctx context.Context, id int64, db, name, field string, r contract.RowRef, offset int64, units int) (*contract.FieldChunk, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	if offset < 0 || units < 1 || units > 1024 {
		return nil, ErrBounds
	}
	column := slices.Index([]string{"id", "document"}, field)
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
	doc, err := l.originalDocument(ctx, c, identity)
	if err != nil {
		return nil, err
	}
	runes := []rune(documentValues(doc)[column].Text)
	total := int64(len(runes))
	start := min(offset, total)
	end := start + min(int64(units), total-start)
	return &contract.FieldChunk{Text: string(runes[start:end]), Offset: offset, NextOffset: end, TotalUnits: total, HasMore: end < total}, ctx.Err()
}
