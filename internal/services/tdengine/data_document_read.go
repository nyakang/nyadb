package tdengine

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func dataCellPreview(c contract.Cell) contract.DataCell {
	v := c.Clone()
	if v.Kind == contract.Binary && len(v.Bytes) > 4096 {
		v.Bytes = v.Bytes[:4096]
		v.Truncated = true
	}
	if len(v.Text) > 4096 {
		v.Text = v.Text[:4096]
		for !utf8.ValidString(v.Text) {
			v.Text = v.Text[:len(v.Text)-1]
		}
		v.Truncated = true
	}
	return contract.DataCell{Value: &v, Text: v.Display(), Null: v.Kind == contract.Null, Truncated: v.Truncated}
}

func (l *dataLease) page(ctx context.Context, id int64, db, table string, o contract.DataOptions) (*contract.DataPage, error) {
	if o.UseKeyset || o.After != nil || o.Condition != nil {
		return nil, contract.ErrUnsupported
	}
	o.Filters, o.Sorts = slices.Clone(o.Filters), slices.Clone(o.Sorts)
	if err := l.identity(id, db, table); err != nil {
		return nil, err
	}
	if o.Object.ProfileID != 0 && o.Object != l.object {
		return nil, connections.ErrInvalid
	}
	start := time.Now()
	var page *contract.DataPage
	err := l.withWire(ctx, func(child context.Context, w *wire) error {
		m, e := l.readMetadata(child, w)
		if e != nil {
			return e
		}
		structure := *m.Structure
		structure.Columns = m.Columns
		q, e := dataQuery(&structure, o, nil, true)
		if e != nil {
			return e
		}
		l.mu.Lock()
		editable := l.writeAdmission() == nil
		l.mu.Unlock()
		page = &contract.DataPage{Object: l.object, Database: db, Table: table, Offset: o.Offset, Limit: o.Limit, Editable: &editable, ColumnMetadata: slices.Clone(m.Columns), ColumnEditKinds: contract.CloneDataColumnEditKinds(m.Permissions), LiveRead: true, PagingMode: "offset", QuerySQL: q}
		for _, c := range m.Columns {
			page.Columns = append(page.Columns, c.Name)
			page.ColumnKinds = append(page.ColumnKinds, c.DataType)
		}
		budget, size := 0, 0
		_, _, e = w.query(child, q, func(columns []contract.Column, cells []contract.Cell) error {
			if len(columns) != len(m.Columns) || len(cells) != len(m.Columns) {
				return contract.ErrDataPage
			}
			for i, c := range columns {
				if c.Name != m.Columns[i].Name {
					return contract.ErrDataPage
				}
			}
			if len(page.Rows) >= o.Limit {
				page.HasMore = true
				return nil
			}
			snapshot, e := l.snapshot(m, cells)
			if e != nil {
				return e
			}
			row := make([]contract.DataCell, len(cells))
			for i, c := range cells {
				row[i] = dataCellPreview(c)
			}
			raw, e := json.Marshal(struct {
				Row      []contract.DataCell
				Snapshot *contract.RowSnapshot
			}{row, snapshot})
			if e != nil {
				return e
			}
			size += len(raw)
			if size > 32<<20 {
				return contract.ErrUnsupported
			}
			page.Rows = append(page.Rows, row)
			page.RowSnapshots = append(page.RowSnapshots, snapshot)
			ref := snapshot.Ref
			page.RowRefs = append(page.RowRefs, &ref)
			return nil
		}, &budget)
		return e
	})
	if err != nil {
		return nil, err
	}
	page.CompletedAt, page.Duration = time.Now(), time.Since(start)
	return page, nil
}

