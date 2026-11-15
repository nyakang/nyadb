package tdengine

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const dataGrantColumn = "__nyadb_tdengine_grant"

func cloneDataRowRef(ref contract.RowRef) contract.RowRef {
	ref.Columns = slices.Clone(ref.Columns)
	ref.Values = slices.Clone(ref.Values)
	for i := range ref.Values {
		ref.Values[i] = bytes.Clone(ref.Values[i])
	}
	ref.TypedValues = slices.Clone(ref.TypedValues)
	for i := range ref.TypedValues {
		ref.TypedValues[i] = ref.TypedValues[i].Clone()
	}
	return ref
}

type dataMetadata struct {
	Structure            *contract.TableStructure
	Columns              []contract.StructureColumn
	Keys                 []int
	Precision, Signature string
	Parent               contract.ObjectRef
	Children             map[string]contract.ObjectRef
	Permissions          map[string][]string
	Epoch                uint64
}

// Keys identify the original child and native timestamp/composite-key group.
// The signature binds original complete fingerprints to this document lease;
// it is not a native incarnation token, transaction snapshot or CAS promise.
type dataRowGrant struct {
	Object, Destination contract.ObjectRef
	Epoch               uint64
	Metadata, Precision string
	Keys                []contract.Cell
	KeyColumns          []string
	Fingerprints        []*string
	Signature           string
}

func (l *dataLease) readMetadata(ctx context.Context, w *wire) (*dataMetadata, error) {
	t, err := l.session.tableMetadataOnWire(ctx, w, l.object)
	if err != nil {
		return nil, err
	}
	m := &dataMetadata{Structure: t, Columns: slices.Clone(t.Columns), Children: map[string]contract.ObjectRef{}, Permissions: map[string][]string{}}
	if len(m.Columns) == 0 || m.Columns[0].DataType != "TIMESTAMP" {
		return nil, contract.ErrUnsupported
	}
	objects, err := l.session.browse(ctx, w, l.object.Namespace())
	if err != nil {
		return nil, err
	}
	for _, object := range objects {
		if object.Ref == l.object {
			m.Parent = object.ParentObject
		}
		if object.ParentObject == l.object {
			m.Children[object.Name] = object.Ref
		}
	}
	if l.object.Kind == "stable" {
		for _, column := range m.Columns {
			if column.Name == "tbname" {
				return nil, contract.ErrUnsupported
			}
		}
		length := int64(192)
		m.Columns = append([]contract.StructureColumn{{Name: "tbname", Type: "VARCHAR(192)", DataType: "VARCHAR", Key: "PRI", Nullable: false, ValueKind: contract.Text, CharacterMaxLength: &length}}, m.Columns...)
	}
	for i := range m.Columns {
		c := &m.Columns[i]
		c.Position = int64(i + 1)
		isTag := strings.Contains(strings.ToUpper(c.Extra), "TAG")
		key := c.DataType == "TIMESTAMP" && (i == 0 || l.object.Kind == "stable" && i == 1) || strings.Contains(strings.ToUpper(c.Extra), "COMPOSITE KEY") || l.object.Kind == "stable" && i == 0
		if key && !isTag {
			c.Key = "PRI"
			c.Nullable = false
			m.Keys = append(m.Keys, i)
		}
		m.Permissions[c.Name] = []string{}
		if !isTag && c.DataType != "JSON" && c.DataType != "GEOMETRY" && !strings.Contains(c.DataType, "BLOB") {
			m.Permissions[c.Name] = []string{"insert"}
			if !key {
				m.Permissions[c.Name] = append(m.Permissions[c.Name], "update")
			}
		}
	}
	// SHOW DATABASES exposes only names in native 3.3.6.13. Capture the
	// original catalog's precision from the authoritative system metadata.
	databases, err := metadataRead(ctx, w, "SELECT `precision` FROM information_schema.ins_databases WHERE `name`="+literal(l.object.Catalog))
	if err != nil {
		return nil, err
	}
	if len(databases.Columns) == 1 && strings.EqualFold(databases.Columns[0].Name, "precision") && len(databases.Rows) == 1 && len(databases.Rows[0]) == 1 && databases.Rows[0][0].Kind == contract.Text {
		m.Precision = databases.Rows[0][0].Text
	}
	if m.Precision != "ns" && m.Precision != "us" && m.Precision != "ms" {
		return nil, contract.ErrUnsupported
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	m.Signature = hex.EncodeToString(digest[:])
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, contract.ErrRevoked
	}
	if l.meta == nil || l.meta.Signature != m.Signature {
		l.epoch++
	}
	m.Epoch = l.epoch
	l.meta = m
	return m, nil
}

