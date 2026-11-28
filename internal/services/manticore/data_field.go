package manticore

import (
	"context"
	"encoding/hex"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// Manticore's native stored fields do not support substring expressions. This
// explicit request reads the complete selected field and checks its original
// observation; only the returned presentation fragment is bounded.
func (d *dataLease) chunk(ctx context.Context, id int64, database, table, field string, ref contract.RowRef, offset int64, limit int) (*contract.FieldChunk, error) {
	if e := d.identity(id, database, table); e != nil {
		return nil, e
	}
	if offset < 0 || limit < 1 || limit > dataPreviewUnits {
		return nil, connections.ErrInvalid
	}
	token, grant, e := d.grant(ref)
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
	if metadataSignature(m) != grant.metadata {
		return nil, contract.ErrDataConflict
	}
	index := -1
	for i, column := range m.Columns {
		if column.Name == field {
			index = i
			break
		}
	}
	if index < 0 {
		return nil, connections.ErrInvalid
	}
	value, e := d.readField(child, m.Columns[index], grant)
	if e != nil {
		return nil, e
	}
	if value.Kind != grant.preview.Original[index].Kind || !reflect.DeepEqual(fingerprints([]contract.Cell{value})[0], grant.preview.Fingerprints[index]) {
		return nil, contract.ErrDataConflict
	}
	fragment, e := fieldFragment(value, offset, limit)
	if e != nil {
		return nil, e
	}
	after, e := d.table(child)
	if e != nil {
		return nil, e
	}
	if metadataSignature(after) != grant.metadata {
		return nil, contract.ErrDataConflict
	}
	if e = d.currentGrant(token, grant); e != nil {
		return nil, e
	}
	if e = child.Err(); e != nil {
		return nil, e
	}
	return fragment, nil
}

func (d *dataLease) readField(ctx context.Context, column contract.StructureColumn, grant *dataGrant) (contract.Cell, error) {
	owner, e := d.auxiliary()
	if e != nil {
		return contract.Cell{}, e
	}
	// Release this owner before the caller rechecks metadata. A session with
	// one auxiliary slot must not wait for a slot held by its own field request.
	defer owner.Release()
	var value contract.Cell
	rows := 0
	source := "SELECT " + quote(column.Name) + " FROM " + quote(d.object.Catalog) + "." + quote(d.object.Name) + " WHERE id=" + grant.id + " LIMIT 2 OPTION max_matches=2"
	_, e = owner.ExecuteStream(ctx, d.object.Catalog, source, func(_ int, columns []contract.Column, row []contract.Cell) error {
		rows++
		if rows != 1 {
			return contract.ErrDataConflict
		}
		cells, err := dataCells([]contract.StructureColumn{column}, columns, row, false)
		if err != nil {
			return err
		}
		value = cells[0]
		return nil
	})
	if e != nil {
		return contract.Cell{}, e
	}
	if rows != 1 {
		return contract.Cell{}, contract.ErrDataConflict
	}
	return value, nil
}

func fieldFragment(value contract.Cell, offset int64, limit int) (*contract.FieldChunk, error) {
	result := &contract.FieldChunk{Offset: offset, NextOffset: offset, Null: value.Kind == contract.Null, Binary: value.Kind == contract.Binary}
	if result.Null {
		if offset != 0 {
			return nil, connections.ErrInvalid
		}
		return result, nil
	}
	if result.Binary {
		result.TotalUnits = int64(len(value.Bytes))
	} else {
		if !utf8.ValidString(value.Text) {
			return nil, contract.ErrDataPage
		}
		result.TotalUnits = int64(utf8.RuneCountInString(value.Text))
	}
	if offset > result.TotalUnits {
		return nil, connections.ErrInvalid
	}
	end := offset + min(int64(limit), result.TotalUnits-offset)
	if result.Binary {
		result.Text = "0x" + hex.EncodeToString(value.Bytes[offset:end])
	} else {
		startByte, endByte := len(value.Text), len(value.Text)
		var position int64
		for at := range value.Text {
			if position == offset {
				startByte = at
			}
			if position == end {
				endByte = at
				break
			}
			position++
		}
		// The fragment must not retain the complete original backing string.
		result.Text = strings.Clone(value.Text[startByte:endByte])
	}
	result.NextOffset = end
	result.HasMore = end < result.TotalUnits
	return result, nil
}