func (l *dataLease) readOriginal(ctx context.Context, w *wire, m *dataMetadata, g dataRowGrant, ref contract.RowRef, columns []string) ([]contract.Cell, error) {
	if err := validateDataGrant(g, m, ref); err != nil {
		return nil, err
	}
	where, err := dataGrantWhere(g, m)
	if err != nil {
		return nil, err
	}
	parts := []string{}
	for _, name := range columns {
		found := false
		for _, c := range m.Columns {
			if c.Name == name {
				found = true
			}
		}
		if !found {
			return nil, connections.ErrInvalid
		}
		if name == "tbname" && l.object.Kind == "stable" {
			parts = append(parts, literal(g.Destination.Name)+" AS "+quote(name))
		} else {
			parts = append(parts, quote(name))
		}
	}
	if len(parts) == 0 {
		return nil, connections.ErrInvalid
	}
	q := "SELECT " + strings.Join(parts, ",") + " FROM " + qualified(g.Destination.Catalog, g.Destination.Name) + " WHERE " + where + " LIMIT 2"
	var found []contract.Cell
	rows, budget := 0, 0
	_, _, err = w.query(ctx, q, func(metadata []contract.Column, cells []contract.Cell) error {
		rows++
		if rows != 1 || len(cells) != len(columns) || len(metadata) != len(columns) {
			return contract.ErrDataConflict
		}
		for i, name := range columns {
			if metadata[i].Name != name {
				return contract.ErrDataPage
			}
		}
		found = make([]contract.Cell, len(cells))
		for i, c := range cells {
			if c.Truncated {
				return contract.ErrDataPage
			}
			found[i] = c.Clone()
		}
		return nil
	}, &budget)
	if err != nil {
		return nil, err
	}
	if rows != 1 {
		return nil, contract.ErrDataConflict
	}
	return found, nil
}

func (l *dataLease) row(ctx context.Context, id int64, db, table string, ref contract.RowRef) (*contract.DataRow, error) {
	ref = cloneDataRowRef(ref)
	if err := l.identity(id, db, table); err != nil {
		return nil, err
	}
	g, err := l.grant(ref)
	if err != nil {
		return nil, err
	}
	var result *contract.DataRow
	err = l.withWire(ctx, func(child context.Context, w *wire) error {
		m, e := l.readMetadata(child, w)
		if e != nil {
			return e
		}
		columns := []string{}
		for _, c := range m.Columns {
			columns = append(columns, c.Name)
		}
		cells, e := l.readOriginal(child, w, m, g, ref, columns)
		if e != nil {
			return e
		}
		total := 0
		for i, c := range cells {
			total += len(c.Text) + len(c.Bytes)
			if !reflect.DeepEqual(fingerprintCell(c), g.Fingerprints[i]) {
				return contract.ErrDataConflict
			}
		}
		if total > contract.DataEditMaxBytes {
			return contract.ErrDataEditLimit
		}
		snapshot, e := l.snapshot(m, cells)
		if e != nil {
			return e
		}
		result = &contract.DataRow{Columns: slices.Clone(m.Columns), Values: map[string]contract.DataValue{}, Snapshot: snapshot}
		for i, c := range cells {
			result.Values[m.Columns[i].Name] = contract.DataValue{Kind: c.Kind, Text: c.Text, Bytes: bytes.Clone(c.Bytes), Null: c.Kind == contract.Null, Binary: c.Kind == contract.Binary}
		}
		return nil
	})
	return result, err
}

