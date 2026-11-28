package manticore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const dataGrantColumn = "__nyadb_manticore_observation"

type dataGrant struct {
	id, metadata string
	preview      *contract.RowSnapshot
}

func metadataSignature(m *contract.TableStructure) string {
	raw, _ := json.Marshal(m)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func fingerprints(cells []contract.Cell) []*string {
	result := make([]*string, len(cells))
	for i, c := range cells {
		if c.Kind != contract.Null {
			raw := []byte(c.Text)
			if c.Kind == contract.Binary {
				raw = c.Bytes
			}
			h := sha256.Sum256(raw)
			s := hex.EncodeToString(h[:])
			result[i] = &s
		}
	}
	return result
}
func snapshotCopy(s *contract.RowSnapshot) *contract.RowSnapshot {
	return contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Edits: []contract.DataEdit{{Original: s}}}).Edits[0].Original
}
func (d *dataLease) grant(ref contract.RowRef) (string, *dataGrant, error) {
	if ref.Object != d.object || ref.Database != d.object.Catalog || ref.Table != d.object.Name || len(ref.Columns) != 1 || ref.Columns[0] != dataGrantColumn || len(ref.Values) != 1 || len(ref.TypedValues) != 0 {
		return "", nil, connections.ErrInvalid
	}
	token := string(ref.Values[0])
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return "", nil, contract.ErrRevoked
	}
	g := d.grants[token]
	if g == nil {
		return "", nil, contract.ErrDataConflict
	}
	return token, g, nil
}
func (d *dataLease) currentGrant(token string, g *dataGrant) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return contract.ErrRevoked
	}
	if d.grants[token] != g {
		return contract.ErrDataConflict
	}
	return nil
}
func (d *dataLease) pageGrant(m *contract.TableStructure, full, preview []contract.Cell) (string, *dataGrant, error) {
	if len(full) == 0 || m.Columns[0].Name != "id" || full[0].Kind != contract.Integer {
		return "", nil, contract.ErrUnsupported
	}
	id, e := strconv.ParseInt(full[0].Text, 10, 64)
	if e != nil {
		return "", nil, contract.ErrDataPage
	}
	token := rand.Text()
	sig := metadataSignature(m)
	ref := contract.RowRef{Object: d.object, Database: d.object.Catalog, Table: d.object.Name, Columns: []string{dataGrantColumn}, Values: [][]byte{[]byte(token)}}
	snap := &contract.RowSnapshot{Ref: ref, Mode: "values", Original: preview, Fingerprints: fingerprints(full), Metadata: sig}
	for _, c := range m.Columns {
		snap.Columns = append(snap.Columns, c.Name)
	}
	return token, &dataGrant{id: strconv.FormatInt(id, 10), metadata: sig, preview: snap}, nil
}
func (d *dataLease) readOriginal(ctx context.Context, m *contract.TableStructure, g *dataGrant) ([]contract.Cell, error) {
	if metadataSignature(m) != g.metadata {
		return nil, contract.ErrDataConflict
	}
	var names []string
	for _, c := range m.Columns {
		names = append(names, quote(c.Name))
	}
	owner, e := d.auxiliary()
	if e != nil {
		return nil, e
	}
	defer owner.Release()
	var cells []contract.Cell
	rows := 0
	_, e = owner.ExecuteStream(ctx, d.object.Catalog, "SELECT "+strings.Join(names, ",")+" FROM "+quote(d.object.Catalog)+"."+quote(d.object.Name)+" WHERE id="+g.id+" LIMIT 2 OPTION max_matches=2", func(_ int, cols []contract.Column, row []contract.Cell) error {
		rows++
		if rows != 1 {
			return contract.ErrDataConflict
		}
		var err error
		cells, err = dataCells(m.Columns, cols, row, false)
		if err != nil {
			return err
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	if rows != 1 || !reflect.DeepEqual(fingerprints(cells), g.preview.Fingerprints) {
		return nil, contract.ErrDataConflict
	}
	return cells, nil
}
func (d *dataLease) row(ctx context.Context, id int64, db, table string, ref contract.RowRef) (*contract.DataRow, error) {
	if e := d.identity(id, db, table); e != nil {
		return nil, e
	}
	token, g, e := d.grant(ref)
	if e != nil {
		return nil, e
	}
	child, done, e := d.request(ctx)
	if e != nil {
		return nil, e
	}
	defer done()
	m, e := d.table(child)
	if e != nil {
		return nil, e
	}
	cells, e := d.readOriginal(child, m, g)
	if e != nil {
		return nil, e
	}
	size := 0
	values := map[string]contract.DataValue{}
	for i, c := range cells {
		size += len(c.Text) + len(c.Bytes)
		if size > contract.DataEditMaxBytes {
			return nil, contract.ErrDataEditLimit
		}
		values[m.Columns[i].Name] = contract.DataValue{Kind: c.Kind, Text: c.Text, Bytes: slices.Clone(c.Bytes), Null: c.Kind == contract.Null, Binary: c.Kind == contract.Binary}
	}
	if e = d.currentGrant(token, g); e != nil {
		return nil, e
	}
	complete := snapshotCopy(g.preview)
	complete.Mode = "values"
	complete.Original = cells
	return &contract.DataRow{Columns: slices.Clone(m.Columns), Values: values, Snapshot: complete, PreviewSnapshot: snapshotCopy(g.preview)}, nil
}
func validateSnapshot(s *contract.RowSnapshot, g *dataGrant) error {
	if s == nil || s.Metadata != g.metadata || !slices.Equal(s.Columns, g.preview.Columns) || !reflect.DeepEqual(s.Fingerprints, g.preview.Fingerprints) || len(s.Original) != len(g.preview.Original) {
		return contract.ErrDataConflict
	}
	switch s.Mode {
	case "fingerprints":
		if !reflect.DeepEqual(s.Original, g.preview.Original) {
			return contract.ErrDataConflict
		}
	case "values":
		if reflect.DeepEqual(s.Original, g.preview.Original) {
			return nil
		}
		for i, c := range s.Original {
			if c.Truncated || c.Kind != g.preview.Original[i].Kind {
				return contract.ErrDataConflict
			}
		}
		if !reflect.DeepEqual(fingerprints(s.Original), g.preview.Fingerprints) {
			return contract.ErrDataConflict
		}
	default:
		return contract.ErrDataConflict
	}
	return nil
}
