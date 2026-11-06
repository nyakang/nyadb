package mongodb

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"go.mongodb.org/mongo-driver/v2/bson"
)

const documentField = "document"
const previewUnits = 1024

// Duplicate JSON/BSON keys have ambiguous update and identity semantics.
func jsonValue(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, e := d.Token()
				if e != nil {
					return e
				}
				k, ok := key.(string)
				if !ok || seen[k] {
					return errors.New("duplicate JSON key")
				}
				seen[k] = true
				if e = jsonValue(d); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if err = jsonValue(d); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		_, err = d.Token()
	}
	return err
}
func parseDocument(text string, limit int) (bson.Raw, error) {
	if len(text) > limit || !utf8.ValidString(text) || !strings.HasPrefix(strings.TrimSpace(text), "{") {
		return nil, errors.New("MongoDB requires one Extended JSON document within the size limit")
	}
	d := json.NewDecoder(strings.NewReader(text))
	d.UseNumber()
	if jsonValue(d) != nil {
		return nil, errors.New("MongoDB requires valid Extended JSON with unique keys")
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("MongoDB accepts one Extended JSON document at a time")
	}
	var doc bson.D
	if bson.UnmarshalExtJSON([]byte(text), false, &doc) != nil {
		return nil, errors.New("invalid MongoDB Extended JSON value")
	}
	raw, err := bson.Marshal(doc)
	if err != nil {
		return nil, errors.New("invalid MongoDB BSON document")
	}
	return raw, nil
}
func extendedJSON(raw bson.Raw) (string, error) {
	value, err := bson.MarshalExtJSON(raw, true, false)
	if err != nil {
		return "", errors.New("MongoDB BSON value could not be displayed")
	}
	return string(value), nil
}
func preview(text string) contract.Cell {
	runes := []rune(text)
	c := contract.Cell{Kind: contract.JSON, Text: text}
	if len(runes) > previewUnits {
		c.Text = string(runes[:previewUnits])
		c.Truncated = true
	}
	return c
}
func idDocument(raw bson.Raw) (bson.Raw, error) {
	id := raw.Lookup("_id")
	if id.Type == 0 {
		return nil, errors.New("MongoDB document has no _id")
	}
	data, err := bson.Marshal(bson.D{{Key: "_id", Value: id}})
	return data, err
}
func sameID(a, b bson.Raw) bool { return a.Lookup("_id").Equal(b.Lookup("_id")) }
func columns() []contract.StructureColumn {
	return []contract.StructureColumn{{Name: "_id", Type: "BSON", DataType: "bson", ValueKind: contract.JSON, Key: "PRI", Generated: true, Position: 1}, {Name: documentField, Type: "Extended JSON", DataType: "json", ValueKind: contract.JSON, Position: 2, Comment: "Complete BSON document in canonical Extended JSON"}}
}
func previews(raw bson.Raw) ([]contract.DataCell, error) {
	id, err := idDocument(raw)
	missing := err != nil
	out := make([]contract.DataCell, 2)
	for i, value := range []bson.Raw{id, raw} {
		if i == 0 && missing {
			c := contract.Cell{Kind: contract.Text, Text: "(missing)"}
			out[i] = contract.DataCell{Text: c.Text, Value: &c}
			continue
		}
		text, err := extendedJSON(value)
		if err != nil {
			return nil, err
		}
		c := preview(text)
		out[i] = contract.DataCell{Text: c.Text, Truncated: c.Truncated, Value: &c}
	}
	return out, nil
}
func (s *Session) snapshot(o contract.ObjectRef, raw bson.Raw) (*contract.RowSnapshot, error) {
	id, err := idDocument(raw)
	if err != nil {
		return nil, err
	}
	idText, err := extendedJSON(id)
	if err != nil {
		return nil, err
	}
	ref := contract.RowRef{Object: o, Database: o.Catalog, Table: o.Name, Columns: []string{"_id"}, Values: [][]byte{bytes.Clone(id)}, TypedValues: []contract.Cell{{Kind: contract.JSON, Text: idText}}}
	hash := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(hash[:])
	idHash := sha256.Sum256(id)
	idFingerprint := hex.EncodeToString(idHash[:])
	snap := &contract.RowSnapshot{Mode: "fingerprints", Ref: ref, Columns: []string{"_id", documentField}, Fingerprints: []*string{&idFingerprint, &fingerprint}}
	snap.Metadata = s.signature(snap)
	return snap, nil
}
func (s *Session) signature(snap *contract.RowSnapshot) string {
	mac := hmac.New(sha256.New, s.key[:])
	identity, _ := json.Marshal(snap.Ref)
	mac.Write(identity)
	for _, fp := range snap.Fingerprints {
		if fp != nil {
			mac.Write([]byte(*fp))
		}
	}
	return hex.EncodeToString(mac.Sum(nil))
}
func (s *Session) validateSnapshot(o contract.ObjectRef, snap *contract.RowSnapshot) error {
	if snap == nil || snap.Ref.Object != o || snap.Ref.Database != o.Catalog || snap.Ref.Table != o.Name || snap.Mode != "fingerprints" || len(snap.Ref.Values) != 1 || len(snap.Fingerprints) != 2 || snap.Fingerprints[0] == nil || snap.Fingerprints[1] == nil || !hmac.Equal([]byte(snap.Metadata), []byte(s.signature(snap))) {
		return errors.New("MongoDB original document belongs to another session or was altered")
	}
	return nil
}
func matchesSnapshot(raw bson.Raw, snap *contract.RowSnapshot) bool {
	if snap == nil || len(snap.Fingerprints) != 2 || snap.Fingerprints[1] == nil {
		return false
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]) == *snap.Fingerprints[1]
}