func (l *dataLease) metadata(ctx context.Context, id int64, db, table string) (*contract.TableStructure, error) {
	if err := l.identity(id, db, table); err != nil {
		return nil, err
	}
	var result *contract.TableStructure
	err := l.withWire(ctx, func(child context.Context, w *wire) error {
		m, e := l.readMetadata(child, w)
		if e == nil {
			result = m.Structure
		}
		return e
	})
	return result, err
}

func (l *dataLease) signGrant(g dataRowGrant) string {
	g.Signature = ""
	raw, _ := json.Marshal(g)
	l.mu.Lock()
	key := l.key
	l.mu.Unlock()
	h := hmac.New(sha256.New, key[:])
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

func fingerprintCell(c contract.Cell) *string {
	if c.Kind == contract.Null {
		return nil
	}
	raw, _ := json.Marshal(c)
	h := sha256.Sum256(raw)
	value := hex.EncodeToString(h[:])
	return &value
}

func (l *dataLease) snapshot(m *dataMetadata, cells []contract.Cell) (*contract.RowSnapshot, error) {
	if len(cells) != len(m.Columns) {
		return nil, contract.ErrDataPage
	}
	g := dataRowGrant{Object: l.object, Destination: l.object, Epoch: m.Epoch, Metadata: m.Signature, Precision: m.Precision}
	if l.object.Kind == "stable" {
		if cells[0].Kind != contract.Text {
			return nil, contract.ErrDataPage
		}
		var ok bool
		g.Destination, ok = m.Children[cells[0].Text]
		if !ok {
			return nil, contract.ErrDataConflict
		}
	}
	ref := contract.RowRef{Object: l.object, Database: l.object.Catalog, Table: l.object.Name}
	for _, i := range m.Keys {
		if cells[i].Kind == contract.Null || cells[i].Truncated {
			return nil, contract.ErrDataReadOnly
		}
		key := cells[i].Clone()
		g.Keys = append(g.Keys, key)
		g.KeyColumns = append(g.KeyColumns, m.Columns[i].Name)
		ref.Columns = append(ref.Columns, m.Columns[i].Name)
		ref.TypedValues = append(ref.TypedValues, key.Clone())
		ref.Values = append(ref.Values, cellKeyBytes(key))
	}
	s := &contract.RowSnapshot{Ref: ref, Mode: "values"}
	complete := 0
	for _, c := range cells {
		complete += len(c.Text) + len(c.Bytes)
	}
	if complete > contract.DataEditMaxBytes {
		s.Mode = "fingerprints"
	}
	for i, c := range cells {
		if c.Truncated {
			return nil, contract.ErrDataPage
		}
		s.Columns = append(s.Columns, m.Columns[i].Name)
		g.Fingerprints = append(g.Fingerprints, fingerprintCell(c))
		if s.Mode == "values" {
			s.Original = append(s.Original, c.Clone())
		}
	}
	g.Signature = l.signGrant(g)
	raw, _ := json.Marshal(g)
	s.Metadata = string(raw)
	s.Fingerprints = g.Fingerprints
	s.Ref.Columns = append(s.Ref.Columns, dataGrantColumn)
	s.Ref.Values = append(s.Ref.Values, bytes.Clone(raw))
	s.Ref.TypedValues = append(s.Ref.TypedValues, contract.Cell{Kind: contract.Text, Text: string(raw)})
	return s, nil
}

func cellKeyBytes(c contract.Cell) []byte {
	if c.Kind == contract.Binary {
		return bytes.Clone(c.Bytes)
	}
	return []byte(c.Text)
}

func (l *dataLease) grant(ref contract.RowRef) (dataRowGrant, error) {
	var g dataRowGrant
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return g, contract.ErrRevoked
	}
	if ref.Object != l.object || ref.Database != l.object.Catalog || ref.Table != l.object.Name || len(ref.Columns) < 2 || ref.Columns[len(ref.Columns)-1] != dataGrantColumn || len(ref.Columns) != len(ref.Values) || len(ref.Columns) != len(ref.TypedValues) {
		return g, connections.ErrInvalid
	}
	raw := ref.Values[len(ref.Values)-1]
	if len(raw) > 128<<10 || json.Unmarshal(raw, &g) != nil || g.Object != l.object || g.Destination.Namespace() != l.object.Namespace() || g.Destination.Kind != "table" || !validName(g.Destination.Name) || len(g.Keys) != len(ref.Columns)-1 || g.Signature == "" || !hmac.Equal([]byte(g.Signature), []byte(l.signGrant(g))) {
		return dataRowGrant{}, contract.ErrDataConflict
	}
	if ref.TypedValues[len(ref.TypedValues)-1].Kind != contract.Text || ref.TypedValues[len(ref.TypedValues)-1].Text != string(raw) {
		return dataRowGrant{}, contract.ErrDataConflict
	}
	if !slices.Equal(ref.Columns[:len(ref.Columns)-1], g.KeyColumns) {
		return dataRowGrant{}, contract.ErrDataConflict
	}
	for i, c := range g.Keys {
		if !reflect.DeepEqual(c, ref.TypedValues[i]) || !bytes.Equal(cellKeyBytes(c), ref.Values[i]) {
			return dataRowGrant{}, contract.ErrDataConflict
		}
	}
	l.mu.Lock()
	closed = l.closed
	l.mu.Unlock()
	if closed {
		return dataRowGrant{}, contract.ErrRevoked
	}
	return g, nil
}