// Native full-field reads preserve bytes and verify the original fingerprint.
// The returned segment is at most 1024 characters/bytes; this is not a bounded
// server projection or a driver-allocation guarantee (VARBINARY has no verified
// native substring conversion on the accepted server).
func (l *dataLease) chunk(ctx context.Context, id int64, db, table, column string, ref contract.RowRef, offset int64, limit int) (*contract.FieldChunk, error) {
	ref = cloneDataRowRef(ref)
	if err := l.identity(id, db, table); err != nil {
		return nil, err
	}
	if offset < 0 || limit < 1 || limit > 1024 {
		return nil, connections.ErrInvalid
	}
	g, err := l.grant(ref)
	if err != nil {
		return nil, err
	}
	var result *contract.FieldChunk
	err = l.withWire(ctx, func(child context.Context, w *wire) error {
		m, e := l.readMetadata(child, w)
		if e != nil {
			return e
		}
		cells, e := l.readOriginal(child, w, m, g, ref, []string{column})
		if e != nil {
			return e
		}
		index := -1
		for i, c := range m.Columns {
			if c.Name == column {
				index = i
			}
		}
		if index < 0 {
			return connections.ErrInvalid
		}
		if !reflect.DeepEqual(fingerprintCell(cells[0]), g.Fingerprints[index]) {
			return contract.ErrDataConflict
		}
		c := cells[0]
		result = &contract.FieldChunk{Offset: offset, NextOffset: offset, Null: c.Kind == contract.Null, Binary: c.Kind == contract.Binary}
		if result.Null {
			return nil
		}
		if result.Binary {
			result.TotalUnits = int64(len(c.Bytes))
			if offset > result.TotalUnits {
				return connections.ErrInvalid
			}
			end := min(result.TotalUnits, offset+int64(limit))
			result.Text = "0x" + hex.EncodeToString(c.Bytes[offset:end])
			result.NextOffset = end
		} else {
			runes := []rune(c.Text)
			result.TotalUnits = int64(len(runes))
			if offset > result.TotalUnits {
				return connections.ErrInvalid
			}
			end := min(result.TotalUnits, offset+int64(limit))
			result.Text = string(runes[offset:end])
			result.NextOffset = end
		}
		result.HasMore = result.NextOffset < result.TotalUnits
		return nil
	})
	return result, err
}

func (l *dataLease) count(ctx context.Context, id int64, db, table string) (int64, error) {
	if err := l.identity(id, db, table); err != nil {
		return 0, err
	}
	var count int64
	err := l.withWire(ctx, func(child context.Context, w *wire) error {
		if _, e := l.readMetadata(child, w); e != nil {
			return e
		}
		r, e := metadataRead(child, w, "SELECT count(*) FROM "+qualified(db, table))
		if e != nil {
			return e
		}
		if len(r.Rows) != 1 || len(r.Rows[0]) != 1 {
			return contract.ErrDataPage
		}
		var value json.Number
		if e = json.Unmarshal([]byte(r.Rows[0][0].Text), &value); e != nil {
			return e
		}
		count, e = value.Int64()
		return e
	})
	return count, err
}

func (l *dataLease) export(ctx context.Context, id int64, db, table string, o contract.DataOptions, columns []string, sink func([]contract.DataValue) error) error {
	if o.UseKeyset || o.After != nil || o.Condition != nil {
		return contract.ErrUnsupported
	}
	o.Filters, o.Sorts = slices.Clone(o.Filters), slices.Clone(o.Sorts)
	columns = slices.Clone(columns)
	if err := l.identity(id, db, table); err != nil {
		return err
	}
	if o.Object.ProfileID != 0 && o.Object != l.object || sink == nil {
		return connections.ErrInvalid
	}
	return l.withWire(ctx, func(child context.Context, w *wire) error {
		m, e := l.readMetadata(child, w)
		if e != nil {
			return e
		}
		t := *m.Structure
		t.Columns = m.Columns
		q, e := dataQuery(&t, o, columns, false)
		if e != nil {
			return e
		}
		budget := 0
		_, _, e = w.query(child, q, func(_ []contract.Column, cells []contract.Cell) error {
			values := make([]contract.DataValue, len(cells))
			for i, c := range cells {
				values[i] = contract.DataValue{Kind: c.Kind, Text: c.Text, Bytes: bytes.Clone(c.Bytes), Null: c.Kind == contract.Null, Binary: c.Kind == contract.Binary}
			}
			return sink(values)
		}, &budget)
		return e
	})
}
func (l *dataLease) exportJSONL(ctx context.Context, writer io.Writer) error {
	encoder := json.NewEncoder(writer)
	return l.export(ctx, l.object.ProfileID, l.object.Catalog, l.object.Name, contract.DataOptions{Object: l.object}, nil, func(values []contract.DataValue) error {
		cells := make([]contract.Cell, len(values))
		for i, v := range values {
			cells[i] = contract.Cell{Kind: v.Kind, Text: v.Text, Bytes: bytes.Clone(v.Bytes)}
		}
		return encoder.Encode(cells)
	})
}
