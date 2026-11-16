package databend

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const dataGrantColumn = "__nyadb_databend_original"

type dataGrant struct {
	Object          contract.ObjectRef
	RowID, Metadata string
	Digests         []*string
	Signature       string
}

func dataMetadataSignature(m *contract.TableStructure) string {
	raw, _ := json.Marshal(struct {
		Object  contract.ObjectRef
		Columns []contract.StructureColumn
		DDL     string
	}{m.Object, m.Columns, m.Definition})
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func (l *dataLease) signGrant(g dataGrant) string {
	g.Signature = ""
	raw, _ := json.Marshal(g)
	l.mu.Lock()
	key := l.key
	l.mu.Unlock()
	h := hmac.New(sha256.New, key[:])
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}
func (l *dataLease) snapshot(m *contract.TableStructure, cells []contract.Cell, digests []*string, id string) (*contract.RowSnapshot, error) {
	if id == "" || len(cells) != len(m.Columns) || len(digests) != len(cells) {
		return nil, contract.ErrDataPage
	}
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return nil, contract.ErrDataPage
	}
	g := dataGrant{Object: l.object, RowID: id, Metadata: dataMetadataSignature(m), Digests: digests}
	g.Signature = l.signGrant(g)
	raw, _ := json.Marshal(g)
	s := &contract.RowSnapshot{Mode: "values", Metadata: string(raw), Ref: contract.RowRef{Object: l.object, Database: l.object.Schema, Table: l.object.Name, Columns: []string{"_row_id", dataGrantColumn}, Values: [][]byte{[]byte(id), bytes.Clone(raw)}, TypedValues: []contract.Cell{{Kind: contract.Integer, Text: id}, {Kind: contract.Text, Text: string(raw)}}}}
	for i, c := range cells {
		s.Columns = append(s.Columns, m.Columns[i].Name)
		s.Original = append(s.Original, c.Clone())
	}
	return s, nil
}
func cloneDataRef(ref contract.RowRef) contract.RowRef {
	r := ref
	r.Columns = append([]string(nil), ref.Columns...)
	r.Values = make([][]byte, len(ref.Values))
	for i, v := range ref.Values {
		r.Values[i] = bytes.Clone(v)
	}
	r.TypedValues = append([]contract.Cell(nil), ref.TypedValues...)
	for i := range r.TypedValues {
		r.TypedValues[i] = r.TypedValues[i].Clone()
	}
	return r
}
func (l *dataLease) grant(ref contract.RowRef) (dataGrant, error) {
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return dataGrant{}, contract.ErrRevoked
	}
	if ref.Object != l.object || ref.Database != l.object.Schema || ref.Table != l.object.Name || len(ref.Columns) != 2 || ref.Columns[0] != "_row_id" || ref.Columns[1] != dataGrantColumn || len(ref.Values) != 2 || len(ref.TypedValues) != 2 {
		return dataGrant{}, connections.ErrInvalid
	}
	var g dataGrant
	raw := ref.Values[1]
	if len(raw) > 128<<10 || json.Unmarshal(raw, &g) != nil || g.Object != l.object || g.Signature == "" || !hmac.Equal([]byte(g.Signature), []byte(l.signGrant(g))) {
		return dataGrant{}, contract.ErrDataConflict
	}
	if _, err := strconv.ParseUint(g.RowID, 10, 64); err != nil {
		return dataGrant{}, contract.ErrDataConflict
	}
	if string(ref.Values[0]) != g.RowID || !reflect.DeepEqual(ref.TypedValues[0], contract.Cell{Kind: contract.Integer, Text: g.RowID}) || ref.TypedValues[1].Kind != contract.Text || ref.TypedValues[1].Text != string(raw) || ref.TypedValues[1].Truncated || len(ref.TypedValues[1].Bytes) != 0 {
		return dataGrant{}, contract.ErrDataConflict
	}
	return g, nil
}
func validateDataGrant(g dataGrant, m *contract.TableStructure, digests []*string, id string) error {
	if g.Object != m.Object || g.RowID != id || g.Metadata != dataMetadataSignature(m) || !reflect.DeepEqual(g.Digests, digests) {
		return contract.ErrDataConflict
	}
	return nil
}
