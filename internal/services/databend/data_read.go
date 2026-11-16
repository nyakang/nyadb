package databend

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func dataHasRowID(m *contract.TableStructure) bool {
	if m == nil || m.Object.Kind != "table" {
		return false
	}
	tokens, err := sqltext.DatabendTokens(m.Definition)
	if err != nil {
		return false
	}
	var terms []sqltext.Token
	for _, t := range tokens {
		if !t.Comment {
			terms = append(terms, t)
		}
	}
	depth := 0
	for i, t := range terms {
		if t.Quoted {
			continue
		}
		switch t.Text {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth == 0 && strings.EqualFold(t.Text, "ENGINE") && i+2 < len(terms) && !terms[i+1].Quoted && terms[i+1].Text == "=" && !terms[i+2].Quoted {
			return strings.EqualFold(terms[i+2].Text, "FUSE")
		}
	}
	return false
}
func (l *dataLease) readRows(ctx context.Context, original *ManagedSession, source string, p *dataProjection, consume func([]contract.Cell, []*string, []int64, string) error) (err error) {
	cursor, page, err := original.OpenQueryCursor(ctx, agentbridge.QueryCursorOptions{SQL: source, PageSize: 1, MaxRows: math.MaxInt32, TimeoutSeconds: l.session.profile.DatabendOptions.QueryTimeoutSeconds, Decoder: DecodeJDBCValue})
	if err != nil {
		l.session.observed("data-cursor", page.Receipt)
		return err
	}
	defer func() { err = errors.Join(err, cursor.Close()) }()
	for {
		l.session.observed("data-cursor", page.Receipt)
		if page.SQL.Truncated || len(page.SQL.Sets) != 1 {
			return contract.ErrDataPage
		}
		set := page.SQL.Sets[0]
		for _, row := range set.Rows {
			cells, digests, units, id, err := p.decode(set.Columns, row)
			if err != nil {
				return err
			}
			if err = consume(cells, digests, units, id); err != nil {
				return err
			}
		}
		if !page.HasMore {
			return ctx.Err()
		}
		page, err = cursor.Fetch(ctx, 1)
		if err != nil {
			l.session.observed("data-cursor", page.Receipt)
			return err
		}
	}
}

func (l *dataLease) page(ctx context.Context, id int64, db, table string, o contract.DataOptions) (*contract.DataPage, error) {
	o.Filters, o.Sorts = slices.Clone(o.Filters), slices.Clone(o.Sorts)
	if err := l.identity(id, db, table); err != nil {
		return nil, err
	}
	if o.Object.ProfileID != 0 && o.Object != l.object {
		return nil, connections.ErrInvalid
	}
	o.Object = l.object
	if o.Offset < 0 || o.Limit < 1 || o.Limit > contract.DataMaxPageSize {
		return nil, connections.ErrInvalid
	}
	started := time.Now()
	var result *contract.DataPage
	err := l.withOriginal(ctx, false, func(child context.Context, original *ManagedSession) error {
		m, err := l.session.readTableMetadata(child, original, l.object)
		if err != nil {
			return err
		}
		rowID := dataHasRowID(m)
		source, total, err := dataSource(m, rowID, false)
		if err != nil {
			return err
		}
		p, err := newDataProjection(m, nil, dataPreviewUnits, 0, rowID, false, total)
		if err != nil {
			return err
		}
		query, display, err := dataQuery(m, p, o, source, true)
		if err != nil {
			return err
		}
		l.mu.Lock()
		editable := rowID && !l.writeBlocked && !l.closed
		l.mu.Unlock()
		result = &contract.DataPage{Object: l.object, Database: db, Table: table, Offset: o.Offset, Limit: o.Limit, Editable: &editable, ColumnMetadata: slices.Clone(m.Columns), ColumnEditKinds: map[string][]string{}, LiveRead: true, QuerySQL: display}
		for _, c := range m.Columns {
			result.Columns = append(result.Columns, c.Name)
			result.ColumnKinds = append(result.ColumnKinds, c.Type)
			if _, err := parseDataType(c.Type); err == nil && editable && !c.Generated && !contract.DataColumnGenerated(c) {
				result.ColumnEditKinds[c.Name] = []string{"insert", "update"}
			}
		}
		budget := 0
		return l.readRows(child, original, query, p, func(cells []contract.Cell, digests []*string, _ []int64, rowid string) error {
			if len(result.Rows) >= o.Limit {
				result.HasMore = true
				return nil
			}
			row := make([]contract.DataCell, len(cells))
			for i, c := range cells {
				v := c.Clone()
				row[i] = contract.DataCell{Value: &v, Text: v.Display(), Null: v.Kind == contract.Null, Truncated: v.Truncated}
				budget += len(v.Text) + len(v.Bytes) + 128
			}
			if budget > 32<<20 {
				return contract.ErrDataPage
			}
			result.Rows = append(result.Rows, row)
			if rowID {
				s, err := l.snapshot(m, cells, digests, rowid)
				if err != nil {
					return err
				}
				result.RowSnapshots = append(result.RowSnapshots, s)
				ref := cloneDataRef(s.Ref)
				result.RowRefs = append(result.RowRefs, &ref)
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	result.CompletedAt = time.Now()
	result.Duration = time.Since(started)
	return result, nil
}

func (l *dataLease) count(ctx context.Context, id int64, db, table string) (int64, error) {
	if err := l.identity(id, db, table); err != nil {
		return 0, err
	}
	var count int64
	err := l.withOriginal(ctx, true, func(child context.Context, original *ManagedSession) error {
		q, err := l.session.query(child, original, "SELECT count() AS __nyadb_count FROM "+qualified(l.object), 1)
		if err != nil {
			return err
		}
		if q.Truncated || len(q.Sets) != 1 || len(q.Sets[0].Rows) != 1 || len(q.Sets[0].Rows[0]) != 1 || q.Sets[0].Rows[0][0].Kind != contract.Integer {
			return contract.ErrDataPage
		}
		count, err = strconv.ParseInt(q.Sets[0].Rows[0][0].Text, 10, 64)
		if err != nil || count < 0 {
			return contract.ErrDataPage
		}
		return nil
	})
	return count, err
}

func (l *dataLease) row(ctx context.Context, id int64, db, table string, ref contract.RowRef) (*contract.DataRow, error) {
	ref = cloneDataRef(ref)
	if err := l.identity(id, db, table); err != nil {
		return nil, err
	}
	g, err := l.grant(ref)
	if err != nil {
		return nil, err
	}
	var result *contract.DataRow
	err = l.withOriginal(ctx, true, func(child context.Context, original *ManagedSession) error {
		m, err := l.session.readTableMetadata(child, original, l.object)
		if err != nil {
			return err
		}
		if !dataHasRowID(m) {
			return contract.ErrUnsupported
		}
		source, total, err := dataSource(m, true, true)
		if err != nil {
			return err
		}
		p, err := newDataProjection(m, nil, contract.DataEditMaxBytes, 0, true, true, total)
		if err != nil {
			return err
		}
		p.rawJSON = true
		query := "SELECT " + strings.Join(p.selects, ",") + " FROM " + source + " WHERE " + quoteName(dataReadAlias) + `."_row_id"=` + g.RowID + " LIMIT 2"
		rows := 0
		err = l.readRows(child, original, query, p, func(cells []contract.Cell, digests []*string, _ []int64, rowid string) error {
			rows++
			if rows != 1 {
				return contract.ErrDataConflict
			}
			if err := validateDataGrant(g, m, digests, rowid); err != nil {
				return err
			}
			preview, complete, err := dataRowProjections(cells)
			if err != nil {
				return err
			}
			observed, err := l.snapshot(m, preview, digests, rowid)
			if err != nil {
				return err
			}
			s, err := l.snapshot(m, complete, digests, rowid)
			if err != nil {
				return err
			}
			result = &contract.DataRow{Columns: slices.Clone(m.Columns), Values: map[string]contract.DataValue{}, Snapshot: s, PreviewSnapshot: observed}
			for i, c := range complete {
				result.Values[m.Columns[i].Name] = contract.DataValue{Kind: c.Kind, Text: c.Text, Bytes: bytes.Clone(c.Bytes), Null: c.Kind == contract.Null, Binary: c.Kind == contract.Binary}
			}
			return nil
		})
		if err == nil && rows != 1 {
			return contract.ErrDataConflict
		}
		return err
	})
	return result, err
}

// Both projections derive from one executor row after its original signed
// grant is checked. Native JSON whitespace counts toward the bounded preview;
// complete typed JSON keeps the established canonical value representation.
func dataRowProjections(cells []contract.Cell) ([]contract.Cell, []contract.Cell, error) {
	preview, complete := make([]contract.Cell, len(cells)), make([]contract.Cell, len(cells))
	for i, c := range cells {
		preview[i], complete[i] = c.Clone(), c.Clone()
		if c.Kind == contract.Null {
			continue
		}
		if c.Kind == contract.Binary {
			if len(c.Bytes) > dataPreviewUnits {
				preview[i].Bytes = bytes.Clone(c.Bytes[:dataPreviewUnits])
				preview[i].Truncated = true
			}
		} else {
			runes := []rune(c.Text)
			if len(runes) > dataPreviewUnits {
				preview[i].Text = string(runes[:dataPreviewUnits])
				preview[i].Truncated = true
			}
		}
		if c.Kind == contract.JSON {
			var compact bytes.Buffer
			if json.Compact(&compact, []byte(c.Text)) != nil {
				return nil, nil, contract.ErrDataPage
			}
			complete[i].Text = compact.String()
			if !preview[i].Truncated {
				preview[i].Text = compact.String()
			}
		}
	}
	return preview, complete, nil
}

func (l *dataLease) chunk(ctx context.Context, id int64, db, table, column string, ref contract.RowRef, offset int64, limit int) (*contract.FieldChunk, error) {
	ref = cloneDataRef(ref)
	if err := l.identity(id, db, table); err != nil {
		return nil, err
	}
	if offset < 0 || offset > (math.MaxInt64-1)/2 || limit < 1 || limit > 1024 {
		return nil, connections.ErrInvalid
	}
	g, err := l.grant(ref)
	if err != nil {
		return nil, err
	}
	var result *contract.FieldChunk
	err = l.withOriginal(ctx, true, func(child context.Context, original *ManagedSession) error {
		m, err := l.session.readTableMetadata(child, original, l.object)
		if err != nil {
			return err
		}
		if !dataHasRowID(m) || g.Metadata != dataMetadataSignature(m) {
			return contract.ErrDataConflict
		}
		index := -1
		for i, c := range m.Columns {
			if c.Name == column {
				index = i
			}
		}
		if index < 0 || len(g.Digests) != len(m.Columns) {
			return connections.ErrInvalid
		}
		p, err := newDataProjection(m, []string{column}, limit, offset, true, false, "")
		if err != nil {
			return err
		}
		p.rawSlice = true
		source, _, err := dataSource(m, true, false)
		if err != nil {
			return err
		}
		query := "SELECT " + strings.Join(p.selects, ",") + " FROM " + source + " WHERE " + quoteName(dataReadAlias) + `."_row_id"=` + g.RowID + " LIMIT 2"
		rows := 0
		err = l.readRows(child, original, query, p, func(cells []contract.Cell, digests []*string, units []int64, rowid string) error {
			rows++
			if rows != 1 || rowid != g.RowID || !reflect.DeepEqual(g.Digests[index], digests[0]) {
				return contract.ErrDataConflict
			}
			c := cells[0]
			result = &contract.FieldChunk{Offset: offset, NextOffset: offset, TotalUnits: units[0], Null: c.Kind == contract.Null, Binary: c.Kind == contract.Binary}
			if offset > units[0] {
				return connections.ErrInvalid
			}
			if result.Null {
				return nil
			}
			if result.Binary {
				result.Text = "0x" + hex.EncodeToString(c.Bytes)
				result.NextOffset = offset + int64(len(c.Bytes))
			} else {
				result.Text = c.Text
				result.NextOffset = offset + int64(len([]rune(c.Text)))
			}
			result.HasMore = result.NextOffset < result.TotalUnits
			return nil
		})
		if err == nil && rows != 1 {
			return contract.ErrDataConflict
		}
		return err
	})
	return result, err
}

func (l *dataLease) export(ctx context.Context, id int64, db, table string, o contract.DataOptions, fields []string, sink func([]contract.DataValue) error) error {
	fields = slices.Clone(fields)
	o.Filters, o.Sorts = slices.Clone(o.Filters), slices.Clone(o.Sorts)
	if sink == nil {
		return connections.ErrInvalid
	}
	if err := l.identity(id, db, table); err != nil {
		return err
	}
	if o.Object.ProfileID != 0 && o.Object != l.object {
		return connections.ErrInvalid
	}
	o.Object = l.object
	return l.exportRows(ctx, o, fields, func(_ []contract.StructureColumn, values []contract.DataValue) error { return sink(values) })
}

func (l *dataLease) exportRows(ctx context.Context, o contract.DataOptions, fields []string, sink func([]contract.StructureColumn, []contract.DataValue) error) error {
	return l.withOriginal(ctx, false, func(child context.Context, original *ManagedSession) error {
		m, err := l.session.readTableMetadata(child, original, l.object)
		if err != nil {
			return err
		}
		source, total, err := dataSource(m, false, true, fields...)
		if err != nil {
			return err
		}
		p, err := newDataProjection(m, fields, dataExportMaxBytes, 0, false, true, total)
		if err != nil {
			return err
		}
		query, _, err := dataQuery(m, p, o, source, false)
		if err != nil {
			return err
		}
		return l.readRows(child, original, query, p, func(cells []contract.Cell, _ []*string, _ []int64, _ string) error {
			values := make([]contract.DataValue, len(cells))
			for i, c := range cells {
				if c.Truncated {
					return contract.ErrDataPage
				}
				values[i] = contract.DataValue{Kind: c.Kind, Text: c.Text, Bytes: bytes.Clone(c.Bytes), Null: c.Kind == contract.Null, Binary: c.Kind == contract.Binary}
			}
			return sink(slices.Clone(p.columns), values)
		})
	})
}

func (l *dataLease) exportJSONL(ctx context.Context, w io.Writer) error {
	encoder := json.NewEncoder(w)
	return l.exportRows(ctx, contract.DataOptions{Object: l.object}, nil, func(metadata []contract.StructureColumn, row []contract.DataValue) error {
		if len(row) != len(metadata) {
			return contract.ErrDataPage
		}
		columns := make([]contract.Column, len(row))
		values := make([]contract.Cell, len(row))
		for i, v := range row {
			columns[i] = contract.Column{Name: metadata[i].Name, DatabaseType: metadata[i].Type, Nullable: metadata[i].Nullable}
			values[i] = contract.Cell{Kind: v.Kind, Text: v.Text, Bytes: bytes.Clone(v.Bytes)}
		}
		return encoder.Encode(struct {
			Object  contract.ObjectRef `json:"object"`
			Columns []contract.Column  `json:"columns"`
			Values  []contract.Cell    `json:"values"`
		}{l.object, columns, values})
	})
}