func validateDataGrant(g dataRowGrant, m *dataMetadata, ref contract.RowRef) error {
	if g.Metadata != m.Signature || g.Epoch != m.Epoch || g.Precision != m.Precision || len(g.Keys) != len(m.Keys) || len(g.Fingerprints) != len(m.Columns) {
		return contract.ErrDataConflict
	}
	if g.Object.Kind == "stable" {
		if g.Keys[0].Kind != contract.Text || m.Children[g.Keys[0].Text] != g.Destination {
			return contract.ErrDataConflict
		}
	} else if g.Destination != g.Object {
		return contract.ErrDataConflict
	}
	for i, index := range m.Keys {
		if ref.Columns[i] != m.Columns[index].Name {
			return contract.ErrDataConflict
		}
	}
	return nil
}

func timestampLiteral(c contract.Cell, precision string) (string, error) {
	if c.Kind != contract.DateTime {
		return "", connections.ErrInvalid
	}
	t, err := time.Parse(time.RFC3339Nano, c.Text)
	if err != nil {
		return "", connections.ErrInvalid
	}
	unit := int64(1)
	if precision == "us" {
		unit = 1000
	} else if precision == "ms" {
		unit = 1000000
	} else if precision != "ns" {
		return "", contract.ErrUnsupported
	}
	if int64(t.Nanosecond())%unit != 0 {
		return "", connections.ErrInvalid
	}
	v := new(big.Int).Mul(big.NewInt(t.Unix()), big.NewInt(1000000000))
	v.Add(v, big.NewInt(int64(t.Nanosecond())))
	v.Quo(v, big.NewInt(unit))
	if !v.IsInt64() {
		return "", connections.ErrInvalid
	}
	return v.String(), nil
}

func dataGrantWhere(g dataRowGrant, m *dataMetadata) (string, error) {
	parts := []string{}
	for i, index := range m.Keys {
		column := m.Columns[index]
		if column.Name == "tbname" && g.Object.Kind == "stable" {
			continue
		}
		cell := g.Keys[i]
		var value string
		var err error
		if column.DataType == "TIMESTAMP" {
			value, err = timestampLiteral(cell, m.Precision)
		} else {
			if cell.Kind != column.ValueKind && column.ValueKind != "" {
				return "", connections.ErrInvalid
			}
			value, err = filterLiteral(column, cell.Text)
		}
		if err != nil {
			return "", err
		}
		parts = append(parts, quote(column.Name)+"="+value)
	}
	if len(parts) == 0 {
		return "", contract.ErrDataReadOnly
	}
	return strings.Join(parts, " AND "), nil
}
